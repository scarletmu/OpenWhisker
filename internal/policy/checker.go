package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/scarletmu/openwhisker/internal/markdown"
	"github.com/scarletmu/openwhisker/internal/model"
)

// minBeforeHashLen is the expected length of a hex-encoded sha256 hash. The
// checker only enforces the shape of BeforeHash; the actual hash match is
// performed by the executor against the on-disk file content, which is the
// authoritative TOCTOU guard for mutating ops.
const minBeforeHashLen = 64

type Conventions struct {
	ProfileID         string   `json:"profile_id"`
	RawInboxDir       string   `json:"raw_inbox_dir"`
	RawProcessedDir   string   `json:"raw_processed_dir"`
	KnowledgeDir      string   `json:"knowledge_dir"`
	KnowledgeDraftDir string   `json:"knowledge_draft_dir"`
	AgentProposalsDir string   `json:"agent_proposals_dir"`
	AgentReportsDir   string   `json:"agent_reports_dir"`
	RequiredDraftTags []string `json:"required_draft_tags,omitempty"`
}

type Checker struct {
	conventions Conventions
}

func NewChecker() Checker {
	return NewCheckerWithConventions(DefaultConventions())
}

func NewCheckerWithConventions(conventions Conventions) Checker {
	return Checker{conventions: conventions.Normalize()}
}

func DefaultConventions() Conventions {
	return Conventions{
		ProfileID:         "generic",
		RawInboxDir:       "Raw/Inbox",
		RawProcessedDir:   "Raw/Processed",
		KnowledgeDir:      "Knowledge",
		KnowledgeDraftDir: "Knowledge/Drafts",
		AgentProposalsDir: "Raw/Agent-Proposals",
		AgentReportsDir:   "Meta/Reports",
	}
}

func KnowledgeVaultConventions() Conventions {
	conventions := DefaultConventions()
	conventions.ProfileID = "knowledge-vault"
	conventions.RequiredDraftTags = []string{"type/knowledge", "status/draft", "status/needs-review"}
	return conventions
}

func ConventionsForProfile(profile string) (Conventions, error) {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", "generic":
		return DefaultConventions(), nil
	case "knowledge-vault":
		return KnowledgeVaultConventions(), nil
	default:
		return Conventions{}, fmt.Errorf("unknown vault profile %q", profile)
	}
}

func (c Conventions) Normalize() Conventions {
	if strings.TrimSpace(c.ProfileID) == "" {
		c.ProfileID = "generic"
	}
	if strings.TrimSpace(c.RawInboxDir) == "" {
		c.RawInboxDir = "Raw/Inbox"
	}
	if strings.TrimSpace(c.RawProcessedDir) == "" {
		c.RawProcessedDir = "Raw/Processed"
	}
	if strings.TrimSpace(c.KnowledgeDir) == "" {
		c.KnowledgeDir = "Knowledge"
	}
	if strings.TrimSpace(c.KnowledgeDraftDir) == "" {
		c.KnowledgeDraftDir = strings.TrimRight(c.KnowledgeDir, "/") + "/Drafts"
	}
	if strings.TrimSpace(c.AgentProposalsDir) == "" {
		c.AgentProposalsDir = "Raw/Agent-Proposals"
	}
	if strings.TrimSpace(c.AgentReportsDir) == "" {
		c.AgentReportsDir = "Meta/Reports"
	}
	c.RawInboxDir = cleanRelativeDir(c.RawInboxDir)
	c.RawProcessedDir = cleanRelativeDir(c.RawProcessedDir)
	c.KnowledgeDir = cleanRelativeDir(c.KnowledgeDir)
	c.KnowledgeDraftDir = cleanRelativeDir(c.KnowledgeDraftDir)
	c.AgentProposalsDir = cleanRelativeDir(c.AgentProposalsDir)
	c.AgentReportsDir = cleanRelativeDir(c.AgentReportsDir)
	c.ProfileID = strings.ToLower(strings.TrimSpace(c.ProfileID))
	c.RequiredDraftTags = cleanStringList(c.RequiredDraftTags)
	return c
}

func (c Checker) Check(plan model.VaultPlan) error {
	conventions := c.conventions.Normalize()
	if plan.RiskLevel != model.RiskLow {
		return fmt.Errorf("risk %q is not auto-allowed", plan.RiskLevel)
	}
	if plan.RequiresApproval {
		return errors.New("plan requires approval")
	}
	if len(plan.Operations) == 0 {
		return errors.New("plan has no operations")
	}
	for _, op := range plan.Operations {
		if op.RiskLevel != model.RiskLow {
			return fmt.Errorf("operation %s risk %q is not auto-allowed", op.ID, op.RiskLevel)
		}
		if err := validateRelativeVaultPath(op.TargetPath); err != nil {
			return fmt.Errorf("operation %s target path: %w", op.ID, err)
		}
		switch op.Type {
		case model.OperationCreateNote:
			if !hasDirPrefix(op.TargetPath, conventions.RawInboxDir) {
				return fmt.Errorf("create_note target %q is outside %s", op.TargetPath, conventions.RawInboxDir)
			}
		case model.OperationAppendNote:
			if !hasDirPrefix(op.TargetPath, conventions.RawInboxDir) {
				return fmt.Errorf("append_note target %q is outside %s", op.TargetPath, conventions.RawInboxDir)
			}
			if err := validateMutatingBeforeHash(op); err != nil {
				return err
			}
			var payload model.AppendNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode append_note payload for %s: %w", op.ID, err)
			}
			if strings.TrimSpace(payload.Content) == "" {
				return fmt.Errorf("append_note payload content is required for %s", op.ID)
			}
		case model.OperationRewriteNote:
			if !hasDirPrefix(op.TargetPath, conventions.RawInboxDir) {
				return fmt.Errorf("rewrite_note target %q is outside %s", op.TargetPath, conventions.RawInboxDir)
			}
			if err := validateMutatingBeforeHash(op); err != nil {
				return err
			}
			var payload model.CreateNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode rewrite_note payload for %s: %w", op.ID, err)
			}
			if strings.TrimSpace(payload.Content) == "" {
				return fmt.Errorf("rewrite_note payload content is required for %s", op.ID)
			}
		case model.OperationWriteAgentReport:
			if !hasDirPrefix(op.TargetPath, conventions.AgentReportsDir) {
				return fmt.Errorf("write_agent_report target %q is outside %s", op.TargetPath, conventions.AgentReportsDir)
			}
		default:
			return fmt.Errorf("operation type %q is not allowed in phase 1", op.Type)
		}
	}
	return nil
}

func (c Checker) CheckForApproval(plan model.VaultPlan) error {
	conventions := c.conventions.Normalize()
	if plan.RiskLevel != model.RiskMedium {
		return fmt.Errorf("approval flow only supports medium risk plans, got %q", plan.RiskLevel)
	}
	if !plan.RequiresApproval {
		return errors.New("medium risk plan must require approval")
	}
	if strings.TrimSpace(plan.ID) == "" {
		return errors.New("plan id is required")
	}
	if strings.TrimSpace(plan.JobID) == "" {
		return errors.New("plan job id is required")
	}
	if strings.TrimSpace(plan.Summary) == "" {
		return errors.New("plan summary is required")
	}
	if len(plan.SourceRefs) == 0 {
		return errors.New("plan source_refs are required")
	}
	for _, ref := range plan.SourceRefs {
		if strings.TrimSpace(ref) == "" {
			return errors.New("plan source_refs must not contain empty values")
		}
	}
	if len(plan.TargetPaths) == 0 {
		return errors.New("plan target_paths are required")
	}
	for _, targetPath := range plan.TargetPaths {
		if err := validateRelativeVaultPath(targetPath); err != nil {
			return fmt.Errorf("plan target path %q: %w", targetPath, err)
		}
	}
	if len(plan.Operations) == 0 {
		return errors.New("plan has no operations")
	}
	targetSet := make(map[string]bool, len(plan.TargetPaths))
	var knowledgeTargetPaths []string
	for _, targetPath := range plan.TargetPaths {
		targetSet[targetPath] = true
		if hasDirPrefix(targetPath, conventions.KnowledgeDir) {
			knowledgeTargetPaths = append(knowledgeTargetPaths, targetPath)
		}
	}
	for _, op := range plan.Operations {
		if strings.TrimSpace(op.ID) == "" {
			return errors.New("operation id is required")
		}
		if strings.TrimSpace(op.Reason) == "" {
			return fmt.Errorf("operation %s reason is required", op.ID)
		}
		if strings.TrimSpace(op.PayloadJSON) == "" {
			return fmt.Errorf("operation %s payload is required", op.ID)
		}
		if op.RiskLevel != model.RiskMedium {
			return fmt.Errorf("operation %s risk %q is not medium", op.ID, op.RiskLevel)
		}
		if err := validateRelativeVaultPath(op.TargetPath); err != nil {
			return fmt.Errorf("operation %s target path: %w", op.ID, err)
		}
		if !targetSet[op.TargetPath] {
			return fmt.Errorf("operation %s target %q is missing from plan target_paths", op.ID, op.TargetPath)
		}
		switch op.Type {
		case model.OperationCreateNote:
			if !hasDirPrefix(op.TargetPath, conventions.KnowledgeDraftDir) {
				return fmt.Errorf("create_note target %q is outside %s", op.TargetPath, conventions.KnowledgeDraftDir)
			}
			var payload model.CreateNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode create_note payload for %s: %w", op.ID, err)
			}
			if err := validateKnowledgeDraftPayload(op, payload, conventions); err != nil {
				return err
			}
		case model.OperationAppendNote:
			if !hasDirPrefix(op.TargetPath, conventions.KnowledgeDir) {
				return fmt.Errorf("append_note target %q is outside %s", op.TargetPath, conventions.KnowledgeDir)
			}
			var payload model.AppendNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode append_note payload for %s: %w", op.ID, err)
			}
			if strings.TrimSpace(payload.Content) == "" {
				return fmt.Errorf("append_note payload content is required for %s", op.ID)
			}
		case model.OperationMoveNote:
			if !hasDirPrefix(op.TargetPath, conventions.RawInboxDir) {
				return fmt.Errorf("move_note source %q is outside %s", op.TargetPath, conventions.RawInboxDir)
			}
			var payload model.MoveNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode move_note payload for %s: %w", op.ID, err)
			}
			if err := validateRelativeVaultPath(payload.DestinationPath); err != nil {
				return fmt.Errorf("operation %s destination path: %w", op.ID, err)
			}
			if !hasDirPrefix(payload.DestinationPath, conventions.RawProcessedDir) {
				return fmt.Errorf("move_note destination %q is outside %s", payload.DestinationPath, conventions.RawProcessedDir)
			}
			if !targetSet[payload.DestinationPath] {
				return fmt.Errorf("operation %s destination %q is missing from plan target_paths", op.ID, payload.DestinationPath)
			}
			if err := validateProcessingNote(op, payload, knowledgeTargetPaths); err != nil {
				return err
			}
		default:
			return fmt.Errorf("operation type %q is not allowed for approval flow", op.Type)
		}
	}
	return nil
}

func (c Checker) CheckApproved(plan model.VaultPlan) error {
	if plan.Status != model.PlanStatusApproved && plan.Status != model.PlanStatusApplying {
		return fmt.Errorf("plan status %q is not approved", plan.Status)
	}
	if err := c.CheckForApproval(plan); err != nil {
		return err
	}
	for _, op := range plan.Operations {
		if op.Type == model.OperationAppendNote || op.Type == model.OperationRewriteNote || op.Type == model.OperationMoveNote {
			if err := validateMutatingBeforeHash(op); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c Checker) CheckForApprovalHighRisk(plan model.VaultPlan) error {
	conventions := c.conventions.Normalize()
	if plan.RiskLevel != model.RiskHigh {
		return fmt.Errorf("high-risk approval flow requires risk %q, got %q", model.RiskHigh, plan.RiskLevel)
	}
	if !plan.RequiresApproval {
		return errors.New("high-risk plan must require approval")
	}
	if strings.TrimSpace(plan.ID) == "" {
		return errors.New("plan id is required")
	}
	if strings.TrimSpace(plan.JobID) == "" {
		return errors.New("plan job id is required")
	}
	if strings.TrimSpace(plan.Summary) == "" {
		return errors.New("plan summary is required")
	}
	if len(plan.SourceRefs) == 0 {
		return errors.New("plan source_refs are required")
	}
	for _, ref := range plan.SourceRefs {
		if strings.TrimSpace(ref) == "" {
			return errors.New("plan source_refs must not contain empty values")
		}
	}
	if len(plan.TargetPaths) == 0 {
		return errors.New("plan target_paths are required")
	}
	for _, targetPath := range plan.TargetPaths {
		if err := validateRelativeVaultPath(targetPath); err != nil {
			return fmt.Errorf("plan target path %q: %w", targetPath, err)
		}
	}
	if len(plan.Operations) == 0 {
		return errors.New("plan has no operations")
	}
	for _, op := range plan.Operations {
		if strings.TrimSpace(op.ID) == "" {
			return errors.New("operation id is required")
		}
		if strings.TrimSpace(op.Reason) == "" {
			return fmt.Errorf("operation %s reason is required", op.ID)
		}
		if strings.TrimSpace(op.PayloadJSON) == "" {
			return fmt.Errorf("operation %s payload is required", op.ID)
		}
		if op.RiskLevel != model.RiskHigh {
			return fmt.Errorf("operation %s risk %q is not high", op.ID, op.RiskLevel)
		}
		if err := validateRelativeVaultPath(op.TargetPath); err != nil {
			return fmt.Errorf("operation %s target path: %w", op.ID, err)
		}
		switch op.Type {
		case model.OperationRenameNote:
			var payload model.RenameNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode rename_note payload for %s: %w", op.ID, err)
			}
			if err := validateRelativeVaultPath(payload.SourcePath); err != nil {
				return fmt.Errorf("operation %s source path: %w", op.ID, err)
			}
			if err := validateRelativeVaultPath(payload.DestinationPath); err != nil {
				return fmt.Errorf("operation %s destination path: %w", op.ID, err)
			}
			if !hasDirPrefix(payload.SourcePath, conventions.KnowledgeDir) {
				return fmt.Errorf("rename_note source %q is outside %s", payload.SourcePath, conventions.KnowledgeDir)
			}
			if hasDirPrefix(payload.SourcePath, conventions.KnowledgeDraftDir) {
				return fmt.Errorf("rename_note source %q is inside %s; draft renames are not high-risk", payload.SourcePath, conventions.KnowledgeDraftDir)
			}
			if payload.SourcePath != op.TargetPath {
				return fmt.Errorf("operation %s target_path %q must match payload source_path %q", op.ID, op.TargetPath, payload.SourcePath)
			}
		case model.OperationBulkRetag:
			var payload model.BulkRetagPayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode bulk_retag payload for %s: %w", op.ID, err)
			}
			if len(payload.AffectedPaths) < model.ProposalBulkAffectsThreshold {
				return fmt.Errorf("operation %s bulk_retag must affect >= %d paths to qualify as high-risk", op.ID, model.ProposalBulkAffectsThreshold)
			}
			if len(payload.AddTags) == 0 && len(payload.RemoveTags) == 0 {
				return fmt.Errorf("operation %s bulk_retag must add or remove at least one tag", op.ID)
			}
			for _, p := range payload.AffectedPaths {
				if err := validateRelativeVaultPath(p); err != nil {
					return fmt.Errorf("operation %s affected path %q: %w", op.ID, p, err)
				}
			}
		case model.OperationBulkLinkRewrite:
			var payload model.BulkLinkRewritePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode bulk_link_rewrite payload for %s: %w", op.ID, err)
			}
			if err := validateRelativeVaultPath(payload.FromPath); err != nil {
				return fmt.Errorf("operation %s from path: %w", op.ID, err)
			}
			if err := validateRelativeVaultPath(payload.ToPath); err != nil {
				return fmt.Errorf("operation %s to path: %w", op.ID, err)
			}
			if len(payload.AffectedPaths) < model.ProposalBulkAffectsThreshold {
				return fmt.Errorf("operation %s bulk_link_rewrite must affect >= %d paths to qualify as high-risk", op.ID, model.ProposalBulkAffectsThreshold)
			}
			for _, p := range payload.AffectedPaths {
				if err := validateRelativeVaultPath(p); err != nil {
					return fmt.Errorf("operation %s affected path %q: %w", op.ID, p, err)
				}
			}
		default:
			return fmt.Errorf("operation type %q is not allowed in high-risk plan", op.Type)
		}
	}
	return nil
}

func ClassifyProposalKind(plan model.VaultPlan) (string, error) {
	if plan.RiskLevel != model.RiskHigh {
		return "", fmt.Errorf("classify proposal kind requires risk %q, got %q", model.RiskHigh, plan.RiskLevel)
	}
	// Fast path: the Knowledge Expander collapses split/merge into a single
	// rename_note op plus affected_paths in TargetPaths, which the count
	// heuristic below would otherwise classify as a plain rename. The
	// expander records the original proposal_kind in Purpose
	// ("propose Knowledge restructure (split)") — honor it if present so the
	// downstream renderer and diff can expand all affected paths.
	if hinted := proposalKindFromPurpose(plan.Purpose); hinted != "" {
		return hinted, nil
	}
	var renameCount, retagCount, linkRewriteCount int
	for _, op := range plan.Operations {
		switch op.Type {
		case model.OperationRenameNote:
			renameCount++
		case model.OperationBulkRetag:
			retagCount++
		case model.OperationBulkLinkRewrite:
			linkRewriteCount++
		}
	}
	switch {
	case retagCount > 0 && renameCount == 0 && linkRewriteCount == 0:
		return model.ProposalKindBulkRetag, nil
	case linkRewriteCount > 0 && renameCount == 0 && retagCount == 0:
		return model.ProposalKindBulkLinkRewrite, nil
	case renameCount == 1 && retagCount == 0 && linkRewriteCount == 0:
		return model.ProposalKindRename, nil
	case renameCount > 1 && retagCount == 0 && linkRewriteCount == 0:
		return mergeOrSplitKind(plan), nil
	case renameCount > 0 || retagCount > 0 || linkRewriteCount > 0:
		return model.ProposalKindRename, nil
	default:
		return "", errors.New("plan has no high-risk operations to classify")
	}
}

func proposalKindFromPurpose(purpose string) string {
	// Match the suffix "(<kind>)" produced by the Knowledge Expander.
	open := strings.LastIndex(purpose, "(")
	close := strings.LastIndex(purpose, ")")
	if open < 0 || close <= open {
		return ""
	}
	kind := strings.TrimSpace(purpose[open+1 : close])
	switch kind {
	case model.ProposalKindSplit,
		model.ProposalKindMerge,
		model.ProposalKindRename,
		model.ProposalKindBulkRetag,
		model.ProposalKindBulkLinkRewrite:
		return kind
	}
	return ""
}

func mergeOrSplitKind(plan model.VaultPlan) string {
	destinations := map[string]struct{}{}
	for _, op := range plan.Operations {
		if op.Type != model.OperationRenameNote {
			continue
		}
		var payload model.RenameNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return model.ProposalKindRename
		}
		destinations[payload.DestinationPath] = struct{}{}
	}
	if len(destinations) == 1 {
		return model.ProposalKindMerge
	}
	return model.ProposalKindSplit
}

// validateMutatingBeforeHash enforces that any mutating op (append, rewrite,
// move, ...) carries a syntactically valid sha256 before_hash. The empty-file
// hash is allowed: a 0-byte file is a legitimate target (e.g. a freshly
// touched Raw/Inbox stub the ingest job appends into), and the executor's
// readGuardedFile already compares against the actual on-disk content, so
// the hash value itself is not a "wildcard" — it is anchored at apply time.
func validateMutatingBeforeHash(op model.VaultOperation) error {
	hash := strings.TrimSpace(op.BeforeHash)
	if hash == "" {
		return fmt.Errorf("%s operation %s requires before_hash", op.Type, op.ID)
	}
	if len(hash) != minBeforeHashLen {
		return fmt.Errorf("%s operation %s before_hash must be a hex sha256 (%d chars)", op.Type, op.ID, minBeforeHashLen)
	}
	for _, r := range hash {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return fmt.Errorf("%s operation %s before_hash must be hex-encoded", op.Type, op.ID)
		}
	}
	return nil
}

func validateRelativeVaultPath(path string) error {
	if path == "" {
		return errors.New("empty path")
	}
	if filepath.IsAbs(path) {
		return errors.New("absolute paths are disabled")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean != path {
		return errors.New("path must already be clean and relative")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return errors.New("path traversal is disabled")
		}
		if strings.HasPrefix(part, ".") {
			return errors.New("hidden path segments are disabled")
		}
	}
	return nil
}

func validateKnowledgeDraftPayload(op model.VaultOperation, payload model.CreateNotePayload, conventions Conventions) error {
	content := strings.TrimSpace(payload.Content)
	if content == "" {
		return fmt.Errorf("create_note payload content is required for %s", op.ID)
	}
	block, body, ok := markdownFrontmatter(content)
	if !ok {
		return fmt.Errorf("create_note payload for %s must start with YAML frontmatter", op.ID)
	}
	doc := markdown.Parse(block)
	for _, key := range []string{
		"title",
		"tags",
		"related",
		"openwhisker",
		"job_id",
		"raw_job_id",
		"processed_path",
	} {
		if !doc.Has(key) {
			return fmt.Errorf("create_note payload for %s frontmatter missing %s", op.ID, key)
		}
	}
	processedPath := doc.Scalar("processed_path")
	if err := validateRelativeVaultPath(processedPath); err != nil {
		return fmt.Errorf("create_note payload for %s processed_path: %w", op.ID, err)
	}
	if !hasDirPrefix(processedPath, conventions.RawProcessedDir) {
		return fmt.Errorf("create_note payload for %s processed_path %q is outside %s", op.ID, processedPath, conventions.RawProcessedDir)
	}
	for _, tag := range conventions.RequiredDraftTags {
		if !doc.HasListValue("tags", tag) {
			return fmt.Errorf("create_note payload for %s tags must include %s", op.ID, tag)
		}
	}
	wantLink := "[[" + strings.TrimSuffix(processedPath, ".md") + "]]"
	if !strings.Contains(body, wantLink) {
		return fmt.Errorf("create_note payload for %s must link to processed_path via %s in the body", op.ID, wantLink)
	}
	return nil
}

func validateProcessingNote(op model.VaultOperation, payload model.MoveNotePayload, knowledgeTargetPaths []string) error {
	note := strings.TrimSpace(payload.ProcessingNote)
	if note == "" {
		return fmt.Errorf("move_note payload for %s processing_note is required", op.ID)
	}
	if !pathReferencedInNote(note, payload.DestinationPath) {
		return fmt.Errorf("move_note payload for %s processing_note must link to destination_path", op.ID)
	}
	for _, targetPath := range knowledgeTargetPaths {
		if pathReferencedInNote(note, targetPath) {
			return nil
		}
	}
	return fmt.Errorf("move_note payload for %s processing_note must link to a Knowledge output", op.ID)
}

func pathReferencedInNote(note, path string) bool {
	if strings.Contains(note, path) {
		return true
	}
	wikilink := "[[" + strings.TrimSuffix(path, ".md") + "]]"
	return strings.Contains(note, wikilink)
}

// markdownFrontmatter splits a note's frontmatter from its body. The boundary
// rule lives in internal/markdown, the single source of truth; this keeps a
// policy-local name for the (block, body, ok) shape both checker.go and
// enrich.go consume.
func markdownFrontmatter(content string) (string, string, bool) {
	return markdown.SplitFrontmatter(content)
}

func cleanRelativeDir(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return path
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func cleanStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func hasDirPrefix(path, dir string) bool {
	dir = cleanRelativeDir(dir)
	path = filepath.ToSlash(path)
	return path == dir || strings.HasPrefix(path, dir+"/")
}
