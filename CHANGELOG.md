# Changelog

## 2026-05-13

OpenWhisker 进入第一版可运行状态。

这一版的重点不是做完整的 wiki agent，而是先把最关键的安全写入链路跑通：用户提交一段原始文本后，系统会先创建一个 `WikiJob`，再生成一份可审计的 `VaultPlan`，通过低风险 policy 检查后，由受控的 executor 写入本地 test vault。LLM 仍然不会直接写文件，也不会获得 shell 或 Obsidian CLI 权限。

本次新增内容：

- 新增 `openwhisker ingest raw` CLI，可以通过 `--text` 或 stdin 接收 raw input。
- 新增 SQLite 持久化，用来记录 job、plan、operation log 和 outbox message。
- 新增 `WikiJob`、`VaultPlan`、`VaultOperation` 等第一版核心数据结构。
- 新增低风险 policy checker，目前只自动允许 `Raw/Inbox/` 下的 raw note 创建，以及预留的 `Meta/Reports/` agent report 写入。
- 新增 direct filesystem executor，负责真正写入 test vault，并记录写入后的 hash。
- 新增严格 path guard，阻止 absolute path、`..` traversal、hidden path、symlink escape 和写出 vault root。
- 新增 raw note 模板，写入内容包含 job id、来源、捕获时间和原始文本，保证后续处理可以追踪来源。
- 新增测试，覆盖 policy、path guard、symlink escape 和完整 raw ingest 写入流程。
- 更新 README，加入第一版 CLI 用法、默认数据库位置和 test vault 说明。
- 更新 Phase 1 文档，标记第一条 `ingest raw` 链路已经落地。
- 归档 Phase 1 过程文档，让当前文档入口只保留架构、决策和变更记录。
- 将原根目录架构草案整理为 `docs/design-philosophy.md`，作为 OpenWhisker 的项目设计哲学入口。
- 新增 `.gitignore`，避免提交本地数据库和 test vault 运行产物。

这一版默认只写入本地 test vault：

```text
testdata/vault
```

默认数据库位置是：

```text
data/openwhisker.db
```

本次仍未包含：

- LLM 调用；
- 真实 Obsidian vault 写入；
- Knowledge note 生成或扩展；
- 中高风险变更审批；
- Obsidian CLI / Sync / Plugin 集成；
- IM adapter 或 HTTP API。

验证结果：

```sh
go test ./...
```

已通过。
