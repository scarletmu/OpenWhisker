# OpenWhisker CLI Index

This package wires local CLI commands to core services.

Current commands:

- `ingest raw` captures raw text into `Raw/Inbox/`.
- `organize last` prepares a medium-risk organization plan.
- `plan diff`, `plan approve`, and `plan reject` drive approval flow.
- `vault profile preview` renders the current Profile / Skill bundle without calling LLMs or writing the vault.
- `jobs show` exposes stored job state for debugging.

Primary files:

- `main.go` handles flags, stdin/stdout, JSON output, and service construction.

Follow:

- Workflow behavior: `internal/core/AGENTS.md`
- Persistent state: `internal/storage/AGENTS.md`
- Policy and write safety: `internal/policy/AGENTS.md`, `internal/executor/AGENTS.md`
