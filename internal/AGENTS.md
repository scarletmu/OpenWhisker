# Internal Module Index

This directory contains the OpenWhisker implementation boundary.

Start here after reading:

- `docs/README.md`
- `docs/architecture/overview.md`
- the active phase document in `docs/phases/`

## Module map

Write-path core (the spine: `VaultPlan -> policy -> approval when needed -> executor`):

- `model/`: shared domain records, statuses, operation payloads, diffs, logs, and content hashing.
- `core/`: use-case orchestration for ingest, plan preparation, approval, rejection, and apply.
- `policy/`: deterministic validation for risk, paths, approval, allowed operation types, and enrichment (`rewrite_note`) guards.
- `executor/`: controlled vault filesystem preparation, hash-guarded writes, and proposal-note emission.
- `storage/`: SQLite persistence for jobs, plans, outbox, locks, operation logs, and the enrich / memory derived tables.

Vault I/O and content helpers:

- `vault/`: the wikilink / backlink index (`linkindex/`) over the vault, with live `fsnotify` updates; a read-only graph consumed by `memory/`.
- `markdown/`: frontmatter split + parse — the single source of truth for frontmatter. Contract: `docs/architecture/frontmatter-parsing.md`.
- `sanitize/`: redaction of secrets / PII in config keys, feed URLs, Skill config, and free text before content leaves the trust boundary (prompts, logs).

LLM reasoning (generates plans, never writes):

- `agent/`: LLM-backed reasoning adapters — Raw Organizer, Knowledge Expander, Intent Classifier, Scheduler Engine. See `internal/agent/AGENTS.md`.
- `agentdispatch/`: the `Dispatcher` that runs tool-calling agent loops against the configured provider.

Feature subsystems:

- `enrich/`: Phase 7 inbox enrichment — an async agent pass that tags newly-captured Raw notes via the `rewrite_note` guard. See `internal/enrich/AGENTS.md`. Contract: `docs/phases/phase-7-inbox-enrichment.md`.
- `memory/`: Phase 8 recall service — controlled tag vocabulary plus tag / text / one-hop-related recall over a SQLite cache derived from frontmatter. See `internal/memory/AGENTS.md`. Contract: `docs/phases/phase-8-memory-recall.md`.
- `scheduler/`: read-only scheduled Skill registry parsing, cron matching, budget / scope lint, an RSS source adapter, and Skill runner boundaries. See `internal/scheduler/AGENTS.md`. Contracts: `docs/phases/phase-5-read-only-skill-scheduler.md`, `docs/phases/phase-6-scheduler-skill-creator.md`.

Context and external entry:

- `profile/`: vault profile records, tool-budget defaults, and task skill generation for skill-driven adapter context. Profile analysis should happen in vault-local skills, not inside OpenWhisker runtime.
- `adapters/`: external entry adapters — Matrix private IM intake and outbox delivery (package `matrix`). Contract: `docs/adapters/matrix-private-im.md`.

Keep LLM reasoning out of direct writes. New write paths should still flow through `VaultPlan -> policy -> approval when needed -> executor`.
