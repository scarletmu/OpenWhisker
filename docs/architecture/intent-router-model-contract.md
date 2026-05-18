# Intent Router 小模型 Contract

状态：partial implementation。Stage 1 已实现 rules-only fallback；OpenAI-compatible 小模型 classifier 已接入第一版，当前仅在 `hybrid` 且配置 `OPENWHISKER_INTENT_API_KEY` 时作为 rules miss fallback 使用；intent audit jsonl 已接入分类摘要记录；pending clarification reply extraction 尚未接入。

本文定义 Phase 4B.5 Intent Router 使用的小模型 contract。小模型只用于入口意图识别和 bucket 关系判断，不参与知识组织和 vault 写入。

## 配置

Intent Router 使用独立模型配置，不复用 Raw Organizer 的 `OPENWHISKER_LLM_*`：

```sh
export OPENWHISKER_INTENT_API_KEY=...
export OPENWHISKER_INTENT_BASE_URL=https://your-compatible-endpoint.example/v1
export OPENWHISKER_INTENT_MODEL=your-small-model
export OPENWHISKER_INTENT_ROUTER=hybrid
```

设计理由：

- intent 分类比 Raw Organizer 更轻量，应允许使用更便宜、更快的小模型。
- intent 模型只看当前消息和最小状态，不需要 vault 内容。
- intent 模型配置应能独立关闭、替换或降级，不影响 Raw Organizer。

默认 `hybrid` 没有 API key 时自动退化为 rules-only，不报错，也不在每条 Matrix 回复中提示。`/status` 或启动日志可以显示：

```text
Intent Router: hybrid (rules-only, model not configured)
```

## 调用参数

模型调用应使用低随机性参数：

```text
temperature = 0 or near 0
```

不要求模型生成自然语言命令，只要求输出 JSON。

## 输入上下文

允许发送：

- 当前用户消息。
- `source_kind`：`matrix`、`cli` 或 `unknown`。
- 当前 active bucket 的短摘要。
- 最近 3-5 条 OpenWhisker 交互类型摘要。
- 是否存在当前 source 下 pending plan。
- 当前 pending clarification 摘要。

不允许发送：

- vault notes。
- Raw/Inbox 全文列表。
- 当前 raw note 全文。
- Knowledge note 内容。
- AGENTS.md。
- Profile / Skill。
- 完整 pending plan diff。
- Matrix room 历史全文。
- Matrix room id、sender id、event id、完整 `source_key`。

Bucket 摘要示例：

```json
{
  "bucket_id": "bucket_xxx",
  "status": "active",
  "started_at": "2026-05-15T21:30:00+08:00",
  "updated_at": "2026-05-15T21:42:00+08:00",
  "topic_hint": "Phase4 Intent Router",
  "excerpt": "讨论 Intent Router 作为通用中间件...",
  "append_count": 3
}
```

最近交互摘要示例：

```json
[
  {"type": "raw_capture_created"},
  {"type": "raw_capture_appended"},
  {"type": "diff_shown"}
]
```

## 输出 schema

模型必须输出一个 JSON object：

```json
{
  "intent": "raw_capture",
  "target": "active_bucket",
  "capture_action": "append",
  "bucket_relation": "same_topic",
  "payload_text": "还有一个补充点...",
  "additional_payload_text": "",
  "confidence_label": "high",
  "confidence": 0.86,
  "reason": "用户在现有记录会话中补充同一主题"
}
```

字段范围：

```text
intent:
- raw_capture
- organize_request
- diff_request
- approve_request
- reject_request
- unclear

target:
- last
- today
- active
- active_bucket
- new_bucket
- none

capture_action:
- create
- append
- close
- none

bucket_relation:
- same_topic
- new_topic
- unrelated
- unclear

confidence_label:
- high
- medium
- low
```

`reason` 只用于调试和必要时展示，不能参与执行分发。

## Confidence 语义

`confidence` 不是校准概率。它是模型自评信号，不代表“模型有 86% 概率正确”。

Router 接受模型结果时必须同时看：

- 受控枚举字段是否合法。
- `confidence_label`。
- `confidence` 数字。
- deterministic hard guard。

第一版主要使用 `confidence_label`：

```text
high:
- 可以进入 hard guard；guard 通过后执行。

medium:
- 不执行，生成定向澄清问题。

low:
- unclear，不执行。
```

自然语言 `approve_request` 更严格：

```text
confidence_label = high
confidence >= 0.85
unique pending plan within source_key
```

## Hard guard

模型输出不能单独决定执行。

Append guard：

```text
append requires:
- active bucket exists
- active bucket source_key matches current source_key
- active bucket is not expired
- active bucket is not organized or closed
- bucket_relation = same_topic
- confidence_label = high
```

Create guard：

```text
create requires:
- intent = raw_capture
- no active bucket, or bucket_relation = new_topic
- confidence_label = high
```

Approval guard：

```text
diff / approve / reject requires:
- intent is matching workflow intent
- exactly one awaiting_approval plan within current source_key
- confidence_label = high
```

Approve additionally requires:

```text
confidence >= 0.85
```

Invalid output handling：

```text
invalid JSON -> unclear
unknown enum -> unclear
missing confidence_label -> unclear
missing required field -> unclear
model timeout/error -> fallback to rules result if any, else unclear
```

## Clarification

`medium` 结果应生成定向澄清，而不是通用 unclear。

示例：

```text
这条是要补充到当前记录组，还是作为一条新的记录？
```

`low` 或 invalid result 使用通用 unclear：

```text
我不确定你是想记录、整理、预览还是审批。你可以说：
- 记录这段：...
- 整理刚才那条
- 处理今天的 raw
- 预览一下
- 写进去
- 先不写
```

## Pending clarification reply extraction

当存在 pending clarification 时，小模型可用于解析：

```text
用户是否选择了候选动作
用户是否同时提供了 additional_payload_text
```

输出示例：

```json
{
  "clarification_action": "append_active_bucket",
  "additional_payload_text": "它应该支持图片",
  "confidence_label": "high",
  "confidence": 0.88,
  "reason": "用户明确选择补充当前组，并额外提供新内容"
}
```

落地规则：

- 永远先把 selected action 应用到保存的 `original_message`。
- 如果 `additional_payload_text` 非空且 high confidence，则作为单独 input block 写入。
- 如果 medium / low，则只处理 `original_message`，不捕获 additional payload。
- rules-only 只支持纯选择，不提取 additional payload。

## Privacy boundary

Intent model 不应看到比完成分类所需更多的信息。

特别是：

- 不发送完整 `source_key`。
- 不发送 room id、sender id、event id。
- 不发送 vault note 内容。
- 不发送完整 raw bucket 内容。
- 不发送 pending plan diff。

如果需要调试模型判断，应通过本地 jsonl audit 的结构化 metadata 调参，不默认复制用户全文。
