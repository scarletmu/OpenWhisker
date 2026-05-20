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
title: "<短人类可读标题，≤ 40 字>"
type: proposal
status: needs-review
risk: high
created: "<ISO8601 UTC>"
updated: "<ISO8601 UTC>"
source:
  plan_id: "<完整 plan_id>"
  job_id: "<完整 job_id>"
  origin: "<organizer | expander>"
tags:
  - type/proposal
  - status/needs-review
  - proposal/<kind>           # split | merge | rename | bulk-retag | bulk-link-rewrite
related:
  - "[[<目标 Knowledge note 1>]]"
  - "[[<目标 Knowledge note 2>]]"
---
```

约束：

- `type` 必须为 `proposal`。
- `status` 初始必须为 `needs-review`；用户手工改为 `accepted` / `rejected` / `archived` 后 OpenWhisker 不再读写。
- `risk` 必须为 `high`（4C.1 范围内只有高风险终态走 proposal 路径；保留字段是为后续若扩展到其它风险等级时不破坏 reader）。
- `title` 是短人类可读标题，长度 ≤ 40 字（中文按 1 字算）；**不得**直接复用 LLM 的 summary 长句。渲染器应基于 `proposal_kind` + 主路径派生标题，例如 `拆分 MultiTopicMixed 提案`、`重命名 Foo 提案`。
- `tags` 必须包含 `type/proposal` 与 `status/needs-review`，必须包含恰好一个 `proposal/<kind>`，且 `<kind>` 必须与 plan 的 `proposal_kind` 一致（不得硬编码 `proposal/rename`）。
- `source.plan_id` / `source.job_id` 必须存在且与生成它的 plan 一致，用于后续审计回溯。
- `source.origin` 必须取 `organizer` 或 `expander`，不夹叙述句。
- `related` 必须列出所有被 proposal 影响的 Knowledge note，使用 wikilink，方便用户在 Obsidian 内反向跳转；**不得**重复出现同一条 wikilink。
- 空字段（如 `aliases:` 没有值）不得写入；省略即可。

## 头部 Callout（强制）

H1 标题之后、`## 来源` 之前必须有且仅有一个 Obsidian warning callout，向人类读者说明 proposal 的关键语义：「未对 vault 做任何写入；需人工评审后在外部工具中执行」。这是 proposal 与"已 apply 的 plan note"在视觉上的唯一区分。

```markdown
> [!warning] 高风险结构变更提案
> 本文档由 OpenWhisker 自动生成。vault 未被修改。
> 请人工评审 → 在外部工具（Codex / Claude Code / Obsidian）执行所需操作。
```

- callout 类型固定为 `warning`，标题固定为 `高风险结构变更提案`。
- 正文两行 fixed template，不夹 LLM 生成内容。
- 同一份 proposal note 出现多于一个 warning callout 视为违规。

## 必填章节

callout 之后，正文按以下 H2 顺序写出。任何一节缺失或为空都视为 schema 违规，应由 policy 在落地前拒绝并降级为可读错误，而不是写入半成品。每个 H2 段后必须保留至少一个空行再进入下一个 H2。

```markdown
## 来源

<!-- 来源 raw / Knowledge / 对话，每条一行，含 wikilink，去重。
     不得在此重复 frontmatter 里已有的 plan_id / job_id —— 那些是元信息，不是来源。 -->

## 目标结构

<!-- 描述变更后 vault 在结构上长什么样：哪些 note 存在、哪些目录、彼此引用关系。
     可以是文字 + 简单文件树。 -->

## 影响路径

<!-- 当受影响路径 ≥ 2 时，强制使用表格；只有 1 条时允许使用列表。
     表格列顺序固定：操作 / 原路径 / 目标路径 / 理由。
     操作枚举与 proposal_kind 对齐：split / merge / rename / bulk-retag / bulk-link-rewrite。
     "理由" 列接 LLM 在 plan 里给出的 rationale；
     渲染器不得回填占位字符串（例如 "Knowledge expander surfaced a high-risk restructure proposal."）。 -->

| 操作  | 原路径                          | 目标路径                                  | 理由                  |
| ---   | ---                            | ---                                       | ---                   |
| split | [[Knowledge/MultiTopicMixed]]  | [[Knowledge/Redis-Persistence]]           | Redis 持久化主题独立  |
| split | [[Knowledge/MultiTopicMixed]]  | [[Knowledge/Go-Goroutine-Scheduler]]      | Go 运行时调度主题独立 |

## 建议操作

<!-- 给人执行的步骤建议。可以包含手工 Obsidian 操作、后续 OpenWhisker plan 拆分建议、
     或建议用户先做某个小 plan 再回来重新审批。 -->

## 待人工确认问题

<!-- 至少 1 条；OpenWhisker / LLM 拿不准的具体问题，给用户回答。
     空列表视为违规——proposal 之所以是 proposal 就因为它含不确定。
     每条问题后必须挂一个 Obsidian block ID `^qN`（N 从 1 递增、连续），
     方便用户在别的 note 里通过 [[proposal_<short>#^q1]] 反向引用特定问题。 -->

- 目标路径是否会破坏现有反向链接？是否需要先扫描 backlinks？ ^q1
- 新名称是否与现有 Knowledge note 冲突或语义重复？ ^q2
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
