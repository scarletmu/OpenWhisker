# Changelog

## 2026-05-27 - Phase 8 落地：Memory Recall Service v1

按 `docs/phases/phase-8-memory-recall.md` 决议端到端实现 Memory Recall Service v1，沿用 Phase 7 单 PR + 单 commit 模式。`go test ./...` 全包绿。

模块边界：

- `internal/memory/`：从 `internal/tagvocab/` 演化而来的共享 memory 包。包含 `Service`（known tags only-grow + 反向 tag_index）、`Recall` 三 pass（`tag_match=1.0` / `text_match=0.6` / `related_link=0.4`，related 邻居经一跳衰减 `*0.7`，重叠项取最高分并合并 sources）、`LinkGraph` 接口及 `LinkIndexAdapter`（决议 #8：复用 Phase 6 `internal/vault/linkindex/`，仅透出 `SourceKindFrontmatterRelated` 边）、`fsTextSearcher`（默认走 `Knowledge/Interview/Life/Raw`，遍历时跳 `Archive/` 和点开头目录）、frontmatter tag 解析（block + flow 两种 YAML 风格）。`Service.Reindex` 是 only-grow 语义的 escape hatch（先 clear 再 RescanAll）。
- `internal/tagvocab/` 整目录删除；`internal/enrich/`、`internal/agent/`、`internal/agentdispatch/`、`cmd/openwhisker/` 全部切到 `memory.Service`。

SQLite schema（仅新增、幂等）：

- `memory_tag_index(tag, note_path, updated_at)`，主键 `(tag, note_path)` + `idx_memory_tag_index_note_path`：反向 tag→notes 索引。`ReplaceTagIndexForNote(note, newTags, now)` 走 DELETE+INSERT，rewrite_note 上无需 diff 即可保证幂等。
- `memory_known_tags`：沿用 Phase 7 表，only-grow 语义不变；`ClearKnownTags` 仅由 `Reindex` 调用。
- `memory_recalls(id, caller_kind, query, tags, tag_mode, result_count, latency_ms, degraded, called_at)` + `idx_memory_recalls_called_at`：召回调用 trace，每次 `Recall` 末尾 best-effort 写一行。

执行路径：

- `internal/executor/direct_fs.go`：新增 `ApplyObserver` 接口与 `WithApplyObserver(obs)`，每次成功 `applyOperation` 后只对 `OperationRewriteNote` 调用 `obs.OnRewriteNote(ctx, path, newContent)`。daemon 与 enrich 都通过这个 hook 让 memory tag_index 实时跟随 vault 写入。
- `memory.Service.OnRewriteNote` 解析新 content 的 frontmatter tags 后调用 `RecordForNote`，同时刷反向索引与 only-grow known tags。

Agent / Phase 6 工具集：

- `internal/agent/vault_tools.go`：闭集工具新增 `recall_memory`，参数 `query` / `tags` / `tag_mode (any|all)` / `scope_dirs` / `since (RFC3339)` / `expand_related` / `limit`（默认 10）。`query` 与 `tags` 全空时显式拒绝；返回 markdown 包含每条 hit 的 path / source / score / excerpt（截到 200 字符）。
- `internal/agent/agent_runner.go`、`internal/agentdispatch/dispatcher.go`：`AgentRunRequest`、`Dispatcher` 透传 `Memory *memory.Service`，scheduler / ad-hoc 两条路径都注入。

CLI / daemon 接线：

- `openwhisker memory reindex`：clear `memory_tag_index` + `memory_known_tags` 后同步全量重扫 `Knowledge/Interview/Life`，受控前缀为 `topic/` / `skill/`。
- `openwhisker daemon`：启动顺序固定为 `linkindex.New` → `memory.NewService` → daemon dispatcher 注入 LinkGraph + Memory → enrich executor `WithApplyObserver(memSvc)`。

测试：`internal/memory/service_test.go` 9 例（frontmatter parse 双风格 / RescanAll 构建 / RescanAll 只增不减 / Reindex 清后重建 / RecordForNote / OnRewriteNote ApplyObserver 行为 / `Ready` + `WaitReady` 生命周期 / `tag_index_not_ready` degraded 路径）；`internal/memory/recall_test.go` 8 例（query/tags 空入参拒绝 / tags-only / TagMode any vs all / query-only stub TextSearcher / ExpandRelated 双向 stub LinkGraph / ExpandRelated 关闭 / link graph 未就绪 degraded 路径 / ScopeDirs + Archive 默认排除）；`internal/memory/linkadapter_test.go` 1 例（真实 linkindex 上验证 body wikilink 不进 related diffusion，只透出 frontmatter `related:`）；`internal/executor/direct_fs_test.go` 新增 `TestDirectFSApplyObserverFiresOnlyOnRewriteNote`（rewrite_note 触发 observer 且 payload 内容正确 / create_note 不触发）；`internal/agent/vault_tools_recall_test.go` 4 例（Memory 未配置时 `memory_unavailable` / query 与 tags 全空 `bad_args` / 正常路径 markdown payload 含 path + `tag_match` 标签 / `scope_dirs` 越界 `scope_violation`）。`memory_known_tags` only-grow 与 `Reindex` 清重建语义都有 dedicated case。

文档：

- 本 CHANGELOG 条目；`docs/progress.md` 当前阶段、验证状态表、下一步优先级同步更新。Phase 8 设计稿在前一日（commit `3be8a43`）已升级为"待实现"并加入决议 #8（复用 linkindex 而非新建 `memory_link_index` SQLite 表）。

未做 / 留下：

- 真实 vault `recall_memory` 召回质量人工评估（tag-only / query+tags / related 扩散三类场景），归入"验证队列"。
- Phase 7 enrich 在新 `memory.Service` 接线下的真实 vault 复跑（确认 tag vocab 行为未回归），同上。

## 2026-05-26 - OPEN-1 demo 通过：Phase 6 解封合并门槛

针对 `docs/phases/phase-6-scheduler-skill-creator.md` §OPEN-1 列出的「合并 `deploy/main` 前必须 ≥9/10 deepseek-chat 多轮稳定性 demo」硬门槛，在真实本地 Obsidian vault (`~/Documents/KnowLedge`) + DeepSeek `deepseek-chat` provider 上跑了两轮独立 demo，合计 20 runs。

测试矩阵：

- 每轮 = 5 次手动 `openwhisker scheduler tick`（间隔 70s 避开 cron 同分钟去重） + 5 次 `openwhisker ask` (--debug)
- vault profile = `knowledge-vault`；engine = `openai-compatible`；model = `deepseek-chat`
- DB 隔离在 `data/openwhisker-open1-demo.db`，不污染既有 daemon 状态
- vault 内 `Agent/Skills/vault-qa/{SKILL.md,SCHEDULE.md}` 为 demo skeleton（保留供用户后续继续调）

结果：

- **18/20 (90%) `termination == "natural"`**（LLM 主动 `submit_result` 收口）
- 2/20 partial 全部为 `call_count_exceeded`（budget 耗尽 → engine `forceFinalize` 守卫拦下后续工具调用）；都仍产出可用 title + summary
- 0 次 `protocol_failed`（DeepSeek 不会拒绝调 `submit_result`）
- 0 次 `hard_fail`（wall-clock 60s 上限对所有 case 都富余）
- 0 次 HTTP 4xx/5xx 或 function-calling 协议错误
- 抽查质量：ask 实际引用的笔记路径全部为 vault 真实存在的笔记，无幻觉路径；vault 内不存在内容（如 `Raw/Sources/`）能诚实回报"目录为空"

两轮独立分数：R1 9/10、R2 9/10。partial 集中出现在「LLM 没有 user query 时的发散探索」场景；ask 路径（有明确 user query）partial 率约 5%，scheduler 路径（无 user query 仅 SKILL 引导）partial 率约 15%。

脱敏摘要 + 完整 run/title 表见 `docs/phases/phase-6-scheduler-skill-creator/demo/results.md`。

OPEN-1 门槛按 spec 要求的「≥9/10」判 **PASS**，Phase 6 解除 `deploy/main` 合并阻塞。剩余事项（scheduler trigger 工具上限收紧、reasoner-style 模型 reasoning_content 透传、CLI usage 字符串补齐）作为不阻塞合并的 follow-up。

文档：

- `docs/progress.md` 当前阶段段落 + 验证状态表 + 下一步优先级同步更新。
- 本 CHANGELOG 条目。
- 新增 `docs/phases/phase-6-scheduler-skill-creator/demo/results.md`。

## 2026-05-26 - Phase 6 代码评审硬化

针对当日 Phase 6 落地 commit 的多 agent 代码评审发现，回填四项 blocking 与三项 strongly-recommended 修复。功能边界不变；OPEN-1 demo 门槛不动。所有包 `go vet ./...` 与 `go test ./... -count=1` 全绿。

安全：

- `internal/vault/linkindex/linkindex.go`：`relInsideVault` 在 `filepath.Rel` 之外补 `filepath.EvalSymlinks` 双向解析（vault root + 候选 absPath），任何 realpath 落在 vault root 之外的路径被显式拒绝，阻断"vault 内符号链接指向 vault 外文件 → 经链接图被 LLM 间接读出"的路径（呼应 Phase 6 硬约束 #6）。EvalSymlinks 在 ENOENT 时回落到 lexical 结果，避免与 fsnotify 删除事件竞争。
- `internal/agent/vault_tools.go`：`vault_text_search` 三处加固。(a) 入参 `scope_subset` 现在按"清洗 → forbidden-prefix 检查 → 严格 `pathInScope`（不再用宽松 `containsAnyPrefix`）→ 用清洗后的值替换 walk 用 scope"流程处理；空串 / 落到 vault root 的条目立即 reject。(b) walk 内显式 `Lstat` 并丢弃任何被标记为符号链接的条目（`read_vault_note` 已经 `O_NOFOLLOW`，搜索路径不应成为非对称逃逸口）。(c) walk 内每条 entry 都重新 `ctx.Err()` 探活并对 `rel` 重跑 `IsForbiddenScopePath`，防止大型 vault 把 wall-clock budget 烧光、防止允许根下深埋的 `.git/` 子树被搜到。

正确性：

- `internal/agent/tool_calling_engine.go`：`Run` 把 `req.Skill.Budget` 通过显式命名的本地 `remaining` 持有并消费。`scheduler.ToolBudget` 是纯值类型、`req` 已按值传入，原代码不会回写 registry，但通过命名 + 注释把"本地副本语义"写死，避免后续重构误改成指针后悄悄回归。`budgetSnapshot` 函数名与 `[budget]` LLM-visible system note 保持不变。
- `internal/agent/tool_calling_engine.go`：`forceFinalize` 模式下新增显式工具名守卫。原本只靠 `tool_choice = "required"` 偏置模型 + 下游 budget 闸门兜底；现在 engine 层直接拒绝任何非 `submit_result` 的工具调用，写 trace 记 `force_finalize: only submit_result is permitted` 并回吐 budget-exhausted 错误给 LLM。契约从 prompt-only 升级为 prompt + code 双重保证。
- `internal/storage/schema.go`：`renameSchedulerRunsIfPresent` 把 `ALTER TABLE … RENAME` 与 `DROP INDEX IF EXISTS …` 包进单一 `sql.Tx`，commit 失败统一 rollback。原顺序执行在两条语句之间崩溃会留下半迁移状态（表已重命名 + 旧索引仍存），下次启动需手工介入；现在 SQLite 要么完整迁移要么完全未迁移。"两表共存即报错"的守卫保留。

脱敏：

- `internal/sanitize/sanitize.go`：`reBearerToken` 收紧。原 `\bBearer\s+[A-Za-z0-9._\-]{8,}` 会把 "Bearer https://api.example.com/resource"、"Bearer authentication scheme"、"Bearer token spec / RFC 6750" 一并替换成 `Bearer <token>`，污染合法 trace 文本。新规则要求开头是 `[A-Za-z0-9_-]`（首字符不允许是 `.`），紧跟 ≥15 个 `[A-Za-z0-9._-]` 字符（总长 ≥16），不含 `/` `:` 与空白；字符类排除 `/` `:` 后 URL 路径形式不再误伤。新增 `TestFreeText_BearerNotOverzealous` 三例守住回归；既有 `sk_test_abcdefgh1234567890`（20 字符）依然被正常 mask。

文档：

- 本 CHANGELOG 条目；评审命中的 spec-level 边界未发生变化，`docs/phases/phase-6-scheduler-skill-creator.md` 不动。

未做 / 留下：

- `linkindex.parseNoteLinks` 在 fenced code block 内仍会把 ` ```[[Foo]]``` ` 当真链接计入索引（污染 backlink 图但不影响安全语义）；评审报告标注为 notable concern，独立 follow-up。
- `vault_text_search` walk 在巨大 vault 上的 wall-clock 检查现在为 per-entry 级别，但 LLM 历史保留策略本身仍是全保留（spec 中明确"等真撞 token 上限再做摘要化"），不在本次硬化范围。
- fsnotify watcher overflow 触发的异步全量重建仍是单次尝试，没有退避重试循环；评审 notable concern，独立 follow-up。

## 2026-05-26 - Phase 6 落地：Agent-Driven Vault Knowledge Response

按 `docs/phases/phase-6-scheduler-skill-creator.md` 草稿完成 Phase 6 一次性实现。Phase 5 read-only scheduler 之外新增了 agent runtime（多轮 ReAct + 受限工具集 + budget enforcement）、三条 trigger 通道（cron / Matrix @-mention / CLI `openwhisker ask`）、vault Skill 创作链路的工程基础（schema + lint CLI）、链接图索引子系统。

新增子系统：

- `internal/sanitize/`：通用脱敏包。`FreeText` 处理私有主机名、邮箱、Bearer token、Matrix ID、`/Users/<user>` 路径、私网 IP；`SkillConfigJSON` / `FeedURLList` / `IsSensitiveConfigKey` 从 `internal/scheduler/rss_adapter.go` 抽取并扩展。RSS adapter 改为薄包装。
- `internal/vault/linkindex/`：fsnotify-based 内存链接图索引。daemon 异步全量构建 + 200ms debounce 增量维护；CLI 进程同步构建并受 5000-文件软门槛保护（`--force-build-index` 越权）。
- `internal/agent/`：新增 `AgentRunner`、`ToolCallingEngine`、5 vault tools (`list_vault_dir` / `read_vault_note` / `vault_outlinks` / `vault_backlinks` / `vault_text_search`) + 内置 `submit_result` 终止协议。OpenAI Chat Completions function-calling 接入；DeepSeek `deepseek-chat` 为主 provider。
- `internal/agentdispatch/`：胶水包，把 `agent.AgentRunner` 适配为 `core.AgentDispatcher`（避免 `core ↔ agent` import cycle）；同时管理 ad-hoc `agent_runs` 行生命周期。

Registry / schema：

- `internal/scheduler/registry.go`：增加 `Agent/Skills/<id>/SKILL.md` 加载路径；Phase 5 `Scheduler/Skills/*/SCHEDULE.md` 作为 backwards-compat 加载源 + deprecation warning。SKILL.md 是 agent runtime 配置的唯一来源（engine / vault_tools / vault_scope / budget / capabilities）；SCHEDULE.md 仅触发字段（cron / timezone / enabled / id），Phase 6 新字段出现在 SCHEDULE.md = lint error，Phase 5 旧字段 = warning。隐式 engine 推断（非空 `vault_tools` → `tool-calling`）；显式冲突 reject。全局 Skill id 唯一性跨两个目录树校验。
- `IsForbiddenScopePath`：拒绝 `.obsidian/`、`.git/`、`.trash/`、`.DS_Store` 前缀；在 registry load 和 tool execute 两层都强制。
- `docs/phases/phase-6-scheduler-skill-creator/{skill,schedule}-schema.json`：正式 JSON Schema，registry loader 与 `openwhisker skill lint` 共享。

存储 / migration：

- `scheduler_runs` → `agent_runs` RENAME；新增 `tool_trace_json TEXT` + `trigger_kind TEXT NOT NULL DEFAULT 'scheduler'` 两列；新增 `idx_agent_runs_trigger_kind` 索引。无 down migration，daemon 首次启动检测旧表自动迁移。`model.SchedulerRun` 增加对应字段；`AgentTriggerKindScheduler/AdhocMatrix/AdhocCLI`、`SchedulerRunStatusPartial`、`AgentTraceTermination*` 常量补齐。
- `Store.FinishAgentRun` 新方法处理 trace 列；旧的 `FinishSchedulerRun` 保留为不带 trace 的兼容入口。`ListRecentAgentRuns(triggerKind, limit)` 支持按触发源筛选。

CLI 命令：

- `openwhisker ask --skill <id> [--json] [--debug] "<query>"`：本地终端单次 ad-hoc 查询。同步现建 link index（受软门槛保护），debug 模式落 `data/agent-debug/<run_id>.ndjson`。
- `openwhisker skill lint [--strict] <path>`：read-only schema 校验。退出码 0/1/2。共用 registry loader 的实际规则，避免 lint 与 runtime 漂移。
- `openwhisker agent runs list [--trigger-kind ...]` / `openwhisker agent runs <id> [--trace]`：trace 查看入口。`openwhisker scheduler runs` 保留为 `--trigger-kind=scheduler` 的兼容别名。

Trigger 通路：

- Scheduler tick / daemon：自动判定 Skill 是否 tool-calling，是则走 AgentRunner；否则 Phase 5 `StaticSkillRunner` 路径不变。
- Matrix Bot：`AdapterService` 增加 `@<skill-id> <query>` 检测，在 hybrid intent classifier 之前路由到 AgentRunner（trigger_kind = `adhoc_matrix`）。客户端补全的 `@user:server` mention（含冒号）被显式视为非 Skill mention，回落 Phase 5 intent 分支。
- 三条路径共用 `core.AgentDispatcher` 接口和 `agentdispatch.Dispatcher` 实现，trace 字段经 `internal/sanitize` 脱敏后落 SQLite。

Budget：

- `profile.SchedulerProfile.ToolBudgetDefaults` 字段；`DefaultToolBudgetDefaults()` 编译期 fallback (`8 / 200 KiB / 60s`)。
- 每个 Skill 的 `budget` 字段可在 cap 内向下覆盖；超 cap reject 加载。运行期 `force_finalize`（call/bytes 超）+ `hard_fail`（wall-clock 超）+ `protocol_failed`（连续两次自由文本未调 submit_result）三档终止。

测试：

- 所有包 `go vet ./... && go test ./... -count=1` 全绿。
- 新增测试覆盖：Phase 6 registry 加载路径、forbidden prefix 防御、engine inference / conflict、budget cap、global id uniqueness、Phase 5 backwards-compat。
- AgentRunner / ToolCallingEngine：happy path、out-of-scope 防御、protocol-failed 双次拒绝、budget force-finalize → partial status、link index not-ready 错误流。
- `parseAtSkillPrefix`：happy / bare mention / newline delim / Matrix user-mention 拒绝 / 非法字符。
- LinkIndex：wikilink + frontmatter related、forbidden dir skip、read roots filter、模糊 wikilink unresolved、generation 计数。
- Sanitize：sensitive key、feed URL list、JSON object 红ぎaction、free text 全模式覆盖、公共域名保留。

未包含 / 留给后续：

- **OPEN-1 demo（≥9/10 通过的 deepseek-chat 多轮稳定性验证）**：实现已完成，但实际 demo run 需要用户提供 LLM API key 并在真实 `~/Documents/KnowLedge` vault 上跑。Phase 6 主体可合入；OPEN-1 在合 deploy/main 前需用户在本地完成验证，结果摘要回写到 `docs/phases/phase-6-scheduler-skill-creator/demo/`。
- vault 内 `Agent/Skills/skill-creator/SKILL.md` 的 prompt 编写（这是 vault 内容，不在仓库内）。
- Phase 7+ 增量：wikilink alias 解析、向量检索、并发 tool execution、per-skill ACL、用户级 budget UI。

## 2026-05-26 - 安全与正确性回归修复

对 `deploy` 分支（v1 部署基建 + read-only scheduler daemon + scheduler skill creator 草案）进行了一次全量代码审查，修复 10 项问题。覆盖范围：信息泄露 / SSRF、moveNote 与 safeCreateAtomic 的数据一致性、scheduler 生命周期、cron DoS、daemon 健康信号、Matrix 投递身份错配。

安全（信息泄露 / SSRF）：

- `internal/scheduler/runner.go`：`readVaultContext` 改用 `Lstat` 并显式拒绝符号链接，文件读取走 `O_NOFOLLOW`；目录列表中的符号链接条目以 `name@` 标记并跳过内容读取，防止 vault 内的恶意符号链接（如指向 `~/.ssh/id_rsa`）被读出后随调度结果投递到 Matrix 房间。
- `internal/scheduler/rss_adapter.go`：`validateFeedURL` 增加 IP 字面量校验（拒绝回环 / 私网 / 链路本地 / 多播 / 0.0.0.0 / RFC6598 共享段）与保留域名校验（`localhost`、`*.local`、`*.internal`、`*.intranet`、`*.corp`、`*.home`、`*.lan`）。默认 RSS 客户端包裹一层 `safeRSSTransport`，在 RoundTrip 时再次解析 hostname 并对所有返回 IP 重跑 `assertPublicIP`，挫败 DNS rebinding 与公开域 CNAME 指向私网的情形。`CheckRedirect` 对每次跳转 URL 重新校验。

数据一致性：

- `internal/executor/path_guard.go`：`safeCreateAtomic` 从 `rename(2)` 改为 `link(2)`，真正获得 "不覆盖已存在文件" 的原子语义（`rename(2)` 在 POSIX 下会静默覆盖）。新增 `safeMoveAtomic`（link + unlink）与共用 `writeTempFile` / `syncParentDir` 辅助函数。`writeViaTempAndRename` 仅保留 replace 路径，create 路径完全交给 `safeCreateAtomic`，并要求 guardInfo 不为空（消除"未守护即替换"的隐式契约）。修正了 `safeCreateAtomic` 注释中关于 "linkat-style rename" 的事实错误描述。
- `internal/executor/direct_fs.go`：`moveNote` 改为先 `safeMoveAtomic`（inode 级移动）再按需在目的端追加 processing note，恢复 v1 之前并发编辑跟随 inode 的原子语义；旧的"读 → 写新位置 → 删旧位置"窗口下，Obsidian Sync 等并发写入会被静默丢弃。失败时不再出现"目的地落盘但审计写 FAILED + 源仍在"的状态错配。
- 回归测试：
  - `TestSafeCreateAtomicRefusesToClobberRace`：在并发情景下 `safeCreateAtomic` 必须拒绝覆盖既有文件。
  - `TestDirectFSMoveNotePreservesConcurrentSourceEdits`：在源文件移动前持有 append FD，移动后的写入应当落到目的端而不是被丢弃。

正确性与契约：

- `internal/policy/checker.go`：`validateMutatingBeforeHash` 不再硬拒绝 sha256("")。原来的"通配空文件"理由站不住：executor 端 `readGuardedFile` 仍然会比对实际磁盘内容，空文件 hash 与其它 hash 同样会被锚定，且现实里对 0 字节 Raw/Inbox stub 的合法 append 因此被错误拦截。引入 `minBeforeHashLen = 64`，改为校验 BeforeHash 形状（64 字符十六进制）。
- `internal/core/scheduler.go` + `internal/scheduler/runner.go` + `internal/model/types.go`：`runSchedule` 在 `runner.Run` 返回后重新检查 `ctx.Err()`，若是 `context.Canceled` / `DeadlineExceeded`（SIGTERM 中断 tick、deadline 触发）则记为新增的 `SchedulerRunStatusCancelled` 而非 `Done`，对应 outbox 标 error kind，不再误投递"调度运行完成"。`StaticSkillEngine.RunSkill` 不再忽略 ctx。被取消的运行不推进 `NextRunAt`，下次启动按原计划重试。

可运维性：

- `cmd/openwhisker/main.go`：`runSchedulerDaemonLoop` 改为返回 error，连续失败 `schedulerMaxConsecutiveFailures` (5) 次后向 `errCh` 升级触发 daemon 退出，让 systemd / launchd 重启策略真正生效；transient 失败仍被吞掉。原行为下持续性故障（OpenAI key 吊销、SCHEDULE.md 解析错、SQLite 损坏）只写入 status 文件，外部 liveness 探针完全感知不到。
- `internal/scheduler/cron.go`：`ParseCron` 新增 `validateCronSatisfiable`，提前拒绝不可满足的 (month, day-of-month) 组合（如 `0 0 30 2 *`、`0 0 31 4 *`），避免每次 Tick 调 `NextAfter` 时进入 ~2.6M 次单分钟迭代。`NextAfter` 改为按天扫描定位匹配日期、再扫描该日内匹配分钟，量级降到 ~5y × 366d + 24×60。新增 `TestParseCronRejectsUnsatisfiableCalendarCombo`。
- `internal/adapters/matrix/matrix.go`：`clientForActor` 对非 knowledge 默认值的 actor 找不到 `DeliveryClients` 条目时显式 log warning（可通过新增的 `ActorFallbackLog *log.Logger` 字段注入），让"开了 `--matrix=on` 但忘配 `OPENWHISKER_MATRIX_SCHEDULER_*`"导致调度结果用 knowledge 身份发出的错配能立即被运维看到。

测试与验证：

- `go vet ./...` 通过；
- `go test ./... -count=1` 全包绿。

未包含：

- 旧 `vault_locks` 30 分钟 TTL 在长 Apply 下的竞争（PLAUSIBLE，留给后续真实压测后再决定是否引入 lock heartbeat）；
- `logRemainingFailed` 中 `AppendOperationLog` 错误吞噬（best-effort 路径，现有注释已声明，留作后续配 metric）；
- `safeWriteReplace` 对未来"create-or-replace"调用方的隐式契约风险（无现存调用点，留作后续抽象）。

## 2026-05-22 - v1 部署基建

为 v1 部署补齐构建基建与原生服务部署形态，并明确 git 仓库边界。

v1 标准部署形态定为本地硬件（桌面 Mac / Linux NUC）上的原生常驻服务；Docker 收窄为可复现构建与镜像验证工具，容器形态作为可选运行方式保留。

- 新增 `Makefile`（`build` / `test` / `vet` / `fmt` / `clean` / `docker-build` / `docker-run`）、多阶段 `Dockerfile` 和 `.dockerignore`。`Dockerfile` 保留 CGO + glibc 运行基（`debian:bookworm-slim`），以兼容 `mattn/go-sqlite3`。
- 新增原生服务模板：`deploy/openwhisker.launchd.example.plist`（macOS launchd）与 `deploy/openwhisker.systemd.example.service`（Linux systemd），全部用占位符。daemon 从 `WorkingDirectory` 起向上查找并加载 `.env.local`，service 文件不内联密钥。
- 新增 `deploy/openwhisker.compose.example.yaml`：可选容器形态，只定义 `openwhisker daemon` 单服务的部署模板，全部用占位符。
- 新增 `docs/deployment/README.md` 部署引导：以原生服务为主路径，覆盖构建、配置、launchd / systemd 运行、同步形态与设备拓扑约束、v1 范围边界，容器形态列为可选。
- `.gitignore` 加注释分节，新增构建产物与 `deploy/local/` 忽略。含真实主机名 / 密钥 / 拓扑的关键运作文档落仓库内 git-ignored 的 `deploy/local/`，不进仓库。
- `.env.local.example` 补齐 `OPENWHISKER_LLM_ORG_ID` / `OPENWHISKER_LLM_PROJECT_ID` / `OPENWHISKER_INTENT_ORG_ID` / `OPENWHISKER_INTENT_PROJECT_ID` / `OPENWHISKER_CAPTURE_BUCKET_TTL`，与代码实际读取的环境变量对齐。
- 更新 README 与文档索引，加入部署入口。

未包含（v1 范围外）：常驻服务内嵌 Obsidian Headless Sync（`ob`）、真实 vault 生产签收、Kubernetes / Terraform 编排、监控告警与备份自动化。

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
