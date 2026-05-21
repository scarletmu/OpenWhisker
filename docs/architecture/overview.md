# 架构概览

本文是 OpenWhisker 的当前架构索引。

详细概念来源仍然是 `docs/architecture/design-philosophy.md`。本文负责把概念文档收敛成当前仓库的实现边界和后续演进方向。

## 当前实现基线

Phase 3 最小闭环已落地。仓库现在包含一条可运行的低风险 raw capture 链路、一条中风险 plan-before-approval 链路，以及受控 Headless Sync client：

```text
raw text input
  -> WikiJob(type=ingest_raw)
  -> VaultPlan(create_note)
  -> PolicyChecker low-risk auto-allow
  -> direct_fs_executor
  -> test vault Raw/Inbox note
  -> operation log
  -> outbox result
```

中风险整理链路会为最近一条 raw capture 生成 deterministic `organize_raw` 计划，先 prepare diff 和 `before_hash`，等待 CLI approval 后才写入 `Knowledge/Drafts/` 并移动 raw note 到 `Raw/Processed/`。

approval apply 现在是 sync-aware 的：默认 test vault 仍关闭同步；当用户显式传入真实 vault 路径且使用默认 `--sync=auto` 时，core 会在 `direct_fs_executor.Apply` 前后通过 Headless `ob` 执行 one-shot sync。pre-sync 后仍由 `DirectFS.Apply` 重新执行 lock、path guard 和 `before_hash` guard；post-sync 失败只作为 warning 返回，不把已成功的 vault write 误标为失败。

当前已实现 Matrix Adapter MVP、长期 Matrix daemon、Core Adapter API、受控 raw context builder、可显式启用的 OpenAI-compatible Raw Organizer 最小路径、第一版 agent output policy gate、`VaultProfile -> VaultRawOrganizerSkill` 生成边界、`vault profile preview` 本地 skill bundle 预览、外部 vault-local `vault-profile-analyzer` Skill 模板，以及 Raw/Processed processing note 写入；默认 `organize last` 仍使用 deterministic planner，避免无意触发外部模型调用。仍未实现加载用户确认后的 Profile / Skill、插件集成、Knowledge Expander 和高风险知识库重构自动执行。下一阶段计划见 `docs/phases/phase-4-wiki-agent-workflow.md`。

## 系统定位

OpenWhisker 是面向个人 Obsidian 知识库的本地优先 workflow host。

它负责：

- 可靠捕获原始输入；
- 区分 raw capture、控制命令和 scheduled workflow 输入；
- 让 LLM-backed agent 生成结构化 vault 变更计划；
- 在任何写入前执行 policy check；
- 对有风险的变更请求人工确认；
- 通过确定性 executor 执行已批准的操作；
- 保留 source traceability 和 operation log。

## Skill-driven 外部对接

OpenWhisker 的外部对接节点采用 skill-driven 方向：程序固定连接、计划、审批、执行和审计边界；外部对象的本地协作方式由 Profile 和 Skill 描述。

```text
External System
  -> Profile 分析它是什么
  -> Skill 描述怎么跟它协作
  -> LLM 基于 Skill 产出结构化计划
  -> OpenWhisker 做 policy、approval 和 deterministic execution
```

在 vault 场景里，`VaultProfile` 是被分析出来的本地事实，例如目录、标签、草稿区、禁止区域和规则来源；`VaultSkill` 是基于 Profile 编译出的任务说明。Profile / Skill 生产应发生在用户自己的 vault 侧，例如通过 vault-local Skill 主动生成和审阅；OpenWhisker runtime 只消费用户确认后的结果。运行时主要给 LLM 的应该是任务 Skill，Profile 作为可审查的事实摘要随附，不应该把某个 vault 的现状硬编码成 OpenWhisker 的通用 schema。

## 主要运行组件

### Core

负责 capture、command routing、job lifecycle、plan lifecycle、approval state、outbox message 和 operation record。

Core 可以调用 agent 和 executor，但不应该把具体 vault 的组织规则硬编码进通用代码。

### Wiki Agent Host

负责 LLM-backed reasoning。

它读取 raw input、任务 Skill、必要的 Profile 摘要、显式允许的 vault context 和用户指令，然后返回结构化 plan。它不直接写文件，不执行 shell，也不任意调用 Obsidian CLI。

Phase 4 已先固定 Core Adapter API、Matrix Adapter MVP、Agent Host contract 和受控 vault context，并接入第一版真实 LLM-backed Raw Organizer。Agent 可以替换 deterministic planner 的 reasoning 层，但不能替代 Policy Checker、VaultExecutor 或 SyncClient。

初始 agent role：

- Raw Organizer
- Knowledge Expander
- Wiki Reader
- Maintenance Agent
- Scheduler Agent

### VaultPlan

reasoning 和 writing 之间的审计边界。

一个 plan 包含 purpose、risk level、source refs、target paths、operations、reasons 和 prepared diff 信息。

### Policy Checker

在 approval 或 execution 前验证 plan。

初始 policy 关注点：

- path safety；
- allowed operation types；
- risk classification；
- approval requirement；
- source traceability；
- OpenWhisker trace frontmatter；
- vault-specific `VaultProfile` / `VaultSkill` 中声明的目录和 controlled tag 要求。

### VaultExecutor

唯一允许修改 vault 的组件。

它在 path guard、lock、before-hash check、operation log 和 sync-aware 行为保护下执行已批准的 `VaultOperation`。

当前 executor：

- `direct_fs_executor`，用于低风险 raw capture、中风险 approved plan 写入和确定性 report 写入。
- `HeadlessSyncClient`，只作为 sync client 调用 `ob sync-status --path <vault>` 和 `ob sync --path <vault>`，不直接修改 vault 文件，也不替代 `direct_fs_executor`。

后续 executor：

- `obsidian_cli_executor`
- `plugin_executor`
- `hybrid_executor`

### Outbox

向选定的 UI 或 IM channel 发送用户可见的状态、审批提示、diff、错误和完成消息。

首期核心 IM 入口实践见 `docs/adapters/matrix-private-im.md`。Matrix adapter 属于交互层和 outbox 通知层，通过 bot client `/sync` 接收命令和 raw input，不直接写 vault，也不绕过 `VaultPlan -> Policy Check -> Approval -> VaultExecutor` 链路。

## 数据对象

第一版 durable object model 已包含：

- `WikiJob`
- `VaultPlan`
- `VaultOperation`
- `VaultApplyResult`
- `OutboxMessage`
- `VaultOperationLog`

Phase 2 已补齐：

- `VaultDiff`
- `VaultLock`

Phase 3 已补齐：

- `SyncResult`
- `SyncClient`
- approval apply 的 `sync_before` / `sync_after` 结果

不要为了某个具体 IM 平台或某个具体 Obsidian 目录结构优化数据模型。

## 第一条架构约束

OpenWhisker 必须保持以下隔离：

```text
LLM reasoning -> structured plan -> policy and approval -> deterministic execution
```

任何允许 LLM 直接写 vault 文件、执行任意 shell、或自由调用 Obsidian CLI 的实现，都不属于本架构。
