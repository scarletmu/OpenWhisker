# Internal Module Index

This directory contains the OpenWhisker implementation boundary.

Start here after reading:

- `docs/README.md`
- `docs/20-architecture/system-overview.md`
- the relevant subsystem design under `docs/30-design/`

## Module map

Write-path core (the spine: `VaultPlan -> policy -> approval when needed -> executor`):

- `model/`: shared domain records, statuses, operation payloads, diffs, logs, and content hashing.
- `core/`: use-case orchestration for ingest, plan preparation, approval, rejection, and apply.
- `policy/`: deterministic validation for risk, paths, approval, allowed operation types, and enrichment (`rewrite_note`) guards.
- `executor/`: controlled vault filesystem preparation, hash-guarded writes, and proposal-note emission.
- `storage/`: SQLite persistence for jobs, plans, outbox, locks, operation logs, and the enrich / memory derived tables.

Vault I/O and content helpers:

- `vault/`: the wikilink / backlink index (`linkindex/`) over the vault, with live `fsnotify` updates; a read-only graph consumed by `memory/`.
- `markdown/`: frontmatter split + parse — the single source of truth for frontmatter. Contract: `docs/30-design/frontmatter-parsing.md`.
- `sanitize/`: redaction of secrets / PII in config keys, feed URLs, Skill config, and free text before content leaves the trust boundary (prompts, logs).
- `safehttp/`: the single SSRF guard for every outbound HTTP caller (RSS adapter, link clipper) — public-IP / reserved-host checks and the guarded `http.Client`.

LLM reasoning (generates plans, never writes):

- `agent/`: LLM-backed reasoning adapters — Raw Organizer, Knowledge Expander, Intent Classifier, Scheduler Engine. See `internal/agent/AGENTS.md`.
- `agentdispatch/`: the `Dispatcher` that runs tool-calling agent loops against the configured provider.

Feature subsystems:

- `enrich/`: Phase 7 inbox enrichment — an async agent pass that tags newly-captured Raw notes via the `rewrite_note` guard. See `internal/enrich/AGENTS.md`. Contract: `docs/30-design/inbox-enrichment.md`.
- `memory/`: Phase 8 recall service — controlled tag vocabulary plus tag / text / one-hop-related recall over a SQLite cache derived from frontmatter. See `internal/memory/AGENTS.md`. Contract: `docs/30-design/memory-recall.md`.
- `scheduler/`: read-only scheduled Skill registry parsing, cron matching, budget / scope lint, an RSS source adapter, and Skill runner boundaries. See `internal/scheduler/AGENTS.md`. Contract: `docs/30-design/scheduler.md`.
- `clip/`: §3.2 link clipping — fetch a captured URL, extract its article to Markdown, and write a `Raw/Sources/` web-clip note through the plan→policy→executor spine; background worker + straggler scan. See `internal/clip/CLAUDE.md`. Contract: `docs/30-design/im-quick-capture.md` §3.2.

Context and external entry:

- `profile/`: vault profile records, tool-budget defaults, and task skill generation for skill-driven adapter context. Profile analysis should happen in vault-local skills, not inside OpenWhisker runtime.
- `adapters/`: external entry adapters — Matrix private IM intake and outbox delivery (package `matrix`). Contract: `docs/30-design/matrix-adapter.md`.

Keep LLM reasoning out of direct writes. New write paths should still flow through `VaultPlan -> policy -> approval when needed -> executor`.
