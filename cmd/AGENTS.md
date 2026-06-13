# Command Index

This directory contains executable entrypoints only.

Navigation:

- `cmd/openwhisker/` is the current CLI.
- Read `docs/README.md` and `docs/20-architecture/system-overview.md` before changing command behavior.
- Command code should delegate workflow logic to `internal/core`.

Do not put vault policy, executor behavior, or storage schema decisions in `cmd/`.
