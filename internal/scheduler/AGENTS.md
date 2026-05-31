# Scheduler Package Index

This package owns the read-only scheduled Skill subsystem (Phase 5, extended by Phase 6 Skill creation).

Responsibilities:

- Load and lint the scheduled Skill registry from the vault, enforcing read roots, vault-tool / scope allowlists, and tool budgets against the active `profile.VaultProfile`.
- Parse and match cron expressions to decide which Skills are due.
- Assemble a read-only Skill bundle (vault context + external info) and hand it to a `SkillRunner` / `SkillEngine`; LLM execution itself lives in `internal/agent` (`OpenAISchedulerEngine`).
- Adapt external read-only sources (RSS) into scheduler inputs via the `ExternalInfoAdapter`.

Key files:

- `registry.go`: `LoadRegistry`, `LintAgentSkillDir`, `ScheduledSkill`, `ToolBudget`, scope / vault-tool validation, agent-skill markdown parsing.
- `cron.go`: `CronSchedule` / `ParseCron` and cron field matching.
- `runner.go`: `SkillRunner` / `SkillEngine` interfaces, `StaticSkillRunner`, request/result envelopes, and no-follow reads of vault context files.
- `rss_adapter.go`: read-only RSS `ExternalInfoAdapter`.

Boundaries:

- Read-only. This package must not write the vault. Vault context reads use no-follow opens and honor forbidden-scope checks (`IsForbiddenScopePath`).
- Any resulting change still flows through `VaultPlan -> policy -> approval when needed -> executor`.

Authoritative contracts: `docs/phases/phase-5-read-only-skill-scheduler.md`, and `docs/phases/phase-6-scheduler-skill-creator.md` for Skill creation.
