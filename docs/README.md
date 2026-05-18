# OpenWhisker 文档索引

本文是 `docs/` 的入口，负责说明当前文档结构和阅读顺序。

## 推荐阅读顺序

1. [架构概览](architecture/overview.md)：当前实现边界和系统组件。
2. [当前进度与交接说明](progress.md)：跨机器切换开发时的当前状态、验证状态和下一步优先级。
3. [项目决策](project-decisions.md)：跨阶段仍然有效的架构决策。
4. [Phase 1 最小切片](phases/phase-1-minimal-slice.md)：低风险 raw capture 基线。
5. [Phase 2 审批与 Diff](phases/phase-2-approval-diff.md)：中风险 approval 和 hash guard 闭环。
6. [Phase 3 Sync-Aware Approval Execution](phases/phase-3-headless-sync-executor.md)：已落地的受控 Headless `ob` sync client、手动 sync 命令和 sync-aware approval execution。
7. [Phase 4 Wiki Agent Workflow](phases/phase-4-wiki-agent-workflow.md)：推进中的真实 LLM + Matrix IM approval workflow、Raw Organizer、Knowledge Expander 和 proposal policy。
8. [Phase 4B.5 IM Intent Router](phases/phase-4-im-intent-router.md)：partial implementation；独立入口层能力，用规则优先 + 小模型补充改善 Matrix 自然语言入口体验。
9. [IM Intent Router 架构规格](architecture/im-intent-router.md)：Intent Router 中间件边界、source_key、IntentResult、rules 优先级和 source-scoped approval binding。
10. [Capture Bucket 规格](architecture/capture-bucket.md)：active raw bucket、pending clarification、Raw append 格式、hash guard 和 bucket 生命周期。
11. [Intent Router 小模型 Contract](architecture/intent-router-model-contract.md)：小模型输入上下文、JSON schema、confidence 语义、hard guard 和隐私边界。
12. [设计哲学](architecture/design-philosophy.md)：完整 VaultPlan / VaultExecutor 方向。
13. [CubeSandbox 运行沙箱评估](architecture/cubesandbox-runtime-sandbox-evaluation.md)：Agent 工具运行时沙箱候选方案评估。

## 目录结构

```text
docs/
  README.md
  progress.md
  project-decisions.md
  architecture/
    overview.md
    design-philosophy.md
    im-intent-router.md
    capture-bucket.md
    intent-router-model-contract.md
    cubesandbox-runtime-sandbox-evaluation.md
  phases/
    phase-1-minimal-slice.md
    phase-2-approval-diff.md
    phase-3-headless-sync-executor.md
    phase-4-wiki-agent-workflow.md
    phase-4-im-intent-router.md
    phase-4-matrix-test-feedback.md
  adapters/
    matrix-private-im.md
  skills/
    vault-profile-analyzer/
      SKILL.md
```

## 当前实现索引

- Phase 1：`ingest raw` 低风险 raw capture 已落地，默认写入 `testdata/vault/Raw/Inbox/`。
- Phase 2：`organize last`、`plan diff`、`plan approve`、`plan reject` 审批闭环已落地，包含 diff、`before_hash` guard、vault lock 和 operation log。
- Phase 3：`vault sync-status`、`vault sync` 和 `plan approve --sync=auto|off|on` 已落地；默认 test vault 不触发外部 sync，显式真实 vault approval 默认启用 Headless one-shot sync。
- Phase 4：推进中；Matrix IM MVP、Core Adapter API、Raw Organizer contract、受控 vault context、`VaultProfile -> VaultSkill` 边界、`vault profile preview` 本地 skill bundle 预览、外部 vault-local `vault-profile-analyzer` Skill 模板、可显式启用的 OpenAI-compatible Raw Organizer 最小路径、第一版 agent output policy gate、Raw/Processed processing note 写入、长期 Matrix daemon，以及 `organize today` grouped plan 已落地。真实 Matrix + test vault deterministic 闭环、真实 vault + deterministic diff/reject、test vault + OpenAI-compatible provider smoke、真实 vault + OpenAI-compatible LLM diff/reject 已验证；真实 vault + LLM approve/apply 闭环仍未验证。Phase 4B 使用反馈已收敛一项：Matrix `/diff` 已从工程化 operation 清单改成第一版人类审批页，并继续通过 Matrix adapter 支持 `formatted_body` 渲染。Phase 4B.5 已进入 partial implementation：Intent Router 作为 adapter-agnostic 中间件，通过 source_key、capture bucket、pending clarification 表结构、rules-only 安全骨架、OpenAI-compatible intent classifier、hybrid rules-miss fallback、hard guard 单元测试和 privacy-safe intent audit jsonl，把 Matrix 自然语言入口归一化为受控结构化意图；该项属于 Phase 4 使用体验改进，不是 Phase 5 scheduler / maintenance。当前 `go test ./...` 已通过；本机缺少 Matrix / intent API 环境变量，真实 Matrix 视觉确认和真实小模型 smoke 待后续环境配置。之后优先做真实 Matrix rules-only 验证、配置 intent 小模型验证 rules miss 场景，或推进 pending clarification、Knowledge Expander 和 high-risk proposal policy。

## 维护规则

- `architecture/` 放长期架构和当前架构索引。
- `phases/` 放阶段文档，每个 Phase 保持同一格式：状态、目标、workflow、范围、决策、policy、验收、Review 问题。
- `project-decisions.md` 只保留跨阶段仍然有效的决策，不再为早期默认值维护分散 ADR。
- `adapters/` 放具体入口或外部系统适配文档。
- 已被阶段文档或项目决策吸收的旧文档应收敛，不保留重复入口。
