# OpenWhisker 当前进度与交接说明

更新时间：2026-05-28

本文用于在不同机器之间切换开发时快速恢复上下文。长期架构以 `docs/architecture/` 和 `docs/phases/` 为准；本文只记录**当前实施进度、验证状态和下一步优先级**。各模块的设计 / 行为细节不在此重复，按"关键文档"指针进入对应 schema / contract。

## 当前阶段

主线已推进到 **Phase 8 Memory Recall Service v1 已落地 main**（2026-05-27，commit `858698b`）；Phase 7 已合并 main（commit `2558928`）。两者代码层已收敛、推送（`origin/main` == `HEAD`），`go test ./...` 全包绿。下一步只剩真实 vault 复跑反馈（验证队列，不挂顶部 priority）。

- **Phase 8** Memory Recall Service v1：按 `docs/phases/phase-8-memory-recall.md` 决议（含决议 #8 复用既有 `internal/vault/linkindex/`）端到端实现。代码层包含 `internal/memory/`（Service / Recall 三 pass / LinkGraph 接口 + linkindex 适配器 / FS text searcher / frontmatter tag 解析）、三张派生 cache 表（`memory_tag_index` / `memory_known_tags`（Phase 7 已有，沿用）/ `memory_recalls` 轨迹）、`executor.ApplyObserver` hook（仅在 `rewrite_note` 后触发，刷 tag_index + known_tags）、Phase 6 工具集新增 `recall_memory`（query / tags / tag_mode / scope_dirs / since / expand_related / limit；默认 limit=10，自动排除 `Archive/`）、把 Phase 7 `internal/tagvocab/` 迁入 `internal/memory/`、`openwhisker memory reindex` CLI（clear 然后 RescanAll）、daemon 启动时构建 memory.Service + 注入 enrich 的 `WithApplyObserver`。Three-pass Recall 评分：`tag_match=1.0` / `text_match=0.6` / `related_link=0.4`，related 经一跳衰减 `*0.7`，重叠取最高分。降级路径覆盖 `tag_index_not_ready` / `link_graph_not_ready` 两种。`go test ./...` 全包绿（含 7 个 service_test + 8 个 recall_test，注入 fake `LinkGraph` / `TextSearcher` 跑 query-only / tags-only / TagMode any vs all / ExpandRelated 双向 / ScopeDirs / Archive 默认排除 / linkindex 未就绪 degraded 等场景）。
- **Phase 7** Inbox Enrichment v1 已落地 main（2026-05-27，commit `2558928`）。代码层包含 `internal/enrich/`（enrich 编排 + worker + scan + queue DAO）、`internal/tagvocab/`（派生自 vault frontmatter 的已知 tag 词表；Phase 8 v1 已迁入 `internal/memory/`）、`enrich_jobs` SQLite 表（状态机 pending / running / done / skipped_concurrent_edit / failed / attempts_exhausted）、policy 三 guard（path / field / body）扩展、`JobTypeEnrichRaw` + `model.frontmatter` 常量、IngestRaw 末尾入队 hook、daemon scan tick（5 分钟周期 + mtime 10 分钟静默期 + attempts 5 次熔断）、enrich worker goroutine 串行消费、内置 enrich skill prompt、CLI `openwhisker enrich <rawJobID>`（手动重跑，重置 attempts，绕过静默期）、Matrix `/no-enrich`（一次性开关）。enrich 复用 Phase 6 `AgentRunner` 与 5 + 1 read-only 工具，独立 budget（`max_tool_calls=8 / wall_clock=20s` 硬编码）。enrich 写回走主线 `VaultPlan → Policy → Executor`，唯一允许的 op 是 `rewrite_note` 且 target_path 严格限定为本次 enrich 对应的 inbox 文件；frontmatter 仅允许追加 `topic/*` / `skill/*`（必须命中已知词表）/ `status/needs-review` / `raw/*`，禁止删除已有项；body sha256 由 body_guard 钉死，Raw Text 永不被改。失败 / 超时 / policy reject 一律静默丢弃 + debug outbox，不进 approval 队列。testdata 烟测全绿；真实 vault demo 放在验证队列（不挂顶部 priority）。
- **Phase 4B** 已闭环：Matrix 私聊入口 + Raw Organizer approve/diff/reject + 人类审批页 diff 渲染。2026-05-20 在 `testdata/vault` 跑通 Matrix daemon + DeepSeek 的 organize → `/diff` → `/approve` 全链路，Knowledge draft 与 Raw/Processed 追加段均匹配新 schema。
- **Phase 4B.5** IM Intent Router 已落地为 partial implementation：rules / hybrid / off 三模式、source-scoped binding、capture bucket、pending clarification 状态机、OpenAI-compatible classifier、intent audit jsonl 均已接入；真实 Matrix + 真实 intent 小模型 smoke 已在 2026-05-18 跑通。
- **Phase 4C.1** high-risk proposal-only policy 已闭环，含 proposal note schema / 渲染 / 多路径 diff 合成 / `proposal_written` 终态。
- **Phase 4C.2** Knowledge Expander 瘦身版已落地：仅 `append` (medium) + `propose_restructure` (high) 两种出口；source-trace `RelatedNotes` 自动注入 + `SourceRefs` 透传已接 (commit 490316f)；2026-05-20 在真实 vault `Knowledge/Systems/Observability.md` 上跑通 plan 合成 + diff（DeepSeek 给出 SLI/SLO + PromQL 段落）。
- **Phase 4C.3 / 4C.4 已取消**，Phase 4C 至此收束。4C.3（`organize` 多主题分组）与 capture bucket 在输入期就完成的话题分组职责冲突；4C.4（Knowledge Expander Matrix 自然语言入口）对非关键模块过度投入。配套地，已废弃的批量入口 `organize today` 整条链路（CLI / core / agent / intent router / model / storage / 测试 / 文档）已一并清理，构建链路收敛为 bucket 驱动的 `organize last` 与 `expand`。决策记录见 `docs/phases/phase-4-wiki-agent-workflow.md`。
- **Phase 6** Agent-Driven Vault Knowledge Response 代码已落地（2026-05-26）。新增 `internal/sanitize/`、`internal/vault/linkindex/`、`internal/agent/` (AgentRunner + ToolCallingEngine + 5 vault tools + submit_result)、`internal/agentdispatch/`；registry 支持 `Agent/Skills/<id>/SKILL.md` 主路径并兼容 Phase 5 `Scheduler/Skills/` 旧路径；SQLite `scheduler_runs` RENAME 为 `agent_runs` + 新增 `tool_trace_json` / `trigger_kind` 列；新增 CLI `openwhisker ask`、`openwhisker skill lint`、`openwhisker agent runs`（`scheduler runs` 保留为 trigger_kind=scheduler 别名）；Matrix Bot 新增 `@<skill-id>` 路由（区分 Matrix native user mention `@user:server`）。Phase 6 硬约束 #9 强制：`.obsidian/` `.git/` `.trash/` `.DS_Store` 永远禁入；ToolCallingEngine 闭集 5 + 1 工具；budget force-finalize / hard-fail / protocol_failed 三档终止。完整变更详见 CHANGELOG。落地后做了一轮多 agent 代码评审，回填了 4 项 blocking + 3 项 strongly-recommended 硬化（linkindex / vault_text_search 符号链接逃逸、`scope_subset` 严格校验、ToolCallingEngine `forceFinalize` 显式守卫、storage migration 入事务、Bearer 正则收紧），见 CHANGELOG「Phase 6 代码评审硬化」条目。**OPEN-1 demo 已通过**（2026-05-26 两轮独立跑、5 scheduler tick + 5 CLI ask × 2 = 20 runs、18 次自然 `submit_result` 收口、2 次 `call_count_exceeded` partial、零协议级失败；脱敏摘要见 `docs/phases/phase-6-scheduler-skill-creator/demo/results.md`）。**未做**：vault 内 `Agent/Skills/skill-creator/SKILL.md` prompt 编写（vault 内容，留给用户）。
- **Phase 5** Read-only Skill Scheduler 已落地最小切片：`VaultProfile.scheduler` 开启 registry、scheduler 专用 Skill root、默认 outbox delivery、只读 vault roots 和外部信息源允许列表；`Scheduler/Skills/*/SCHEDULE.md` 隐式绑定同目录 `SKILL.md`；支持 5 字段 cron + timezone；runtime/run log 进 SQLite；`openwhisker scheduler tick` 可手动执行 due schedule 并写 outbox；同一 schedule 已 running 时记录 skipped；registry 阶段拒绝 vault 写入、自动审批、外部副作用、任意 shell/HTTP/文件写等 capability。当前 Scheduler Host 是安全最小 host，已拆出可替换 `SkillEngine` 和 info-only `ExternalInfoAdapter` 接口；CLI 默认使用 static engine，也可显式启用 OpenAI-compatible Scheduler Engine；首个真实外部 adapter 为只读 `rss`，可接入 RSSHub route、普通 RSS feed 或 Atom feed。交互身份上已支持 Scheduler Bot 和 Knowledge Bot 共处同一个 Matrix room：outbox 使用 `actor` 区分 `scheduler` / `knowledge`，Matrix delivery 可按 actor 选择发送 bot，并忽略两个 bot 自己发出的消息。`suggested_raw_captures` 已有显式确认入口：CLI `scheduler accept` 和 Matrix `/scheduler accept` 会从 scheduler run payload 取指定条目并转入既有 low-risk raw capture workflow。独立 `openwhisker daemon` 已实现最小版：scheduler tick loop、可选 Matrix poll loop、tick 后 outbox delivery、SIGINT/SIGTERM 退出、基础错误日志和 `daemon status` 状态文件。可观测入口已补齐 `scheduler status` / `scheduler runs` / Matrix `/scheduler status` / `/scheduler runs`，并支持通过 SQLite override 对 schedule 做 `schedules list|enable|disable`，不直接改 vault 中的 `SCHEDULE.md`。

## 验证状态

| 阶段 | 验证 | 路径 | 状态 |
| --- | --- | --- | --- |
| Phase 4B | 真实 Matrix + `testdata/vault` deterministic 闭环 | Matrix poll → organize → diff → approve | 已通 |
| Phase 4B | 真实 vault + deterministic diff/reject | CLI | 已通 |
| Phase 4B | test vault + OpenAI-compatible provider smoke | CLI | 已通 |
| Phase 4B | 真实 vault + OpenAI-compatible LLM diff/reject | CLI | 已通 |
| Phase 4B | 真实 vault + LLM **approve/apply** 闭环 | CLI, DeepSeek `deepseek-v4-flash`, `--sync=off`, 2026-05-19 | 已通 |
| Phase 4B | 真实 Matrix + LLM approve/apply | Matrix daemon, DeepSeek, `testdata/vault`, 2026-05-20 | 已通 |
| Phase 4B.5 | rules-only raw bucket 创建 + audit privacy | 真实 Matrix, 2026-05-16 | 已通 |
| Phase 4B.5 | hybrid + DeepSeek classifier round-trip | 真实 Matrix, 2026-05-18 | 已通 |
| Phase 4B.5 | clarification 触发（`bucket_relation=unclear` + active bucket → 3 候选）+ 回复解析 | core 单元测试, 2026-05-21 | 已通 |
| Phase 4C.1 | high-risk proposal note 写入 + ops log `outcome=proposed` | CLI, DeepSeek, 隔离 `testdata/vault`, 2026-05-20 | 已通 |
| Phase 4C.2 | `expand <path>` append plan synthesize + diff | CLI, DeepSeek, 真实 vault, 2026-05-20 | 已通 |
| Phase 5 | scheduler registry / cron / runtime / run log / outbox / read-only context guard | `go test ./...`, 2026-05-23 | 已通 |
| Phase 5 | RSSHub-compatible RSS / Atom info-only adapter | `go test ./cmd/openwhisker ./internal/scheduler`, 2026-05-23 | 已通 |
| Phase 5 | daemon status / scheduler status-runs / schedule override 管理 / launchd 与 systemd 文档 | `go test ./...`, 2026-05-23 | 已通 |
| Phase 6 | sanitize / linkindex / agent runtime / registry / Matrix @-mention / CLI ask + lint + agent runs / SQLite migration 单元测试全绿 | `go test ./...`, 2026-05-26 | 已通 |
| Phase 6 | OPEN-1 demo（5 次 scheduler + 5 次 CLI ask）真实 deepseek-chat 多轮稳定性 ≥9/10 | 真实 vault + DeepSeek `deepseek-chat`，两轮独立跑共 20 runs，18/20 (90%) 自然收口；摘要 `docs/phases/phase-6-scheduler-skill-creator/demo/results.md`，2026-05-26 | 已通 |
| Phase 6 | vault 内 `Agent/Skills/skill-creator/SKILL.md` 创作 | 未做 | 留给用户写在自己 vault |
| Phase 7 | tagvocab / enrich / policy 三 guard / enrich_jobs DAO / IngestRaw hook 单元测试与 testdata 烟测 | `go test ./...`, 2026-05-27 | 已通 |
| Phase 7 | 真实 vault Demo（明显归属 / needs-review / new_tag_candidate 三类样本各若干条） | 未做 | 验证队列，不挂顶部 priority |
| Phase 8 | memory service / Recall 三 pass / linkindex 适配器 / executor ApplyObserver / `recall_memory` 工具 / tagvocab 迁入 / `memory reindex` CLI 单元测试 | `go test ./...`, 2026-05-27 | 已通 |
| Phase 8 | 真实 vault Recall 召回质量人工检验（tag-only / query+tags / related 扩散三类场景） | `memory reindex` 真实 `~/Documents/KnowLedge`：57 tag（24 skill/* + 33 topic/*）/ 132 note / 268 映射，<1s 无报错；直接驱动 `Recall()`（绕过 LLM）跑 5 场景全部非降级，tag_match（TagMode any/all 正确）与 related 一跳扩散（0.4×0.7=0.28 衰减正确）召回质量好。2026-05-28 | 已通（含一处观察） |

`GOCACHE=/private/tmp/openwhisker-go-cache go test ./...` 2026-05-23 全包绿。

## 关键已知遗留

1. **clarification reply 不抽取 `additional_payload_text`**：澄清回复中附带的新内容会被丢弃，等 classifier prompt 升级。
2. **DeepSeek `json_object` 偶发空 content**：classifier / organizer / expander 三处均已加单次重试，两次都空降级为 unclear；未做退避或参数扰动。
3. **clarification 仅覆盖 `raw_capture`**：organize / diff / approve / reject 的低 confidence 输入仍降级为 unclear。
4. **多 pending plan 时的自然语言 approve/reject** 必须回到显式 slash 命令。
5. **多模态 / 图片 / 文件 bucket 输入** 未实现。
6. **Scheduler 仍缺真实长期运行反馈**：daemon、status、launchd / systemd 文档和 schedule override 已补齐；仍需要真实 vault + 真实 RSS feed 的持续运行反馈，观察输出质量、重复提醒、失败退避和 Matrix 投递稳定性。

## 验证队列（可选 follow-up，不阻塞主线）

testdata 已通的能力默认视为验证充分；真实 vault apply 只是 nice-to-have 复核，需要时再单独安排：

- 真实 vault 上 Matrix + LLM approve/apply 复核（覆盖 sync=on / ob CLI 路径与真实 Knowledge draft 落盘视觉确认）。
- 真实 vault 上 Phase 4C.2 `expand` 完整 approve/apply 复核（含 before/after hash 在并发 Obsidian Sync 下表现，以及 high-risk → proposal note 出口）。
- Raw Organizer `## 笔记` 内 H3 约束的真实 DeepSeek 复核（2026-05-21 system prompt 已硬化要求 draft_body 从 H3 起步、few-shot 示例同步改 H3，单测已绿；未在真实模型输出上实跑确认是否仍出现 H2 嵌套）。
- `bucket_relation=unclear` 触发的定向澄清在真实 Matrix + DeepSeek 上的复核（2026-05-21 触发条件已从 `confidence_label=medium` 改为 `bucket_relation` 结构信号，core 单元测试已绿；真实小模型在 active bucket 在场时是否会判 `unclear` 仍待实跑确认）。

## 关键文档

- `docs/phases/phase-4-wiki-agent-workflow.md`
- `docs/phases/phase-4-im-intent-router.md`
- `docs/phases/phase-5-read-only-skill-scheduler.md`
- `docs/phases/phase-6-scheduler-skill-creator.md`
- `docs/phases/phase-7-inbox-enrichment.md`
- `docs/phases/phase-8-memory-recall.md`
- `docs/architecture/im-intent-router.md`
- `docs/architecture/capture-bucket.md`
- `docs/architecture/intent-router-model-contract.md`
- `docs/architecture/knowledge-expander-model-contract.md`
- `docs/architecture/proposal-note-schema.md`
- `docs/architecture/knowledge-draft-schema.md`

## 下一步优先级

1. ~~**Phase 8 v1 真实 vault 复跑反馈**~~（2026-05-28 已完成）：`memory reindex` 真实 vault 57 tag / 132 note / 268 映射;直接驱动 `Recall()` 5 场景全非降级、召回质量好;Phase 7 enrich 真实 `ingest raw → enrich` 两条样本（Go+K8s、Redis 锁）端到端跑通、tag 全 in-vocab、related 无幻觉,**新 memory.Service 接线下无回归**。复跑顺带修了 thinking 模型多轮 `reasoning_content` 往返 400（见 CHANGELOG 2026-05-28）。遗留：enrich 对 optional `route_suggestion` 偶发字符串硬失败（重试即过,健壮性可改进,非阻塞）。
2. 根据真实使用反馈扩展 rules-only 短句词表与 clarification 回复词表（基于真实未命中样本，避免盲扩）。
3. Phase 4C 主干已收束，没有预排的 4C.3 / 4C.4；后续推进改为真实使用驱动。新增能力（多模态 bucket 输入、clarification 回复 `additional_payload_text` 抽取等）按"关键已知遗留"里的实际需求单独立项。
4. Phase 5 下一步进入真实运行反馈：用 launchd 或 systemd 跑 `openwhisker daemon`，结合 `daemon status`、`scheduler status`、`scheduler runs` 和 `scheduler schedules list|enable|disable` 观察真实 RSSHub / RSS scheduled Skill 输出质量。RSSHub / Folo 仍只作为上游 feed 来源，不进入 Scheduler Core 概念，adapter 只返回信息快照，不提供外部写操作。
5. Phase 6 follow-up（OPEN-1 后续，不阻塞合并）：
   - **scheduler trigger 工具上限收紧**：当 `AgentRunRequest.Query == ""` 时把 `max_tool_calls` 砍半（≤4）并写 trace，降低 scheduler 无 query 时 LLM 发散探索导致 `call_count_exceeded` partial 的概率；当前实测 partial 率约 10%。
   - **CLI usage 字符串补齐**：`cmd/openwhisker/main.go` 顶部 usage 漏列 `ask` / `agent runs` / `skill lint` 三个 Phase 6 命令，命令本身可用，只是 `--help` 不见。
   - ~~**LLM 多模型对齐**~~（2026-05-28 已修）：thinking 模型（`deepseek-v4-flash`）多轮工具调用要求回传 tool-call 轮的 `reasoning_content`。已给 `ChatMessage` 加 `ReasoningContent` 字段,引擎整条回传 assistant 消息时自动带回（thinking 模式下安全,非 thinking 模型零影响）。契约见 `internal/agent/AGENTS.md` 与官方文档,真实 `deepseek-v4-flash` 已复跑通过,不再需要 `--llm-model deepseek-chat` 覆盖。
6. Phase 6 vault skill-creator：在 `~/Documents/KnowLedge/Agent/Skills/skill-creator/SKILL.md` 写 prompt（vault 内容；本仓库不动）；可通过 `openwhisker ask --skill skill-creator "..."` 触发新 Skill 创建流，输出 VaultPlan 走 medium-risk approval。
7. Phase 7 真实 vault demo：覆盖明显归属命中 / `status/needs-review` 触发 / `new_tag_candidate` 触发三类样本各若干条，人工评估 tag 选择是否合理（不挂顶部 priority，按需触发）。
