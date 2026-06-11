# 当前状态与路线图

本文给出 OpenWhisker 当前**已落地的能力边界**、**已知限制**和**后续方向**，面向想了解项目走到哪一步的读者。长期架构以 [`docs/architecture/`](../README.md) 为准；本文不重复各模块的设计细节，按"延伸阅读"指针进入对应文档。

## 已落地能力

写入主链路（plan-before-write）：

- **低风险 raw capture**：raw 输入 → `WikiJob` → `VaultPlan(create_note)` → policy 自动放行 → executor 写入 `Raw/Inbox/` → operation log → outbox 回执，全自动。
- **中风险整理**：`organize last` / `expand <path>` 生成 `VaultPlan`，先出 diff + `before_hash`，等用户 approve 才写入 `Knowledge/Drafts/` 并把 raw note 移到 `Raw/Processed/`。reasoning 层默认 deterministic planner，可显式切到真实 OpenAI-compatible LLM。
- **高风险 proposal-only**：split / merge / rename / 大规模 retag / link rewrite 不直接 apply，改写一篇 proposal note 到 `Raw/Agent-Proposals/`，终态 `proposal_written`。
- **sync-aware apply**：真实 vault + 默认 `--sync=auto` 时，apply 前后各做一次 Obsidian Headless Sync；executor 仍自负 lock / path guard / `before_hash`。

IM 入口与意图路由：

- Matrix Adapter + 长期 daemon + Core Adapter API；
- IM Intent Router：rules / hybrid / off 三模式、source-scoped binding、capture bucket、pending clarification 状态机、OpenAI-compatible classifier、intent audit。

agent 能力（均为只读 vault，写回一律走主线 policy / approval）：

- **只读定时 Skill 调度**：profile 驱动、5 字段 cron + timezone、skill-local `SCHEDULE.md`、只读 vault context + info-only 外部信息源（首个 adapter 为 `rss`，可接 RSSHub route / RSS / Atom）、独立 `openwhisker daemon`、Scheduler / Knowledge 双 bot 身份、`suggested_raw_captures` 显式确认入口。详见 [scheduler.md](../30-design/scheduler.md)。
- **Agent 工具调用 runtime**：host-agnostic `AgentRunner` + `ToolCallingEngine` + 5 read-only vault 工具 + `recall_memory`，被 scheduler / Matrix `@<skill-id>` / CLI `ask` / inbox enrich 四类触发共用；budget 三上限 + trace + 脱敏；链接图索引子系统。详见 [agent-tooling.md](../30-design/agent-tooling.md)。
- **收件箱自动 enrich**：raw 落盘后异步触发，在 vault 已有 tag 词表里定位归属并追加 frontmatter；path / field / body 三道 policy guard，Raw Text 永不被改。详见 [inbox-enrichment.md](../30-design/inbox-enrichment.md)。
- **记忆召回**：跨调用方共享的只读 `internal/memory/`，tag / text / link 三 pass 召回 + 已知 tag 词表服务，索引完全派生自 vault frontmatter。详见 [memory-recall.md](../30-design/memory-recall.md)。

CLI 与部署：完整命令面见 `go run ./cmd/openwhisker`；作为长期服务运行见[部署指南](../50-deployment/README.md)（macOS launchd / Linux systemd）。

## 已知限制

1. **clarification 覆盖面**：澄清仅覆盖 `raw_capture`；organize / diff / approve / reject 的低 confidence 输入仍降级为 unclear。澄清回复中附带的新内容（`additional_payload_text`）暂被丢弃。
2. **多 pending plan 的自然语言 approve/reject** 仍需回到显式 slash 命令。
3. **多模态 / 图片 / 文件 bucket 输入** 未实现。
4. **provider 兼容**：OpenAI-compatible `json_object` 偶发空 content，classifier / organizer / expander 三处均已加单次重试，两次都空则降级；未做退避或参数扰动。
5. **Scheduler 长期运行反馈**：daemon / status / 部署模板 / schedule override 已具备；仍需真实 vault + 真实 feed 的持续运行反馈，观察输出质量、重复提醒、失败退避和投递稳定性。

## 后续方向

- 按真实使用反馈扩展 rules-only 短句词表与 clarification 回复词表（基于真实未命中样本，避免盲扩）。
- 新增能力（多模态 bucket 输入、clarification 回复 `additional_payload_text` 抽取等）按"已知限制"里的实际需求单独立项。
- intent router / scheduler skill 接入 `memory.Recall()`，把"这条消息是否延续某个 tag 主题"作为分类信号。
- 词表演化辅助：把 enrich 累计的 `openwhisker_new_tag_candidates` 聚合成"待词表评审"摘要。
- 向量检索作为召回 Pass 4 兜底（保持 frontmatter 为权威，向量只补召回）。

## 延伸阅读

- [设计哲学](../20-architecture/design-philosophy.md) · [架构概览](../20-architecture/system-overview.md)
- [IM Intent Router 规格](../30-design/im-intent-router.md) · [Capture Bucket 规格](../40-api/capture-bucket.md)
- Schema / Contract：[Proposal Note](../40-api/proposal-note-schema.md) · [Knowledge Draft](../40-api/knowledge-draft-schema.md) · [Intent Router 模型](../40-api/intent-router-model-contract.md) · [Knowledge Expander 模型](../40-api/knowledge-expander-model-contract.md)
