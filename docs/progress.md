# OpenWhisker 当前进度与交接说明

更新时间：2026-05-20

本文用于在不同机器之间切换开发时快速恢复上下文。长期架构以 `docs/architecture/` 和 `docs/phases/` 为准；本文只记录**当前实施进度、验证状态和下一步优先级**。各模块的设计 / 行为细节不在此重复，按"关键文档"指针进入对应 schema / contract。

## 当前阶段

主线仍在 Phase 4：Wiki Agent Workflow。

- **Phase 4B** 已完成 Matrix 私聊入口 + Raw Organizer approve/diff/reject 闭环，包含人类审批页 diff 渲染。
- **Phase 4B.5** IM Intent Router 已落地为 partial implementation：rules / hybrid / off 三模式、source-scoped binding、capture bucket、pending clarification 状态机、OpenAI-compatible classifier、intent audit jsonl 均已接入；真实 Matrix + 真实 intent 小模型 smoke 已在 2026-05-18 跑通，详见下方"验证状态"。
- **Phase 4C.1** high-risk proposal-only policy 已闭环，含 proposal note schema / 渲染 / 多路径 diff 合成 / `proposal_written` 终态。
- **Phase 4C.2** Knowledge Expander 瘦身版已落地：仅 `append` (medium) + `propose_restructure` (high) 两种出口。真实 vault Matrix 端到端验证待后续。
- **Phase 4C.3 / 4C.4** 未开工。

## 验证状态

| 阶段 | 验证 | 路径 | 状态 |
| --- | --- | --- | --- |
| Phase 4B | 真实 Matrix + `testdata/vault` deterministic 闭环 | Matrix poll → organize → diff → approve | 已通 |
| Phase 4B | 真实 vault + deterministic diff/reject | CLI | 已通 |
| Phase 4B | test vault + OpenAI-compatible provider smoke | CLI | 已通 |
| Phase 4B | 真实 vault + OpenAI-compatible LLM diff/reject | CLI | 已通 |
| Phase 4B | 真实 vault + LLM **approve/apply** 闭环 | CLI, DeepSeek `deepseek-v4-flash`, `--sync=off`, 2026-05-19 | 已通 |
| Phase 4B | 真实 Matrix + LLM approve/apply | Matrix daemon | **未验证** |
| Phase 4B.5 | rules-only raw bucket 创建 + audit privacy | 真实 Matrix, 2026-05-16 | 已通 |
| Phase 4B.5 | hybrid + DeepSeek classifier round-trip | 真实 Matrix, 2026-05-18 | 已通 |
| Phase 4B.5 | medium clarification 闭环（3 候选 / 2 候选） | 真实 Matrix | **未验证** |
| Phase 4C.1 | high-risk proposal note 写入 + ops log `outcome=proposed` | CLI, DeepSeek, 隔离 `testdata/vault`, 2026-05-20 | 已通 |
| Phase 4C.2 | `expand <path>` append plan + high-risk → proposal 出口 | 真实 vault | **未验证** |

`go test ./...` 在 2026-05-20 schema 硬化后全包绿。

## 关键已知遗留

1. **Knowledge Expander 真实 vault smoke** 未跑：当前 4C.2 仅在单元测试 + 隔离 vault 上有信号。
2. **真实 Matrix 路径 LLM approve/apply** 未验证：CLI 路径 2026-05-19 已通，Matrix `/diff` formatted_body、approval guard、outbox 投递在真实 vault apply 上的视觉确认还没做。
3. **clarification reply 不抽取 `additional_payload_text`**：澄清回复中附带的新内容会被丢弃，等 classifier prompt 升级。
4. **DeepSeek `json_object` 偶发空 content**：classifier / organizer / expander 三处均已加单次重试，两次都空降级为 unclear；未做退避或参数扰动。
5. **clarification 仅覆盖 `raw_capture`**：organize / diff / approve / reject 的低 confidence 输入仍降级为 unclear。
6. **多 pending plan 时的自然语言 approve/reject** 必须回到显式 slash 命令。
7. **多模态 / 图片 / 文件 bucket 输入** 未实现。
8. **真实 DeepSeek 输出偶发 `## 笔记` 后直接接 `## 测试背景`** 造成 H2 嵌套；属 prompt 工程层，不影响 policy / executor。

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

1. 真实 Matrix 路径下走一次 LLM approve/apply（CLI 已通），同步覆盖 Matrix `/diff` 渲染、approval guard、outbox 投递在真实 vault apply 上的表现。
2. 真实 Matrix + DeepSeek 验证 medium clarification 闭环（同 topic / 新 topic 模糊消息；以及"无 active bucket 时给 2 候选"路径）。
3. Phase 4C.2 Knowledge Expander 真实 vault + LLM 端到端验证：`expand <path>` append plan 走 diff/approve/apply；high-risk 输出落 proposal note。
4. Knowledge note frontmatter 中 source trace 的关联 raw / processed note 自动注入（当前 expander 只发目标 note）。
5. 根据真实使用反馈扩展 rules-only 短句词表与 clarification 回复词表（基于真实未命中样本，避免盲扩）。
6. Phase 4C.3 / 4C.4 未开工，等 4C.2 真实验证后再排期。
