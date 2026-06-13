# Storage Package Index

This package owns local SQLite persistence.

Primary responsibilities:

- Store `WikiJob` lifecycle state.
- Store `VaultPlan` operations, diffs, timestamps, approval state, and errors.
- Store operation logs and outbox messages.
- Provide minimal vault path locks during apply.

Primary files:

- `schema.go`: migration and table definitions.
- `store.go`: persistence API used by `internal/core` and `internal/executor`.

Related docs:

- `docs/20-architecture/system-overview.md`

Keep migrations additive unless the user explicitly asks for a breaking local database reset.
