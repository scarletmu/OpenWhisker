# Changelog

## 2026-05-21 - Phase 4 收束（v1）

自 2026-05-13 的 Phase 4 首版之后，Wiki Agent Workflow 经 4B / 4B.5 / 4C.1 / 4C.2 推进至收束，4C.3 / 4C.4 主动取消。OpenWhisker v1 设计弧线至此收尾。完整回顾见 `docs/architecture/openwhisker-v1-review.md`。

Phase 4B（真实 LLM-backed Raw Organizer）：

- `organize last` 的 reasoning 层接入真实 OpenAI-compatible provider；默认仍为 deterministic，真实 provider 需 `--organizer=openai-compatible` 显式启用。
- Raw Organizer 结构化输出从 OpenAI strict `json_schema` 迁移到 `json_object` + 客户端 `validateRawOrganizerOutput` 硬校验 + 单次空 content 重试，以兼容 DeepSeek 等只支持 `json_object` 的端点。
- 完成真实 vault + DeepSeek 的 approve/apply 闭环验证，以及真实 Matrix + DeepSeek 在 `testdata/vault` 上的 organize → diff → approve 全链路验证。

Phase 4B.5（IM Intent Router）：

- 在 Matrix Adapter 与 Core Adapter API 之间新增 IM Intent Router 中间件，把"整理刚才 / 写进去 / 先不写"等自然语言归一为受控命令；slash 命令继续 passthrough 作为 debug / fallback。
- 新增 rules / hybrid / off 三种路由模式、capture bucket、source-scoped binding、pending clarification 状态机和 intent audit jsonl。
- intent classifier 同样迁移到 `json_object`；澄清触发条件从 classifier 自评的 `confidence_label=medium` 改为结构信号 `bucket_relation=unclear` + active bucket 在场。

Phase 4C.1（high-risk proposal-only policy）：

- plan lifecycle 新增 `proposal_written` 终态：high-risk plan（split / merge / rename / 大规模 retag / link rewrite）在 approve 时不进 apply 路径，改写一篇 proposal note。
- proposal note 默认写入 `Raw/Agent-Proposals/`，frontmatter 与必填章节由 `docs/architecture/proposal-note-schema.md` 定义。
- `vault_operation_logs` 区分 `applied` / `proposed`，保留 hash chain 完整性。

Phase 4C.2（Knowledge Expander）：

- 新增 `KnowledgeExpander` LLM agent 和 `expand <path>` CLI，对已有 thin Knowledge note 生成末尾 append plan。
- 输出只有 `append`（medium-risk）和 `propose_restructure`（high-risk，含新建子 note 需求）两种 kind，有意不支持 `create_child_note`。
- Knowledge Expander 保持 CLI-only。

范围收窄：

- 取消 4C.3（`organize today` 多 topic 分组），并删除整条 `organize today` 链路（CLI / core / agent / intent router / model / storage / 测试 / 文档）：capture bucket 已在输入期完成 topic 分组，整理期再聚类会与捕获期决策冲突。整理链路收敛为 bucket 驱动的 `organize last` 与 `expand`。
- 取消 4C.4（Knowledge Expander 的 Matrix 自然语言入口）：Expander 为非关键模块，其产出本身是普通文档，会自然回流既有 capture / review 链路。

文档：

- 新增 `docs/architecture/openwhisker-v1-review.md`，回顾 FlashBang 起点到 Phase 4C 收束的完整设计弧线。
- 收敛文档结构，knowledge-draft / proposal-note 渲染对齐 obsidian-markdown skill。

验证结果：

```sh
go test ./...
```

已通过（2026-05-21 全包绿）。

## 2026-05-13 - Phase 4

Phase 4 Wiki Agent Workflow 进入真实 LLM + Matrix IM approval workflow 的第一版可验证状态。

本次新增内容：

- 新增 Core Adapter API，支持普通文本 raw capture、`/raw`、`/organize last`、`/organize today`、`/diff`、`/approve`、`/reject`、`/status` 和 `/jobs`。
- 新增 Matrix Adapter MVP 和 `openwhisker matrix poll-once`，通过 Matrix `/sync` 接收文本消息，并通过 outbox 回发状态、审批提示和结果。
- 新增 `openwhisker matrix daemon`，支持长期运行、`next_batch` token 本地持久化、错误退避、interrupt / SIGTERM 正常退出，以及无新消息时继续投递 pending outbox。
- 新增 `adapter_events` 去重表，避免 Matrix event retry 重复创建 job。
- 将 `organize last` 的 planner 抽象为 `RawOrganizer` 合同，并新增受控 raw context builder。
- 新增 OpenAI-compatible Chat Completions Raw Organizer 最小实现；默认仍为 deterministic，真实 LLM 需要显式 `--organizer=openai-compatible`。
- LLM 配置口径统一为 `OPENWHISKER_LLM_API_KEY`、`OPENWHISKER_LLM_BASE_URL` 和 `OPENWHISKER_LLM_MODEL`；旧 `OPENWHISKER_OPENAI_*` 和 `OPENAI_API_KEY` 仅作为 fallback。
- 新增 `.env.local` 自动加载和 `.env.local.example` 模板；真实 `.env.local` 已被 git ignore。
- 新增第一版 agent output policy gate：中风险 plan 必须有 source refs、target paths、operation reason / payload；Knowledge draft 必须有 frontmatter、controlled tags、`needs_review` 和 Raw/Processed 正文链接。
- 扩展 `move_note` payload，approved raw move 会在 `Raw/Processed/` note 末尾追加 OpenWhisker processing note，包含 plan job、raw job、处理时间、产出链接和剩余 review 项。
- 新增 `openwhisker organize today` 和 Matrix `/organize today`，为当天仍在 `Raw/Inbox` 的 raw captures 生成一个 grouped Knowledge draft plan 和多条 raw move。
- 更新 README、文档索引、架构概览、Matrix adapter 文档和 Phase 4 文档，记录下一步真实环境验证顺序：真实 Matrix + test vault、真实 vault + deterministic、真实 vault + OpenAI-compatible LLM。

本次仍未包含：

- 真实 Matrix homeserver 部署验证；
- 真实 vault + LLM 的生产验证；
- Knowledge Expander；
- high-risk proposal policy；
- 多房间 Matrix routing 和 room-scoped outbox；
- Obsidian plugin 集成。

验证结果：

```sh
env GOCACHE=/private/tmp/openwhisker-gocache go test ./...
```

已通过。

## 2026-05-13 - Phase 3

Phase 3 Sync-Aware Approval Execution 最小闭环已落地。

本次新增内容：

- 新增受控 `SyncClient` 边界，以及 `NoopSyncClient` 和 `HeadlessSyncClient`。
- `HeadlessSyncClient` 只允许调用 `ob sync-status --path <vault>` 和 `ob sync --path <vault>`，生产执行使用 `exec.CommandContext`，不经过 shell。
- 新增 `SyncResult`，并在 approval apply 结果中记录 `sync_before` / `sync_after`。
- 新增 `openwhisker vault sync-status` 和 `openwhisker vault sync` 命令。
- `openwhisker plan approve` 新增 `--sync=auto|off|on` 和 `--ob-bin`。
- `OPENWHISKER_OB_BIN` 可作为 `--ob-bin` 默认值来源。
- 默认 `testdata/vault` 仍不触发外部 sync；显式真实 vault 在 `--sync=auto` 下默认执行 apply 前后 one-shot sync。
- pre-sync 失败不会调用 `DirectFS.Apply`，不会创建 Knowledge draft 或移动 raw note。
- pre-sync 后继续由 `DirectFS.Apply` 执行 lock、path guard 和 `before_hash` guard。
- post-sync 失败时 plan/job 保持 applied/done，结果和 outbox 带 warning。
- 新增命令 allowlist、pre-sync failure、post-sync warning、sync 后 hash conflict 和 CLI `--sync=off` 回归测试。
- 更新 README、文档索引、架构概览、项目决策和 Phase 3 文档状态。

验证结果：

```sh
go test ./...
```

已通过。

## 2026-05-13 - 初始可运行基线

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
