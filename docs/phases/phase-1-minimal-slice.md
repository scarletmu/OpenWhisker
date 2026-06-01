# Phase 1 最小切片

状态：已完成，归档。

这是第一阶段值得先构建的最小实现边界。

当前实现状态：Phase 1 第一条 `ingest raw` CLI 链路已经落地。

目标不是实现完整 wiki agent，而是用低风险 vault 输出和可 review 的数据结构验证 plan-before-write pipeline。

## 目标

构建一个工作骨架，使 OpenWhisker 可以：

1. 接收一段 raw text input；
2. 创建一个 `WikiJob`；
3. 创建一个包含单个低风险 operation 的 `VaultPlan`；
4. 对 plan 执行 policy check；
5. 通过受控 executor apply；
6. 写入 operation log；
7. 生成用户可见的结果消息。

## 初始支持的 workflow

```text
raw text input
  -> WikiJob(type=ingest_raw)
  -> VaultPlan
  -> create_note operation
  -> PolicyChecker auto-allows low-risk raw capture
  -> direct_fs_executor writes Raw/Inbox note
  -> operation log
  -> outbox result
```

## 范围内

- [x] 项目 package layout。
- [x] 最小 job、plan、operation 和 outbox record 的 SQLite schema。
- [x] raw ingest path 所需的 `WikiJob` lifecycle state。
- [x] `VaultPlan` 和 `VaultOperation` 数据结构。
- [x] 面向低风险 raw capture 的 `PolicyChecker`。
- [x] 带严格 path guard 的 `direct_fs_executor`。
- [x] applied operation 的 operation log。
- [x] raw text input 的 CLI entrypoint。
- [x] policy check、path guard 和 raw note creation 的测试。

## 已确认的 Phase 1 决策

- 入口：先做 CLI。
- 存储：直接使用 SQLite。
- Raw note filename：允许实现时在 `timestamp + slug` 和 `job_id` 之间择优，但必须保持确定性、可追踪、无路径风险。
- Vault target：默认写入本地 test vault，不直接写真实 Obsidian vault。

## 范围外

- LLM 调用。
- Knowledge note expansion。
- 中风险 approval workflow。
- `/diff`、`/approve`、`/reject` 和 `/replan`。
- Obsidian CLI 集成。
- Obsidian Sync 或 Headless Sync 集成。
- Obsidian plugin 工作。
- IM platform adapter 实现。
- 写入旧 FlashBang 仓库。

## 最小 policy

只自动允许：

- `Raw/Inbox/` 下的 `create_note`
- `Meta/Reports/` 下的 `write_agent_report`

阻止：

- absolute path；
- `..` path traversal；
- symlink escape；
- `.obsidian` 和 `.git` 等 hidden path；
- 写出配置的 vault root；
- arbitrary file write；
- arbitrary shell。

以下能力必须等 approval 支持后再启用：

- patch existing note；
- frontmatter update；
- move raw to processed；
- 写入 `Knowledge/`；
- rename；
- archive move。

## Review 问题

- Raw note filename 的最终格式是否需要进入用户可见契约？
- Test vault 应该放在仓库内的 `testdata/`，还是放在仓库外的临时目录？
- CLI 是否只支持 `ingest raw`，还是同时提供 `jobs list` 和 `operations list` 便于调试？
