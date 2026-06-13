# IM Intent Router 架构规格

状态：partial implementation。Stage 1 rules-only 入口、`source_key` 绑定、Matrix 接入、source-scoped approval guard、raw bucket append metadata rewrite、intent-triggered create/organize/approve/reject outbox suppression、Stage 2 OpenAI-compatible classifier、intent audit jsonl，以及 `bucket_relation=unclear` 触发的 pending clarification 状态机（创建 / 数字+短语规则匹配回复 / 新消息自动 cancel / 5 分钟 TTL 惰性 expire）已落代码；澄清回复抽取 `additional_payload_text` 尚未实现。

> **方向提示**：本文记录当前**已落代码**的 bucket + 审批意图路由实现。IM 入口正按 [`im-quick-capture.md`](im-quick-capture.md) 重定位为零摩擦速记收件箱——届时 `organize_request` / `diff_request` / `approve_request` / `reject_request` 四类意图与 topic-bucket 绑定将收敛或移除，输入路由退化为"链接还是文字 + 三个保留命令（看今天 / 撤回上一条 / 找一下）"。本文描述的是**现状**，不是目标形态；改造落代码前不要据此假设未来行为。

本文定义 OpenWhisker 的通用 IM Intent Router 边界。它是 adapter 入站消息和 Core Adapter API 之间的中间件，用于把自然语言入口归一化成受控结构化意图。

## 位置

```text
Adapter inbound message
  -> Intent Router
  -> IntentResult
  -> Adapter calls Core Adapter API
```

Router 不直接调用 Core。Adapter 负责根据 `IntentResult` 调用 Core，并负责 IM 回复格式、source metadata、outbox 和 adapter-specific 行为。

## 责任边界

Router 可以：

- 识别当前消息的入口意图。
- 输出结构化 `IntentResult`。
- 根据传入状态判断是否可绑定 active bucket 或 pending plan。
- 返回澄清问题。
- 写本地 jsonl audit log。

Router 不可以：

- 直接写 vault。
- 直接调用 `VaultExecutor`。
- 生成 `VaultPlan`。
- 调用 Raw Organizer。
- 读取 vault notes、AGENTS.md、Profile 或 Skill。
- 生成任意命令文本供 Core 执行。
- 绕过 policy、diff、approval、hash guard 或 sync-aware executor。

## 上游无关性

Router 不接收 Matrix event 原始结构。Adapter 只传递通用输入：

```text
IntentInput:
- text
- source_key
- source_kind
- router_mode
- received_at
- active_bucket_summary
- pending_clarification_summary
- pending_plan_summary
- recent_interaction_summary
```

`source_key` 是 adapter 生成的不透明会话隔离键。Router 不解析其中结构。

Matrix 第一版：

```text
source_key = matrix:<room_id>:<sender_id>
source_kind = matrix
```

Intent 模型上下文可以包含 `source_kind`，但不能包含 room id、sender id、event id 或完整 `source_key`。

## 输出 schema

Router 主输出是结构化对象，不是命令字符串。

```text
IntentResult:
- intent
- target
- capture_action
- bucket_relation
- payload_text
- additional_payload_text
- confidence_label
- confidence
- reason
- bound_plan_id
- bucket_id
- display_action
- clarification
- accepted
- reject_reason
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

`display_action` 只用于 IM 展示，例如：

```text
识别为：整理当前记录组
识别为：批准 plan_xxx
```

Core 调用必须基于结构化字段分发，不能重新 parse `display_action`。

## Router mode

第一版支持：

```text
hybrid
rules
off
```

优先级：

```text
CLI flag > env > default hybrid
```

行为：

```text
hybrid + configured intent model -> rules + model
hybrid + no model config -> rules-only
rules -> only deterministic rules
off -> bypass Intent Router completely
```

`off` 必须完全保持 Phase 4B 当前行为：

```text
slash command -> existing command parser
non-slash text -> existing raw capture
```

## 规则优先级

Router 分类顺序固定：

```text
1. slash command passthrough
2. pending clarification reply
3. approval workflow intents: diff / approve / reject
4. organize intents
5. capture control intents: close bucket / explicit create / explicit append
6. model-assisted ambiguous classification
7. unclear
```

关键原则：

- `approval > capture`
- `organize > capture`
- `clarification reply` 只在命中候选动作时优先。
- slash command 绕过所有 Router 逻辑。

## Rules-only 能力

rules-only 可以处理：

- 明确“记录一下：...”创建 bucket。
- 明确“补充：...”且 active bucket 存在时 append。
- 明确“继续：...”且 active bucket 存在时 append。
- “结束记录”“这组结束”关闭 bucket。
- “整理刚才”“处理这组”整理 active bucket 或 fallback 到 last。
- “预览一下”触发 source-scoped diff。
- “写进去”“确认写入”“批准”触发 source-scoped approve。
- “先不写”“不要写”“拒绝”“这版不行”触发 source-scoped reject。

rules-only 不做：

- 判断直接粘贴长文本是否 raw。
- 判断模糊消息是否同 topic。
- 从澄清回复中提取新增 payload。
- 自动判断新旧 topic 切换。

## Approval intent guard

自然语言 `diff` / `approve` / `reject` 只绑定当前 `source_key` 下唯一 `awaiting_approval` plan。

```text
0 pending plan -> no execution, explain no pending plan
1 pending plan -> bind plan id
2+ pending plans -> no execution, ask explicit command
```

显式 slash 命令带 `plan_id` / `job_id` 时保持现有行为，不受自然语言 active binding 限制。

Approve 规则层触发词应保守：

```text
写进去
确认写入
批准
批准这个
approve
apply
```

不建议规则层直接把这些词当 approve：

```text
可以
好
行
就这样
嗯
```

这些模糊短句只能由模型在 high confidence 且唯一 pending plan 时判断。

Reject 规则层触发词：

```text
先不写
不写
不要写
拒绝
reject
这版不行
取消这个计划
```

## Source binding

Phase 4B.5 需要补 source binding，不能复用 `SourceRefs`。

最低要求：

- Adapter 生成稳定 `source_key`。
- `AdapterRequest` 携带 `source_key`。
- Raw ingest job 持久化 `source_key`。
- Capture bucket 持久化 `source_key`。
- Organize job 从 active bucket 或 adapter request 继承 `source_key`。
- VaultPlan 必须可按 origin `source_key` 查询。
- Pending plan resolver 用 `source_key` 过滤。

实现上可以选择：

```text
wiki_jobs.source_key
vault_plans join wiki_jobs by job_id
```

也可以在 `vault_plans` 冗余 `source_key`。无论具体 schema 如何，行为上必须支持 source-scoped pending plan resolution。

## Natural organize semantics

自然语言：

```text
整理刚才 / 处理这组 / 整理这段
```

规则：

```text
if active bucket exists:
  organize active bucket
else:
  fallback to existing organize last semantics
```

## Matrix 接入

第一版只接入：

```sh
openwhisker matrix poll-once --intent-router=hybrid|rules|off
openwhisker matrix daemon --intent-router=hybrid|rules|off
```

普通 CLI 命令不接 Router。未来如果增加 interactive CLI，可复用同一 Router。

## Audit log

Intent audit 默认写 jsonl，不进入 SQLite core tables。

默认路径：

```text
data/intent-router.jsonl
```

可配置：

```sh
OPENWHISKER_INTENT_AUDIT_FILE=data/intent-router.jsonl
OPENWHISKER_INTENT_DEBUG_LOG_TEXT=1
```

默认记录结构化 metadata，不记录完整用户文本：

```json
{"ts":"2026-05-15T22:10:00+08:00","source_key":"matrix:...","mode":"hybrid","intent":"raw_capture","capture_action":"append","bucket_relation":"same_topic","confidence_label":"high","confidence":0.86,"accepted":true,"bucket_id":"bucket_xxx","model_used":"intent-model"}
```

开启 debug 后才允许记录截断 excerpt，例如前 80 字。

运行状态不能依赖 jsonl 恢复；bucket 和 clarification 必须进入 SQLite。
