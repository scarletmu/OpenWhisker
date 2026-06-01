# Phase 2 审批与 Diff

状态：已完成核心闭环。

这是第二阶段值得先构建的最小实现边界。

当前实现状态：Phase 2 第一条 `organize_raw` CLI 审批链路已经落地。

目标不是实现完整 wiki agent，也不是接入真实 Obsidian Sync，而是用中风险 vault 输出、可 review diff、`before_hash` 和人工 approval 验证 plan-before-write 的审批执行 pipeline。

## 目标

构建一个中风险审批骨架，使 OpenWhisker 可以：

1. 读取最近一条已完成的 raw capture；
2. 创建一个 `WikiJob(type=organize_raw)`；
3. 创建一个需要审批的 medium-risk `VaultPlan`；
4. 对 plan 执行 medium-risk policy check；
5. 在写入前执行 `Prepare`，生成 `VaultDiff` 和 `before_hash`；
6. 等待用户通过 CLI approve 或 reject；
7. approve 后在 vault lock 和 hash guard 保护下 apply；
8. 写入 operation log；
9. 生成用户可见的 outbox 结果、拒绝或 conflict 消息。

## 初始支持的 workflow

```text
Raw/Inbox note
  -> WikiJob(type=organize_raw)
  -> VaultPlan(risk=medium, requires_approval=true)
  -> PolicyChecker validates approval-required plan
  -> direct_fs_executor prepares diff + before_hash
  -> plan status awaiting_approval
  -> CLI approve or reject
  -> vault lock
  -> preflight hash/path checks
  -> create Knowledge/Drafts note
  -> move Raw/Inbox note to Raw/Processed
  -> operation log
  -> outbox result
```

## 范围内

- [x] `VaultDiff` 和 diff entry 数据结构。
- [x] plan approval、reject、applying、conflict lifecycle state。
- [x] `vault_plans` 的 diff、prepared、approved、rejected、error 持久化字段。
- [x] `vault_locks` 最小 SQLite schema。
- [x] medium-risk `PolicyChecker` 分支。
- [x] `direct_fs_executor.Prepare`。
- [x] `before_hash` guard。
- [x] apply 前的 lock 内 preflight check。
- [x] `create_note` 写入 `Knowledge/Drafts/`。
- [x] `move_note` 将 `Raw/Inbox/` 移到 `Raw/Processed/`。
- [x] `append_note` 的 executor 和 policy 基础支持。
- [x] `organize last` CLI entrypoint。
- [x] `plan diff`、`plan approve`、`plan reject` CLI entrypoint。
- [x] approval、reject、conflict 和 Phase 1 regression 测试。

## 已确认的 Phase 2 决策

- 入口：继续使用 CLI，不先实现 IM adapter。
- Planner：先使用 deterministic `organize_raw` planner，不接 LLM。
- Vault target：默认仍写入本地 test vault，不直接写真实 Obsidian vault。
- Knowledge target：Phase 2 只写 `Knowledge/Drafts/`，不直接改长期正式 knowledge note。
- Raw processed：只有 approve 后才把 raw note 从 `Raw/Inbox/` 移到 `Raw/Processed/`。
- Conflict 行为：prepare 后目标文件变化时，apply 失败并进入 conflict，不执行部分写入。

## 范围外

- LLM-backed Raw Organizer。
- Knowledge Expander。
- 正式 `Knowledge/` note patch。
- frontmatter update。
- `/replan`。
- Obsidian CLI 集成。
- Obsidian Sync 或 Headless Sync 集成。
- Obsidian plugin 工作。
- IM platform adapter 实现。
- 高风险 vault restructuring 自动执行。
- 写入旧 FlashBang 仓库。

## 最小 policy

需要 approval 的 medium-risk plan 允许：

- `Knowledge/Drafts/` 下的 `create_note`；
- `Raw/Inbox/` 到 `Raw/Processed/` 的 `move_note`；
- `Knowledge/` 下的 `append_note` 基础执行能力。

写入前必须满足：

- plan risk 是 `medium`；
- plan 设置 `requires_approval=true`；
- operation risk 是 `medium`；
- 所有 target path 都是 clean relative path；
- move destination 也必须通过 path guard；
- move 和 append 在 apply 前必须有 `before_hash`；
- apply 前必须重新读取 current hash；
- apply 前必须获得目标路径 lock；
- conflict 时不能留下部分 Knowledge 写入。

继续阻止：

- absolute path；
- `..` path traversal；
- symlink escape；
- `.obsidian` 和 `.git` 等 hidden path；
- 写出配置的 vault root；
- 未审批的 medium-risk 写入；
- arbitrary file write；
- arbitrary shell。

## CLI 验收

```sh
go run ./cmd/openwhisker ingest raw --text "raw input"
go run ./cmd/openwhisker organize last
go run ./cmd/openwhisker plan diff <plan_id|job_id>
go run ./cmd/openwhisker plan approve <plan_id|job_id>
go run ./cmd/openwhisker plan reject <plan_id|job_id> --reason "暂不整理"
go run ./cmd/openwhisker jobs show <job_id>
```

## Review 问题

- `Knowledge/Drafts/` 是否应该长期保留为正式 staging 区，还是 Phase 3 后改为 proposal note？
- `append_note` 是否应在 Phase 2 保持只作为底层 executor 能力，不暴露给 deterministic planner？
- conflict 后是否需要单独的 `/replan` 命令，还是先要求用户重新运行 `organize last`？
- `vault_locks` 是否需要 TTL 和 stale lock cleanup？
