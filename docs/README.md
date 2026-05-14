# OpenWhisker 文档索引

本文是 `docs/` 的入口，负责说明当前文档结构和阅读顺序。

## 推荐阅读顺序

1. [架构概览](architecture/overview.md)：当前实现边界和系统组件。
2. [项目决策](project-decisions.md)：跨阶段仍然有效的架构决策。
3. [Phase 1 最小切片](phases/phase-1-minimal-slice.md)：低风险 raw capture 基线。
4. [Phase 2 审批与 Diff](phases/phase-2-approval-diff.md)：中风险 approval 和 hash guard 闭环。
5. [Phase 3 Sync-Aware Approval Execution](phases/phase-3-headless-sync-executor.md)：已落地的受控 Headless `ob` sync client、手动 sync 命令和 sync-aware approval execution。
6. [Phase 4 Wiki Agent Workflow](phases/phase-4-wiki-agent-workflow.md)：推进中的真实 LLM + Matrix IM approval workflow、Raw Organizer、Knowledge Expander 和 proposal policy。
7. [设计哲学](architecture/design-philosophy.md)：完整 VaultPlan / VaultExecutor 方向。
8. [CubeSandbox 运行沙箱评估](architecture/cubesandbox-runtime-sandbox-evaluation.md)：Agent 工具运行时沙箱候选方案评估。

## 目录结构

```text
docs/
  README.md
  project-decisions.md
  architecture/
    overview.md
    design-philosophy.md
    cubesandbox-runtime-sandbox-evaluation.md
  phases/
    phase-1-minimal-slice.md
    phase-2-approval-diff.md
    phase-3-headless-sync-executor.md
    phase-4-wiki-agent-workflow.md
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
- Phase 4：推进中；Matrix IM MVP、Core Adapter API、Raw Organizer contract、受控 vault context、`VaultProfile -> VaultSkill` 边界、`vault profile preview` 本地 skill bundle 预览、外部 vault-local `vault-profile-analyzer` Skill 模板、可显式启用的 OpenAI-compatible Raw Organizer 最小路径、第一版 agent output policy gate、Raw/Processed processing note 写入、长期 Matrix daemon，以及 `organize today` grouped plan 已落地。下一步按“真实 Matrix + test vault -> 真实 vault + deterministic -> 真实 vault + OpenAI-compatible LLM”顺序验证，再继续补加载用户确认后的 Profile / Skill、Knowledge Expander 和 high-risk proposal policy。

## 维护规则

- `architecture/` 放长期架构和当前架构索引。
- `phases/` 放阶段文档，每个 Phase 保持同一格式：状态、目标、workflow、范围、决策、policy、验收、Review 问题。
- `project-decisions.md` 只保留跨阶段仍然有效的决策，不再为早期默认值维护分散 ADR。
- `adapters/` 放具体入口或外部系统适配文档。
- 已被阶段文档或项目决策吸收的旧文档应收敛，不保留重复入口。
