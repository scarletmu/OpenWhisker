# Executor Package Index

This package is the controlled vault write boundary.

Primary responsibilities:

- Resolve target paths under the configured vault root.
- Prepare diffs and `before_hash` values before approval.
- Apply approved `VaultOperation` values with locks and hash guards.
- Render approved high-risk plans into proposal notes under the profile-supplied agent-proposals directory (passed by `core` as `baseDir`; default `Raw/Agent-Proposals/`) instead of executing the operations (`ApplyAsProposal`, `RenderProposalNote`).
- Record applied operation logs through storage; proposal writes carry `Outcome=proposed` for auditing.

Primary files:

- `direct_fs.go`: prepare/apply implementation for local filesystem vault writes.
- `proposal_note.go`: high-risk plan → proposal note rendering and the `ApplyAsProposal` write path.
- `path_guard.go`: vault-root path resolution and escape protection.
- `*_test.go`: path and write-safety behavior anchors.

Related docs:

- `docs/20-architecture/design-philosophy.md`
- `docs/40-api/proposal-note-schema.md`

Do not give LLM code a bypass around this package for vault writes.
