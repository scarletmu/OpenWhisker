# ADR 0001：VaultPlan Before Vault Write

## 状态

Proposed

## 背景

本项目需要使用 LLM 辅助整理个人 Obsidian vault。让 LLM 直接写入会带来不清晰的权限边界、较弱的审计能力，以及 path safety、prompt injection、意外覆盖、未 review 知识改动等风险。

概念文档提出了 reasoning 和 execution 的隔离：

```text
LLM -> VaultPlan -> Policy Check -> Approval -> VaultExecutor
```

## 决策

OpenWhisker 使用 `VaultPlan` 作为 LLM reasoning 和 vault mutation 之间的边界。

LLM 可以提出 plan，但不能直接写 vault 文件、执行 shell，或任意调用 Obsidian CLI。

只有受控的 `VaultExecutor` 可以 apply 已批准的 `VaultOperation`。

## 影响

正向影响：

- Vault change 变得可审计。
- Risk classification 和 approval 可以在写入前强制执行。
- Operation log 可以解释改了什么、为什么改。
- 来自 raw input 的 prompt injection 影响面更小。
- Executor 可以独立于 LLM 行为进行测试。

代价：

- 在可见用户价值出现前，需要更多数据结构。
- 简单 capture 也需要比直接写文件更多的 lifecycle machinery。
- Diff、approval 和 conflict handling 必须作为一等流程设计。

## 初始范围

Phase 1 只支持通过确定性 `direct_fs_executor` 执行低风险 raw capture。

在 approval 和 diff flow 存在前，中风险和高风险 workflow 保持禁用。
