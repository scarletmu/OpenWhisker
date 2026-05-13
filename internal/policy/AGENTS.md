# Policy Package Index

This package validates plans before approval or execution.

Primary responsibilities:

- Auto-allow only low-risk raw capture/report plans.
- Require approval for medium-risk knowledge organization plans.
- Validate clean relative vault paths.
- Enforce allowed operation types and target directories.
- Require `before_hash` for approved append and move operations.

Primary files:

- `checker.go`: current deterministic policy checker.
- `checker_test.go`: behavior anchors for policy limits.

Related docs:

- `docs/architecture/design-philosophy.md`
- `docs/phases/phase-1-minimal-slice.md`
- `docs/phases/phase-2-approval-diff.md`

Policy changes should be made before executor changes when enabling new write behavior.
