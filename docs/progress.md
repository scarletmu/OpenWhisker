# OpenWhisker 当前进度与交接说明

更新时间：2026-05-16

本文用于在不同机器之间切换开发时快速恢复上下文。长期架构仍以 `docs/architecture/` 和 `docs/phases/` 为准；本文只记录当前实施进度、验证状态和下一步优先级。

## 当前阶段

当前主线仍在 Phase 4：Wiki Agent Workflow。

Phase 4B 已完成 Matrix 私聊入口、approval/diff/reject 的基本闭环，并根据真实使用反馈改进了面向人类的 diff 预览格式。

Phase 4B.5 已提升为独立入口层能力：IM Intent Router。它位于 adapter 入站和 Core Adapter API 之间，目标是把自然语言 IM 输入归一化为受控结构化意图，降低 slash command 使用成本。

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

## 验证状态

- `go test ./...` 在 Stage 1 rules/bucket/outbox suppression/frontmatter rewrite 收敛后已通过。
- Stage 2 OpenAI-compatible intent classifier 骨架接入后尚未重新运行 `go test ./...`。
- 尚未做真实 Matrix + intent classifier 视觉验证。
- 尚未做真实 vault + LLM approve/apply 闭环验证。

## 关键文档

- `docs/phases/phase-4-wiki-agent-workflow.md`
- `docs/phases/phase-4-matrix-test-feedback.md`
- `docs/phases/phase-4-im-intent-router.md`
- `docs/architecture/im-intent-router.md`
- `docs/architecture/capture-bucket.md`
- `docs/architecture/intent-router-model-contract.md`

## 下一步优先级

1. 重新运行 `go test ./...`，优先确认 Stage 2 classifier 接入没有编译或测试回归。
2. 为 `OpenAIIntentClassifier` 和 Core model hard guard 补单元测试。
3. 用真实 Matrix + test vault 验证 `hybrid` 未配置 API key 时的 rules-only 体验。
4. 配置 `OPENWHISKER_INTENT_API_KEY` 后，用小模型验证 rules miss 场景。
5. 接入 pending clarification 状态机，处理 `medium` classifier 输出。
6. 做真实 vault + LLM approve/apply 闭环验证。

## 当前已知限制

- `pending_clarifications` 目前只有表结构，尚未接入运行流程。
- 小模型 `medium` 输出目前不会生成定向澄清，只会降级为 unclear。
- Stage 2 classifier 当前没有专门 audit jsonl。
- Natural language approve/reject 只绑定当前 source 下唯一 pending plan；多个 pending plan 时必须回到显式 slash 命令。
- 图片、文件、多模态 bucket 输入尚未实现。
