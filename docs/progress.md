# OpenWhisker 当前进度与交接说明

更新时间：2026-05-20

本文用于在不同机器之间切换开发时快速恢复上下文。长期架构以 `docs/architecture/` 和 `docs/phases/` 为准；本文只记录**当前实施进度、验证状态和下一步优先级**。各模块的设计 / 行为细节不在此重复，按"关键文档"指针进入对应 schema / contract。

## 当前阶段

主线仍在 Phase 4：Wiki Agent Workflow。

- **Phase 4B** 已闭环：Matrix 私聊入口 + Raw Organizer approve/diff/reject + 人类审批页 diff 渲染。2026-05-20 在 `testdata/vault` 跑通 Matrix daemon + DeepSeek 的 organize → `/diff` → `/approve` 全链路，Knowledge draft 与 Raw/Processed 追加段均匹配新 schema。
- **Phase 4B.5** IM Intent Router 已落地为 partial implementation：rules / hybrid / off 三模式、source-scoped binding、capture bucket、pending clarification 状态机、OpenAI-compatible classifier、intent audit jsonl 均已接入；真实 Matrix + 真实 intent 小模型 smoke 已在 2026-05-18 跑通。
- **Phase 4C.1** high-risk proposal-only policy 已闭环，含 proposal note schema / 渲染 / 多路径 diff 合成 / `proposal_written` 终态。
- **Phase 4C.2** Knowledge Expander 瘦身版已落地：仅 `append` (medium) + `propose_restructure` (high) 两种出口；source-trace `RelatedNotes` 自动注入 + `SourceRefs` 透传已接 (commit 490316f)；2026-05-20 在真实 vault `Knowledge/Systems/Observability.md` 上跑通 plan 合成 + diff（DeepSeek 给出 SLI/SLO + PromQL 段落）。
- **Phase 4C.3 / 4C.4** 未开工。

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
| Phase 4B.5 | medium clarification 闭环（3 候选 / 2 候选） | 真实 Matrix | **未触发** —— 2026-05-20 两次尝试 DeepSeek 直接判 high-conf，绕过 `ConfidenceLabel=="medium"` 分支；属设计调优（见遗留 1）|
| Phase 4C.1 | high-risk proposal note 写入 + ops log `outcome=proposed` | CLI, DeepSeek, 隔离 `testdata/vault`, 2026-05-20 | 已通 |
| Phase 4C.2 | `expand <path>` append plan synthesize + diff | CLI, DeepSeek, 真实 vault, 2026-05-20 | 已通 |

`go test ./...` 在 2026-05-20 schema 硬化后全包绿。

## 关键已知遗留

1. **medium clarification 真实触发未达成**：2026-05-20 两条尝试语料均被 DeepSeek 判 high-confidence 直接路由（一条 unclear/target=none，一条 0.9 new_topic），均未走 `ConfidenceLabel=="medium"` 分支。属设计调优 —— 要么改阈值、要么在 confidence 之外加 ambiguity / bucket 重叠判据。
2. **Knowledge draft `## 笔记` 内部出现 H2 嵌套**：真实 DeepSeek 输出会在 `## 笔记` 后接 `## 核心思路` / `## 示例` 等同级 H2，schema 要求 H3 起步；属 prompt 工程层，不影响 policy / executor 与 schema 校验。
3. **clarification reply 不抽取 `additional_payload_text`**：澄清回复中附带的新内容会被丢弃，等 classifier prompt 升级。
4. **DeepSeek `json_object` 偶发空 content**：classifier / organizer / expander 三处均已加单次重试，两次都空降级为 unclear；未做退避或参数扰动。
5. **clarification 仅覆盖 `raw_capture`**：organize / diff / approve / reject 的低 confidence 输入仍降级为 unclear。
6. **多 pending plan 时的自然语言 approve/reject** 必须回到显式 slash 命令。
7. **多模态 / 图片 / 文件 bucket 输入** 未实现。

## 验证队列（可选 follow-up，不阻塞主线）

testdata 已通的能力默认视为验证充分；真实 vault apply 只是 nice-to-have 复核，需要时再单独安排：

- 真实 vault 上 Matrix + LLM approve/apply 复核（覆盖 sync=on / ob CLI 路径与真实 Knowledge draft 落盘视觉确认）。
- 真实 vault 上 Phase 4C.2 `expand` 完整 approve/apply 复核（含 before/after hash 在并发 Obsidian Sync 下表现，以及 high-risk → proposal note 出口）。

## 关键文档

- `docs/phases/phase-4-wiki-agent-workflow.md`
- `docs/phases/phase-4-im-intent-router.md`
- `docs/architecture/im-intent-router.md`
- `docs/architecture/capture-bucket.md`
- `docs/architecture/intent-router-model-contract.md`
- `docs/architecture/knowledge-expander-model-contract.md`
- `docs/architecture/proposal-note-schema.md`
- `docs/architecture/knowledge-draft-schema.md`

## 下一步优先级

1. medium clarification 触发条件调优：现有 `ConfidenceLabel=="medium"` 阈值自然难命中真实 DeepSeek 输出，需要在 confidence 之外加 ambiguity / bucket 重叠判据，或显式调阈值。
2. Raw Organizer `## 笔记` H2 嵌套 prompt 修复：在 system prompt 中显式要求 `## 笔记` 内部从 H3 起步，避免破坏 Obsidian outline。
3. 根据真实使用反馈扩展 rules-only 短句词表与 clarification 回复词表（基于真实未命中样本，避免盲扩）。
4. Phase 4C.3 / 4C.4 排期 —— 现有 4B / 4C.1 / 4C.2 已具备足够 testdata 信号即可推进，不再以"真实 vault 复核"作为前置门槛。
