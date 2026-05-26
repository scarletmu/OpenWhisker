# OpenWhisker 文档索引

本文是 `docs/` 的入口，负责说明当前文档结构和阅读顺序。

## 推荐阅读顺序

1. [架构概览](architecture/overview.md)：当前实现边界和系统组件。
2. [当前进度与交接说明](progress.md)：跨机器切换开发时的当前状态、验证状态、已知遗留和下一步优先级。
3. [项目决策](project-decisions.md)：跨阶段仍然有效的架构决策。
4. [v1 设计回顾](architecture/openwhisker-v1-review.md)：Phase 1–4 完整设计弧线的收官回顾，动机、取舍与教训。
5. [设计哲学](architecture/design-philosophy.md)：完整 VaultPlan / VaultExecutor 方向。
6. Phase 文档：[1 最小切片](phases/phase-1-minimal-slice.md) · [2 审批与 Diff](phases/phase-2-approval-diff.md) · [3 Sync-Aware Approval Execution](phases/phase-3-headless-sync-executor.md) · [4 Wiki Agent Workflow](phases/phase-4-wiki-agent-workflow.md) · [4B.5 IM Intent Router](phases/phase-4-im-intent-router.md) · [5 Read-only Skill Scheduler](phases/phase-5-read-only-skill-scheduler.md)。
7. Schema / Contract：[Proposal Note](architecture/proposal-note-schema.md) · [Knowledge Draft](architecture/knowledge-draft-schema.md) · [Knowledge Expander 模型](architecture/knowledge-expander-model-contract.md) · [Intent Router 模型](architecture/intent-router-model-contract.md) · [IM Intent Router](architecture/im-intent-router.md) · [Capture Bucket](architecture/capture-bucket.md)。
8. 适配与外部对接：[Matrix Private IM](adapters/matrix-private-im.md) · [Vault Profile Analyzer Skill](skills/vault-profile-analyzer/SKILL.md)。
9. 启动草案：[Android Matrix Console STARTUP](startup/android-matrix-console.md)，基于 Matrix 的小平板个人知识收件台方向；初版设计工具 prompt 见 [Android Matrix Console Design Prompt](startup/android-matrix-console-design-prompt.md)，模块调整 prompt 见 [Android Matrix Console UI Diff Prompt](startup/android-matrix-console-ui-diff-prompt.md)。
10. 部署：[部署指南](deployment/README.md)，OpenWhisker daemon 的构建、配置与原生服务运行。

延后议题：[CubeSandbox 运行沙箱评估](architecture/cubesandbox-runtime-sandbox-evaluation.md) 仅在 sandbox 课题恢复时阅读，不在主线路径上。

## 目录结构

```text
docs/
  README.md
  progress.md
  project-decisions.md
  architecture/
    overview.md
    openwhisker-v1-review.md
    design-philosophy.md
    im-intent-router.md
    capture-bucket.md
    intent-router-model-contract.md
    knowledge-draft-schema.md
    knowledge-expander-model-contract.md
    proposal-note-schema.md
    cubesandbox-runtime-sandbox-evaluation.md
  phases/
    phase-1-minimal-slice.md
    phase-2-approval-diff.md
    phase-3-headless-sync-executor.md
    phase-4-wiki-agent-workflow.md
    phase-4-im-intent-router.md
    phase-4-matrix-test-feedback.md
    phase-5-read-only-skill-scheduler.md
  adapters/
    matrix-private-im.md
  deployment/
    README.md
    launchd.md
    systemd.md
    openwhisker.daemon.plist.example
  startup/
    android-matrix-console.md
    android-matrix-console-design-prompt.md
    android-matrix-console-ui-diff-prompt.md
  skills/
    vault-profile-analyzer/
      SKILL.md
```

## 当前实现索引

各阶段已落地能力的摘要、验证状态和已知遗留以 [`progress.md`](progress.md) 为准。本 README 只承担稳定索引职责，不再镜像近况描述。

## 维护规则

- `architecture/` 放长期架构和当前架构索引。
- `phases/` 放阶段文档，每个 Phase 保持同一格式：状态、目标、workflow、范围、决策、policy、验收、Review 问题。
- `project-decisions.md` 只保留跨阶段仍然有效的决策，不再为早期默认值维护分散 ADR。
- `adapters/` 放具体入口或外部系统适配文档。
- `deployment/` 放部署引导；含真实主机名 / 密钥 / 拓扑的关键运作文档不进 git，落仓库内 `deploy/local/`。
- `startup/` 放尚未正式排期但可作为未来工作入口的启动草案。
- 已被阶段文档或项目决策吸收的旧文档应收敛，不保留重复入口。
