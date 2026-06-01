# OpenWhisker 文档索引

本文是 `docs/` 的入口，负责说明当前文档结构和阅读顺序。

## 推荐阅读顺序

1. [架构概览](architecture/overview.md)：当前实现边界和系统组件。
2. [当前状态与路线图](progress.md)：已落地能力、已知限制和后续方向。
3. [项目决策](project-decisions.md)：跨阶段仍然有效的架构决策。
4. [设计哲学](architecture/design-philosophy.md)：完整 VaultPlan / VaultExecutor 方向。
5. [工具驱动的捕获](architecture/tool-driven-capture.md)：让捕获/输入环节具备现状与记忆感知的设计稿（走法 A，未落代码）。
6. 子系统设计：[只读定时 Skill 调度](architecture/scheduler.md) · [Agent 工具调用](architecture/agent-tooling.md) · [收件箱自动 enrich](architecture/inbox-enrichment.md) · [记忆召回](architecture/memory-recall.md)。
7. Schema / Contract：[Proposal Note](architecture/proposal-note-schema.md) · [Knowledge Draft](architecture/knowledge-draft-schema.md) · [Knowledge Expander 模型](architecture/knowledge-expander-model-contract.md) · [Intent Router 模型](architecture/intent-router-model-contract.md) · [IM Intent Router](architecture/im-intent-router.md) · [Capture Bucket](architecture/capture-bucket.md) · [Frontmatter 解析](architecture/frontmatter-parsing.md)。
8. 适配与外部对接：[Matrix Private IM](adapters/matrix-private-im.md) · [Vault Profile Analyzer Skill](skills/vault-profile-analyzer/SKILL.md)。
9. 部署：[部署指南](deployment/README.md)，OpenWhisker daemon 的构建、配置与原生服务运行。

延后议题：[CubeSandbox 运行沙箱评估](architecture/cubesandbox-runtime-sandbox-evaluation.md) 仅在 sandbox 课题恢复时阅读，不在主线路径上。

## 目录结构

```text
docs/
  README.md
  progress.md
  project-decisions.md
  architecture/
    overview.md
    design-philosophy.md
    tool-driven-capture.md
    scheduler.md
    agent-tooling.md
    inbox-enrichment.md
    memory-recall.md
    im-intent-router.md
    capture-bucket.md
    intent-router-model-contract.md
    knowledge-draft-schema.md
    knowledge-expander-model-contract.md
    proposal-note-schema.md
    frontmatter-parsing.md
    cubesandbox-runtime-sandbox-evaluation.md
  adapters/
    matrix-private-im.md
  deployment/
    README.md
    launchd.md
    systemd.md
    openwhisker.daemon.plist.example
  skills/
    vault-profile-analyzer/
      SKILL.md
```

## 当前实现索引

各能力的落地摘要、已知限制和后续方向以 [`progress.md`](progress.md) 为准。本 README 只承担稳定索引职责，不再镜像近况描述。

## 维护规则

- `architecture/` 放长期架构、子系统设计和当前架构索引。
- 子系统设计文档（`scheduler.md` / `agent-tooling.md` / `inbox-enrichment.md` / `memory-recall.md`）描述当前态，不带阶段编号；不在文档里维护"待实现 / 决议 / PR 边界"这类过程信息。
- `project-decisions.md` 只保留跨阶段仍然有效的决策，不再为早期默认值维护分散 ADR。
- `adapters/` 放具体入口或外部系统适配文档。
- `deployment/` 放部署引导；含真实主机名 / 密钥 / 拓扑的关键运作文档不进 git，落仓库内 `deploy/local/`。
- 已被架构或子系统文档吸收的旧文档应收敛，不保留重复入口。

### 受众分离

文档按读者分两类，物理分开、不混写：

- **人类向**：`docs/**` 全部、仓库根 `README.md`。默认中文，可详细。
- **LLM 向**：各级 `AGENTS.md`（root + `cmd/` + `internal/**`）。精炼英文，agent 协议 / 模块地图。

唯一例外：`skills/vault-profile-analyzer/SKILL.md` 位于 `docs/` 树但用英文——它是 vault-local SKILL 模板，按 skill 约定（SKILL 文件是 agent 指令）使用英文。

### 开发过程文档已归档

Phase 1–8 的逐阶段开发文档、Matrix 测试反馈、v1 设计回顾等**开发过程产物**不在本分支，已整体归档到 orphan 分支 `docs-archive`（与 `main` 无共享历史的独立快照）。`main` 的 `docs/` 只保留面向当前的精选参考集。需要追溯演进脉络时查阅该分支。
