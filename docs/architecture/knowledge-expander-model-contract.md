# Knowledge Expander Contract

状态：spec only。4C.2 实现进行中。配套实现位于 `internal/agent/openai_knowledge_expander.go`（待落地）和 `internal/core/plans.go` 的 `KnowledgeExpander` 接口（待落地）。high-risk 降级出口复用 [`proposal-note-schema.md`](proposal-note-schema.md) 与 4C.1 流程。

本文定义 Phase 4C.2 Knowledge Expander 使用的 LLM provider 契约。Knowledge Expander 只负责把"一篇已有 Knowledge note + 已有关联 raw"扩展为 medium-risk `VaultPlan`（或在拿不准时降级为 high-risk 计划，交 4C.1 proposal 出口处理）。它不主动扫描 vault、不主动选 raw、不直接写入 vault。

## 配置

Knowledge Expander 复用 Raw Organizer 的 `OPENWHISKER_LLM_*` 配置：它属于"知识组织"agent，不是入口分类器。

```sh
export OPENWHISKER_LLM_API_KEY=...
export OPENWHISKER_LLM_BASE_URL=https://your-compatible-endpoint.example/v1
export OPENWHISKER_LLM_MODEL=your-model
```

设计理由：

- Knowledge Expander 与 Raw Organizer 都对 vault 内容做结构化整理，需求同类（更大上下文、更稳输出），共享同一组 model / key / org。
- intent classifier 的 `OPENWHISKER_INTENT_*` 是入口分类的小模型预算，不复用。
- CLI 入口通过 `--organizer=openai-compatible` 显式启用真实 provider；默认仍为 deterministic fallback，避免无意触发外部模型调用。

## 调用参数

Knowledge Expander 与 Raw Organizer 一致：

```text
response_format = {"type": "json_object"}
max_output_tokens = 4096（与 Raw Organizer 默认对齐）
```

不使用 OpenAI 的 strict `json_schema`，以兼容 DeepSeek 等只支持 `json_object` 的 OpenAI-compatible 端点。schema 约束以英文规则 + JSON 示例的形式写在 system prompt 里，由客户端 `validateKnowledgeExpanderOutput` 做硬校验。

空 content 在共享 `createWithRetry` helper 中做一次同请求体重试（无指数退避），两次都空则返回 `empty content after one retry` 错误。该实现与 Raw Organizer 同源（见 [`docs/phases/phase-4-wiki-agent-workflow.md`](../phases/phase-4-wiki-agent-workflow.md) 中的 Phase 4B 备注）。

## 输入上下文

第一版 Knowledge Expander 上下文构造与 Raw Organizer 默认 `minimal` 模式对齐，只读取：

- 目标 Knowledge note 全文（必填，由 CLI / Matrix 显式指定路径）。
- 由 source trace 选出的关联 raw / processed note（可选，第一版只用 note 内 backlink / `source_raw_path` / `source_processed_path` frontmatter 指向的 path）。
- 当前 `VaultProfile` 编译出的 `VaultKnowledgeExpanderSkill`（task-specific guidance；profile 缺失时使用 generic skill 文本）。
- 当前 `VaultProfile` 摘要。

第一版不发送：

- vault 根 `AGENTS.md` 等规则源（除非显式 `--context-mode=vault-rules`，与 Raw Organizer 对齐）。
- `.obsidian` / `.git` / secrets / 隐藏路径 / vault root 外文件。
- 与目标 Knowledge note 无 source trace 关联的其他 note。
- Matrix room 历史。
- 完整 vault 索引。

context builder 应限制每个文档的字节数（与 Raw Organizer 一致：context document 限 6000 字符，目标 Knowledge note 限 24000 字符），超出部分尾部截断并附 `...[truncated]`。

## 输出 schema

模型必须输出一个 JSON object：

```json
{
  "kind": "append",
  "title": "Phase4 笔记扩展",
  "summary": "在已有 Knowledge note 末尾补充 raw 中的新结论。",
  "append_section": "## 新增结论\n\n- ...",
  "child_note": null,
  "restructure": null,
  "review_items": ["确认新增结论是否需要补充来源。"]
}
```

`kind` 枚举与字段组合规则：

```text
kind = append
  - append_section: 非空字符串，Markdown 片段，将作为 H2 章节追加到目标 note 末尾；不得包含 frontmatter，不得替换原文。
  - child_note: 必须为 null。
  - restructure: 必须为 null。

kind = create_child_note
  - child_note: 必填对象，字段见下。
  - append_section: 必须为空字符串或省略。
  - restructure: 必须为 null。

kind = propose_restructure
  - restructure: 必填对象，字段见下。声明这是 high-risk 计划候选。
  - append_section: 必须为空字符串或省略。
  - child_note: 必须为 null。
```

`child_note` schema：

```json
{
  "relative_path": "subtopic-slug.md",
  "title": "子主题标题",
  "draft_body": "## 概念\n\n..."
}
```

- `relative_path` 必须是相对路径，由 OpenWhisker 与当前 profile 的 `KnowledgeDraftDir`（默认 `Knowledge/Drafts/`）拼接为最终 vault 路径；不得以 `/` 开头、不得包含 `..`、不得离开 vault root；不得指向已存在文件。
- 第一版 child note 一律落在 `KnowledgeDraftDir` 下，保留与 Raw Organizer 一致的 draft 中间态。"提升草稿为正式 Knowledge note + 同名子目录"是 vault 侧的人工动作（或由 `propose_restructure` 触发的高风险计划），不在本 expander 的 medium-risk 边界内。
- `title` 短中文标题。
- `draft_body` Markdown 正文，不含 frontmatter、不含顶级 H1（frontmatter 由 OpenWhisker 在 plan render 时统一注入，含 `needs_review`、controlled tags、反向链接到目标 Knowledge note 与关联 Raw/Processed）。

`restructure` schema：

```json
{
  "proposal_kind": "split",
  "rationale": "目标 note 已经混杂两个独立主题。",
  "affected_paths": ["Knowledge/topic/old.md", "Knowledge/topic/old/sub-a.md", "Knowledge/topic/old/sub-b.md"]
}
```

- `proposal_kind` 枚举与 `model.ProposalKind*` 对齐：`split`、`merge`、`rename`、`bulk-retag`、`bulk-link-rewrite`。第一版主要使用 `split` / `merge` / `rename`，后两者通常不由 Knowledge Expander 主动提出。
- `affected_paths` 至少包含目标 Knowledge note 自身；不得越界、不得指向 `.obsidian` / `.git` / `Meta/Agent-Proposals/`。
- `rationale` 用于填入 proposal note 的"来源 / 目标结构 / 建议操作"章节，不进入 vault 写入路径。

所有 `kind` 共享字段：

- `title`：本次输出对应的人类可读标题；append 时复用目标 note 标题或描述新增章节，create_child_note 时是 child note 标题，propose_restructure 时是 proposal 主旨。
- `summary`：一两句中文，写入 plan summary。
- `review_items`：字符串数组，写入 plan 渲染的"待核查"章节；为空时由 OpenWhisker 注入默认提示。

## 客户端硬校验

`validateKnowledgeExpanderOutput` 在客户端拒绝以下情况，不依赖 server-side strict schema：

```text
kind 非合法枚举 -> error
kind=append && append_section 为空 -> error
kind=append && (child_note != null || restructure != null) -> error
kind=create_child_note && child_note == nil -> error
kind=create_child_note && child_note.relative_path 不合法 -> error
kind=create_child_note && child_note.draft_body 为空 -> error
kind=create_child_note && (append_section 非空 || restructure != null) -> error
kind=propose_restructure && restructure == nil -> error
kind=propose_restructure && restructure.proposal_kind 非合法枚举 -> error
kind=propose_restructure && len(affected_paths) == 0 -> error
title 为空 -> error
summary 为空 -> error
```

路径硬校验：

```text
target_path（append 时）必须落在 conventions.KnowledgeDir 下，并指向 vault 内已存在文件
child_note.relative_path 必须解析为 clean relative path
拼接 conventions.KnowledgeDraftDir + relative_path 后:
  - 不得以 / 开头
  - 不得包含 ..
  - 不得离开 vault root
  - 不得指向 .obsidian / .git / Meta/Agent-Proposals / 隐藏目录
  - 不得指向已存在文件
```

非法输出在 client 一次性拒绝，job 失败并返回结构化错误；不进入 plan lifecycle，不写 vault，不污染 outbox。

## Plan 输出形态

客户端硬校验通过后，Knowledge Expander 把输出转换为 `VaultPlan`：

`kind = append`：

```text
operations:
  - type: append_note
    target_path: <目标 Knowledge note>
    before_hash: <现读取目标 note 后计算>
    payload: AppendNotePayload{content: append_section}
    risk_level: medium
risk_level: medium
requires_approval: true
source_refs: [<expander job id>, <目标 note path>, <关联 raw/processed path>...]
target_paths: [<目标 note path>]
status: proposed
```

`kind = create_child_note`：

```text
operations:
  - type: create_note
    target_path: <KnowledgeDraftDir>/<relative_path>
    payload: CreateNotePayload{content: 注入 frontmatter + draft_body}
    risk_level: medium
risk_level: medium
requires_approval: true
source_refs: [<expander job id>, <目标 note path>, <关联 raw/processed path>...]
target_paths: [<child note path>]
status: proposed
```

`kind = propose_restructure`：

```text
operations:
  - type: rename_note | bulk_retag | bulk_link_rewrite
    target_path: <affected_paths[0]>
    payload: 对应 high-risk payload struct
    risk_level: high
risk_level: high
requires_approval: true
source_refs: [<expander job id>, <目标 note path>, <affected_paths>...]
target_paths: <affected_paths>
status: proposed
```

`propose_restructure` plan 进入现有 4C.1 path：policy `CheckForApprovalHighRisk` -> `synthesizeHighRiskDiff` -> 用户 approve -> `ApplyAsProposal` 写 `Meta/Agent-Proposals/proposal_<short>.md`。Knowledge Expander 自身**不**写 proposal note。

第一版 `propose_restructure` 仅生成单 operation；多 operation high-risk 组合（如 split = rename + bulk_link_rewrite）留给后续阶段，由 4C.1 policy 校验为 high-risk 即可。

## Hard guard

Knowledge Expander 输出不能单独决定执行。policy 层已有约束在 Knowledge Expander 路径上继续生效：

```text
medium-risk plan:
  - must have source_refs (>=2: expander job id + target note path)
  - must have target_paths
  - operations must have type / target_path / payload / reason / risk_level
  - append_note must have before_hash
  - create_note relative_path must be clean and inside vault root
  - knowledge draft content must contain required tags / source link / 待核查 markers

high-risk plan:
  - delegated to CheckForApprovalHighRisk (4C.1) and ApplyAsProposal
  - never writes to Knowledge/ directly
```

非法输出处理：

```text
invalid JSON -> error, no retry on schema mismatch (only empty content triggers retry)
unknown kind -> error
missing required field -> error
empty content -> single retry, then error
model timeout / network error -> error returned to caller, plan job failed
```

## CLI / Matrix 入口语义

第一版 CLI 入口：

```sh
go run ./cmd/openwhisker expand <path-or-topic> \
  --organizer=deterministic|openai-compatible
```

- 必填位置参数 `<path>` 指向 vault 内现有 Knowledge note 相对路径。
- 不主动选 raw；source trace 由 OpenWhisker 从目标 note frontmatter / backlink 反查。
- 输出 plan id 与 awaiting_approval prompt，与 `organize last` 一致。
- 后续 `plan diff` / `plan approve` / `plan reject` 路径完全复用现有命令。

Matrix 自然语言入口（"扩展一下 <topic>"）由 4C.4 接入。本契约不约束 IM 入口的解析过程，只约束 expander LLM call。

## Privacy boundary

Knowledge Expander 模型可以看到目标 Knowledge note 全文和由 source trace 选出的关联 raw / processed note。不允许发送：

- vault 根 `AGENTS.md` 等规则源（除非显式 `--context-mode=vault-rules`）。
- `.obsidian` / `.git` / secrets / 隐藏路径 / vault root 外文件。
- 与目标 note 无 source trace 关联的其他 Knowledge note。
- 完整 pending plan diff、Matrix room 历史。

vault context builder 在加载文档前必须按上述边界过滤；这部分约束与 Raw Organizer 的 `buildRawOrganizerContext` 同源。
