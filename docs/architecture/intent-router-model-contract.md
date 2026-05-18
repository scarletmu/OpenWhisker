# Intent Router 小模型 Contract

状态：partial implementation。Stage 1 已实现 rules-only fallback；OpenAI-compatible 小模型 classifier 已接入第一版，当前仅在 `hybrid` 且配置 `OPENWHISKER_INTENT_API_KEY` 时作为 rules miss fallback 使用；intent audit jsonl 已接入分类摘要记录；`confidence_label=medium` 触发的 pending clarification 创建 / 规则回复匹配 / 自动 cancel 已落地（候选目前由 OW 客户端基于 raw_capture+active_bucket 合成，模型暂不直接产出 `candidate_actions`）；澄清回复中 `additional_payload_text` 的抽取尚未接入。

实现备注（2026-05-18）：classifier 请求体使用 `response_format: {"type": "json_object"}`，不依赖 OpenAI 的 strict `json_schema` 类型，以兼容 DeepSeek 等仅支持 `json_object` 的 OpenAI-compatible 端点。enum 与字段约束放在 system prompt 显式声明，并依赖客户端 `validateIntentClassifierOutput` 做硬校验；非法 enum / 缺字段 / 非法 JSON 仍按下文"Invalid output handling"降级。Core hard guard 不受影响，仍是最外层兜底。`OpenAIIntentClassifier.ClassifyIntent` 在响应 `content` 为空字符串（含纯空白）时做一次同请求体重试（无指数退避），两次都空则返回 `empty content after one retry` 错误并由 router 按 unclear 降级——这是针对 DeepSeek `json_object` 已知偶发空 content 的轻量兜底，单测 `TestOpenAIIntentClassifierRetriesOnceOnEmptyContent` / `TestOpenAIIntentClassifierFailsAfterTwoEmptyContents` 覆盖。

实现备注 (2026-05-18, audit executed_action)：audit 行新增 `executed_action` 字段,在 handler 返回后填入,反映 executor 实际结果而非 router 采纳决策。`accepted=true && executed_action=rejected_*` 表示 router 采纳意图但 deterministic guard / handler 拒绝执行。枚举完整列表见 "Audit executed_action" 一节。

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

## Audit executed_action

`accepted_intent` 表示 router 采纳了哪个意图,不表示 executor 实际写入了 vault。例如分类器返回合法的 `raw_append` + `same_topic` + `high`,router 采纳但运行时 active bucket 已不存在,append handler 仍会返回 `no_active_bucket`,此时 audit 行 `accepted=true` 但实际未写入。

为了让 audit 反映执行结果,每条 audit 在 handler 返回后写入 `executed_action`,枚举范围:

```text
created                        - raw_create handler 成功写入 vault
appended                       - raw_append handler 成功追加 active bucket
closed                         - raw_close handler 成功关闭 active bucket
organized_pending_approval     - organize handler 生成 plan,等待审批
diff_shown                     - diff handler 展示当前 source 唯一 plan diff
approved                       - approve handler 应用了 plan
plan_rejected                  - reject handler 拒绝了 plan
rejected_no_active_bucket      - raw_append / raw_close handler 因无 active bucket 拒绝
rejected_no_pending_plan       - diff / approve / reject 因 source 无 pending plan 拒绝
rejected_ambiguous_pending_plan- diff / approve / reject 因 source 有多个 pending plan 拒绝
clarification_requested        - medium classifier 输出触发,router 已写入 pending_clarifications 并发出 IM 数字编号提示
clarification_cancelled        - 用户回复匹配到 cancel 候选,旧 clarification 被标记为 cancelled
not_executed                   - intent 未被采纳(unclear / 分类器低置信度 / 缺 active bucket guard 等),无 handler 运行
failed                         - handler 返回 error
```

回复解析路径不写新枚举值:用户回复匹配到非 cancel 候选时,audit 行的 `executed_action` 与所选动作执行结果一致(`appended` / `created` 等),并额外携带 `clarification_id` 与 `clarification_resolution=resolved`。新消息无法匹配候选时旧 clarification 被自动取消,audit 行携带 `clarification_id` 与 `clarification_resolution=cancelled_superseded`,`executed_action` 反映新消息自身的执行结果。

每次 `HandleText` 只写入一行 audit,在 handler 返回后一次性附加 `executed_action`,不重复写入分类摘要。`accepted` / `accepted_intent` 仍保留旧语义,与 `executed_action` 联合解读:`accepted=true && executed_action=appended` 是成功路径;`accepted=true && executed_action=rejected_*` 表示 router 采纳了意图但 deterministic guard / handler 拒绝了执行。

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
