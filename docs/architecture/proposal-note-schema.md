# Proposal Note Schema

状态：draft，配合 Phase 4C.1（High-risk proposal-only policy）落地。本文定义 high-risk `VaultPlan` 经 `approve` 后生成的 proposal note 形态与生命周期约束。proposal note 是给人看的终态产物，不再走 OpenWhisker `apply` 路径。

## 适用范围

本 schema 仅适用于 plan policy 判定为 **high-risk** 的 plan：

- `split`：把一篇 Knowledge note 拆为多篇。
- `merge`：把多篇 Knowledge note 合并为一篇。
- `rename`：已有正式 Knowledge note 改名或迁移（不含 raw → processed 的常规 move）。
- 大规模 retag：单 plan 内 retag 操作影响 ≥ N 篇（N 由 policy 实现决定，初版建议 5）。
- 大规模 link rewrite：单 plan 内 link rewrite 操作影响 ≥ N 篇（同上）。

medium-risk Raw Organizer / Knowledge Expander plan **不**进入本 schema，仍走原有 approve + apply 路径。

## 路径与命名

- 目录由 vault profile 提供：`policy.Conventions.AgentProposalsDir`。OpenWhisker 自身不硬编码任何 vault 路径，所有 vault 形状决策都从 profile 流入。
- 当前 `KnowledgeVaultConventions` 默认值：`Raw/Agent-Proposals/`。
  - 选择 `Raw/` 的理由：proposal note 是 agent 主动产出、等人决策的「未被人加工的输入」，与 `Raw/Inbox/`（用户捕获）、`Raw/Processed/`（已完成 raw）同属「未处理输入」分支。`Meta/` 留给规则 / 模板 / 架构文档，不放运行时产出；`Knowledge/Drafts/` 是 Knowledge 维度的进行中草稿，与结构层决策建议语义不符。`Agent-Proposals/` 子目录把 agent-generated proposal 与人工 design proposal 区分开。
  - 如果你的 vault 想用别的位置（例如 `Meta/CustomProposals/`），改 profile 即可，无需改 OpenWhisker 代码。
- 文件名：`proposal_<plan_id_short>.md`，其中 `plan_id_short` 是 plan_id 的前 12 字符，与现有 `job_<short>.md` 命名风格一致。
- proposal note 一旦写入，OpenWhisker **不再覆盖、不再追加**。用户可以手工编辑、归档或转写为正式 plan / Knowledge note。

## Frontmatter

```yaml
---
title: "<人类可读标题，由 plan 内 summary 字段或 fallback 生成>"
type: proposal
status: needs-review
created: "<ISO8601 UTC>"
updated: "<ISO8601 UTC>"
source:
  plan_id: "<完整 plan_id>"
  job_id: "<完整 job_id>"
  origin: "<organizer | expander | other agent name>"
tags:
  - type/proposal
  - status/needs-review
  - proposal/<kind>           # split | merge | rename | bulk-retag | bulk-link-rewrite
aliases:
related:
  - "[[<目标 Knowledge note 1>]]"
  - "[[<目标 Knowledge note 2>]]"
---
```

约束：

- `type` 必须为 `proposal`。
- `status` 初始必须为 `needs-review`；用户手工改为 `accepted` / `rejected` / `archived` 后 OpenWhisker 不再读写。
- `tags` 必须包含 `type/proposal` 与 `status/needs-review`，必须包含恰好一个 `proposal/<kind>`。
- `source.plan_id` / `source.job_id` 必须存在且与生成它的 plan 一致，用于后续审计回溯。
- `related` 必须列出所有被 proposal 影响的 Knowledge note，使用 wikilink，方便用户在 Obsidian 内反向跳转。

## 必填章节

proposal note 正文按以下 H2 顺序写出。任何一节缺失或为空都视为 schema 违规，应由 policy 在落地前拒绝并降级为可读错误，而不是写入半成品。

```markdown
## 来源

<!-- 来源 raw / Knowledge / 对话，每条一行，含 wikilink。说明这份 proposal 是基于什么观察生成的。 -->

## 目标结构

<!-- 描述变更后 vault 在结构上长什么样：哪些 note 存在、哪些目录、彼此引用关系。可以是文字 + 简单文件树。 -->

## 影响路径

<!-- 列出受影响的所有 vault 路径与对它们的预期操作（rename / split / merge / retag / link rewrite）。每条必须含原路径、目标路径或目标状态。 -->

## 建议操作

<!-- 给人执行的步骤建议。可以包含手工 Obsidian 操作、后续 OpenWhisker plan 拆分建议、或建议用户先做某个小 plan 再回来重新审批。 -->

## 待人工确认问题

<!-- 至少 1 条；OpenWhisker / LLM 拿不准的具体问题，给用户回答。空列表视为违规——proposal 之所以是 proposal 就因为它含不确定。 -->
```

## 与 VaultPlan / vault_operation_logs 的关系

- proposal 生成属于 plan lifecycle 的 **替代终态**：plan 仍走 `prepared → approved`，但在 `approved` 之后进入 `proposal_written` 而非 `applied`。
- `vault_operation_logs` 必须能区分 `applied` 与 `proposed`：
  - 初版建议在现有行上新增 `outcome` 列（`applied` / `proposed`），或新增专门的 `proposed` 行类型，由 4C.1 实现时具体选定。无论选哪个，hash chain 必须保持完整。
  - proposal_written 不调用 vault 内 hash guard：proposal note 是新文件，不存在 `before_hash`；仅记录 `after_hash` 以便后续比对。
- 原始 plan 中的 high-risk operations **不**写入 `vault_operation_logs` 作为 `applied`。它们的内容被序列化进 proposal note 的「影响路径」章节，仅供人类参考。

## `plan diff` 展示约束

- `plan diff <plan_id>` 在检测到 plan 是 high-risk 时，必须在 diff 顶部输出明确提示，例如：

  ```text
  ⚠ 高风险计划：approve 后只生成 proposal note，不会写入正式 Knowledge note。
  ```

- diff 主体保留 plan 的 operation 列表，但每个 operation 标记 `→ proposal only`，避免误以为是普通 medium-risk 审批。

## 范围外

- proposal note 上的二次审批 / 自动执行 / 自动重写为 plan：不在 4C.1 范围内。
- proposal note 状态机的 OpenWhisker 端读写：4C.1 只写一次，写完不再读取。
- proposal note 自动归档：用户手工或后续阶段实现。

## 安全 invariant

- 写 proposal note 不修改任何 `Knowledge/` 路径。
- 写 proposal note 不修改 `.obsidian` / `.git` / secrets / hidden path。
- proposal note 路径必须在 profile 指定的 `AgentProposalsDir` 之内，不接受 `..` / 绝对路径 / 跨 vault root。
- proposal note 内容来自 plan + 模板拼装，不含任何 LLM 自由输出之外的 vault content；不夹带 raw 全文或其它 Knowledge note 全文。
