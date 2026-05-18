# OpenWhisker 当前进度与交接说明

更新时间：2026-05-18

本文用于在不同机器之间切换开发时快速恢复上下文。长期架构仍以 `docs/architecture/` 和 `docs/phases/` 为准；本文只记录当前实施进度、验证状态和下一步优先级。

## 当前阶段

当前主线仍在 Phase 4：Wiki Agent Workflow。

Phase 4B 已完成 Matrix 私聊入口、approval/diff/reject 的基本闭环，并根据真实使用反馈改进了面向人类的 diff 预览格式。

Phase 4B.5 已提升为独立入口层能力：IM Intent Router。它位于 adapter 入站和 Core Adapter API 之间，目标是把自然语言 IM 输入归一化为受控结构化意图，降低 slash command 使用成本。

当前代码状态：Stage 1 rules/bucket 主路径已落地；Stage 2 OpenAI-compatible intent classifier、hybrid rules-miss fallback、hard guard 测试和 intent audit jsonl 已落地。真实 Matrix 和真实 intent 小模型 smoke 仍待环境配置后验证。

## 已落地能力

- Matrix adapter 会为入站消息生成 `source_key`，用于 source-scoped bucket、plan binding 和 approval guard。
- `intent-router=hybrid|rules|off` 已接入 Matrix `poll-once` / `daemon`。
- `off` 保持 Phase 4B 旧行为：非 slash 文本直接 raw capture。
- `rules` / `hybrid` 支持 rules-first 自然语言入口：
  - `记录一下：...` 创建 active raw bucket。
  - `补充：...` / `继续：...` / `还有：...` 追加 active bucket。
  - `结束记录` / `这组结束` / `先到这里` 关闭 active bucket。
  - `整理刚才` / `处理这组` 整理 active bucket 或当前 source 最近 raw。
  - `处理今天` 只整理当前 source 今天的 raw。
  - `预览一下` / `写进去` / `先不写` 绑定当前 source 下唯一 awaiting approval plan。
- `capture_buckets` 已持久化到 SQLite，并包含状态、TTL、append count、raw hash guard 和 source_key。
- raw bucket append 通过受控 `rewrite_note` VaultOperation 更新 frontmatter `updated` 并追加 `## 输入 N` block。
- intent-triggered create / organize / approve / reject 会抑制 core outbox，由 router 同步回复 IM，避免重复通知。
- Stage 2 OpenAI-compatible intent classifier 已接入骨架：
  - 只在 `hybrid` 且配置 `OPENWHISKER_INTENT_API_KEY` 时启用。
  - rules miss 后才调用小模型。
  - 输入只包含当前消息、source kind、active bucket 摘要、当前 source 待审批 plan 数量。
  - 不发送 vault note、raw note 全文、diff、完整 source_key、room id 或 sender id。
  - 模型输出只作为候选意图，仍必须通过 hard guard。
- Stage 2 intent audit jsonl 已接入：
  - 通过 `OPENWHISKER_INTENT_AUDIT_FILE` 启用。
  - 记录 mode、source kind、rules/model intent、confidence、accepted intent 和错误摘要。
  - 不记录消息全文、完整 source_key、room id、sender id 或 payload text。

## 验证状态

- `go test ./...` 在 Stage 2 OpenAI-compatible intent classifier 骨架接入后已于 2026-05-16 在当前 Mac 上通过。
- `OpenAIIntentClassifier` 和 Core model hard guard 单元测试已于 2026-05-16 补齐；同时修正了 `hybrid` 模式 rules miss 后不会调用小模型 fallback 的问题。
- 真实 Matrix + test vault 已于 2026-05-16 验证 `hybrid` 未配置 `OPENWHISKER_INTENT_API_KEY` 时的 rules-only raw bucket 创建路径：`记录一下：...` 成功写入 `testdata/vault/Raw/Inbox/`，并创建 source-scoped active bucket。
- 已补充 classifier、Core hard guard 和 audit privacy 单元测试，覆盖 structured output、非法枚举拒绝、hybrid rules miss fallback、append 无 active bucket 拦截和 audit 不泄露 source_key/message。
- 2026-05-18 在当前 Mac 配置完整环境后跑真实 Matrix smoke：
  - `.env.local` 加入 `OPENWHISKER_INTENT_ROUTER=hybrid` 与 `OPENWHISKER_INTENT_API_KEY/_BASE_URL/_MODEL`，本地复用 `OPENWHISKER_LLM_*` 的值（当前 LLM 端点本身就是一个小模型），并新增 `OPENWHISKER_INTENT_AUDIT_FILE=data/intent-router.jsonl`。架构层面仍要求 INTENT 与 LLM 独立配置；复用只用于本地 smoke，不视为长期建议。
  - Matrix poll → adapter → intent router 全链路通：rules 命中消息成功生成 `bucket_a6f4e495337a` 与 raw note `testdata/vault/Raw/Inbox/job_8cf349b3bc00.md`，audit 记录 `rules_intent=raw_create, classifier_used=false, accepted=true`。
  - hybrid fallback 链路按预期触发：rules miss 后 classifier 被调用，audit 出现 `classifier_used=true` 行，过程不影响 bucket / inbox 状态。
  - 隐私守卫成立：audit jsonl 仅包含 `mode / source_kind / rules_intent / classifier_used / accepted_intent / error`，不写入消息全文、`source_key`、room id、sender id 或 payload text。
  - 本次 smoke 期间最初使用的 OpenAI-compatible 上游网关出现服务侧故障，所有 classifier 请求返回 `502 agent disconnected`（已在该 endpoint 日志侧确认是供应商瞬时故障，无关本项目代码）。错误被 classifier 优雅捕获并写入 audit，未导致进程崩溃或 bucket 状态损坏。
  - 随后切换到 DeepSeek (`https://api.deepseek.com/v1`) 作为 classifier 端点时暴露出 protocol-level 不兼容：DeepSeek 仅支持 `response_format: {"type":"json_object"}`，对 OpenAI 的 `json_schema` 直接返回 `400 invalid_request_error: "This response_format type is unavailable now"`。考虑到 DeepSeek 是 OpenWhisker 的官方目标 provider，classifier 改为 `json_object` 模式 + 客户端 `validateIntentClassifierOutput` 硬校验（详见 `docs/architecture/intent-router-model-contract.md` 实现备注 2026-05-18），Core hard guard 保持不变。
  - 切换后取得首条成功 round-trip：audit 行 `model_intent=raw_capture, target=active_bucket, capture_action=append, bucket_relation=same_topic, confidence_label=high, confidence=0.95, accepted_intent=raw_append, accepted=true`，证明 Matrix → adapter → intent router → DeepSeek classifier → audit 全链路打通。
  - 已知 DeepSeek `json_object` 偶发返回空 `content`（官方文档列为 known issue），目前由 classifier 当作普通失败写入 audit 并按 `unclear` 降级，未做重试，符合"先暴露后决策"原则。
- 2026-05-18 已为 audit 加入 `executed_action` 字段,区分"router 采纳的意图"与"executor 实际结果"。枚举见 `docs/architecture/intent-router-model-contract.md#audit-executed_action`:`created` / `appended` / `closed` / `organized_pending_approval` / `diff_shown` / `approved` / `plan_rejected` 表示 handler 成功;`rejected_no_active_bucket` / `rejected_no_pending_plan` / `rejected_ambiguous_pending_plan` 表示 handler 拒绝;`not_executed` / `failed` 覆盖未采纳和异常路径。每次 `HandleText` 仍只写一行 audit,在 handler 返回后填入。单测 `TestIntentRouterAuditExecutedActionRejectedNoActiveBucket` 覆盖 accepted=true 但 handler 拒绝的关键场景,`go test ./...` 全绿。
- 2026-05-18 已接入 pending clarification 状态机。classifier 在 `confidence_label=medium` 且当前 source 存在 active bucket 时,合成 `bucket_relation` 候选（补充到上一组 / 新建一组 / 取消）写入 `pending_clarifications` 表,IM 端给出带数字编号的中文澄清提示。澄清回复优先级最高:`HandleText` 入口先 lazy expire 当前 source 的过期 clarification,然后尝试规则匹配数字 / 候选短语 / 取消词,命中即以保存的 `original_message` 套用所选动作并标记 `resolved`;若新消息不像澄清回复则自动 cancel 旧 clarification,新消息走完整 rules + classifier 流程。新增 `executed_action` 枚举值:`clarification_requested` / `clarification_cancelled`,audit 行额外携带 `clarification_id` 与 `clarification_resolution`,仍不写入 message 全文 / source_key。单测 `TestIntentRouterClassifierMediumCreatesPendingClarification` / `TestIntentRouterReplyResolvesPendingClarificationAndAppends` / `TestIntentRouterNewMessageAutoCancelsPendingClarification` 覆盖创建 / 回复解析 / 自动取消三条路径,`go test ./...` 全绿。当前 TTL 固定 5 分钟,过期由下次 `HandleText` 入口惰性清理。
- 2026-05-18 本地扩展批次（无新真实反馈,所有更改纯属代码层硬化,行为 backward-compatible）:
  - clarification 回复匹配支持更丰富的输入变体：`1)` / `1）` / `(1)` / `（1）` / `[1]` / `【1】` / `1.` / `1。` / `选1` / `我选 1` / `第 1` / `要 1` / `选项1`;append/create/cancel 候选短语词表显著扩展（如 `加上去` / `合并` / `接着上一组` / `另开一组` / `单独一组` / `都不用` 等）。单测 `TestParseCandidateNumberAcceptsCommonVariants` / `TestLabelMatchesReplyKnownSynonyms` / `TestMatchClarificationReplyHandlesNumberAndPhraseAndCancel` 覆盖。
  - rules-only 入口短语词表扩展：create 前缀新增 `记一下：` / `写下来：` / `存一下：` / `记下：`；append 前缀新增 `再加：` / `再补充：` / `后面还有：` / `另外：`；close/organize/diff/approve/reject exactAny 词表分别新增 `结束这组` / `到这里` / `就这些` / `整理一下` / `处理一下` / `看一下` / `看看` / `同意` / `通过` / `写吧` / `驳回` / `这版重来` 等。单测 `TestClassifyIntentRulesCoversCommonPhrasings` 覆盖。
  - clarification 合成扩展到 `raw_capture` 但无 active bucket 的 medium 情形：给出 2 候选（新建一组 / 取消）。原有 active bucket 路径仍给 3 候选（补充到上一组 / 新建一组 / 取消）,不变。单测 `TestIntentRouterClassifierMediumWithoutActiveBucketOffersTwoCandidates` / `TestIntentRouterClarificationReplyCreatesBucketWhenNoActiveBucket` 覆盖。
  - `OpenAIIntentClassifier.ClassifyIntent` 对 OpenAI-compatible response 的空 `content` 做单次重试（同一请求体,无指数退避）。两次都空则报 `empty content after one retry`,被 router 当作普通分类错误降级为 unclear,行为与旧路径一致。单测 `TestOpenAIIntentClassifierRetriesOnceOnEmptyContent` / `TestOpenAIIntentClassifierFailsAfterTwoEmptyContents` 覆盖。
- 尚未做真实 vault + LLM approve/apply 闭环验证。

## 关键文档

- `docs/phases/phase-4-wiki-agent-workflow.md`
- `docs/phases/phase-4-matrix-test-feedback.md`
- `docs/phases/phase-4-im-intent-router.md`
- `docs/architecture/im-intent-router.md`
- `docs/architecture/capture-bucket.md`
- `docs/architecture/intent-router-model-contract.md`

## 下一步优先级

1. 做真实 vault + LLM approve/apply 闭环验证。
2. 真实 Matrix + DeepSeek 验证 medium clarification 闭环（同 topic / 新 topic 模糊消息；以及"无 active bucket 时给 2 候选"路径）。
3. 根据真实使用反馈进一步扩展 rules-only 短句词表与 clarification 回复词表（已做一次本地硬化；继续扩张应基于实际未命中样本）。

## 当前已知限制

- clarification 合成目前覆盖 `raw_capture` + 有 active bucket（3 候选）和 `raw_capture` + 无 active bucket（2 候选）两种情形；其它 medium 情形（organize / diff / approve / reject 的低 confidence 输入）仍降级为 unclear。
- clarification reply 不抽取 `additional_payload_text`，澄清回复中携带的新内容会和保存的 `original_message` 一并丢失；rules-only 路径短期不计划实现，hybrid 路径要等 classifier prompt 升级再补。
- Stage 2 classifier audit jsonl 只记录分类摘要，尚未接入更完整的运行状态观测面板。
- Natural language approve/reject 只绑定当前 source 下唯一 pending plan；多个 pending plan 时必须回到显式 slash 命令。
- 图片、文件、多模态 bucket 输入尚未实现。
- DeepSeek `json_object` 偶发空 content 已有单次重试兜底；仍有概率两次都空，此时降级为 unclear。当前未做更复杂的退避或参数扰动。
