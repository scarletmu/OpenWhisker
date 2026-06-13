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

- `docs/20-architecture/design-philosophy.md`
- `docs/20-architecture/system-overview.md`
- `docs/80-decisions/project-decisions.md`

Keep model changes compatible with `internal/storage` schema and existing phase documents.
