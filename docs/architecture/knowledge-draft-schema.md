# Knowledge Draft Schema

状态：draft，配合 Phase 4B（Raw Organizer）落地。本文定义 medium-risk Raw Organizer plan 经 `approve` 后写入 Knowledge draft 与 Raw/Processed 追加块的形态约束。Knowledge draft 是给人审阅的 vault 内笔记，使用 Obsidian Flavored Markdown，必须与 Obsidian 的 wikilink / callout / Properties 约定对齐。

## 适用范围

本 schema 覆盖 medium-risk Raw Organizer 通过 `organize last` / `organize today` 落盘的两类内容：

1. **Knowledge draft**：写入 `policy.Conventions.KnowledgeDraftDir`（默认 `Knowledge/Drafts/`）。需要用户审阅、补充、签出到正式 Knowledge note。
2. **Raw/Processed 追加段**：在原 raw 文件 move 到 `policy.Conventions.RawProcessedDir`（默认 `Raw/Processed/`）后，由 OpenWhisker 在文件末尾追加的 `## OpenWhisker Processing` 段。

high-risk plan 走 [proposal-note-schema](proposal-note-schema.md)，不在本 schema 范围。Phase 1 的 raw capture 写 `Raw/Inbox/` 的初始 frontmatter 由 ingest 路径决定，亦不在本 schema 范围。

## 设计原则

- **vault 内引用一律用 wikilink**。OpenWhisker 写入的 vault 笔记必须与人类手写笔记在 Obsidian 内有同等的可跳转性、可反向链接、可被重命名追踪能力。任何 vault 内路径（raw、processed、output draft、相关 Knowledge note）以正文形式出现时必须是 `[[<vault-relative-path-without-md>]]`。
- **frontmatter 用 Obsidian Properties 友好的扁平字段名**。OpenWhisker 元信息（plan_id / job_id / origin / raw_kind 等）放在嵌套对象 `openwhisker:` 下，避免污染 Obsidian 顶层 Properties 面板；`tags` / `aliases` / `related` 用 Obsidian 默认/约定属性。
- **审阅状态用 callout 强调**。`needs-review` 不只是 frontmatter 字段，正文里也要有 `> [!todo]` / `> [!note]` 让 reading view 第一眼可见。
- **OpenWhisker 元信息可识别**。frontmatter 顶上保留 `openwhisker:` 命名空间，让后续工具能从任何 vault 笔记里识别出"这是 OpenWhisker 写的"，与人工笔记区分。

## 路径与命名

- Knowledge draft：`<KnowledgeDraftDir>/<raw_basename>.md`。文件名沿用原 raw 笔记 basename，便于和 `Raw/Processed/<raw_basename>.md` 双向定位。
- Raw/Processed 追加段：直接在 move 后的文件末尾追加 `## OpenWhisker Processing` 二级标题段，**不**修改原文 raw 内容、frontmatter。
- 所有路径决策从 `policy.Conventions` 流入，OpenWhisker 自身不硬编码。

## Knowledge Draft Frontmatter

```yaml
---
title: "<原 raw 标题或 LLM 派生的短标题>"
tags:
  - type/knowledge-draft
  - status/needs-review
  # 加上 profile.RequiredDraftTags 的其余项
related:
  - "[[<raw_processed_path_without_md>]]"
  # 单 raw：只有 1 条；today 批：每条 raw 一条
openwhisker:
  job_id: "<plan job id>"
  job_type: "<job type，例如 organize-raw / organize-raw-today>"
  raw_job_id: "<raw 捕获 job id；today 批为 batch>"
  raw_path: "<raw 捕获时路径，字符串>"
  processed_path: "<approved 后路径，字符串>"
  raw_kind: "<concept-seed | web-clip | todo-list | llm-chat | mixed>"
  created_at: "<ISO8601 UTC>"
  # today 批额外字段：
  raw_job_ids:
    - "<rawA>"
    - "<rawB>"
  raw_paths:
    - "<rawA_path>"
    - "<rawB_path>"
  processed_paths:
    - "<rawA_processed>"
    - "<rawB_processed>"
---
```

约束：

- 顶层只保留 `title` / `tags` / `related` / `openwhisker`。Obsidian 默认 Properties 面板因此干净，OpenWhisker 私有字段全部收纳进 `openwhisker:`。
- `tags` 必须包含 `type/knowledge-draft` 与 `status/needs-review`；其余按 profile `RequiredDraftTags`。
- `related` 仅放 wikilink，去重；至少包含对应的 `processed_path` 反向链接，让 Obsidian backlinks / graph view 能识别。
- `openwhisker.raw_path` / `processed_path` 是**字符串路径**，不是 wikilink —— 这是给程序读的元信息，不是给人点的链接。正文里跳转用的 wikilink 由 `related` 与 H2 章节负责。
- 不写空字段、不写 `aliases: ` 占位。

## Knowledge Draft 必填章节

H1 标题之后、`## 摘要` 之前必须有头部 callout：

```markdown
> [!todo] OpenWhisker Raw Organizer 草稿
> 由 OpenWhisker 从 raw 输入整理。请人工审阅 → 补全 → 转写为正式 Knowledge note 后归档此 draft。
```

callout 类型固定为 `todo`，标题固定为 `OpenWhisker Raw Organizer 草稿`，正文两行 fixed template，不夹 LLM 输出。

之后正文按以下 H2 顺序，缺失任一节视为 schema 违规：

```markdown
## 摘要

<!-- 一两句中文，概括 draft 主题。从 LLM summary 来；deterministic planner 用固定占位。 -->

## 笔记

<!-- LLM 整理出的 Knowledge 内容主体。不含顶级 H1，按 H3 起步划分小节。
     deterministic planner 写固定占位行，明确 "Replace this section with LLM-backed organization"。 -->

## 来源

<!-- 单 raw：- [[<processed_path>]]
     today 批：每条 raw 一条 wikilink。
     不得回退为纯字符串路径。 -->

## 待核查

<!-- callout 包裹的 todo 列表。空列表禁止 —— Raw Organizer 默认必有至少一条
     "核对从 raw 输入推断出的内容是否准确" 兜底。 -->

> [!todo] 待核查
> - 核对从 raw 输入推断出的内容是否准确。
> - <LLM review_items 其余项>
```

约束：

- `## 来源` 章节里所有 vault 内路径必须是 wikilink，**不得**出现 `Raw path before approval: <裸字符串>` 之类的纯文本。
- `## 待核查` 必须用 `> [!todo] 待核查` callout 包裹列表；adapter 在面向人类 diff 里抓 `待核查` substring，包到 callout 里仍命中。
- today 批增加 `## 来源` 多条 wikilink，但不再额外写 `## Sources` 英文章节（保持单一来源）。

## Raw/Processed 追加段 Schema

OpenWhisker 在 approval 完成时把 raw move 到 Raw/Processed/ 后，在文件末尾追加：

```markdown


---

> [!note] OpenWhisker Processing
> 由 OpenWhisker 处理为 Knowledge draft；以下为处理元信息与输出。

### Outputs

- [[<knowledge_draft_path>]]
<!-- 多 output 时逐条 wikilink；零 output 时保留 "- 待补充" 占位 -->

### Processing Note

<!-- LLM summary 或 deterministic 固定文案。一段话。 -->

### Remaining Review

> [!todo] Remaining Review
> - <review_items 每条>

### Trace

- plan_job: `<plan_job_id>`
- raw_job: `<raw_job_id>`
- raw_path: `<raw_path_string>`
- processed_path: `<processed_path_string>`
- processed_at: `<ISO8601>`
- raw_kind: `<raw_kind>`（today 批省略）
```

约束：

- 与原 raw 内容之间用 `---` thematic break 分隔。
- 追加段标题降为 `> [!note]` callout（不是 H2），避免在 Obsidian outline 里污染原 raw 笔记的章节层级。
- `Outputs` 列表项必须是 wikilink。
- `Trace` 子段用 inline code 包裹 ID 与字符串路径 —— 这些是给程序读的，不应被 Obsidian 当链接解析。
- 一篇 Raw/Processed note 在多次 batch 处理后追加多个 `> [!note] OpenWhisker Processing` block 是允许的；但 deterministic 单 raw 路径假设只追加一次。

## 与 vault_operation_logs / hash chain 的关系

- Knowledge draft 写入是 `op_type=create_note`，`outcome=applied`，记录 `after_hash`；无 `before_hash`（新文件）。
- Raw/Processed 追加段是 `op_type=append_note`，要求 `before_hash` 与 move 后状态匹配，写入后记录 `after_hash`。
- approve 过程任何 hash mismatch 都不应静默落盘 —— PlanService 现有 hash guard 路径不变。

## adapter 渲染（面向人类 diff）

- `plan diff <plan_id>` 在 medium-risk path 展示 draft preview 时，**不**输出 `openwhisker:` 嵌套对象 —— 这些是程序元信息，会污染人类 diff 视野。
- adapter 现有逻辑用 `strings.Contains(line, "待核查")` 抓 review heading；本 schema 把 `## 待核查` 包到 callout 里，仍兼容 substring 匹配，无需改 adapter。

## 范围外

- Knowledge draft 状态机的 OpenWhisker 端读写：4B 写一次，写完不再读取。
- Knowledge draft 转写为正式 Knowledge note 的自动化：人工或后续阶段实现。
- `openwhisker:` 嵌套 frontmatter 的版本化迁移：本 schema 是第一版，旧 draft 的 flat 字段读路径不再维护，老 draft 由人工迁移或归档。

## 安全 invariant

- Knowledge draft 路径必须在 `KnowledgeDraftDir` 之内，不接受 `..` / 绝对路径 / 跨 vault root。
- Raw/Processed 追加段只能追加（append-only），不得修改原 raw 内容与 frontmatter。
- 写入内容来自 plan + 模板拼装，不含任何 LLM 自由输出之外的 vault content；不夹带其它 Knowledge note 全文。
