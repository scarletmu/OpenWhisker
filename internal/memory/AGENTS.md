# Memory Package Index

This package is the shared Phase 8 memory recall service.

It exposes:

- `KnownTags(prefix)` / `HasTag(tag)` — the vault-native controlled tag vocabulary read by the Phase 7 enrich agent and future writers.
- `Recall(req)` — tag / text / one-hop-related recall used by the Phase 6 `recall_memory` tool and Go-side callers.

Source-of-truth model:

- Memory owns no new truth source. Frontmatter is the source; SQLite (`memory_tag_index`, `memory_known_tags`, `memory_recalls`) is a derived cache; the `related` adjacency graph is borrowed from `internal/vault/linkindex`.

Key files:

- `service.go`: `Service` / `Config`, lifecycle (rescan, ready/wait-ready), and the `ApplyObserver` hook that keeps the index fresh on rewrites.
- `recall.go`: `RecallRequest` / `RecallItem` / `RecallResult` and the three recall passes (tag, text, one-hop related); excludes `Archive/` by default and supports scope-dir filtering.
- `frontmatter.go`: `ExtractFrontmatterTags` (frontmatter parsing via `internal/markdown`).
- `linkgraph.go`: `LinkGraph` interface + `LinkIndexAdapter` (frontmatter `related:` edges only — body wikilinks are intentionally excluded).
- `textsearch.go`: `TextSearcher` / `NewFSTextSearcher` for Recall Pass 2 literal search.

Boundaries:

- Recall informs plans; it is an input to reasoning, never a vault write path.

Authoritative contract: `docs/phases/phase-8-memory-recall.md`. Read it before changing recall behavior or the derived-cache shape.
