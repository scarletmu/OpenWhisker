package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/scarletmu/openwhisker/internal/model"
)

type Conventions struct {
	ProfileID         string   `json:"profile_id"`
	RawInboxDir       string   `json:"raw_inbox_dir"`
	RawProcessedDir   string   `json:"raw_processed_dir"`
	KnowledgeDir      string   `json:"knowledge_dir"`
	KnowledgeDraftDir string   `json:"knowledge_draft_dir"`
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
	c.RawInboxDir = cleanRelativeDir(c.RawInboxDir)
	c.RawProcessedDir = cleanRelativeDir(c.RawProcessedDir)
	c.KnowledgeDir = cleanRelativeDir(c.KnowledgeDir)
	c.KnowledgeDraftDir = cleanRelativeDir(c.KnowledgeDraftDir)
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
			var payload model.CreateNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode rewrite_note payload for %s: %w", op.ID, err)
			}
			if strings.TrimSpace(payload.Content) == "" {
				return fmt.Errorf("rewrite_note payload content is required for %s", op.ID)
			}
		case model.OperationWriteAgentReport:
			if !strings.HasPrefix(op.TargetPath, "Meta/Reports/") {
				return fmt.Errorf("write_agent_report target %q is outside Meta/Reports", op.TargetPath)
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
		if (op.Type == model.OperationAppendNote || op.Type == model.OperationRewriteNote || op.Type == model.OperationMoveNote) && op.BeforeHash == "" {
			return fmt.Errorf("%s operation %s requires before_hash", op.Type, op.ID)
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
	frontmatter, body, ok := markdownFrontmatter(content)
	if !ok {
		return fmt.Errorf("create_note payload for %s must start with YAML frontmatter", op.ID)
	}
	for _, key := range []string{
		"openwhisker_job_id",
		"source_raw_job_id",
		"source_raw_path",
		"source_processed_path",
		"status",
		"needs_review",
		"tags",
	} {
		if !frontmatterHasKey(frontmatter, key) {
			return fmt.Errorf("create_note payload for %s frontmatter missing %s", op.ID, key)
		}
	}
	if frontmatterValue(frontmatter, "status") != "draft" {
		return fmt.Errorf("create_note payload for %s status must be draft", op.ID)
	}
	if frontmatterValue(frontmatter, "needs_review") != "true" {
		return fmt.Errorf("create_note payload for %s needs_review must be true", op.ID)
	}
	processedPath := frontmatterValue(frontmatter, "source_processed_path")
	if err := validateRelativeVaultPath(processedPath); err != nil {
		return fmt.Errorf("create_note payload for %s source_processed_path: %w", op.ID, err)
	}
	if !hasDirPrefix(processedPath, conventions.RawProcessedDir) {
		return fmt.Errorf("create_note payload for %s source_processed_path %q is outside %s", op.ID, processedPath, conventions.RawProcessedDir)
	}
	for _, tag := range conventions.RequiredDraftTags {
		if !frontmatterHasListValue(frontmatter, tag) {
			return fmt.Errorf("create_note payload for %s tags must include %s", op.ID, tag)
		}
	}
	if !strings.Contains(body, processedPath) {
		return fmt.Errorf("create_note payload for %s must link to source_processed_path in the body", op.ID)
	}
	return nil
}

func validateProcessingNote(op model.VaultOperation, payload model.MoveNotePayload, knowledgeTargetPaths []string) error {
	note := strings.TrimSpace(payload.ProcessingNote)
	if note == "" {
		return fmt.Errorf("move_note payload for %s processing_note is required", op.ID)
	}
	if !strings.Contains(note, payload.DestinationPath) {
		return fmt.Errorf("move_note payload for %s processing_note must link to destination_path", op.ID)
	}
	for _, targetPath := range knowledgeTargetPaths {
		if strings.Contains(note, targetPath) {
			return nil
		}
	}
	return fmt.Errorf("move_note payload for %s processing_note must link to a Knowledge output", op.ID)
}

func markdownFrontmatter(content string) (string, string, bool) {
	content = strings.TrimLeft(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") {
		return "", "", false
	}
	end := strings.Index(content[len("---\n"):], "\n---")
	if end < 0 {
		return "", "", false
	}
	frontmatterEnd := len("---\n") + end
	bodyStart := frontmatterEnd + len("\n---")
	return content[len("---\n"):frontmatterEnd], content[bodyStart:], true
}

func frontmatterHasKey(frontmatter, key string) bool {
	prefix := key + ":"
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

func frontmatterValue(frontmatter, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), `"'`)
		}
	}
	return ""
}

func frontmatterHasListValue(frontmatter, value string) bool {
	want := "- " + value
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
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
