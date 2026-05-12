# ADR 0002：Phase 1 Execution Defaults

## 状态

Accepted

## 背景

Phase 1 的目标是用最小实现证明 plan-before-write pipeline，而不是提前铺开完整服务形态。

需要先确定入口、存储、文件命名和 vault 写入目标，避免第一版实现时在基础选择上反复摇摆。

## 决策

Phase 1 采用以下默认选择：

- 入口先使用 CLI。
- 持久化直接使用 SQLite。
- Raw note filename 暂不作为强约束；实现可以在 `timestamp + slug` 或 `job_id` 之间选择，但必须保证确定性、可追踪、路径安全。
- 默认写入本地 test vault，不直接写真实 Obsidian vault。

## 理由

CLI 可以让第一版避开 HTTP server、认证、adapter 和 IM channel 的干扰，直接验证核心 pipeline。

SQLite 与最终形态一致，能尽早暴露 schema、transaction、operation log 和 lifecycle 的真实约束。

文件名策略暂时保持弹性，因为 Phase 1 的重点是写入安全和可追踪，而不是用户最终可读路径设计。

Test vault 可以降低误写真实知识库的风险，也便于测试 path guard、operation log 和 executor 行为。

## 影响

- Phase 1 不需要 HTTP API。
- Phase 1 不需要 IM adapter。
- Phase 1 的测试应围绕 test vault 夹具或临时 test vault 构建。
- 真实 vault 写入必须等 test vault 流程稳定后再显式启用。
