# Vault Profile Analyzer Skill

Use this skill inside a user's own Obsidian vault to produce a reviewed `VaultProfile` and task `VaultSkill` files for OpenWhisker.

This skill is intentionally vault-local. It should be run by the user or their local agent in the vault workspace, not by the OpenWhisker service runtime.

## Goal

Generate a candidate profile bundle that the vault owner can review, edit, and then choose to expose to OpenWhisker.

Recommended outputs:

- `OpenWhisker/VaultProfile.md`
- `OpenWhisker/VaultRawOrganizerSkill.md`
- Optional `OpenWhisker/ProfileAnalysisReport.md`

## Read Scope

Read only local vault rule and workflow files selected by the user.

Good default inputs:

- `AGENTS.md`
- `Meta/README.md`
- `Meta/Knowledge-Base-Data-Organization-Process.md`
- `Meta/LLM-Workflow.md`
- `Meta/Tagging.md`
- `Raw/AGENTS.md`
- `Knowledge/AGENTS.md`
- relevant existing vault-local `Skills/**/SKILL.md`

Do not read:

- `.obsidian/`
- `.git/`
- hidden files or directories
- secrets, tokens, local databases, or sync state
- unrelated personal notes unless the user explicitly selects them as examples

## Output Rules

`VaultProfile.md` should describe observed local facts, not universal OpenWhisker rules:

- profile id
- raw inbox directory
- raw processed directory
- knowledge directory
- draft output directory
- required draft tags, if any
- known protected or ignored areas
- source documents used as evidence
- unresolved questions for the owner

`VaultRawOrganizerSkill.md` should be task guidance compiled from the reviewed profile:

- where raw input comes from
- where draft output should go
- what source traceability must remain visible
- what tags or frontmatter are expected
- what the LLM should avoid doing
- how to mark uncertainty and review items

## Review Requirement

Treat all generated files as candidates until the vault owner approves them.

Do not claim that OpenWhisker will use these files automatically. OpenWhisker should only consume profile or skill material that the user has explicitly reviewed and configured.

## Safety Boundary

This skill can read and write inside the user's vault when the user runs it there. It must not call OpenWhisker APIs, approve OpenWhisker plans, write OpenWhisker databases, or bypass OpenWhisker's `VaultPlan -> policy -> approval -> executor` flow.
