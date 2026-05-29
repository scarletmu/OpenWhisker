package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/scarletmu/openwhisker/internal/markdown"
	"github.com/scarletmu/openwhisker/internal/profile"
)

const ScheduleFileName = "SCHEDULE.md"
const SkillFileName = "SKILL.md"

const (
	CapabilityVaultRead            = "vault_read"
	CapabilityExternalInfoRead     = "external_info_read"
	CapabilitySchedulerRunLogWrite = "scheduler_run_log_write"
	CapabilityOutboxNotify         = "outbox_notify"

	CapabilityVaultLinkGraphRead = "vault_link_graph_read"
	CapabilityVaultTextSearch    = "vault_text_search"
	// AgentRunLogWrite is the successor of scheduler_run_log_write. Both
	// names are accepted in SKILL.md so Phase 5 skills load unchanged.
	CapabilityAgentRunLogWrite = "agent_run_log_write"
)

// Phase 6 hard constraint #9: these path prefixes are never allowed inside
// a vault_scope / read_only_vault_roots declaration, regardless of profile
// configuration. Match by base name so e.g. "Notes/.git/" is rejected too.
var ForbiddenScopePathPrefixes = []string{
	".obsidian/",
	".git/",
	".trash/",
	".DS_Store",
}

// Phase 6 fixed tool catalog. SKILL.md.vault_tools must be a subset of this.
// submit_result is NOT included here; it is implicit and always available to
// the ToolCallingEngine.
var AllowedVaultTools = []string{
	"list_vault_dir",
	"read_vault_note",
	"vault_outlinks",
	"vault_backlinks",
	"vault_text_search",
}

var allowedCapabilities = map[string]bool{
	CapabilityVaultRead:            true,
	CapabilityExternalInfoRead:     true,
	CapabilitySchedulerRunLogWrite: true,
	CapabilityOutboxNotify:         true,
	CapabilityVaultLinkGraphRead:   true,
	CapabilityVaultTextSearch:      true,
	CapabilityAgentRunLogWrite:     true,
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

// ToolBudget is the per-Skill cap on a single AgentRunner run. A zero field
// means "use the global default" (profile-level → compile-time).
type ToolBudget struct {
	MaxToolCalls        int `json:"max_tool_calls,omitempty"`
	MaxTotalBytes       int `json:"max_total_bytes,omitempty"`
	MaxWallClockSeconds int `json:"max_wall_clock_seconds,omitempty"`
}

// LintAgentSkillDir validates a single Phase 6 agent Skill directory the
// same way LoadRegistry would, but without requiring a vault root or any
// glob pattern setup. Returns the parsed ScheduledSkill (best-effort) and
// the list of issues encountered. Errors are fatal lint failures; warnings
// are non-fatal deprecation notes.
//
// skillDirAbs must be a directory containing SKILL.md (and optionally
// SCHEDULE.md). The Skill's vault_scope is validated against
// vaultProfile.Scheduler.ReadOnlyVaultRoots.
func LintAgentSkillDir(skillDirAbs string, vaultProfile profile.VaultProfile) (ScheduledSkill, []LintIssue) {
	var issues []LintIssue
	add := func(severity, message string) {
		issues = append(issues, LintIssue{Severity: severity, Path: skillDirAbs, Message: message})
	}

	skillPath := filepath.Join(skillDirAbs, SkillFileName)
	skillData, err := os.ReadFile(skillPath)
	if err != nil {
		add("error", fmt.Sprintf("SKILL.md is required: %v", err))
		return ScheduledSkill{}, issues
	}

	defaultDelivery := cleanList(vaultProfile.Scheduler.DefaultDelivery)
	if len(defaultDelivery) == 0 {
		defaultDelivery = []string{"outbox"}
	}
	skill, err := parseAgentSkillMarkdown(string(skillData), defaultDelivery)
	if err != nil {
		add("error", "SKILL.md: "+err.Error())
		return skill, issues
	}

	readRoots, _ := cleanRootSet(vaultProfile.Scheduler.ReadOnlyVaultRoots)
	if err := validateCapabilities(skill); err != nil {
		add("error", "capabilities: "+err.Error())
	}
	if err := validateVaultScope(skill.VaultScope, readRoots); err != nil {
		add("error", "vault_scope: "+err.Error())
	}
	if err := validateVaultTools(skill.VaultTools); err != nil {
		add("error", "vault_tools: "+err.Error())
	}
	defaultEngine := strings.TrimSpace(vaultProfile.Scheduler.DefaultEngine)
	if defaultEngine == "" {
		defaultEngine = profile.AgentSkillEngineStatic
	}
	resolved, err := resolveEngine(skill.ExplicitEngine, skill.VaultTools, defaultEngine)
	if err != nil {
		add("error", "engine: "+err.Error())
	} else {
		skill.Engine = resolved
	}
	budgetCaps := vaultProfile.Scheduler.ToolBudgetDefaults.Resolve()
	if newBudget, err := resolveBudget(skill.Budget, budgetCaps); err != nil {
		add("error", "budget: "+err.Error())
	} else {
		skill.Budget = newBudget
	}

	schedulePath := filepath.Join(skillDirAbs, ScheduleFileName)
	if scheduleData, err := os.ReadFile(schedulePath); err == nil {
		warnings, err := applyAgentSchedule(&skill, string(scheduleData))
		if err != nil {
			add("error", "SCHEDULE.md: "+err.Error())
		} else {
			skill.HasSchedule = true
			if _, err := ParseCron(skill.CronExpr); err != nil {
				add("error", "SCHEDULE.md cron_expr: "+err.Error())
			}
			if _, err := loadLocation(skill.Timezone); err != nil {
				add("error", "SCHEDULE.md timezone: "+err.Error())
			}
		}
		for _, w := range warnings {
			add("warning", "SCHEDULE.md: "+w)
		}
	}
	return skill, issues
}

// LintIssue is one finding from LintAgentSkillDir.
type LintIssue struct {
	Severity string // "error" | "warning"
	Path     string
	Message  string
}

// IsForbiddenScopePath reports whether path falls under any of the always-
// forbidden prefixes (Phase 6 hard constraint #9). Called both at registry
// load time and again at tool execution time as defense in depth.
func IsForbiddenScopePath(path string) bool {
	clean := strings.TrimSpace(strings.TrimPrefix(filepath.ToSlash(path), "/"))
	if clean == "" {
		return false
	}
	cleanSlash := strings.TrimRight(clean, "/") + "/"
	for _, prefix := range ForbiddenScopePathPrefixes {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(cleanSlash, prefix) {
				return true
			}
			// Also flag any path containing /<forbidden>/ as a segment.
			if strings.Contains("/"+cleanSlash, "/"+prefix) {
				return true
			}
		} else {
			// File-name match anywhere in the path.
			base := filepath.Base(clean)
			if base == prefix {
				return true
			}
		}
	}
	return false
}

type ScheduledSkill struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	CronExpr        string          `json:"cron_expr,omitempty"`
	Timezone        string          `json:"timezone,omitempty"`
	Delivery        []string        `json:"delivery,omitempty"`
	Capabilities    []string        `json:"capabilities,omitempty"`
	VaultContext    []string        `json:"vault_context,omitempty"`
	ExternalSources []string        `json:"external_info_sources,omitempty"`
	SkillConfigJSON json.RawMessage `json:"skill_config,omitempty"`
	RegistryPath    string          `json:"registry_path"`
	RegistryHash    string          `json:"registry_hash"`
	SkillDir        string          `json:"skill_dir"`
	SkillPath       string          `json:"skill_path"`
	Body            string          `json:"body,omitempty"`

	VaultTools          []string   `json:"vault_tools,omitempty"`
	VaultScope          []string   `json:"vault_scope,omitempty"`
	Budget              ToolBudget `json:"budget,omitempty"`
	Engine              string     `json:"engine,omitempty"`           // resolved after inference
	ExplicitEngine      string     `json:"explicit_engine,omitempty"`  // raw from frontmatter
	HasSchedule         bool       `json:"has_schedule"`               // false for ad-hoc-only agent skills
	DeprecationWarnings []string   `json:"deprecation_warnings,omitempty"`
}

// HasToolCalling reports whether this Skill is configured to dispatch through
// the Phase 6 ToolCallingEngine (Engine == tool-calling AND non-empty
// VaultTools). Used by the scheduler to choose between legacy single-turn and
// the new agent runtime.
func (s ScheduledSkill) HasToolCalling() bool {
	return s.Engine == profile.AgentSkillEngineToolCalling && len(s.VaultTools) > 0
}

func LoadRegistry(vaultRoot string, vaultProfile profile.VaultProfile) ([]ScheduledSkill, error) {
	if strings.TrimSpace(vaultRoot) == "" {
		return nil, errors.New("vault root is required")
	}
	decl := vaultProfile.Scheduler
	if !decl.Enabled {
		return nil, nil
	}

	// Phase 6: reject forbidden prefixes in profile.read_only_vault_roots
	// before any scan runs. This catches operator typos that would otherwise
	// be silently re-enforced at every tool call.
	for _, root := range decl.ReadOnlyVaultRoots {
		if IsForbiddenScopePath(root) {
			return nil, fmt.Errorf("scheduler read_only_vault_roots contains forbidden prefix %q", root)
		}
	}

	roots, err := cleanRootSet(decl.ScheduledSkillRoots)
	if err != nil {
		return nil, err
	}
	agentRoots, err := cleanRootSet(decl.AgentSkillRoots)
	if err != nil {
		return nil, err
	}
	// Either source path must exist; otherwise we have nothing to scan.
	if len(roots) == 0 && len(agentRoots) == 0 {
		return nil, errors.New("scheduler scheduled_skill_roots or agent_skill_roots are required")
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
		if delivery != "outbox" && delivery != "matrix" {
			return nil, fmt.Errorf("unsupported scheduler default delivery %q", delivery)
		}
	}

	defaultEngine := strings.TrimSpace(decl.DefaultEngine)
	if defaultEngine == "" {
		defaultEngine = profile.AgentSkillEngineStatic
	}
	budgetCaps := decl.ToolBudgetDefaults.Resolve()

	var schedules []ScheduledSkill
	seen := map[string]string{}

	// 1. Phase 5 legacy: Scheduler/Skills/*/SCHEDULE.md (SCHEDULE.md is source
	//    of truth; SKILL.md is the prompt body only). HasSchedule=true.
	if len(decl.RegistryPaths) > 0 && len(roots) > 0 {
		legacy, err := loadLegacyScheduleSkills(vaultRoot, decl.RegistryPaths, roots, readRoots, allowedExternalSources, defaultDelivery, defaultEngine, budgetCaps)
		if err != nil {
			return nil, err
		}
		for _, sched := range legacy {
			if previous, ok := seen[sched.ID]; ok {
				return nil, fmt.Errorf("duplicate skill id %q in %s and %s", sched.ID, previous, sched.RegistryPath)
			}
			seen[sched.ID] = sched.RegistryPath
			schedules = append(schedules, sched)
		}
	}

	// 2. Phase 6 agent skills: Agent/Skills/*/SKILL.md (SKILL.md is source of
	//    truth; SCHEDULE.md is an optional cron hook). Per-skill HasSchedule
	//    is determined per directory.
	if len(decl.AgentRegistryPaths) > 0 && len(agentRoots) > 0 {
		agentSkills, err := loadAgentSkills(vaultRoot, decl.AgentRegistryPaths, agentRoots, readRoots, allowedExternalSources, defaultDelivery, defaultEngine, budgetCaps)
		if err != nil {
			return nil, err
		}
		for _, sched := range agentSkills {
			if previous, ok := seen[sched.ID]; ok {
				return nil, fmt.Errorf("duplicate skill id %q in %s and %s", sched.ID, previous, sched.RegistryPath)
			}
			seen[sched.ID] = sched.RegistryPath
			schedules = append(schedules, sched)
		}
	}

	// Deterministic order so registry_hash / CLI listings are stable.
	sort.SliceStable(schedules, func(i, j int) bool { return schedules[i].ID < schedules[j].ID })
	return schedules, nil
}

// loadLegacyScheduleSkills implements the Phase 5 SCHEDULE.md-driven loader,
// preserved for backwards-compat with vaults that still use the
// Scheduler/Skills/*/SCHEDULE.md layout. Phase 6 vaults use Agent/Skills/
// instead; see loadAgentSkills.
func loadLegacyScheduleSkills(
	vaultRoot string,
	registryPaths []string,
	roots, readRoots []string,
	allowedExternalSources map[string]bool,
	defaultDelivery []string,
	defaultEngine string,
	budgetCaps profile.ToolBudgetDefaults,
) ([]ScheduledSkill, error) {
	var schedules []ScheduledSkill
	for _, pattern := range registryPaths {
		cleanPattern, err := CleanRelativePattern(pattern)
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
			skillAbs := filepath.Join(vaultRoot, filepath.FromSlash(skillPath))
			if _, err := os.Stat(skillAbs); err != nil {
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
			// Best-effort: read the SKILL.md body so callers don't need to
			// open the file again later. Failure is non-fatal; runtime can
			// re-read.
			if body, err := os.ReadFile(skillAbs); err == nil {
				schedule.Body = strings.TrimSpace(string(body))
			}
			hash := sha256.Sum256(data)
			schedule.RegistryPath = rel
			schedule.RegistryHash = hex.EncodeToString(hash[:])
			schedule.SkillDir = skillDir
			schedule.SkillPath = skillPath
			schedule.HasSchedule = true
			schedule.Engine = inferEngine("", schedule.VaultTools, defaultEngine)
			// Legacy SCHEDULE.md has no per-Skill budget; use profile caps.
			schedule.Budget = ToolBudget{
				MaxToolCalls:        budgetCaps.MaxToolCalls,
				MaxTotalBytes:       budgetCaps.MaxTotalBytes,
				MaxWallClockSeconds: budgetCaps.MaxWallClockSeconds,
			}
			schedule.DeprecationWarnings = append(schedule.DeprecationWarnings,
				fmt.Sprintf("Scheduler/Skills/ is the Phase 5 path; migrate %s to Agent/Skills/<id>/ for Phase 6 features", rel))
			schedules = append(schedules, schedule)
		}
	}
	return schedules, nil
}

// loadAgentSkills implements the Phase 6 SKILL.md-driven loader. Each match is
// a SKILL.md file; the loader looks for an optional sibling SCHEDULE.md to
// derive HasSchedule + cron/timezone. SCHEDULE.md is field-tier validated:
// Phase 6 fields (vault_tools / vault_scope / budget / engine / vault_context)
// in SCHEDULE.md are errors; Phase 5 fields (capabilities / external_info_
// sources / skill_config / delivery) emit deprecation warnings but still load.
func loadAgentSkills(
	vaultRoot string,
	registryPaths []string,
	roots, readRoots []string,
	allowedExternalSources map[string]bool,
	defaultDelivery []string,
	defaultEngine string,
	budgetCaps profile.ToolBudgetDefaults,
) ([]ScheduledSkill, error) {
	var schedules []ScheduledSkill
	for _, pattern := range registryPaths {
		cleanPattern, err := CleanRelativePattern(pattern)
		if err != nil {
			return nil, fmt.Errorf("agent skill registry path %q: %w", pattern, err)
		}
		matches, err := filepath.Glob(filepath.Join(vaultRoot, filepath.FromSlash(cleanPattern)))
		if err != nil {
			return nil, fmt.Errorf("glob agent skill registry path %q: %w", cleanPattern, err)
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
			if filepath.Base(rel) != SkillFileName {
				return nil, fmt.Errorf("agent skill registry path %q is not %s", rel, SkillFileName)
			}
			skillDir := filepath.ToSlash(filepath.Dir(rel))
			if !isUnderAnyRoot(skillDir, roots) {
				return nil, fmt.Errorf("agent skill %q is outside agent_skill_roots", rel)
			}
			data, err := os.ReadFile(match)
			if err != nil {
				return nil, err
			}
			schedule, err := parseAgentSkillMarkdown(string(data), defaultDelivery)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
			if err := validateCapabilities(schedule); err != nil {
				return nil, fmt.Errorf("%s capabilities: %w", rel, err)
			}
			if err := validateVaultScope(schedule.VaultScope, readRoots); err != nil {
				return nil, fmt.Errorf("%s vault_scope: %w", rel, err)
			}
			if err := validateVaultTools(schedule.VaultTools); err != nil {
				return nil, fmt.Errorf("%s vault_tools: %w", rel, err)
			}
			if err := validateExternalSources(schedule.ExternalSources, allowedExternalSources); err != nil {
				return nil, fmt.Errorf("%s external_info_sources: %w", rel, err)
			}

			// Engine resolution. Reject explicit-but-inconsistent declarations.
			resolved, err := resolveEngine(schedule.ExplicitEngine, schedule.VaultTools, defaultEngine)
			if err != nil {
				return nil, fmt.Errorf("%s engine: %w", rel, err)
			}
			schedule.Engine = resolved

			// Per-Skill budget must not exceed profile caps.
			schedule.Budget, err = resolveBudget(schedule.Budget, budgetCaps)
			if err != nil {
				return nil, fmt.Errorf("%s budget: %w", rel, err)
			}

			hash := sha256.Sum256(data)
			schedule.RegistryPath = rel
			schedule.RegistryHash = hex.EncodeToString(hash[:])
			schedule.SkillDir = skillDir
			schedule.SkillPath = rel

			// Optional sibling SCHEDULE.md → enables cron triggering.
			schedulePath := filepath.ToSlash(filepath.Join(skillDir, ScheduleFileName))
			scheduleAbs := filepath.Join(vaultRoot, filepath.FromSlash(schedulePath))
			if _, statErr := os.Stat(scheduleAbs); statErr == nil {
				scheduleData, err := os.ReadFile(scheduleAbs)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", schedulePath, err)
				}
				warnings, err := applyAgentSchedule(&schedule, string(scheduleData))
				if err != nil {
					return nil, fmt.Errorf("%s: %w", schedulePath, err)
				}
				schedule.DeprecationWarnings = append(schedule.DeprecationWarnings, warnings...)
				if _, err := ParseCron(schedule.CronExpr); err != nil {
					return nil, fmt.Errorf("%s cron_expr: %w", schedulePath, err)
				}
				if _, err := loadLocation(schedule.Timezone); err != nil {
					return nil, fmt.Errorf("%s timezone: %w", schedulePath, err)
				}
				schedule.HasSchedule = true
			}

			schedules = append(schedules, schedule)
		}
	}
	return schedules, nil
}

// parseAgentSkillMarkdown parses a Phase 6 SKILL.md. The frontmatter carries
// the full agent-runtime configuration; the body is the LLM-facing prompt.
// Returns the skill with its body field populated.
func parseAgentSkillMarkdown(content string, defaultDelivery []string) (ScheduledSkill, error) {
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return ScheduledSkill{}, err
	}
	doc, err := parseFrontmatterDoc(frontmatter)
	if err != nil {
		return ScheduledSkill{}, err
	}

	id := doc.scalar("id")
	if id == "" {
		return ScheduledSkill{}, errors.New("id is required")
	}
	name := doc.scalar("name")
	if name == "" {
		name = id
	}
	delivery := doc.list("delivery")
	if len(delivery) == 0 {
		delivery = append([]string(nil), defaultDelivery...)
	}
	for _, target := range delivery {
		if target != "outbox" && target != "matrix" {
			return ScheduledSkill{}, fmt.Errorf("unsupported delivery %q", target)
		}
	}
	configJSON, err := json.Marshal(doc.subMap("skill_config"))
	if err != nil {
		return ScheduledSkill{}, err
	}

	capabilities := canonicalizeCapabilities(doc.list("capabilities"))

	skill := ScheduledSkill{
		ID:              id,
		Name:            name,
		Enabled:         true, // SCHEDULE.md may override
		Delivery:        delivery,
		Capabilities:    capabilities,
		ExternalSources: doc.list("external_info_sources"),
		SkillConfigJSON: configJSON,
		Body:            strings.TrimSpace(body),

		VaultTools:     canonicalizeVaultTools(doc.list("vault_tools")),
		VaultScope:     normalizeScopeList(doc.list("vault_scope")),
		ExplicitEngine: strings.ToLower(strings.TrimSpace(doc.scalar("engine"))),
	}
	if budgetDoc := doc.subMap("budget"); len(budgetDoc) > 0 {
		budget, err := parseBudgetMap(budgetDoc)
		if err != nil {
			return ScheduledSkill{}, err
		}
		skill.Budget = budget
	}
	// SKILL.md must not declare vault_context (that field belongs to Phase 5
	// SCHEDULE.md and was removed in Phase 6).
	if vc := doc.list("vault_context"); len(vc) > 0 {
		return ScheduledSkill{}, errors.New("vault_context is removed in Phase 6; declare vault_scope in SKILL.md instead")
	}
	// SKILL.md must not declare cron / timezone / enabled (those belong to
	// the sibling SCHEDULE.md).
	for _, k := range []string{"cron_expr", "timezone"} {
		if doc.scalar(k) != "" {
			return ScheduledSkill{}, fmt.Errorf("%s belongs in SCHEDULE.md, not SKILL.md", k)
		}
	}

	if len(skill.ExternalSources) > 0 && !containsString(skill.Capabilities, CapabilityExternalInfoRead) {
		skill.Capabilities = append(skill.Capabilities, CapabilityExternalInfoRead)
	}
	if !containsString(skill.Capabilities, CapabilityVaultRead) {
		skill.Capabilities = append([]string{CapabilityVaultRead}, skill.Capabilities...)
	}
	return skill, nil
}

// applyAgentSchedule merges a sibling SCHEDULE.md into an already-parsed
// agent skill. SCHEDULE.md may declare only id / enabled / cron_expr /
// timezone. Returns deprecation warnings (Phase 5 fields detected); returns
// an error if Phase 6 fields appear (these belong in SKILL.md only).
func applyAgentSchedule(skill *ScheduledSkill, content string) ([]string, error) {
	frontmatter, _, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}
	doc, err := parseFrontmatterDoc(frontmatter)
	if err != nil {
		return nil, err
	}

	// Phase 6 fields in SCHEDULE.md → hard error.
	for _, k := range []string{"vault_tools", "vault_scope", "engine", "budget"} {
		if doc.hasKey(k) {
			return nil, fmt.Errorf("%s belongs in SKILL.md, not SCHEDULE.md", k)
		}
	}
	if doc.hasKey("vault_context") {
		return nil, errors.New("vault_context is removed; declare vault_scope in SKILL.md")
	}

	// Phase 5 fields in SCHEDULE.md → deprecation warning, still loadable.
	var warnings []string
	for _, k := range []string{"capabilities", "external_info_sources", "skill_config", "delivery"} {
		if doc.hasKey(k) {
			warnings = append(warnings, fmt.Sprintf("%q in SCHEDULE.md is deprecated; migrate to sibling SKILL.md", k))
		}
	}

	id := doc.scalar("id")
	if id != "" && id != skill.ID {
		return nil, fmt.Errorf("SCHEDULE.md id %q does not match SKILL.md id %q", id, skill.ID)
	}
	if raw := doc.scalar("enabled"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("enabled must be boolean")
		}
		skill.Enabled = enabled
	}
	if cron := doc.scalar("cron_expr"); cron != "" {
		skill.CronExpr = cron
	} else {
		return nil, errors.New("cron_expr is required in SCHEDULE.md")
	}
	if tz := doc.scalar("timezone"); tz != "" {
		skill.Timezone = tz
	}
	if skill.Timezone == "" {
		skill.Timezone = "UTC"
	}
	return warnings, nil
}

// resolveEngine implements the Phase 6 implicit-inference + conflict-reject
// rules: explicit value wins (subject to validity), absent value infers from
// vault_tools presence, conflict (e.g. engine=static + non-empty vault_tools)
// is rejected.
func resolveEngine(explicit string, vaultTools []string, defaultEngine string) (string, error) {
	explicit = strings.ToLower(strings.TrimSpace(explicit))
	if explicit != "" {
		switch explicit {
		case profile.AgentSkillEngineStatic, profile.AgentSkillEngineOpenAICompatible:
			if len(vaultTools) > 0 {
				return "", fmt.Errorf("engine %q conflicts with non-empty vault_tools (use tool-calling or remove vault_tools)", explicit)
			}
			return explicit, nil
		case profile.AgentSkillEngineToolCalling:
			if len(vaultTools) == 0 {
				return "", errors.New("engine tool-calling requires non-empty vault_tools")
			}
			return explicit, nil
		default:
			return "", fmt.Errorf("unsupported engine %q", explicit)
		}
	}
	return inferEngine(explicit, vaultTools, defaultEngine), nil
}

// inferEngine is the no-error form of resolveEngine, used by the legacy
// loader (where vault_tools is always empty and explicit is always "").
func inferEngine(explicit string, vaultTools []string, defaultEngine string) string {
	if explicit != "" {
		return explicit
	}
	if len(vaultTools) > 0 {
		return profile.AgentSkillEngineToolCalling
	}
	if defaultEngine == "" {
		return profile.AgentSkillEngineStatic
	}
	return defaultEngine
}

// resolveBudget clamps per-Skill budget to the profile caps and returns an
// error if the skill tries to raise any limit above its cap. A zero per-Skill
// field means "use the cap value" rather than "use zero" (zero would block
// every tool call, which is never the user's intent).
func resolveBudget(budget ToolBudget, caps profile.ToolBudgetDefaults) (ToolBudget, error) {
	caps = caps.Resolve()
	if budget.MaxToolCalls < 0 || budget.MaxTotalBytes < 0 || budget.MaxWallClockSeconds < 0 {
		return ToolBudget{}, errors.New("budget fields must be > 0")
	}
	if budget.MaxToolCalls == 0 {
		budget.MaxToolCalls = caps.MaxToolCalls
	} else if budget.MaxToolCalls > caps.MaxToolCalls {
		return ToolBudget{}, fmt.Errorf("max_tool_calls %d exceeds profile cap %d", budget.MaxToolCalls, caps.MaxToolCalls)
	}
	if budget.MaxTotalBytes == 0 {
		budget.MaxTotalBytes = caps.MaxTotalBytes
	} else if budget.MaxTotalBytes > caps.MaxTotalBytes {
		return ToolBudget{}, fmt.Errorf("max_total_bytes %d exceeds profile cap %d", budget.MaxTotalBytes, caps.MaxTotalBytes)
	}
	if budget.MaxWallClockSeconds == 0 {
		budget.MaxWallClockSeconds = caps.MaxWallClockSeconds
	} else if budget.MaxWallClockSeconds > caps.MaxWallClockSeconds {
		return ToolBudget{}, fmt.Errorf("max_wall_clock_seconds %d exceeds profile cap %d", budget.MaxWallClockSeconds, caps.MaxWallClockSeconds)
	}
	return budget, nil
}

func parseBudgetMap(values map[string]string) (ToolBudget, error) {
	var b ToolBudget
	for key, raw := range values {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return ToolBudget{}, fmt.Errorf("budget.%s must be integer", key)
		}
		if n <= 0 {
			return ToolBudget{}, fmt.Errorf("budget.%s must be > 0", key)
		}
		switch key {
		case "max_tool_calls":
			b.MaxToolCalls = n
		case "max_total_bytes":
			b.MaxTotalBytes = n
		case "max_wall_clock_seconds":
			b.MaxWallClockSeconds = n
		default:
			return ToolBudget{}, fmt.Errorf("unsupported budget field %q", key)
		}
	}
	return b, nil
}

func canonicalizeVaultTools(tools []string) []string {
	out := cleanList(tools)
	if len(out) == 0 {
		return nil
	}
	// Stable storage order; registry_hash must not depend on input order.
	sort.Strings(out)
	return out
}

func canonicalizeCapabilities(caps []string) []string {
	out := cleanList(caps)
	if len(out) == 0 {
		return nil
	}
	// Normalize the Phase 5 / Phase 6 name pair so downstream checks see
	// only one form.
	for i, c := range out {
		if c == CapabilitySchedulerRunLogWrite {
			out[i] = CapabilityAgentRunLogWrite
		}
	}
	sort.Strings(out)
	// Dedup (sort puts duplicates adjacent).
	dedup := out[:0]
	for i, c := range out {
		if i == 0 || out[i-1] != c {
			dedup = append(dedup, c)
		}
	}
	return dedup
}

func normalizeScopeList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range cleanList(values) {
		clean, err := CleanRelativePattern(v)
		if err != nil {
			// Keep the raw value; downstream validateVaultScope will surface
			// the proper error context.
			out = append(out, v)
			continue
		}
		if !strings.HasSuffix(clean, "/") {
			clean = clean + "/"
		}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

func validateVaultTools(tools []string) error {
	allowed := map[string]bool{}
	for _, t := range AllowedVaultTools {
		allowed[t] = true
	}
	for _, t := range tools {
		if !allowed[t] {
			return fmt.Errorf("%q is not in the Phase 6 tool catalog %v", t, AllowedVaultTools)
		}
	}
	return nil
}

func validateVaultScope(scope, readRoots []string) error {
	for _, raw := range scope {
		if IsForbiddenScopePath(raw) {
			return fmt.Errorf("%q contains a forbidden prefix (.obsidian/.git/.trash/.DS_Store)", raw)
		}
		clean, err := cleanRelativePath(strings.TrimSuffix(raw, "/"))
		if err != nil {
			return err
		}
		if len(readRoots) == 0 {
			return fmt.Errorf("%q is not allowed because read_only_vault_roots is empty", clean)
		}
		if !isUnderAnyRoot(clean, readRoots) {
			return fmt.Errorf("%q is outside read_only_vault_roots", clean)
		}
	}
	return nil
}

// frontmatterDoc is the parse result of a YAML-lite frontmatter block. It
// preserves the distinction between scalar / list / submap so downstream
// validators can apply field-tier rules (Phase 6 SKILL.md vs. SCHEDULE.md
// field assignments).
type frontmatterDoc struct {
	scalars map[string]string
	lists   map[string][]string
	maps    map[string]map[string]string
}

func newFrontmatterDoc() frontmatterDoc {
	return frontmatterDoc{
		scalars: map[string]string{},
		lists:   map[string][]string{},
		maps:    map[string]map[string]string{},
	}
}

func (d frontmatterDoc) hasKey(key string) bool {
	if _, ok := d.scalars[key]; ok {
		return true
	}
	if _, ok := d.lists[key]; ok {
		return true
	}
	if _, ok := d.maps[key]; ok {
		return true
	}
	return false
}

func (d frontmatterDoc) scalar(key string) string {
	return strings.TrimSpace(d.scalars[key])
}

func (d frontmatterDoc) list(key string) []string {
	return append([]string(nil), d.lists[key]...)
}

func (d frontmatterDoc) subMap(key string) map[string]string {
	out := map[string]string{}
	for k, v := range d.maps[key] {
		out[k] = v
	}
	return out
}

// parseFrontmatterDoc is a thin extension of parseSimpleFrontmatter that
// keeps lists / submaps separate. The set of list-typed and submap-typed
// keys is fixed (matching the union of Phase 5 + Phase 6 SKILL.md / SCHEDULE.md
// schemas) so the parser stays simple and predictable.
func parseFrontmatterDoc(frontmatter string) (frontmatterDoc, error) {
	listKeys := map[string]bool{
		"delivery":              true,
		"capabilities":          true,
		"vault_context":         true,
		"external_info_sources": true,
		"vault_tools":           true,
		"vault_scope":           true,
	}
	mapKeys := map[string]bool{
		"skill_config": true,
		"budget":       true,
	}
	doc := newFrontmatterDoc()
	lines := strings.Split(frontmatter, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return doc, fmt.Errorf("unexpected indented line %q", strings.TrimSpace(line))
		}
		key, raw, ok := strings.Cut(line, ":")
		if !ok {
			return doc, fmt.Errorf("invalid frontmatter line %q", line)
		}
		key = strings.TrimSpace(key)
		raw = strings.TrimSpace(raw)
		switch {
		case listKeys[key]:
			if raw != "" {
				doc.lists[key] = cleanList(splitInlineList(raw))
				continue
			}
			items, next := parseIndentedList(lines, i+1)
			doc.lists[key] = cleanList(items)
			i = next - 1
		case mapKeys[key]:
			sub, next, err := parseIndentedMap(lines, i+1)
			if err != nil {
				return doc, err
			}
			doc.maps[key] = sub
			i = next - 1
		default:
			doc.scalars[key] = unquote(raw)
		}
	}
	return doc, nil
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

// splitFrontmatter is the scheduler's strict wrapper over the shared
// internal/markdown boundary rule: SCHEDULE.md must carry frontmatter, so the
// scheduler turns a missing block into an error instead of tolerating it. The
// two distinct messages stay as authoring diagnostics; the boundary logic
// itself lives in internal/markdown, the single source of truth.
func splitFrontmatter(content string) (string, string, error) {
	block, body, found := markdown.SplitFrontmatter(content)
	if found {
		return block, body, nil
	}
	normalized := strings.TrimPrefix(strings.ReplaceAll(content, "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", "", errors.New("frontmatter is required")
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
		clean, err := CleanRelativeDir(value)
		if err != nil {
			return nil, err
		}
		roots = append(roots, clean)
	}
	return roots, nil
}

func CleanRelativePattern(value string) (string, error) {
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

func CleanRelativeDir(value string) (string, error) {
	clean, err := CleanRelativePattern(value)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(clean, "*?[") {
		return "", fmt.Errorf("wildcards are not allowed in scheduled_skill_roots")
	}
	return strings.TrimRight(clean, "/") + "/", nil
}

func cleanRelativePath(value string) (string, error) {
	clean, err := CleanRelativePattern(value)
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
