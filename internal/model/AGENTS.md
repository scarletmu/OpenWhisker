# Model Package Index

This package defines shared OpenWhisker domain records.

Core concepts:

- `WikiJob`
- `VaultPlan`
- `VaultOperation`
- `VaultDiff`
- `VaultApplyResult`
- `VaultOperationLog`
- `OutboxMessage`

Primary files:

- `types.go`: durable object shapes, statuses, risks, operation types, and payload structs.
- `id.go`: local ID generation.

Related docs:

- `docs/architecture/design-philosophy.md`
- `docs/architecture/overview.md`
- `docs/project-decisions.md`

Keep model changes compatible with `internal/storage` schema and existing phase documents.
