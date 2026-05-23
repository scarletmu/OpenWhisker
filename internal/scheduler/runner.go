package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxVaultContextFileBytes = 20 * 1024
	maxVaultContextEntries   = 50
)

type SkillRunRequest struct {
	Schedule ScheduledSkill
	Now      time.Time
}

type SkillRunResult struct {
	ScheduleID string          `json:"schedule_id"`
	SkillID    string          `json:"skill_id"`
	Title      string          `json:"title"`
	Summary    string          `json:"summary"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type SkillRunner interface {
	Run(context.Context, SkillRunRequest) (SkillRunResult, error)
}

type StaticSkillRunner struct {
	VaultRoot            string
	ExternalInfoAdapters map[string]ExternalInfoAdapter
	Engine               SkillEngine
}

type VaultContextItem struct {
	Path      string   `json:"path"`
	Kind      string   `json:"kind"`
	Content   string   `json:"content,omitempty"`
	Entries   []string `json:"entries,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

type ExternalInfoRequest struct {
	Source   string
	Schedule ScheduledSkill
	Now      time.Time
}

type ExternalInfoItem struct {
	Source    string          `json:"source"`
	Summary   string          `json:"summary,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	FetchedAt time.Time       `json:"fetched_at"`
}

type ExternalInfoAdapter interface {
	ReadInfo(context.Context, ExternalInfoRequest) (ExternalInfoItem, error)
}

type SkillExecutionRequest struct {
	Schedule     ScheduledSkill
	Now          time.Time
	SkillContent string
	SkillHeading string
	VaultContext []VaultContextItem
	ExternalInfo []ExternalInfoItem
}

type SkillEngine interface {
	RunSkill(context.Context, SkillExecutionRequest) (SkillRunResult, error)
}

type StaticSkillEngine struct{}

func (r StaticSkillRunner) Run(ctx context.Context, req SkillRunRequest) (SkillRunResult, error) {
	if err := ctx.Err(); err != nil {
		return SkillRunResult{}, err
	}
	skillPath := filepath.Join(r.VaultRoot, filepath.FromSlash(req.Schedule.SkillPath))
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return SkillRunResult{}, err
	}
	heading := firstMarkdownHeading(string(data))
	if heading == "" {
		heading = req.Schedule.Name
	}
	vaultContext, err := r.readVaultContext(req.Schedule.VaultContext)
	if err != nil {
		return SkillRunResult{}, err
	}
	externalInfo, err := r.readExternalInfo(ctx, req)
	if err != nil {
		return SkillRunResult{}, err
	}
	engine := r.Engine
	if engine == nil {
		engine = StaticSkillEngine{}
	}
	return engine.RunSkill(ctx, SkillExecutionRequest{
		Schedule:     req.Schedule,
		Now:          req.Now,
		SkillContent: string(data),
		SkillHeading: heading,
		VaultContext: vaultContext,
		ExternalInfo: externalInfo,
	})
}

func (StaticSkillEngine) RunSkill(_ context.Context, req SkillExecutionRequest) (SkillRunResult, error) {
	payload, err := json.Marshal(map[string]any{
		"registry_path":         req.Schedule.RegistryPath,
		"skill_path":            req.Schedule.SkillPath,
		"skill_heading":         req.SkillHeading,
		"capabilities":          req.Schedule.Capabilities,
		"vault_context":         req.VaultContext,
		"external_info_sources": req.Schedule.ExternalSources,
		"external_info":         req.ExternalInfo,
		"skill_config":          json.RawMessage(SanitizedSkillConfigJSON(req.Schedule)),
	})
	if err != nil {
		return SkillRunResult{}, err
	}
	return SkillRunResult{
		ScheduleID: req.Schedule.ID,
		SkillID:    req.Schedule.ID,
		Title:      req.Schedule.Name,
		Summary:    fmt.Sprintf("Scheduled skill %q is due. Loaded %s.", req.Schedule.ID, req.Schedule.SkillPath),
		Payload:    payload,
	}, nil
}

func (r StaticSkillRunner) readExternalInfo(ctx context.Context, req SkillRunRequest) ([]ExternalInfoItem, error) {
	var items []ExternalInfoItem
	for _, source := range cleanList(req.Schedule.ExternalSources) {
		adapter := r.ExternalInfoAdapters[source]
		if adapter == nil {
			return nil, fmt.Errorf("scheduler external info adapter %q is not configured", source)
		}
		item, err := adapter.ReadInfo(ctx, ExternalInfoRequest{
			Source:   source,
			Schedule: req.Schedule,
			Now:      req.Now,
		})
		if err != nil {
			return nil, fmt.Errorf("read external info %q: %w", source, err)
		}
		if strings.TrimSpace(item.Source) == "" {
			item.Source = source
		}
		if item.FetchedAt.IsZero() {
			item.FetchedAt = req.Now.UTC()
		}
		items = append(items, item)
	}
	return items, nil
}

func (r StaticSkillRunner) readVaultContext(paths []string) ([]VaultContextItem, error) {
	var items []VaultContextItem
	for _, path := range cleanList(paths) {
		cleanPath, err := cleanRelativePath(path)
		if err != nil {
			return nil, err
		}
		absPath := filepath.Join(r.VaultRoot, filepath.FromSlash(cleanPath))
		rel, err := filepath.Rel(r.VaultRoot, absPath)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
			return nil, fmt.Errorf("vault context path %q is outside vault root", path)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			return nil, fmt.Errorf("read vault context %q: %w", cleanPath, err)
		}
		if info.IsDir() {
			item, err := readVaultContextDir(cleanPath, absPath)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
			continue
		}
		item, err := readVaultContextFile(cleanPath, absPath)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func readVaultContextFile(path, absPath string) (VaultContextItem, error) {
	file, err := os.Open(absPath)
	if err != nil {
		return VaultContextItem{}, fmt.Errorf("read vault context %q: %w", path, err)
	}
	defer file.Close()
	limited := io.LimitReader(file, maxVaultContextFileBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return VaultContextItem{}, fmt.Errorf("read vault context %q: %w", path, err)
	}
	truncated := len(data) > maxVaultContextFileBytes
	if truncated {
		data = data[:maxVaultContextFileBytes]
	}
	return VaultContextItem{
		Path:      path,
		Kind:      "file",
		Content:   string(data),
		Truncated: truncated,
	}, nil
}

func readVaultContextDir(path, absPath string) (VaultContextItem, error) {
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return VaultContextItem{}, fmt.Errorf("read vault context %q: %w", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	truncated := len(names) > maxVaultContextEntries
	if truncated {
		names = names[:maxVaultContextEntries]
	}
	return VaultContextItem{
		Path:      path,
		Kind:      "directory",
		Entries:   names,
		Truncated: truncated,
	}, nil
}

func firstMarkdownHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}
