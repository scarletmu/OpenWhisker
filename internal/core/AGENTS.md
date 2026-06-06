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

- `ingest.go`: low-risk raw capture and capture-bucket flow.
- `plans.go`: medium-risk organize, diff, approve, and reject lifecycle (PlanService).
- `adapter.go`: external adapter command routing (AdapterService); IM intake and outbox.
- `intent_router.go`: rules / hybrid / off intent classification and clarification state machine.
- `scheduler.go` / `scheduler_status.go` / `scheduler_suggestions.go`: read-only scheduler tick, status, and suggested-capture acceptance.
- `render.go`: pure presentation — markdown diff/preview/summary helpers and note renderers. No SQL, no IO.
- `context.go`: LLM vault-context assembly (raw / expander) and frontmatter source-trace parsing.
- `*_test.go`: behavior anchors for workflow lifecycle.

`render.go` and `context.go` hold only free functions extracted from the files above; keep new presentation / context-building helpers there rather than growing the service files.

Related docs:

- `docs/phases/phase-1-minimal-slice.md`
- `docs/phases/phase-2-approval-diff.md`
- `docs/architecture/overview.md`

Do not put filesystem safety or SQL details here; use `internal/executor` and `internal/storage`.
