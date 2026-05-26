# Agent Package Index

This package owns LLM-backed reasoning adapters.

Primary responsibilities:

- Convert controlled core context into provider prompts.
- Call configured LLM providers.
- Parse and validate structured model output.
- Return `VaultPlan` values through the core `RawOrganizer` and `KnowledgeExpander` contracts.

Implemented providers:

- `OpenAIRawOrganizer`: organize raw captures into medium-risk drafts. Contract: `docs/architecture/` (Phase 4B section).
- `OpenAIKnowledgeExpander`: expand thin Knowledge notes into medium-risk append / create-child plans, or surface high-risk restructure proposals. Contract: `docs/architecture/knowledge-expander-model-contract.md`.
- `OpenAIIntentClassifier`: classify IM inputs into controlled router intents. Contract: `docs/architecture/intent-router-model-contract.md`.
- `OpenAISchedulerEngine`: turn a Scheduler Host-provided read-only skill bundle into a scheduler result envelope. Contract: `docs/phases/phase-5-read-only-skill-scheduler.md`.

All providers share the same `OpenAICompatibleClient` + `json_object` + client-side hard validation + single empty-content retry pattern.

Do not write vault files, execute shell commands, call Obsidian CLI, or bypass policy / approval / executor flow from this package.
