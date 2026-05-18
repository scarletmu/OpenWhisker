# Phase 4B.5 IM Intent Router

状态：planned，已提升为独立入口层能力。

本文是 Phase 4B.5 的阶段入口页。详细实现规格拆分到：

- [IM Intent Router 架构规格](../architecture/im-intent-router.md)
- [Capture Bucket 规格](../architecture/capture-bucket.md)
- [Intent Router 小模型 Contract](../architecture/intent-router-model-contract.md)

## 背景

真实 Matrix + vault 使用反馈显示，当前 slash 命令已经能跑通 raw capture、organize、diff、approve、reject，但日常在 IM 里反复输入 `/organize`、`/diff`、`/approve`、`/reject` 仍然偏命令行化。

Phase 4B.5 的目标是把 IM 入口从“命令触发”推进到“自然语言意图触发”，同时不改变 OpenWhisker 的核心安全边界。

## 总体目标

在 adapter 入站消息和 Core Adapter API 之间加入一个通用 Intent Router：

```text
Adapter inbound message
  -> Intent Router
  -> structured IntentResult
  -> Adapter orchestrates Core Adapter API
  -> WikiJob / VaultPlan / Diff / Approval / VaultExecutor
```

Intent Router 是 adapter-agnostic 中间件：

- 不耦合 Matrix event、room、event id 或 outbox。
- 不直接调用 Core 执行。
- 不直接写 vault。
- 不生成 `VaultPlan`。
- 不调用 `VaultExecutor`。
- 不绕过 policy、diff、approval、hash guard 或 sync-aware executor。

## 第一版范围

第一版覆盖知识流入核心闭环：

- `raw_capture`：创建或追加当前记录组。
- `organize_request`：整理当前记录组、最近 raw 或当前 source 当天 raw。
- `diff_request`：预览当前 source 下唯一待审批 plan。
- `approve_request`：批准当前 source 下唯一待审批 plan。
- `reject_request`：拒绝当前 source 下唯一待审批 plan。
- `unclear`：无法安全判断，不写入、不执行。

明确不做：

- 不覆盖 Knowledge Expander。
- 不覆盖 Profile / Skill 生成。
- 不覆盖 scheduler、daily review 或 weekly maintenance。
- 不覆盖一般闲聊能力。
- 不支持图片 / 文件 capture；第一版只处理文本。
- 不把自然语言转成任意 shell、Obsidian CLI 或 vault write。
- 不让小模型直接生成 `VaultPlan` 或 `VaultOperation`。

## 为什么仍属于 Phase 4

Phase 5 的主题是 Maintenance 与 Scheduler，即系统主动维护 vault 健康，例如 daily raw review、weekly report、broken link check 和 stale needs-review report。

Intent Router 解决的是 Phase 4 真实 Matrix approval workflow 的入口体验问题。它让用户用“整理刚才”“预览一下”“写进去”“先不写”推进现有受控流程，而不是新增系统主动维护能力。

## 已确认设计结论

- Intent Router 是通用中间件，不关心上游是否 Matrix。
- Router 输出结构化 `IntentResult`，命令字符串只用于 IM 展示。
- Adapter 根据 `IntentResult` 编排调用 Core，Router 不调用 Core。
- 默认 `hybrid`；没有 intent 小模型配置时自动降级 `rules-only`。
- `off` 模式完全绕过 Router，保持现有非 slash 文本自动 raw capture 行为。
- 引入 `source_key` 做入口会话隔离；Matrix 第一版为 `matrix:<room_id>:<sender_id>`。
- 自然语言 `diff` / `approve` / `reject` 只绑定当前 `source_key` 下唯一 `awaiting_approval` plan。
- 引入 `active raw bucket`，按 topic / task 聚合准备被同一次 organize 处理的 raw material。
- bucket / pending clarification 按 `source_key` 隔离并持久化。
- bucket 每次 append 都写入 `Raw/Inbox`，并使用轻量 hash guard。
- organized bucket 不再 append；补充内容需要新建 bucket。
- 自然语言“处理今天”只处理当前 `source_key` 今天产生的 raw；显式 `/organize today` 保持当前 global 语义。

## Stage 1：rules-only + 状态骨架

Stage 1 先不依赖 intent 小模型，完成安全骨架和常见短句体验。

实现目标：

- adapter 生成并传递 `source_key`。
- Core 持久化 job / plan 的 source binding。
- 新增 `capture_buckets` 和 `pending_clarifications` 运行状态。
- 新增 rules classifier。
- 支持明确 raw create / append / close。
- 支持 `OrganizeCaptureBucket(source_key, bucket_id)`，内部仍生成标准 `WikiJob(type=organize_raw)`。
- 支持 source-scoped pending plan resolver。
- 接入 `matrix poll-once` 和 `matrix daemon`。
- 支持 `--intent-router=hybrid|rules|off`，默认 `hybrid`。

Stage 1 不做：

- 不判断直接粘贴长文本是否 raw。
- 不判断模糊消息是否同 topic。
- 不从澄清回复中提取新增 payload。
- 不调用 intent 小模型。

## Stage 2：OpenAI-compatible small intent model

Stage 2 接入小模型增强模糊意图识别和 bucket 关系判断。

实现目标：

- 支持独立 `OPENWHISKER_INTENT_*` 配置。
- 小模型只看当前消息、active bucket 摘要、最近交互类型摘要、pending plan 是否存在。
- 小模型不看 vault notes、Raw 全文、Knowledge 内容、AGENTS.md、Profile、Skill、完整 diff 或 Matrix 历史全文。
- 输出受控 JSON schema。
- 使用 `confidence_label` 作为主要接受信号，`confidence` 只作调试和调参辅助。
- `medium` 走定向澄清，`low` / invalid JSON fail closed。
- 支持从澄清回复中提取 `additional_payload_text`。
- intent audit 写本地 jsonl，不进 SQLite。

## Matrix 行为

Slash 命令保持 fallback / debug 能力：

```text
/raw <text>
/organize last
/organize today
/diff <plan_id|job_id>
/approve <plan_id|job_id>
/reject <plan_id|job_id>
/status
/jobs
```

自然语言识别后，Matrix 回复应展示归一化动作：

```text
识别为：整理当前记录组

Plan plan_xxx awaits approval: ...
```

批准、拒绝、diff 同理：

```text
识别为：批准 plan_xxx

Plan plan_xxx applied.
```

输出继续同时支持 plain text fallback 和 Matrix `org.matrix.custom.html` `formatted_body` 渲染。正文使用 Markdown 标题、列表和代码块，不依赖客户端私有格式。

## 验收口径

实现完成后至少覆盖：

- `--intent-router=off` 完全保持 Phase 4B 当前行为。
- slash 命令不经过 intent 分类，行为保持不变。
- 没有 `OPENWHISKER_INTENT_API_KEY` 时，默认 `hybrid` 自动降级 rules-only。
- “记录一下：...” 创建 active bucket 并写 Raw/Inbox。
- “补充：...” 在 active bucket 存在且未过期时 append。
- “结束记录”关闭 active bucket。
- “整理刚才”优先整理 active bucket；没有 active bucket 时 fallback 到现有 last 语义。
- 自然语言“处理今天”只处理当前 `source_key` 今天 raw。
- 显式 `/organize today` 保持现有 global 语义。
- “预览一下”“写进去”“先不写”只绑定当前 `source_key` 下唯一 pending plan。
- 没有 pending plan 或多个 pending plan 时不执行。
- organized bucket 不再 append。
- hash mismatch 时关闭 bucket 并 fail closed，不自动创建新 bucket。
- unclear 输入不写入 Raw/Inbox。
- Matrix 回复包含 `识别为：...` 并继续支持 `formatted_body`。

## 当前结论

Phase 4B.5 先按两阶段推进：先完成 rules-only + bucket state + source_key guard，再接入 OpenAI-compatible 小模型。这样即使没有模型配置，也能先改善常见短句体验，并保持失败时不写入、不执行。

## 实施阶段成果

### 2026-05-15 Stage 1 代码骨架

本次已推进 rules-only Stage 1 的核心代码路径：

- `WikiJob` 增加 `source_key`，SQLite 通过 additive migration 补齐。
- 新增 `capture_buckets` / `pending_clarifications` 持久化表；本轮先落 bucket 主路径，clarification 先保留表结构。
- Matrix 入站事件生成 `source_key = matrix:<room_id>:<sender_id>`，并传入 Core Adapter API。
- Matrix `poll-once` / `daemon` 增加 `--intent-router=hybrid|rules|off`，默认 `hybrid`。
- `off` 模式绕过 Intent Router，保留 Phase 4B 非 slash 文本自动 raw capture 行为。
- rules-only 支持：
  - “记录一下：...”创建 active bucket 并写 `Raw/Inbox`。
  - “补充：...”/“继续：...”/“还有：...”追加 active bucket。
  - “结束记录”/“这组结束”/“先到这里”关闭 active bucket。
  - “整理刚才”/“处理这组”优先整理 active bucket，无 active bucket 时 fallback 到当前 `source_key` 最近 raw。
  - “处理今天”只整理当前 `source_key` 今天的 raw。
  - “预览一下”/“写进去”/“先不写”绑定当前 `source_key` 下唯一 `awaiting_approval` plan。
- Core 新增受控 `AppendRawBucket` 和 `OrganizeCaptureBucket` 路径，均校验 `source_key`。
- bucket append / organize 使用 `raw_hash_after_last_append` 做轻量 hash guard；mismatch 时 bucket 进入 `hash_mismatch`，不自动创建新 bucket。
- 新增低风险 `rewrite_note` VaultOperation，raw bucket append 通过 hash guard 重写 `Raw/Inbox` 目标文件，从而同时更新 frontmatter `updated` 和追加新的 `## 输入 N` block。
- natural language 识别后的 Matrix/plain text 回复包含 `识别为：...`，仍使用 Markdown 文本以支持 Matrix `formatted_body` 渲染。
- intent router 触发的 raw bucket create、organize、approve、reject 路径会抑制对应 core outbox，由 router 同步返回 IM 回复；slash 命令和 CLI 路径继续保留 outbox 通知。
- 旧 Phase 4B adapter 行为测试已显式使用 `intent-router=off`，避免和默认 `hybrid` 的自然语言 router 行为混淆。
- 已通过 `go test ./...` 验证；测试覆盖到 CLI 编译、adapter 旧行为、Matrix adapter、core、executor、policy、profile。

本阶段已知限制：

- Stage 2 intent 小模型已接入第一版 OpenAI-compatible classifier：`hybrid` 在配置 `OPENWHISKER_INTENT_API_KEY` 时会在 rules-only 未命中后调用小模型；未配置时仍自动降级 rules-only。
- `medium` 定向澄清已接入 pending clarification 状态机（2026-05-18）：classifier 返回 `medium` + 当前 source 存在 active bucket 时，OW 合成 `bucket_relation` 候选写入 `pending_clarifications` 并发出带数字编号的中文 IM 提示；澄清回复通过规则匹配（数字 / 候选短语 / 取消词）解析，新消息非澄清回复则自动 cancel 旧 clarification。TTL 固定 5 分钟，由下次 `HandleText` 入口惰性 expire。
- 澄清回复中 `additional_payload_text` 抽取尚未实现；当前回复只选择候选动作，将保存的 `original_message` 重新分发到对应 handler。
- Stage 2 classifier + pending clarification 已补充单元测试并通过 `go test ./...`；medium 路径尚未真实 Matrix 验证。

### 2026-05-15 Stage 2 小模型接入骨架

本次继续推进了 Stage 2 的最小可运行骨架：

- Core 新增 `IntentClassifier` 抽象和最小输入/输出结构，Core 不直接依赖 `internal/agent`。
- `internal/agent` 新增 OpenAI-compatible intent classifier，复用现有 chat completions client 和 JSON schema structured output。
- Matrix `poll-once` / `daemon` 在 `--intent-router=hybrid` 且存在 `OPENWHISKER_INTENT_API_KEY` 时注入 classifier；`rules` / `off` 不调用模型。
- classifier 输入只包含当前消息、`source_kind`、active bucket 短摘要和当前 source 待审批 plan 数量，不发送完整 `source_key`、vault note、raw note 全文或 diff。
- Core dispatch 仍先走 deterministic rules；只有 rules 未命中且 `hybrid` 配置了 classifier 时才调用模型。
- 模型输出只作为候选意图，必须继续通过 hard guard：append 必须有 active bucket 且 `bucket_relation=same_topic`，approve 必须 `confidence>=0.85` 且后续 source-scoped unique pending plan guard 通过。
- 已修复 rules miss 返回 `unclear` 阻断 hybrid fallback 的问题；当前只有明确 rules 命中会跳过模型。
- 已接入 `OPENWHISKER_INTENT_AUDIT_FILE` jsonl audit，记录分类摘要和接受结果，不记录消息全文、完整 `source_key`、room id、sender id 或 payload text。
- 已补充 `OpenAIIntentClassifier` structured output/validation 测试，以及 Core hybrid fallback / append hard guard / audit privacy 测试。

本节点已执行 `go test ./...` 并通过。下一步需要做真实 Matrix rules-only 视觉验证和配置 `OPENWHISKER_INTENT_API_KEY` 后的小模型 rules miss smoke。
