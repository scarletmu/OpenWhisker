# OpenWhisker

OpenWhisker 是一个本地优先的 Obsidian 知识库处理 agent。

这个项目从 FlashBang 的经验出发，但 FlashBang 之后只作为历史起点和实现参考。OpenWhisker 的目标是构建一个 wiki-first、plan-before-write 的知识处理系统：先可靠捕获原始输入，再由 agent 生成可审计的 VaultPlan，对有风险的改动进行人工确认，最后由受控的 VaultExecutor 执行写入。

## 当前能力

OpenWhisker 当前提供一个最小可运行的本地捕获流程：

- 通过 CLI 接收一段原始文本。
- 为输入创建可追踪的 `WikiJob`。
- 生成结构化的 `VaultPlan`。
- 在写入前执行低风险 policy check。
- 通过受控的 filesystem executor 写入本地 test vault。
- 记录 operation log 和用户可见的 outbox 结果。

默认只写入本地 test vault，路径是 `testdata/vault`，不会直接修改真实 Obsidian vault。

## 快速开始

```sh
go run ./cmd/openwhisker ingest raw \
  --text "需要整理的一段原始输入"
```

默认会使用：

- SQLite 数据库：`data/openwhisker.db`
- Test vault：`testdata/vault`

也可以显式指定：

```sh
go run ./cmd/openwhisker ingest raw \
  --db /tmp/openwhisker.db \
  --vault /tmp/openwhisker-vault \
  --text "raw input"
```

从 stdin 读取：

```sh
printf 'raw input\n' | go run ./cmd/openwhisker ingest raw
```

运行测试：

```sh
go test ./...
```

## 文档

- [Project Guide](AGENTS.md)：面向 LLM agent 的项目规则、参考源关系和工作边界。
- [设计哲学](docs/design-philosophy.md)：VaultPlan / VaultExecutor 架构理念和核心约束。
- [架构概览](docs/architecture.md)：当前架构边界和后续方向。
- [ADR 0001](docs/decisions/0001-vaultplan-before-write.md)：关于 plan-before-write 的第一条架构决策。
- [ADR 0002](docs/decisions/0002-phase-1-execution-defaults.md)：初始入口、存储和 test vault 默认选择。
- [Changelog](CHANGELOG.md)：面向人的版本变化记录。

## 设计方向

```text
Capture / Command
  -> WikiJob
  -> Wiki Agent Host
  -> VaultPlan
  -> Policy Check / Diff / Risk Classification
  -> Human Approval or Low-Risk Auto-Allow
  -> VaultExecutor
  -> Local Vault Files
  -> Obsidian Sync
  -> Operation Log / User Notification
```

## 安全边界

- LLM 不直接写 vault 文件。
- LLM 不执行任意 shell。
- LLM 不自由调用 Obsidian CLI。
- 真实 Obsidian vault 写入需要显式启用和更完整的审批流程。
- 高风险知识库重构默认不自动执行。
