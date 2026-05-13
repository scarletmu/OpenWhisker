# Internal Module Index

This directory contains the OpenWhisker implementation boundary.

Start here after reading:

- `docs/README.md`
- `docs/architecture/overview.md`
- the active phase document in `docs/phases/`

Module map:

- `model/`: shared domain records, statuses, operation payloads, diffs, and logs.
- `core/`: use-case orchestration for ingest, plan preparation, approval, rejection, and apply.
- `policy/`: deterministic validation for risk, paths, approval, and allowed operation types.
- `executor/`: controlled vault filesystem preparation and writes.
- `storage/`: SQLite persistence for jobs, plans, outbox, locks, and operation logs.

Keep LLM reasoning out of direct writes. New write paths should still flow through `VaultPlan -> policy -> approval when needed -> executor`.
