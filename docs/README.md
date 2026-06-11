# OpenWhisker 文档

本文是 `docs/` 的入口与导航主索引。文档按 `00`–`90` 两位数字前缀分区，每个分区有自己的 `README.md`。本索引只承担稳定导航职责，近况描述以 [`00-overview/project-status.md`](00-overview/project-status.md) 为准。

## 推荐阅读顺序

1. [系统架构概览](20-architecture/system-overview.md)：当前实现边界和系统组件。
2. [当前状态与路线图](00-overview/project-status.md)：已落地能力、已知限制和后续方向。
3. [项目决策](80-decisions/project-decisions.md)：跨阶段仍然有效的架构决策。
4. [设计哲学](20-architecture/design-philosophy.md)：完整 VaultPlan / VaultExecutor 方向。
5. [工具驱动的捕获](30-design/tool-driven-capture.md)：让捕获/输入环节具备现状与记忆感知的设计稿（走法 A，未落代码）。
6. 子系统设计：[只读定时 Skill 调度](30-design/scheduler.md) · [Agent 工具调用](30-design/agent-tooling.md) · [收件箱自动 enrich](30-design/inbox-enrichment.md) · [记忆召回](30-design/memory-recall.md) · [IM Intent Router](30-design/im-intent-router.md) · [Frontmatter 解析](30-design/frontmatter-parsing.md)。
7. Schema / Contract：[Capture Bucket](40-api/capture-bucket.md) · [Proposal Note](40-api/proposal-note-schema.md) · [Knowledge Draft](40-api/knowledge-draft-schema.md) · [Intent Router 模型](40-api/intent-router-model-contract.md) · [Knowledge Expander 模型](40-api/knowledge-expander-model-contract.md)。
8. 适配与外部对接：[Matrix Adapter](30-design/matrix-adapter.md) · [Vault Profile Analyzer Skill](90-appendix/skills/vault-profile-analyzer/SKILL.md)。
9. 部署：[部署指南](50-deployment/README.md)，OpenWhisker daemon 的构建、配置与原生服务运行。

延后议题：[CubeSandbox 运行沙箱评估](90-appendix/cubesandbox-runtime-sandbox-evaluation.md) 仅在 sandbox 课题恢复时阅读，不在主线路径上。

## 目录结构

文档采用两位数字前缀分区，`10` 步进预留插入空间。各分区语义：

| 分区 | 用途 | 现状 |
| --- | --- | --- |
| [`00-overview/`](00-overview/) | 项目状态、入口导读 | 已有内容 |
| [`10-context/`](10-context/) | 领域背景、产品上下文 | 预留 |
| [`20-architecture/`](20-architecture/) | 系统级架构与设计哲学 | 已有内容 |
| [`30-design/`](30-design/) | 子系统与模块详细设计 | 已有内容 |
| [`40-api/`](40-api/) | Schema / Contract / 消息格式 | 已有内容 |
| [`50-deployment/`](50-deployment/) | 部署与环境配置 | 已有内容 |
| [`60-operations/`](60-operations/) | 运行期可观测性与运维 | 预留 |
| [`70-troubleshooting/`](70-troubleshooting/) | 排障与恢复 | 预留 |
| [`80-decisions/`](80-decisions/) | 架构与技术决策记录 | 已有内容 |
| [`90-appendix/`](90-appendix/) | 延后议题、模板、归档参考 | 已有内容 |

`10-context/`、`60-operations/`、`70-troubleshooting/` 当前为占位分区，含 `README.md` 但尚无正式内容，待对应材料出现时填充。

## 维护规则

- 顶层分区一律使用两位数字前缀（`00`/`10`/.../`90`），文件与目录用 `kebab-case`，每个顶层分区必须含 `README.md`。
- 需要在既有分区之间插入新分区时用 `15-*`、`25-*` 这类中间号，不要破坏现有编号。
- `20-architecture/` 放长期系统架构与设计哲学；`30-design/` 放子系统/模块详细设计（描述当前态，不带阶段编号，不维护"待实现/决议/PR 边界"这类过程信息）。
- `40-api/` 放对内对外契约：LLM provider 契约、note schema、消息/数据格式。
- `80-decisions/` 只保留跨阶段仍然有效的决策（[`project-decisions.md`](80-decisions/project-decisions.md)），不再为早期默认值维护分散 ADR；新决策按 `0001-*.md` 形式追加。
- `50-deployment/` 放部署引导；含真实主机名/密钥/拓扑的关键运作文档不进 git，落仓库内 `deploy/local/`。
- 已被架构或子系统文档吸收的旧文档应收敛，不保留重复入口。

### 受众分离

文档按读者分两类，物理分开、不混写：

- **人类向**：`docs/**` 全部、仓库根 `README.md`。默认中文，可详细。
- **LLM 向**：各级 `AGENTS.md`（root + `cmd/` + `internal/**`）。精炼英文，agent 协议 / 模块地图。

唯一例外：[`90-appendix/skills/vault-profile-analyzer/SKILL.md`](90-appendix/skills/vault-profile-analyzer/SKILL.md) 位于 `docs/` 树但用英文——它是 vault-local SKILL 模板，按 skill 约定（SKILL 文件是 agent 指令）使用英文。

### 开发过程文档已归档

Phase 1–8 的逐阶段开发文档、Matrix 测试反馈、v1 设计回顾等**开发过程产物**不在本分支，已整体归档到 orphan 分支 `docs-archive`（与 `main` 无共享历史的独立快照）。`main` 的 `docs/` 只保留面向当前的精选参考集。需要追溯演进脉络时查阅该分支。
