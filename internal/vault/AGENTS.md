# Vault Package Index

This package currently houses the vault link graph.

- `linkindex/`: a wikilink / backlink index over the vault (`Index`, `New(vaultRoot, readRoots)`). It walks notes under the configured read roots, parses body wikilinks and frontmatter `related:` edges, resolves targets (leaving ambiguous targets unresolved), builds backlinks, and keeps itself fresh with `fsnotify`. It skips forbidden directories and tracks a generation counter so consumers can detect rebuilds.

Consumers:

- `internal/memory` borrows the `related` adjacency from here for Recall Pass 3 (one-hop related diffusion).

Boundaries:

- Read-oriented. Vault mutation is owned by `internal/executor` (hash-guarded writes). Do not write the vault from this package; produce a `VaultPlan` and route it through policy -> approval -> executor.
