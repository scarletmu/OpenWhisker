# OpenWhisker

OpenWhisker 是一个本地优先的 Obsidian 知识库处理 agent。

这个项目从 FlashBang 的经验出发，但 FlashBang 之后只作为历史起点和实现参考。OpenWhisker 的目标是构建一个 wiki-first、plan-before-write 的知识处理系统：先可靠捕获原始输入，再由 agent 生成可审计的 VaultPlan，对有风险的改动进行人工确认，最后由受控的 VaultExecutor 执行写入。

## 当前能力

OpenWhisker 当前提供一条最小可运行的本地捕获、审批和 sync-aware 执行流程：

- 通过 CLI 接收一段原始文本。
- 为输入创建可追踪的 `WikiJob`。
- 生成结构化的 `VaultPlan`。
- 在写入前执行低风险 policy check。
- 通过受控的 filesystem executor 写入本地 test vault。
- 记录 operation log 和用户可见的 outbox 结果。
- 为最近一条 raw capture 生成中风险整理计划。
- 在 approval 前准备 diff 和 `before_hash`。
- 通过 CLI 执行 `diff`、`approve` 和 `reject`。
- `approve` 后创建 `Knowledge/Drafts/` 草稿，并将 raw note 移到 `Raw/Processed/`。
- 提供受控 Headless Sync client，只允许执行 `ob sync-status --path <vault>` 和 `ob sync --path <vault>`。
- 提供 `vault sync-status` 和 `vault sync` 手动同步命令。
- `plan approve --sync=auto` 对默认 test vault 关闭同步，对显式真实 vault 默认执行 apply 前后 one-shot sync。

默认只写入本地 test vault，路径是 `testdata/vault`，不会直接修改真实 Obsidian vault。真实 vault 必须通过 `--vault` 显式传入；此时审批执行默认启用 Headless Sync，除非传入 `--sync=off`。

## 快速开始

本地配置可以放在 `.env.local`，该文件已被 git ignore，并会在 CLI 启动时自动加载。可以从模板开始：

```sh
cp .env.local.example .env.local
```

`.env.local` 适合放 Matrix token、OpenAI-compatible LLM endpoint、本机 `ob` 路径等配置；当前 shell 里已经设置的环境变量优先级更高。

LLM 配置优先使用通用 OpenAI-compatible 变量：

```sh
OPENWHISKER_LLM_API_KEY=
OPENWHISKER_LLM_BASE_URL=
OPENWHISKER_LLM_MODEL=
```

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

生成最近一条 raw capture 的整理计划：

```sh
go run ./cmd/openwhisker organize last
```

生成当天未处理 raw capture 的 grouped 整理计划：

```sh
go run ./cmd/openwhisker organize today
```

查看、批准或拒绝待审批计划：

```sh
go run ./cmd/openwhisker plan diff <plan_id>
go run ./cmd/openwhisker plan approve <plan_id>
go run ./cmd/openwhisker plan reject <plan_id> --reason "暂不整理"
```

对真实 vault 执行审批时，`--sync=auto` 会在 apply 前后调用 Headless `ob` 做 one-shot sync：

```sh
go run ./cmd/openwhisker plan approve \
  --vault /Users/wang/Documents/KnowLedge \
  <plan_id>
```

手动查看或触发 vault sync：

```sh
go run ./cmd/openwhisker vault sync-status \
  --vault /Users/wang/Documents/KnowLedge

go run ./cmd/openwhisker vault sync \
  --vault /Users/wang/Documents/KnowLedge
```

运行测试：

```sh
go test ./...
```

## 文档

- [Project Guide](AGENTS.md)：面向 LLM agent 的项目规则、参考源关系和工作边界。
- [文档索引](docs/README.md)：当前文档结构和阅读顺序。
- [设计哲学](docs/architecture/design-philosophy.md)：VaultPlan / VaultExecutor 架构理念和核心约束。
- [架构概览](docs/architecture/overview.md)：当前架构边界和后续方向。
- [项目决策](docs/project-decisions.md)：已收敛的长期架构决策和阶段默认值。
- [Phase 1 最小切片](docs/phases/phase-1-minimal-slice.md)：低风险 raw capture 基线。
- [Phase 2 审批与 Diff](docs/phases/phase-2-approval-diff.md)：中风险计划的 diff、approval、hash guard 和执行闭环。
- [Phase 3 Sync-Aware Approval Execution](docs/phases/phase-3-headless-sync-executor.md)：通过受控 Headless `ob` backend 提供 sync-aware approval execution。
- [Matrix Adapter 实践](docs/adapters/matrix-private-im.md)：Matrix / Synapse 作为 OpenWhisker 核心 IM 入口的 adapter 实践、部署基线和审批边界。
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
- Headless `ob` 只作为受控 sync client 使用，不作为通用 shell 或 LLM 工具。
- 真实 Obsidian vault 写入需要用户显式指定 `--vault`，中风险变更仍需 approval。
- 高风险知识库重构默认不自动执行。
