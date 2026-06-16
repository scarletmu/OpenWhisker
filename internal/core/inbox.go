package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/model"
)

// InboxEntry is one parsed "## 输入 N" block from a quick-capture day file.
type InboxEntry struct {
	Index     int    `json:"index"`
	Kind      string `json:"kind,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Source    string `json:"source,omitempty"`
	Text      string `json:"text"`
}

// InboxSearchHit pairs a matched entry with the day file it came from.
type InboxSearchHit struct {
	Day   string     `json:"day"`
	Path  string     `json:"path"`
	Entry InboxEntry `json:"entry"`
}

// inboxDayPath returns the relative vault path of the day file for the given
// local time, e.g. Raw/Inbox/2026-06-15.md.
func (s IngestService) inboxDayPath(local time.Time) string {
	return fmt.Sprintf("%s/%s.md", s.conventions.RawInboxDir, local.Format("2006-01-02"))
}

// ListInboxDay parses today's quick-capture day file into its input blocks.
// Returns (nil, path, nil) when the day file does not exist yet.
func (s IngestService) ListInboxDay(local time.Time) ([]InboxEntry, string, error) {
	path := s.inboxDayPath(local)
	content, exists, err := s.readVaultFileIfExists(path)
	if err != nil {
		return nil, path, err
	}
	if !exists {
		return nil, path, nil
	}
	return parseInboxEntries(content), path, nil
}

// undoMaxAttempts bounds UndoLastInbox's retry loop. A retry only happens when
// a concurrent rewrite — typically a background enrich pass tagging the same day
// file — flips the content hash between our read and the guarded apply. A few
// attempts comfortably outlast a single enrich landing.
const undoMaxAttempts = 3

// ErrInboxBusy is returned when repeated concurrent rewrites of today's inbox
// (e.g. a background enrich pass) keep changing the file out from under an undo.
// The undo made no change and is safe to retry.
var ErrInboxBusy = errors.New("inbox is being updated in the background; please retry")

// UndoLastInbox removes the most recent input block from today's day file under
// a content-hash guard and returns the removed entry. ok is false when there is
// nothing to undo (no day file, or no input blocks). Removing the only block
// leaves the day file with just its frontmatter + heading — undo never deletes
// the day file itself (no delete_note operation exists in the low-risk policy).
//
// The undo races the background enrich pass, which rewrites the same day file
// under its own hash guard. The guard makes the loser of that race fail safely
// (no clobber) rather than corrupt the file, so a hash conflict is retried from
// a fresh read instead of surfaced to the user; ErrInboxBusy is returned only if
// the conflict persists across undoMaxAttempts.
func (s IngestService) UndoLastInbox(ctx context.Context, local time.Time) (InboxEntry, bool, error) {
	path := s.inboxDayPath(local)
	for range undoMaxAttempts {
		removed, ok, err := s.undoLastInboxOnce(ctx, local, path)
		if executor.IsConflict(err) {
			continue // concurrent rewrite flipped the hash guard; re-read and retry
		}
		if err != nil {
			return InboxEntry{}, false, err
		}
		return removed, ok, nil
	}
	return InboxEntry{}, false, ErrInboxBusy
}

// undoLastInboxOnce is one attempt of UndoLastInbox. It returns an
// executor.ConflictError (detectable via executor.IsConflict) when the hash
// guard rejects the rewrite, so the caller can retry from a fresh read.
func (s IngestService) undoLastInboxOnce(ctx context.Context, local time.Time, path string) (InboxEntry, bool, error) {
	existing, exists, err := s.readVaultFileIfExists(path)
	if err != nil {
		return InboxEntry{}, false, err
	}
	if !exists {
		return InboxEntry{}, false, nil
	}
	header, blocks := splitInboxBlocks(existing)
	if len(blocks) == 0 {
		return InboxEntry{}, false, nil
	}
	removed := parseInboxBlock(blocks[len(blocks)-1])
	now := s.now()

	job := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeAppendRaw,
		Status:    model.JobStatusPending,
		Source:    "inbox-undo",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return InboxEntry{}, false, err
	}

	nextContent := rewriteRawBucketUpdated(rebuildInbox(header, blocks[:len(blocks)-1]), local)
	payload, err := json.Marshal(model.CreateNotePayload{Content: nextContent})
	if err != nil {
		_ = s.failJob(job.ID, err)
		return InboxEntry{}, false, err
	}
	plan := model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            job.ID,
		Purpose:          "undo last quick-capture",
		RiskLevel:        model.RiskLow,
		RequiresApproval: false,
		Summary:          "Remove the most recent input block from today's quick-capture inbox under a hash guard.",
		SourceRefs:       []string{job.ID, path},
		TargetPaths:      []string{path},
		Operations: []model.VaultOperation{{
			ID:          model.NewID("op"),
			Type:        model.OperationRewriteNote,
			TargetPath:  path,
			BeforeHash:  model.ContentHash([]byte(existing)),
			PayloadJSON: string(payload),
			Reason:      "Withdraw the last quick-capture entry on the user's request, under a content-hash guard.",
			RiskLevel:   model.RiskLow,
		}},
		Status:    model.PlanStatusProposed,
		CreatedAt: now,
	}
	if err := s.store.SavePlan(plan); err != nil {
		_ = s.failJob(job.ID, err)
		return InboxEntry{}, false, err
	}
	if err := s.checker.Check(plan); err != nil {
		_ = s.failJob(job.ID, err)
		return InboxEntry{}, false, err
	}
	if err := s.store.UpdateJobStatus(job.ID, model.JobStatusApplying, "", ""); err != nil {
		return InboxEntry{}, false, err
	}
	if _, err := s.executor.Apply(ctx, plan); err != nil {
		_ = s.store.UpdatePlanStatus(plan.ID, model.PlanStatusFailed, nil)
		_ = s.failJob(job.ID, err)
		return InboxEntry{}, false, err
	}
	appliedAt := s.now()
	if err := s.store.UpdatePlanStatus(plan.ID, model.PlanStatusApplied, &appliedAt); err != nil {
		_ = s.failJob(job.ID, err)
		return InboxEntry{}, false, err
	}
	if err := s.store.UpdateJobStatus(job.ID, model.JobStatusDone, "", ""); err != nil {
		return InboxEntry{}, false, err
	}
	return removed, true, nil
}

// SearchInbox scans the quick-capture day files (text inbox only — clips live
// in Raw/Sources and are intentionally excluded) for blocks whose text contains
// keyword (case-insensitive). Hits are returned newest day first, capped at
// limit.
func (s IngestService) SearchInbox(keyword string, limit int) ([]InboxSearchHit, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, errors.New("search keyword is required")
	}
	if limit <= 0 {
		limit = 10
	}
	inboxAbs, err := executor.ResolveVaultPath(s.vaultRoot, s.conventions.RawInboxDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(inboxAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Day files are named YYYY-MM-DD.md, so a reverse lexical sort is also a
	// reverse chronological sort.
	days := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		days = append(days, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))

	needle := strings.ToLower(keyword)
	var hits []InboxSearchHit
	for _, name := range days {
		rel := s.conventions.RawInboxDir + "/" + name
		content, exists, err := s.readVaultFileIfExists(rel)
		if err != nil || !exists {
			continue
		}
		day := strings.TrimSuffix(name, filepath.Ext(name))
		for _, entry := range parseInboxEntries(content) {
			if strings.Contains(strings.ToLower(entry.Text), needle) {
				hits = append(hits, InboxSearchHit{Day: day, Path: rel, Entry: entry})
				if len(hits) >= limit {
					return hits, nil
				}
			}
		}
	}
	return hits, nil
}

// splitInboxBlocks splits a day file into its header (frontmatter + heading,
// everything before the first input block) and the raw text of each input
// block (each beginning with its "## 输入 N" line).
func splitInboxBlocks(content string) (header string, blocks []string) {
	lines := strings.Split(content, "\n")
	starts := []int{}
	offset := 0
	for _, line := range lines {
		start := offset
		offset += len(line) + 1 // +1 for the split-stripped newline
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "## 输入 %d", &n); err == nil {
			starts = append(starts, start)
		}
	}
	if len(starts) == 0 {
		return content, nil
	}
	header = content[:starts[0]]
	for i, start := range starts {
		end := len(content)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		blocks = append(blocks, content[start:end])
	}
	return header, blocks
}

// rebuildInbox reassembles a day file from its header and remaining blocks,
// restoring the blank line that separates the heading from each input block
// and ending on a single trailing newline.
func rebuildInbox(header string, blocks []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(header, "\n"))
	for _, block := range blocks {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(block))
	}
	b.WriteString("\n")
	return b.String()
}

func parseInboxEntries(content string) []InboxEntry {
	_, blocks := splitInboxBlocks(content)
	entries := make([]InboxEntry, 0, len(blocks))
	for _, block := range blocks {
		entries = append(entries, parseInboxBlock(block))
	}
	return entries
}

// parseInboxBlock extracts the structured fields from one "## 输入 N" block.
// It tolerates missing metadata lines and a fenced or unfenced body.
func parseInboxBlock(block string) InboxEntry {
	entry := InboxEntry{}
	lines := strings.Split(block, "\n")
	var fence string
	var bodyLines []string
	inBody := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case inBody:
			if fence != "" && trimmed == fence {
				inBody = false
				continue
			}
			bodyLines = append(bodyLines, line)
		case strings.HasPrefix(trimmed, "## 输入 "):
			fmt.Sscanf(trimmed, "## 输入 %d", &entry.Index)
		case strings.HasPrefix(trimmed, "- 类型："):
			entry.Kind = strings.TrimSpace(strings.TrimPrefix(trimmed, "- 类型："))
		case strings.HasPrefix(trimmed, "- 时间："):
			entry.Timestamp = strings.TrimSpace(strings.TrimPrefix(trimmed, "- 时间："))
		case strings.HasPrefix(trimmed, "- 来源："):
			entry.Source = strings.TrimSpace(strings.TrimPrefix(trimmed, "- 来源："))
		case strings.HasPrefix(trimmed, "```"):
			// Opening fence: capture the leading backtick run, ignoring any
			// language tag ("```text" → "```").
			fence = trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, "`"))]
			inBody = true
		}
	}
	entry.Text = strings.TrimRight(strings.Join(bodyLines, "\n"), "\n")
	return entry
}
