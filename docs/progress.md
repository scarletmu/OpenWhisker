# OpenWhisker 当前进度与交接说明

更新时间：2026-05-23

本文用于在不同机器之间切换开发时快速恢复上下文。长期架构以 `docs/architecture/` 和 `docs/phases/` 为准；本文只记录**当前实施进度、验证状态和下一步优先级**。各模块的设计 / 行为细节不在此重复，按"关键文档"指针进入对应 schema / contract。

## 当前阶段

主线已经从 Phase 4 收束后进入 Phase 5：Read-only Skill Scheduler 的最小实现。

- **Phase 4B** 已闭环：Matrix 私聊入口 + Raw Organizer approve/diff/reject + 人类审批页 diff 渲染。2026-05-20 在 `testdata/vault` 跑通 Matrix daemon + DeepSeek 的 organize → `/diff` → `/approve` 全链路，Knowledge draft 与 Raw/Processed 追加段均匹配新 schema。
- **Phase 4B.5** IM Intent Router 已落地为 partial implementation：rules / hybrid / off 三模式、source-scoped binding、capture bucket、pending clarification 状态机、OpenAI-compatible classifier、intent audit jsonl 均已接入；真实 Matrix + 真实 intent 小模型 smoke 已在 2026-05-18 跑通。
- **Phase 4C.1** high-risk proposal-only policy 已闭环，含 proposal note schema / 渲染 / 多路径 diff 合成 / `proposal_written` 终态。
- **Phase 4C.2** Knowledge Expander 瘦身版已落地：仅 `append` (medium) + `propose_restructure` (high) 两种出口；source-trace `RelatedNotes` 自动注入 + `SourceRefs` 透传已接 (commit 490316f)；2026-05-20 在真实 vault `Knowledge/Systems/Observability.md` 上跑通 plan 合成 + diff（DeepSeek 给出 SLI/SLO + PromQL 段落）。
- **Phase 4C.3 / 4C.4 已取消**，Phase 4C 至此收束。4C.3（`organize` 多主题分组）与 capture bucket 在输入期就完成的话题分组职责冲突；4C.4（Knowledge Expander Matrix 自然语言入口）对非关键模块过度投入。配套地，已废弃的批量入口 `organize today` 整条链路（CLI / core / agent / intent router / model / storage / 测试 / 文档）已一并清理，构建链路收敛为 bucket 驱动的 `organize last` 与 `expand`。决策记录见 `docs/phases/phase-4-wiki-agent-workflow.md`。
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
| Phase 5 | daemon status / scheduler status-runs / schedule override 管理 / launchd 文档 | `go test ./...`, 2026-05-23 | 已通 |

`GOCACHE=/private/tmp/openwhisker-go-cache go test ./...` 2026-05-23 全包绿。

## 关键已知遗留

1. **clarification reply 不抽取 `additional_payload_text`**：澄清回复中附带的新内容会被丢弃，等 classifier prompt 升级。
2. **DeepSeek `json_object` 偶发空 content**：classifier / organizer / expander 三处均已加单次重试，两次都空降级为 unclear；未做退避或参数扰动。
3. **clarification 仅覆盖 `raw_capture`**：organize / diff / approve / reject 的低 confidence 输入仍降级为 unclear。
4. **多 pending plan 时的自然语言 approve/reject** 必须回到显式 slash 命令。
5. **多模态 / 图片 / 文件 bucket 输入** 未实现。
6. **Scheduler 仍缺真实长期运行反馈**：daemon、status、launchd 文档和 schedule override 已补齐；仍需要真实 vault + 真实 RSS feed 的持续运行反馈，观察输出质量、重复提醒、失败退避和 Matrix 投递稳定性。Linux `systemd` 模板仍可后续补齐。

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
- `docs/architecture/im-intent-router.md`
- `docs/architecture/capture-bucket.md`
- `docs/architecture/intent-router-model-contract.md`
- `docs/architecture/knowledge-expander-model-contract.md`
- `docs/architecture/proposal-note-schema.md`
- `docs/architecture/knowledge-draft-schema.md`

## 下一步优先级

1. 根据真实使用反馈扩展 rules-only 短句词表与 clarification 回复词表（基于真实未命中样本，避免盲扩）。
2. Phase 4C 主干已收束，没有预排的 4C.3 / 4C.4；后续推进改为真实使用驱动。新增能力（多模态 bucket 输入、clarification 回复 `additional_payload_text` 抽取等）按"关键已知遗留"里的实际需求单独立项。
3. Phase 5 下一步进入真实运行反馈：用 launchd 跑 `openwhisker daemon`，结合 `daemon status`、`scheduler status`、`scheduler runs` 和 `scheduler schedules list|enable|disable` 观察真实 RSSHub / RSS scheduled Skill 输出质量。RSSHub / Folo 仍只作为上游 feed 来源，不进入 Scheduler Core 概念，adapter 只返回信息快照，不提供外部写操作。
