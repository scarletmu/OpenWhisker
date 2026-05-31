# Enrich Package Index

This package implements Phase 7 inbox enrichment: an async pass that gives newly-captured Raw notes vault-native `topic/*` / `skill/*` tags by running a tool-calling agent against the vault and writing the result back through the `rewrite_note` policy guard.

Module layout:

- `queue.go`: thin connector between the SQLite `enrich_jobs` table (DAO lives in `internal/storage`) and the rest of the package.
- `enrich.go`: one-job orchestration — vocab lookup → content hash guard → agent run → plan → policy → executor. Holds `Service`, `Config`, `EnrichResult`, `RouteSuggestion`, `NewTagCand`, and the `AgentRunner` interface.
- `worker.go`: daemon goroutine that drains the queue serially (one run per vault at a time; the queue `Claim` transition is the serializer).
- `render.go`: pure rendering primitive (`applyEnrichToFrontmatter`) that merges the writable frontmatter fields.
- `scan.go`: enqueues eligible Raw files, skipping bucket files and already-enriched notes.

Dependencies / boundaries:

- Reads the controlled tag vocabulary from `internal/memory` (`Service` via a vocab adapter); unknown `topic/*` tags are rejected.
- Enrichment policy / the `rewrite_note` guard live in `internal/policy` (`internal/policy/enrich.go`).
- Does not write the vault directly — durable changes go through `VaultPlan -> policy -> approval when needed -> executor`.

Authoritative contract: `docs/phases/phase-7-inbox-enrichment.md`.
