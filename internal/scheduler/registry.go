package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/scarletmu/openwhisker/internal/profile"
)

const ScheduleFileName = "SCHEDULE.md"
const SkillFileName = "SKILL.md"

const (
	CapabilityVaultRead            = "vault_read"
	CapabilityExternalInfoRead     = "external_info_read"
	CapabilitySchedulerRunLogWrite = "scheduler_run_log_write"
	CapabilityOutboxNotify         = "outbox_notify"
)

var allowedCapabilities = map[string]bool{
	CapabilityVaultRead:            true,
	CapabilityExternalInfoRead:     true,
	CapabilitySchedulerRunLogWrite: true,
	CapabilityOutboxNotify:         true,
}

var disabledCapabilities = map[string]bool{
	"vault_write":              true,
	"vault_executor_apply":     true,
	"auto_approve":             true,
	"external_api_side_effect": true,
	"arbitrary_shell":          true,
	"arbitrary_http":           true,
	"arbitrary_file_write":     true,
}

type ScheduledSkill struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	CronExpr        string          `json:"cron_expr"`
	Timezone        string          `json:"timezone"`
	Delivery        []string        `json:"delivery"`
	Capabilities    []string        `json:"capabilities,omitempty"`
	VaultContext    []string        `json:"vault_context,omitempty"`
	ExternalSources []string        `json:"external_info_sources,omitempty"`
	SkillConfigJSON json.RawMessage `json:"skill_config,omitempty"`
	RegistryPath    string          `json:"registry_path"`
	RegistryHash    string          `json:"registry_hash"`
	SkillDir        string          `json:"skill_dir"`
	SkillPath       string          `json:"skill_path"`
	Body            string          `json:"body,omitempty"`
}

func LoadRegistry(vaultRoot string, vaultProfile profile.VaultProfile) ([]ScheduledSkill, error) {
	if strings.TrimSpace(vaultRoot) == "" {
		return nil, errors.New("vault root is required")
	}
	decl := vaultProfile.Scheduler
	if !decl.Enabled {
		return nil, nil
	}
	if len(decl.RegistryPaths) == 0 {
		return nil, nil
	}
	roots, err := cleanRootSet(decl.ScheduledSkillRoots)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, errors.New("scheduler scheduled_skill_roots are required")
	}
	readRoots, err := cleanRootSet(decl.ReadOnlyVaultRoots)
	if err != nil {
		return nil, err
	}
	allowedExternalSources := cleanStringSet(decl.ExternalInfoSources)
	defaultDelivery := cleanList(decl.DefaultDelivery)
	if len(defaultDelivery) == 0 {
		defaultDelivery = []string{"outbox"}
	}
	for _, delivery := range defaultDelivery {
		if delivery != "outbox" {
			return nil, fmt.Errorf("unsupported scheduler default delivery %q", delivery)
		}
	}

	var schedules []ScheduledSkill
	seen := map[string]string{}
	for _, pattern := range decl.RegistryPaths {
		cleanPattern, err := cleanRelativePattern(pattern)
		if err != nil {
			return nil, fmt.Errorf("scheduler registry path %q: %w", pattern, err)
		}
		matches, err := filepath.Glob(filepath.Join(vaultRoot, filepath.FromSlash(cleanPattern)))
		if err != nil {
			return nil, fmt.Errorf("glob scheduler registry path %q: %w", cleanPattern, err)
		}
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				continue
			}
			rel, err := relativeVaultPath(vaultRoot, match)
			if err != nil {
				return nil, err
			}
			if filepath.Base(rel) != ScheduleFileName {
				return nil, fmt.Errorf("scheduler registry path %q is not %s", rel, ScheduleFileName)
			}
			skillDir := filepath.ToSlash(filepath.Dir(rel))
			if !isUnderAnyRoot(skillDir, roots) {
				return nil, fmt.Errorf("scheduler registry path %q is outside scheduled_skill_roots", rel)
			}
			skillPath := filepath.ToSlash(filepath.Join(skillDir, SkillFileName))
			if _, err := os.Stat(filepath.Join(vaultRoot, filepath.FromSlash(skillPath))); err != nil {
				return nil, fmt.Errorf("scheduler skill for %q: %w", rel, err)
			}
			data, err := os.ReadFile(match)
			if err != nil {
				return nil, err
			}
			schedule, err := parseScheduleMarkdown(string(data), defaultDelivery)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
			if _, err := ParseCron(schedule.CronExpr); err != nil {
				return nil, fmt.Errorf("%s cron_expr: %w", rel, err)
			}
			if _, err := loadLocation(schedule.Timezone); err != nil {
				return nil, fmt.Errorf("%s timezone: %w", rel, err)
			}
			if err := validateCapabilities(schedule); err != nil {
				return nil, fmt.Errorf("%s capabilities: %w", rel, err)
			}
			if err := validateVaultContext(schedule.VaultContext, readRoots); err != nil {
				return nil, fmt.Errorf("%s vault_context: %w", rel, err)
			}
			if err := validateExternalSources(schedule.ExternalSources, allowedExternalSources); err != nil {
				return nil, fmt.Errorf("%s external_info_sources: %w", rel, err)
			}
			hash := sha256.Sum256(data)
			schedule.RegistryPath = rel
			schedule.RegistryHash = hex.EncodeToString(hash[:])
			schedule.SkillDir = skillDir
			schedule.SkillPath = skillPath
			if previous, ok := seen[schedule.ID]; ok {
				return nil, fmt.Errorf("duplicate scheduler id %q in %s and %s", schedule.ID, previous, rel)
			}
			seen[schedule.ID] = rel
			schedules = append(schedules, schedule)
		}
	}
	return schedules, nil
}

func parseScheduleMarkdown(content string, defaultDelivery []string) (ScheduledSkill, error) {
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return ScheduledSkill{}, err
	}
	values, skillConfig, err := parseSimpleFrontmatter(frontmatter)
	if err != nil {
		return ScheduledSkill{}, err
	}
	enabled := true
	if raw, ok := values["enabled"]; ok {
		enabled, err = strconv.ParseBool(raw)
		if err != nil {
			return ScheduledSkill{}, fmt.Errorf("enabled must be boolean")
		}
	}
	delivery := valuesList(values, "delivery")
	if len(delivery) == 0 {
		delivery = append([]string(nil), defaultDelivery...)
	}
	for _, target := range delivery {
		if target != "outbox" {
			return ScheduledSkill{}, fmt.Errorf("unsupported delivery %q", target)
		}
	}
	configJSON, err := json.Marshal(skillConfig)
	if err != nil {
		return ScheduledSkill{}, err
	}
	schedule := ScheduledSkill{
		ID:              values["id"],
		Name:            values["name"],
		Enabled:         enabled,
		CronExpr:        values["cron_expr"],
		Timezone:        values["timezone"],
		Delivery:        delivery,
		Capabilities:    valuesList(values, "capabilities"),
		VaultContext:    valuesList(values, "vault_context"),
		ExternalSources: valuesList(values, "external_info_sources"),
		SkillConfigJSON: configJSON,
		Body:            strings.TrimSpace(body),
	}
	if schedule.ID == "" {
		return ScheduledSkill{}, errors.New("id is required")
	}
	if schedule.Name == "" {
		schedule.Name = schedule.ID
	}
	if schedule.CronExpr == "" {
		return ScheduledSkill{}, errors.New("cron_expr is required")
	}
	if schedule.Timezone == "" {
		schedule.Timezone = "UTC"
	}
	if len(schedule.Capabilities) == 0 {
		schedule.Capabilities = []string{CapabilityVaultRead, CapabilitySchedulerRunLogWrite, CapabilityOutboxNotify}
	}
	if len(schedule.ExternalSources) > 0 && !containsString(schedule.Capabilities, CapabilityExternalInfoRead) {
		schedule.Capabilities = append(schedule.Capabilities, CapabilityExternalInfoRead)
	}
	return schedule, nil
}

func splitFrontmatter(content string) (string, string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return "", "", errors.New("frontmatter is required")
	}
	lines := strings.Split(content, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), nil
		}
	}
	return "", "", errors.New("frontmatter closing marker is required")
}

func parseSimpleFrontmatter(frontmatter string) (map[string]string, map[string]string, error) {
	values := map[string]string{}
	lists := map[string][]string{}
	skillConfig := map[string]string{}
	lines := strings.Split(frontmatter, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return nil, nil, fmt.Errorf("unexpected indented line %q", strings.TrimSpace(line))
		}
		key, raw, ok := strings.Cut(line, ":")
		if !ok {
			return nil, nil, fmt.Errorf("invalid frontmatter line %q", line)
		}
		key = strings.TrimSpace(key)
		raw = strings.TrimSpace(raw)
		switch key {
		case "delivery", "capabilities", "vault_context", "external_info_sources":
			if raw != "" {
				lists[key] = splitInlineList(raw)
				continue
			}
			items, next := parseIndentedList(lines, i+1)
			lists[key] = items
			i = next - 1
		case "skill_config":
			config, next, err := parseIndentedMap(lines, i+1)
			if err != nil {
				return nil, nil, err
			}
			for k, v := range config {
				skillConfig[k] = v
			}
			i = next - 1
		default:
			values[key] = unquote(raw)
		}
	}
	for key, list := range lists {
		values[key] = strings.Join(cleanList(list), "\n")
	}
	return values, skillConfig, nil
}

func parseIndentedList(lines []string, start int) ([]string, int) {
	var values []string
	i := start
	for ; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		item := strings.TrimSpace(line)
		item = strings.TrimPrefix(item, "-")
		item = strings.TrimSpace(item)
		if item != "" {
			values = append(values, unquote(item))
		}
	}
	return values, i
}

func parseIndentedMap(lines []string, start int) (map[string]string, int, error) {
	values := map[string]string{}
	i := start
	for ; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		key, raw, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			return nil, i, fmt.Errorf("invalid skill_config line %q", strings.TrimSpace(line))
		}
		values[strings.TrimSpace(key)] = unquote(strings.TrimSpace(raw))
	}
	return values, i, nil
}

func validateCapabilities(schedule ScheduledSkill) error {
	for _, capability := range cleanList(schedule.Capabilities) {
		if disabledCapabilities[capability] {
			return fmt.Errorf("%q is disabled for scheduler", capability)
		}
		if !allowedCapabilities[capability] {
			return fmt.Errorf("%q is not an allowed scheduler capability", capability)
		}
	}
	return nil
}

func validateVaultContext(paths, readRoots []string) error {
	for _, path := range cleanList(paths) {
		cleanPath, err := cleanRelativePath(path)
		if err != nil {
			return err
		}
		if len(readRoots) == 0 {
			return fmt.Errorf("%q is not allowed because read_only_vault_roots is empty", cleanPath)
		}
		if !isUnderAnyRoot(cleanPath, readRoots) {
			return fmt.Errorf("%q is outside read_only_vault_roots", cleanPath)
		}
	}
	return nil
}

func validateExternalSources(sources []string, allowed map[string]bool) error {
	for _, source := range cleanList(sources) {
		if len(allowed) == 0 || !allowed[source] {
			return fmt.Errorf("%q is not allowed by scheduler external_info_sources", source)
		}
	}
	return nil
}

func valuesList(values map[string]string, key string) []string {
	raw := strings.TrimSpace(values[key])
	if raw == "" {
		return nil
	}
	return cleanList(strings.Split(raw, "\n"))
}

func cleanRootSet(values []string) ([]string, error) {
	var roots []string
	for _, value := range values {
		clean, err := cleanRelativeDir(value)
		if err != nil {
			return nil, err
		}
		roots = append(roots, clean)
	}
	return roots, nil
}

func cleanRelativePattern(value string) (string, error) {
	value = strings.TrimSpace(filepath.ToSlash(value))
	if value == "" {
		return "", errors.New("path is required")
	}
	if strings.HasPrefix(value, "/") {
		return "", errors.New("absolute paths are not allowed")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", errors.New("parent traversal is not allowed")
		}
	}
	return filepath.ToSlash(filepath.Clean(value)), nil
}

func cleanRelativeDir(value string) (string, error) {
	clean, err := cleanRelativePattern(value)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(clean, "*?[") {
		return "", fmt.Errorf("wildcards are not allowed in scheduled_skill_roots")
	}
	return strings.TrimRight(clean, "/") + "/", nil
}

func cleanRelativePath(value string) (string, error) {
	clean, err := cleanRelativePattern(value)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(clean, "*?[") {
		return "", fmt.Errorf("wildcards are not allowed in scheduler vault_context")
	}
	return clean, nil
}

func relativeVaultPath(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		return "", fmt.Errorf("path %q is outside vault root", path)
	}
	return rel, nil
}

func isUnderAnyRoot(path string, roots []string) bool {
	path = strings.TrimRight(filepath.ToSlash(path), "/") + "/"
	for _, root := range roots {
		if strings.HasPrefix(path, root) {
			return true
		}
	}
	return false
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cleanStringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range cleanList(values) {
		out[value] = true
	}
	return out
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func splitInlineList(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, unquote(strings.TrimSpace(part)))
	}
	return out
}

func unquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}
