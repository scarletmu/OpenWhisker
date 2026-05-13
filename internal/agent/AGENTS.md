# Agent Package Index

This package owns LLM-backed reasoning adapters.

Primary responsibilities:

- Convert controlled core context into provider prompts.
- Call configured LLM providers.
- Parse and validate structured model output.
- Return `VaultPlan` values through the core `RawOrganizer` contract.

Do not write vault files, execute shell commands, call Obsidian CLI, or bypass policy / approval / executor flow from this package.
