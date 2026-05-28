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

Provider protocol notes (OpenAI-compatible surface, verified against vendor docs):

- `json_object` is used instead of `json_schema` because DeepSeek (the official target provider) only supports `json_object`. The prompt must literally contain the word "json".
- `reasoning_content` round-trip (thinking-mode models, e.g. `deepseek-v4-flash`): the API returns `reasoning_content` at the same level as `content`. For an assistant turn that performed a `tool_call`, its `reasoning_content` MUST be passed back in all subsequent requests or the API returns 400; for non-tool turns it is ignored if present. `ChatMessage` carries a `ReasoningContent` field and the tool-calling engine re-appends the whole assistant message, so it round-trips verbatim — safe in thinking mode, and a no-op for non-thinking models (`deepseek-chat`) which never emit it. Note `deepseek-reasoner` (R1) has the OPPOSITE rule (must NOT pass it back) but does not support tool calls, so the multi-turn engine never round-trips with it. Ref: https://api-docs.deepseek.com/guides/thinking_mode.
- When parsing a provider response, do not silently drop fields the struct doesn't model if they may need to be echoed back in a later request (the `reasoning_content` 400 was exactly this lossy-round-trip class of bug).

Do not write vault files, execute shell commands, call Obsidian CLI, or bypass policy / approval / executor flow from this package.
