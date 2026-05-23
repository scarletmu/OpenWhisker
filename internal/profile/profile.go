package profile

import (
	"strings"

	"github.com/scarletmu/openwhisker/internal/policy"
)

const (
	ProfileDocumentPath        = "OpenWhisker/VaultProfile.md"
	RawOrganizerSkillPath      = "OpenWhisker/VaultRawOrganizerSkill.md"
	RawOrganizerSkillName      = "vault-raw-organizer"
	KnowledgeExpanderSkillPath = "OpenWhisker/VaultKnowledgeExpanderSkill.md"
	KnowledgeExpanderSkillName = "vault-knowledge-expander"
	ProfileStatusConfigured    = "configured"
	ProfileSourceManualConfig  = "manual-config"
	ProfileSourceVaultSkill    = "vault-local-skill"
)

type VaultProfile struct {
	ID          string             `json:"id"`
	Status      string             `json:"status"`
	Source      string             `json:"source"`
	Conventions policy.Conventions `json:"conventions"`
	Scheduler   SchedulerProfile   `json:"scheduler,omitempty"`
	Notes       []string           `json:"notes,omitempty"`
}

type SchedulerProfile struct {
	Enabled             bool     `json:"enabled"`
	RegistryPaths       []string `json:"registry_paths,omitempty"`
	ScheduledSkillRoots []string `json:"scheduled_skill_roots,omitempty"`
	DefaultDelivery     []string `json:"default_delivery,omitempty"`
	ReadOnlyVaultRoots  []string `json:"read_only_vault_roots,omitempty"`
	ExternalInfoSources []string `json:"external_info_sources,omitempty"`
}

type VaultSkill struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Purpose string `json:"purpose"`
	Content string `json:"content"`
}

type Bundle struct {
	Profile VaultProfile `json:"profile"`
	Skills  []VaultSkill `json:"skills"`
}

type Document struct {
	Path    string
	Content string
}

func NewConfiguredVaultProfile(conventions policy.Conventions) VaultProfile {
	conventions = conventions.Normalize()
	schedulerProfile := SchedulerProfile{}
	if conventions.ProfileID == "knowledge-vault" {
		schedulerProfile = DefaultSchedulerProfile()
	}
	return VaultProfile{
		ID:          conventions.ProfileID,
		Status:      ProfileStatusConfigured,
		Source:      ProfileSourceManualConfig,
		Conventions: conventions,
		Scheduler:   schedulerProfile,
		Notes: []string{
			"This profile is currently built from explicit OpenWhisker configuration.",
			"Future profile files should be produced by a vault-local skill and approved by the vault owner before OpenWhisker consumes them.",
		},
	}
}

func DefaultSchedulerProfile() SchedulerProfile {
	return SchedulerProfile{
		Enabled:             true,
		RegistryPaths:       []string{"Scheduler/Skills/*/SCHEDULE.md"},
		ScheduledSkillRoots: []string{"Scheduler/Skills/"},
		DefaultDelivery:     []string{"outbox"},
		ReadOnlyVaultRoots:  []string{"Raw/", "Knowledge/", "Interview/", "Life/", "Meta/"},
		ExternalInfoSources: []string{"rss"},
	}
}

func BuildBundle(conventions policy.Conventions) Bundle {
	vaultProfile := NewConfiguredVaultProfile(conventions)
	return Bundle{
		Profile: vaultProfile,
		Skills: []VaultSkill{
			RawOrganizerSkill(vaultProfile),
		},
	}
}

func ContextDocuments(conventions policy.Conventions) []Document {
	vaultProfile := NewConfiguredVaultProfile(conventions)
	skill := RawOrganizerSkill(vaultProfile)
	return []Document{
		{Path: skill.Path, Content: skill.Content},
		{Path: ProfileDocumentPath, Content: RenderProfileMarkdown(vaultProfile)},
	}
}

func KnowledgeExpanderContextDocuments(conventions policy.Conventions) []Document {
	vaultProfile := NewConfiguredVaultProfile(conventions)
	skill := KnowledgeExpanderSkill(vaultProfile)
	return []Document{
		{Path: skill.Path, Content: skill.Content},
		{Path: ProfileDocumentPath, Content: RenderProfileMarkdown(vaultProfile)},
	}
}

func KnowledgeExpanderSkill(vaultProfile VaultProfile) VaultSkill {
	conventions := vaultProfile.Conventions.Normalize()
	return VaultSkill{
		Name:    KnowledgeExpanderSkillName,
		Path:    KnowledgeExpanderSkillPath,
		Purpose: "expand an existing thin Knowledge note into a reviewable medium-risk plan, or surface a high-risk restructure proposal when boundaries are unclear",
		Content: strings.Join([]string{
			"# OpenWhisker Vault Knowledge Expander Skill",
			"",
			"Purpose: expand an existing thin Knowledge note into a reviewable medium-risk plan, or surface a high-risk restructure proposal when topic boundaries are unclear.",
			"",
			"This task skill is compiled from the current VaultProfile. Treat it as the main vault-specific guidance for this run; the profile is included only as the auditable fact summary behind the skill.",
			"",
			"Profile facts used by this skill:",
			"- Profile id: " + conventions.ProfileID,
			"- Knowledge directory: " + conventions.KnowledgeDir,
			"- Draft output directory: " + conventions.KnowledgeDraftDir,
			"- Raw processed directory: " + conventions.RawProcessedDir,
			"- Required draft tags: " + renderInlineList(conventions.RequiredDraftTags),
			"",
			"Rules:",
			"- Output one JSON object with `kind` one of `append` / `propose_restructure`.",
			"- For `append`, return a Markdown H2-level section to append to the target note; do not rewrite or replace existing content.",
			"- This skill does NOT directly create new child notes. If the target note actually needs a new child note, a split, a merge, a rename, or bulk tag / link changes, return `kind = propose_restructure` with a clear `rationale` and the full `affected_paths` list. OpenWhisker will turn that into a high-risk proposal note for the human to review and execute manually (often via a stronger external tool).",
			"- For `propose_restructure`, return one of split / merge / rename / bulk-retag / bulk-link-rewrite together with `rationale` and the full `affected_paths` list. Do not attempt to execute the restructure directly.",
			"- Write human-facing note content in Chinese by default; keep fixed technical terms, paths, property names, tag values, commands, APIs, library names, and protocol names in English.",
			"- Preserve source traceability: surface the target Knowledge note path and any related Raw/Processed paths in `review_items` when the inferred content depends on them.",
			"- Mark uncertain facts, version-sensitive claims, missing sources, and inferred content under `review_items`.",
			"- Do not propose writes outside the configured Knowledge directory.",
			"- Do not ask to write files, run shell, call Obsidian CLI, approve plans, or bypass policy.",
			"",
			"Local OpenWhisker policy validates paths, OpenWhisker trace metadata, profile-specific tags, source links, risk level, operations, and approval before any vault write. High-risk plans are routed to a proposal note in Raw/Agent-Proposals/ instead of touching Knowledge/.",
		}, "\n"),
	}
}

func RawOrganizerSkill(vaultProfile VaultProfile) VaultSkill {
	conventions := vaultProfile.Conventions.Normalize()
	return VaultSkill{
		Name:    RawOrganizerSkillName,
		Path:    RawOrganizerSkillPath,
		Purpose: "turn raw vault input into a reviewable Knowledge draft plan for this specific vault",
		Content: strings.Join([]string{
			"# OpenWhisker Vault Raw Organizer Skill",
			"",
			"Purpose: turn raw vault input into a reviewable Knowledge draft plan for this specific vault.",
			"",
			"This task skill is compiled from the current VaultProfile. Treat it as the main vault-specific guidance for this run; the profile is included only as the auditable fact summary behind the skill.",
			"",
			"Profile facts used by this skill:",
			"- Profile id: " + conventions.ProfileID,
			"- Raw inbox directory: " + conventions.RawInboxDir,
			"- Raw processed directory: " + conventions.RawProcessedDir,
			"- Knowledge directory: " + conventions.KnowledgeDir,
			"- Draft output directory: " + conventions.KnowledgeDraftDir,
			"- Required draft tags: " + renderInlineList(conventions.RequiredDraftTags),
			"",
			"Rules:",
			"- Write human-facing note content in Chinese by default; keep fixed technical terms, paths, property names, tag values, commands, APIs, library names, and protocol names in English.",
			"- Create only a draft under the configured draft output directory.",
			"- Move processed raw only to the configured raw processed directory after approval.",
			"- Do not place unprocessed raw content directly into long-lived knowledge areas.",
			"- Preserve source traceability: keep the raw job id, raw source path, and processed raw path visible in the output.",
			"- Produce a draft that is useful for review, not a final evergreen note.",
			"- Mark uncertain facts, version-sensitive claims, missing sources, and inferred content under 待核查.",
			"- Use the required draft tags listed above. If none are listed, keep tags minimal.",
			"- Prefer one draft for a simple concept seed. If the raw input is mixed, describe suggested splits instead of creating many outputs.",
			"- Do not ask to write files, run shell, call Obsidian CLI, approve plans, or bypass policy.",
			"",
			"Local OpenWhisker policy will validate paths, OpenWhisker trace metadata, profile-specific tags, source links, risk level, operations, and approval before any vault write.",
		}, "\n"),
	}
}

func RenderProfileMarkdown(vaultProfile VaultProfile) string {
	conventions := vaultProfile.Conventions.Normalize()
	return strings.Join([]string{
		"# OpenWhisker Vault Profile",
		"",
		"This is the current vault's local convention summary, not a universal OpenWhisker schema. Long term, this should be generated by analyzing the vault's local rules and then approved by the vault owner.",
		"",
		"- Profile id: " + conventions.ProfileID,
		"- Profile status: " + strings.TrimSpace(vaultProfile.Status),
		"- Profile source: " + strings.TrimSpace(vaultProfile.Source),
		"- Raw inbox directory: " + conventions.RawInboxDir,
		"- Raw processed directory: " + conventions.RawProcessedDir,
		"- Knowledge directory: " + conventions.KnowledgeDir,
		"- Draft output directory: " + conventions.KnowledgeDraftDir,
		"- Required draft tags: " + renderInlineList(conventions.RequiredDraftTags),
		"- Scheduler enabled: " + renderBool(vaultProfile.Scheduler.Enabled),
		"- Scheduler registry paths: " + renderInlineList(vaultProfile.Scheduler.RegistryPaths),
		"- Scheduler skill roots: " + renderInlineList(vaultProfile.Scheduler.ScheduledSkillRoots),
		"- Scheduler default delivery: " + renderInlineList(vaultProfile.Scheduler.DefaultDelivery),
		"- Scheduler read-only vault roots: " + renderInlineList(vaultProfile.Scheduler.ReadOnlyVaultRoots),
		"- Scheduler external info sources: " + renderInlineList(vaultProfile.Scheduler.ExternalInfoSources),
		"",
		"Runtime prompts should primarily use the task-specific Vault Skill compiled from this profile. OpenWhisker-required trace metadata still applies: openwhisker_job_id, source_raw_job_id, source_raw_path, source_processed_path, status=draft, needs_review=true.",
	}, "\n")
}

func renderInlineList(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

func renderBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
