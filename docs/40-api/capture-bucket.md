# Capture Bucket 规格

状态：partial implementation。`capture_buckets` 持久化、active bucket create/append/close/organize、source_key 校验、hash guard、raw append frontmatter `updated` rewrite 和 intent-triggered create/organize/approve/reject outbox suppression 已落代码；图片/文件和 plan revision 未实现。

> **方向提示**：本文记录当前**已落代码**的"按 topic 分组 bucket + append + organize + 审批"实现。IM 入口正按 [`../30-design/im-quick-capture.md`](../30-design/im-quick-capture.md) 重定位为速记收件箱：bucket 的 topic 分组语义将被"按天收件箱"（`Raw/Inbox/YYYY-MM-DD.md`）取代，IM 路径不再有 organize / approve / diff 往返。`capture_buckets` schema 在新模型下的去留待实现阶段评估。本文描述**现状**，不是目标形态。

本文定义 Phase 4B.5 的 active raw bucket、pending clarification 和 raw append 行为。

## 定义

一个 bucket 表示：

```text
一组准备被同一次 organize 处理的 raw material。
```

bucket 边界近似按 topic / task 聚合，而不是按单条消息或纯时间窗口。

同一个 bucket 示例：

- Phase 4 Intent Router 的连续设计讨论。
- 一篇文章的摘录、评论和后续补充。
- 一个面试故事的素材补充。
- 一次旅行计划的多个约束。

不应放进同一个 bucket：

- Phase 4 router 设计 + 晚饭计划。
- LLM 输出策略 + 旅游签证材料。
- 技术文章摘录 + 买菜清单。

topic / task 是主边界，TTL 是防污染保护。

## 状态隔离

bucket 按 `source_key` 隔离。

所有 bucket 操作都必须带 `source_key`：

```text
AppendRawBucket(source_key, bucket_id, input_part)
CloseCaptureBucket(source_key, bucket_id, reason)
OrganizeCaptureBucket(source_key, bucket_id)
```

Core 必须校验：

```text
bucket.source_key == request.source_key
```

## 生命周期

状态：

```text
active
closed
organized
hash_mismatch
expired
```

进入 bucket：

- “记录一下：...”
- “开始记录：...”
- “帮我收一下这个材料：...”

继续 append：

- “继续：...”
- “补充：...”
- “还有：...”
- 模型判断 same topic 且 high confidence。

结束 bucket：

- 用户显式结束：“结束记录”“这组结束”“先到这里”。
- 用户触发整理：“整理刚才”“处理这组”。
- TTL 过期。
- 出现 approval 阶段动作：“预览一下”“写进去”“先不写”。
- hash mismatch。

默认 TTL：

```sh
OPENWHISKER_CAPTURE_BUCKET_TTL=15m
```

TTL 过期后不自动 append。用户需要明确“继续刚才那个...”或重新“记录一下：...”。

## organized 后行为

bucket 一旦进入 `organized`，后续不再 append。

如果用户继续说“补充一下...”，系统不修改已经生成的 pending plan，也不做 revise plan。

回复应提示：

```text
当前记录组已经进入整理/审批流程，不能继续追加。请用“记录一下：...”开一条新记录。
```

第一版不实现 plan revision。

## 持久化

bucket 状态必须持久化到 SQLite，而不是只放内存。

最小字段：

```text
capture_buckets:
- bucket_id
- source_key
- raw_job_id
- raw_plan_id
- raw_path
- status
- topic_hint
- excerpt
- append_count
- raw_hash_after_last_append
- started_at
- updated_at
- expires_at
- closed_at
- close_reason
```

`topic_hint` 和 `excerpt` 第一版 deterministic 维护：

- `topic_hint` 从第一段 payload 第一行或前 40 字生成。
- `excerpt` 保存前 200 字或最近两段短摘。
- 不调用模型生成摘要。
- Router 不重新读取 Raw note 全文。

理由：

- Router 只需要一张便签，不需要写正式摘要。
- 保持快、便宜、可复现。
- 避免和 Raw Organizer 职责重叠。

## Raw note 写入策略

第一版选择：

```text
每次 append 都写 Raw/Inbox。
```

原因：

- 符合 local-first。
- 输入马上进入 vault。
- 不容易因 daemon 或 DB 异常丢失。
- 后续 organize 可以复用 Raw/Inbox 流程。

Router 不直接写 Raw note。流程是：

```text
Router -> IntentResult
Adapter -> Core API
Core -> controlled raw create / append
Vault write layer -> Raw/Inbox
```

## Raw note append 格式

采用“单文件，多段 source block”：

```markdown
---
title: "Raw Capture - 2026-05-15 21:30"
status: inbox
source: matrix
source_key: "matrix:..."
created: 2026-05-15T21:30:00+08:00
updated: 2026-05-15T21:42:00+08:00
openwhisker_bucket_id: bucket_xxx
openwhisker_job_id: job_xxx
---

# Raw Capture - 2026-05-15 21:30

## 输入 1

- 类型：text
- 时间：2026-05-15T21:30:00+08:00
- 来源：matrix

```text
第一段内容...
```

## 输入 2

- 类型：text
- 时间：2026-05-15T21:35:00+08:00
- 来源：matrix
- 说明：append

```text
补充内容...
```
```

append 时只追加新的 `## 输入 N` block，并更新 frontmatter `updated`。

未来图片 / 文件可以扩展 `类型` 和附件引用，但 Phase 4B.5 第一版只处理文本。

## AppendRawBucket

Core 需要新增受控能力：

```text
AppendRawBucket(source_key, bucket_id, input_part)
```

它只能：

- 找到 bucket 对应 `raw_path`。
- 确认 `source_key` 匹配。
- 确认 bucket status = `active`。
- 确认未过期。
- 执行轻量 hash guard。
- 追加新的 `## 输入 N` block。
- 更新 frontmatter `updated`。
- 更新 bucket `append_count`、`updated_at`、`expires_at`、`raw_hash_after_last_append`。

它不能：

- append 任意 vault path。
- 修改 Knowledge。
- 修改 Raw/Processed。
- 修改 already organized / closed bucket。

## Hash guard

bucket append 需要轻量 hash guard。

规则：

```text
capture_buckets.raw_hash_after_last_append stores last known raw note hash.
AppendRawBucket reads current raw note hash.
current hash must equal raw_hash_after_last_append.
If match, append and update raw_hash_after_last_append.
If mismatch, do not append.
```

hash mismatch 后：

- 关闭 bucket。
- status = `hash_mismatch`。
- 不自动创建新 bucket。
- 不自动整理。

回复：

```text
当前记录组已被外部修改，我没有继续追加。请用“记录一下：...”重新开始，或手动处理这条 Raw note。
```

`OrganizeCaptureBucket` 前也必须检查 hash。hash mismatch 时不 organize。

## OrganizeCaptureBucket

不提供任意 path 入口：

```text
不提供 OrganizeRaw(path)
不提供 OrganizeArbitraryVaultPath(path)
```

提供受控 bucket 入口：

```text
OrganizeCaptureBucket(source_key, bucket_id)
```

Core 内部：

- 查 `capture_buckets`。
- 校验 `source_key`。
- 校验 status / TTL / hash。
- 取 bucket 绑定的 raw job / raw path。
- 创建标准 `WikiJob(type=organize_raw)`。
- 走现有 Raw Organizer / VaultPlan / Policy / Diff 流程。
- bucket status = `organized`。

bucket id 不是任意文件入口，只能指向系统自己创建的 Raw/Inbox capture。

## Pending clarification

实现状态 (2026-05-18)：已落地最小可用闭环。Stage 2 classifier 在 `confidence_label=medium` + `intent=raw_capture` 时，由 OW 客户端合成 `bucket_relation` 候选并写入 `pending_clarifications`：当前 source 存在 active bucket 时给 3 候选（"补充到上一组" / "新建一组" / "取消"）；无 active bucket 时给 2 候选（"新建一组" / "取消"），IM 端均返回带数字编号的中文提示。Router 入口先 lazy expire 已超期 clarification，再依次尝试规则匹配数字 / 候选短语 / 取消词，命中即以保存的 `original_message` 套用所选动作并将 clarification 标记为 `resolved`；新消息不像澄清回复则自动 `cancelled` 旧 clarification，新消息走完整 rules + classifier 流程。`additional_payload_text` 抽取尚未接入。`raw_capture` 之外的 medium 情形（organize / diff / approve / reject 的低 confidence 输入）仍降级为 unclear。

当 Router 不能安全判断，但存在候选动作时，创建短期 clarification 状态。

默认 TTL：

```text
5m
```

最小字段：

```text
pending_clarifications:
- clarification_id
- source_key
- question_type
- original_message
- original_received_at
- candidate_actions
- status
- created_at
- expires_at
- resolved_at
```

`original_message` 必须有长度限制：

```text
max 8KB
```

超过 8KB 时不创建 pending clarification，直接要求用户用明确表达重发。

Clarification reply 只负责选择动作。实际写入的是保存的 `original_message`。

如果用户澄清回复中同时携带新内容，Stage 2 可以由小模型提取 `additional_payload_text`。写入时必须和 original message 分成两个输入块：

```markdown
## 输入 3

- 时间：原始消息时间
- 说明：clarified append

```text
原始消息...
```

## 输入 4

- 时间：澄清回复时间
- 说明：additional clarification payload

```text
澄清回复中的新增内容...
```
```

rules-only 不提取 additional payload。
