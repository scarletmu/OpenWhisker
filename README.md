# OpenWhisker

OpenWhisker 是一个本地优先的 Obsidian 知识库处理 agent。

这个项目从 FlashBang 的经验出发，但 FlashBang 之后只作为历史起点和实现参考。OpenWhisker 的目标是构建一个 wiki-first、plan-before-write 的知识处理系统：先可靠捕获原始输入，再由 agent 生成可审计的 VaultPlan，对有风险的改动进行人工确认，最后由受控的 VaultExecutor 执行写入。

## 当前状态

当前仓库处于架构铺设阶段。

这里暂时不放实现代码。第一步是先确认最小可行架构切片，再决定具体包结构、运行入口和实现细节。

## 阅读顺序

- [Project Guide](AGENTS.md)：面向 LLM agent 的项目规则、参考源关系和工作边界。
- [VaultPlan / VaultExecutor 架构草案](FlashBang-VaultPlan-VaultExecutor-Architecture.md)：概念源文档。
- [架构概览](docs/architecture.md)：当前仓库的最小架构边界。
- [Phase 1 最小切片](docs/phase-1-minimal-slice.md)：第一阶段可 review 的实现边界。
- [ADR 0001](docs/decisions/0001-vaultplan-before-write.md)：关于 plan-before-write 的第一条架构决策。
- [ADR 0002](docs/decisions/0002-phase-1-execution-defaults.md)：Phase 1 的入口、存储和 test vault 默认选择。

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

## 当前切片的非目标

- 不做通用 agent 平台。
- 不提供任意 shell 执行能力。
- 不提供不受限的 Obsidian CLI 调用能力。
- 不自动执行高风险 vault 重构。
- 不更新旧的 FlashBang 仓库。
- 在最小架构切片 review 前不进入实现。
