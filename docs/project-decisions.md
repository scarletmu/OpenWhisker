# Project Decisions

本文收敛原 `docs/decisions/` 下的 ADR 内容，作为当前仍然有效的项目级决策索引。

不再为早期阶段维护分散 ADR。阶段内的执行默认值记录在对应 Phase 文档中；跨阶段长期约束记录在本文和架构文档中。

## 有效决策

### 1. VaultPlan before Vault Write

OpenWhisker 使用 `VaultPlan` 作为 LLM reasoning 和 vault mutation 之间的边界。

- LLM 可以提出 plan，但不能直接写 vault 文件。
- LLM 不能执行任意 shell。
- LLM 不能任意调用 Obsidian CLI。
- 只有受控的 `VaultExecutor` 可以 apply 已批准的 `VaultOperation`。
- Risk classification、approval、diff、hash guard 和 operation log 是写入路径的一等能力。

这条决策来自原 `ADR 0001`，现在已经成为系统第一架构约束。详细设计见 [设计哲学](architecture/design-philosophy.md) 和 [架构概览](architecture/overview.md)。

### 2. 早期阶段使用 CLI + SQLite + test vault

Phase 1 到 Phase 3 都采用保守执行默认值：

- 入口先使用 CLI。
- 持久化直接使用 SQLite。
- 默认写入本地 test vault。
- 不默认写真实 Obsidian vault；真实 vault 需要用户显式传入 `--vault`。
- 不先实现 HTTP API、IM adapter、LLM-backed planner 或 Obsidian plugin。

这条决策来自原 `ADR 0002`，对应 Phase 1–3 的早期执行默认值（逐阶段文档已归档至 `docs-archive` 分支）。当前实现边界见[架构概览](architecture/overview.md)。

### 3. Knowledge 写入先进入 Draft

Phase 2 的中风险整理 workflow 只写入 `Knowledge/Drafts/`，不直接 patch 长期正式 knowledge note。

批准后可以移动 raw note 到 `Raw/Processed/`，但 durable knowledge promotion 仍留给后续 Phase。

### 4. Sync 不是事务系统

Obsidian Sync 或 Headless Sync 只作为同步层，不作为写入事务系统。

VaultExecutor 仍必须自己负责：

- path guard；
- vault lock；
- `before_hash` check；
- conflict state；
- operation log。

Phase 3 的实现遵循这一点：Headless `ob` 只作为受控 sync client，approval apply 前后执行 one-shot sync；vault mutation 仍由 `direct_fs_executor` 完成。

### 5. 外部对接节点优先 Skill-driven

OpenWhisker 的关键外部对接节点不应主要靠硬编码业务规则驱动，而应采用类似 OpenClaw 的 skill-driven 模式。

- `Profile` 描述某个外部对象当前是什么样，例如一个 vault 的目录、标签、草稿习惯、禁止区域和规则来源。
- `Profile` 可以由用户在自己的 vault 里运行 vault-local Skill 生成候选版本，但候选版本需要人工确认后才进入稳定运行；OpenWhisker runtime 不负责主动扫描用户 vault 来生成 Profile。
- `Skill` 是从已确认 Profile 编译出的任务说明，面向具体 workflow，例如 Raw Organizer、Knowledge Expander、Matrix command detector。
- 运行时发给 LLM 的主要参考物应是任务 Skill，而不是完整规则文档或硬编码 schema。
- Core、Policy 和 Executor 仍固定安全契约：plan-before-write、path safety、risk、approval、hash guard、traceability 和 deterministic execution。

当前 Phase 4 的第一处实践是 `VaultProfile -> VaultRawOrganizerSkill -> VaultPlan`。后续 Matrix room、Web source、Git、Calendar 或 Task 系统也应沿用同一原则：adapter 固定接入和安全边界，Profile / Skill 描述本地协作方式。

### 9. Matrix 当前部署采用单自动化房间

当前部署目标只使用一个私有、非 E2EE 的 Matrix 自动化房间。该房间同时承载
capture、command、diff / approve / reject、scheduler 输出和告警；
`OPENWHISKER_MATRIX_ROOM_ID` 是目标配置边界。不要在当前部署文档、模板或
Self-Deploy 对接说明中拆分 Inbox / Approval / Alert 等多房间拓扑。
