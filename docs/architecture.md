# 架构概览

本文是 OpenWhisker 的当前架构索引。

详细概念来源仍然是 `docs/design-philosophy.md`。本文负责把概念文档收敛成当前仓库的实现边界和后续演进方向。

## 当前实现基线

Phase 1 已完成，仓库现在包含一条可运行的低风险 raw capture 链路：

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

这个基线只写入本地 test vault，默认路径是 `testdata/vault`。真实 Obsidian vault、LLM 调用、中高风险 approval、Sync 和插件集成仍然不在当前实现范围内。

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

## 主要运行组件

### Core

负责 capture、command routing、job lifecycle、plan lifecycle、approval state、outbox message 和 operation record。

Core 可以调用 agent 和 executor，但不应该把具体 vault 的组织规则硬编码进通用代码。

### Wiki Agent Host

负责 LLM-backed reasoning。

它读取 raw input、相关 vault notes、适用的 `AGENTS.md` 和用户指令，然后返回结构化 plan。它不直接写文件，不执行 shell，也不任意调用 Obsidian CLI。

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
- durable note 的 frontmatter 和 controlled tag 要求。

### VaultExecutor

唯一允许修改 vault 的组件。

它在 path guard、lock、before-hash check、operation log 和 sync-aware 行为保护下执行已批准的 `VaultOperation`。

当前 executor：

- `direct_fs_executor`，用于低风险 raw capture 和确定性 report 写入。

后续 executor：

- `obsidian_cli_executor`
- `headless_sync_executor`
- `plugin_executor`
- `hybrid_executor`

### Outbox

向选定的 UI 或 IM channel 发送用户可见的状态、审批提示、diff、错误和完成消息。

## 数据对象

第一版 durable object model 已包含：

- `WikiJob`
- `VaultPlan`
- `VaultOperation`
- `VaultApplyResult`
- `OutboxMessage`
- `VaultOperationLog`

后续在 approval 和 conflict handling 阶段再补齐：

- `VaultDiff`
- `VaultLock`

不要为了某个具体 IM 平台或某个具体 Obsidian 目录结构优化数据模型。

## 第一条架构约束

OpenWhisker 必须保持以下隔离：

```text
LLM reasoning -> structured plan -> policy and approval -> deterministic execution
```

任何允许 LLM 直接写 vault 文件、执行任意 shell、或自由调用 Obsidian CLI 的实现，都不属于本架构。
