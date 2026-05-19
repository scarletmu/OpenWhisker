# Policy Package Index

This package validates plans before approval or execution.

Primary responsibilities:

- Auto-allow only low-risk raw capture/report plans.
- Require approval for medium-risk knowledge organization plans.
- Require approval for high-risk restructuring plans; on approval the executor writes a proposal note instead of applying operations (see `CheckForApprovalHighRisk` and `ClassifyProposalKind`).
- Validate clean relative vault paths.
- Enforce allowed operation types and target directories.
- Require `before_hash` for approved append and move operations.

Primary files:

- `checker.go`: current deterministic policy checker.
- `checker_test.go`: behavior anchors for policy limits.

Related docs:

- `docs/architecture/design-philosophy.md`
- `docs/architecture/proposal-note-schema.md`
- `docs/phases/phase-1-minimal-slice.md`
- `docs/phases/phase-2-approval-diff.md`
- `docs/phases/phase-4-wiki-agent-workflow.md` (Phase 4C.1)

Policy changes should be made before executor changes when enabling new write behavior.
