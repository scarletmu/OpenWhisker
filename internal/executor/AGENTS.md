# Executor Package Index

This package is the controlled vault write boundary.

Primary responsibilities:

- Resolve target paths under the configured vault root.
- Prepare diffs and `before_hash` values before approval.
- Apply approved `VaultOperation` values with locks and hash guards.
- Record applied operation logs through storage.

Primary files:

- `direct_fs.go`: prepare/apply implementation for local filesystem vault writes.
- `path_guard.go`: vault-root path resolution and escape protection.
- `*_test.go`: path and write-safety behavior anchors.

Related docs:

- `docs/architecture/design-philosophy.md`
- `docs/phases/phase-1-minimal-slice.md`
- `docs/phases/phase-2-approval-diff.md`

Do not give LLM code a bypass around this package for vault writes.
