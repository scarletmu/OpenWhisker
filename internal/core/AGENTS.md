# Core Package Index

This package owns workflow orchestration.

Primary responsibilities:

- Create and update `WikiJob` lifecycle state.
- Build deterministic Phase 1 and Phase 2 `VaultPlan` values.
- Call policy checks before prepare or apply.
- Prepare approval diffs and route approve/reject actions.
- Emit user-visible outbox messages.
- Route `RawOrganizer` and `KnowledgeExpander` LLM-backed plans through prepare / approve. High-risk plans skip `executor.Prepare` and reach `awaiting_approval` via `prepareHighRiskApprovalPlan`; `Approve` then writes a proposal note via the 4C.1 path.

Primary files:

- `ingest.go`: low-risk raw capture flow.
- `plans.go`: medium-risk organize, diff, approve, and reject flow.
- `*_test.go`: behavior anchors for workflow lifecycle.

Related docs:

- `docs/phases/phase-1-minimal-slice.md`
- `docs/phases/phase-2-approval-diff.md`
- `docs/architecture/overview.md`

Do not put filesystem safety or SQL details here; use `internal/executor` and `internal/storage`.
