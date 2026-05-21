# Knowledge Expander Contract

状态：已落地。配套实现位于 `internal/agent/openai_knowledge_expander.go` 和 `internal/core/plans.go` 的 `KnowledgeExpander` 接口。high-risk 降级出口复用 [`proposal-note-schema.md`](proposal-note-schema.md) 与 4C.1 流程。

本文定义 Phase 4C.2 Knowledge Expander 使用的 LLM provider 契约。Knowledge Expander 只负责把"一篇已有 Knowledge note"扩展为 medium-risk `append` 计划（或在拿不准时降级为 high-risk `propose_restructure` 计划，交 4C.1 proposal 出口处理）。它不主动扫描 vault、不主动选 raw、不直接写入 vault、不直接创建新的子 note。

**设计取向**：Knowledge Expander 被定位为非关键模块。OpenWhisker 自接的开源 / 小厂 LLM 在工程能力和联网搜索能力上天然不如闭源工具（Codex / Claude Code）+ 闭源旗舰模型；因此 Expander 只承担最确定有用的 `append`（向已有 note 末尾加段内容），其余"创建新子 note / 拆分 / 合并 / 改 tag / 改 link"统一通过 `propose_restructure` 产出一份高质量入口文档（proposal note），由人在外部强工具中实际执行。这避免了主干 policy / executor 为 Expander 的不确定性买单。

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
- 由 source trace 选出的关联 raw / processed note（可选，第一版读取目标 note 的 YAML frontmatter 中 `openwhisker.raw_path` / `openwhisker.processed_path` 标量——由 Raw Organizer 按 [knowledge-draft-schema](knowledge-draft-schema.md) 写入；并兼容人工维护的多来源 Knowledge note 里可能出现的 `openwhisker.raw_paths` / `openwhisker.processed_paths` 列表；缺失的 path 静默跳过，不读取 `related:` wikilink 中可能指向其他 Knowledge note 的项以保留 privacy boundary）。
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
  "restructure": null,
  "review_items": ["确认新增结论是否需要补充来源。"]
}
```

`kind` 枚举与字段组合规则：

```text
kind = append
  - append_section: 非空字符串，Markdown 片段，将作为 H2 章节追加到目标 note 末尾；不得包含 frontmatter，不得替换原文。
  - restructure: 必须为 null。

kind = propose_restructure
  - restructure: 必填对象，字段见下。声明这是 high-risk 计划候选。
  - append_section: 必须为空字符串或省略。
```

> **不支持 `create_child_note`**。如果目标 note 实际上需要新建一篇子 note（或拆分、合并、重命名、批量改 tag / link），返回 `kind = propose_restructure`，由 4C.1 写一份 proposal note 给人，由人在外部强工具中执行实际写入。第一版有意压缩 Expander 的能力面，避免让主干 policy / executor 为弱 LLM 的不确定性绕路。

`restructure` schema：

```json
{
  "proposal_kind": "split",
  "rationale": "目标 note 已经混杂两个独立主题。",
  "affected_paths": ["Knowledge/topic/old.md", "Knowledge/topic/old/sub-a.md", "Knowledge/topic/old/sub-b.md"]
}
```

- `proposal_kind` 枚举与 `model.ProposalKind*` 对齐：`split`、`merge`、`rename`、`bulk-retag`、`bulk-link-rewrite`。**新建子 note 也归入 `split`**（rationale 应说清新增哪几篇、与目标 note 的边界关系）。
- `affected_paths` 至少包含目标 Knowledge note 自身；不得越界、不得指向 `.obsidian` / `.git` / `Raw/Agent-Proposals/`。新建子 note 时把建议路径列在 affected_paths 后段。
- `rationale` 用于填入 proposal note 的"来源 / 目标结构 / 建议操作"章节，不进入 vault 写入路径。

所有 `kind` 共享字段：

- `title`：本次输出对应的人类可读标题；append 时复用目标 note 标题或描述新增章节，propose_restructure 时是 proposal 主旨。
- `summary`：一两句中文，写入 plan summary。
- `review_items`：字符串数组，写入 plan 渲染的"待核查"章节；为空时由 OpenWhisker 注入默认提示。

## 客户端硬校验

`validateKnowledgeExpanderOutput` 在客户端拒绝以下情况，不依赖 server-side strict schema：

```text
kind 非合法枚举（含历史的 create_child_note）-> error
kind=append && append_section 为空 -> error
kind=append && restructure != null -> error
kind=propose_restructure && restructure == nil -> error
kind=propose_restructure && restructure.proposal_kind 非合法枚举 -> error
kind=propose_restructure && len(affected_paths) == 0 -> error
kind=propose_restructure && append_section 非空 -> error
title 为空 -> error
summary 为空 -> error
```

路径硬校验：

```text
target_path（append 时）必须落在 conventions.KnowledgeDir 下，并指向 vault 内已存在文件
propose_restructure 的 affected_paths 由 policy.CheckForApprovalHighRisk 在 4C.1 通道校验
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

`propose_restructure` plan 进入现有 4C.1 path：policy `CheckForApprovalHighRisk` -> `synthesizeHighRiskDiff` -> 用户 approve -> `ApplyAsProposal` 写 `Raw/Agent-Proposals/proposal_<short>.md`。Knowledge Expander 自身**不**写 proposal note。

第一版 `propose_restructure` 仅生成单 operation；多 operation high-risk 组合（如 split = rename + bulk_link_rewrite）留给后续阶段，由 4C.1 policy 校验为 high-risk 即可。

## Hard guard

Knowledge Expander 输出不能单独决定执行。policy 层已有约束在 Knowledge Expander 路径上继续生效：

```text
medium-risk plan:
  - must have source_refs (>=2: expander job id + target note path)
  - must have target_paths
  - operations must have type / target_path / payload / reason / risk_level
  - append_note must have before_hash

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

Knowledge Expander 保持 CLI-only，不接入 Matrix 自然语言入口：其产出（append plan 或 proposal note）只是普通文档，会自然回流到既有 capture / review 链路，不需要为非关键模块单独做 IM 交互 UX。

## Privacy boundary

Knowledge Expander 模型可以看到目标 Knowledge note 全文和由 source trace 选出的关联 raw / processed note。不允许发送：

- vault 根 `AGENTS.md` 等规则源（除非显式 `--context-mode=vault-rules`）。
- `.obsidian` / `.git` / secrets / 隐藏路径 / vault root 外文件。
- 与目标 note 无 source trace 关联的其他 Knowledge note。
- 完整 pending plan diff、Matrix room 历史。

vault context builder 在加载文档前必须按上述边界过滤；这部分约束与 Raw Organizer 的 `buildRawOrganizerContext` 同源。
