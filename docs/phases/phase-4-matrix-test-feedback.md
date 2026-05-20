# Phase 4 Matrix Test 反馈

状态：已收敛。本文只作为反馈条目入口；当时所有 "已修复" 反馈对应的设计 / 实现现状以 `phase-4-wiki-agent-workflow.md` 与 `progress.md` 为准。

## 已收敛反馈

来自 2026-05-14 真实 Matrix homeserver + `testdata/vault` 冒烟。除最后一项外，全部已在后续 commit 中落地：

| # | 反馈 | 现状 |
| --- | --- | --- |
| 1 | Element 把 `/jobs`、`/diff` 等优先识别为客户端 slash | 临时方案 `//cmd`；长期由 Phase 4B.5 IM Intent Router 接管，见 `phases/phase-4-im-intent-router.md` |
| 2 | low-risk raw capture 上跑 `/diff` 报 `has no prepared diff` | 已改为 `no_diff` 人类提示 |
| 3 | 启动初期偶发 `TLS handshake timeout` | 自动重试恢复，daemon 行为不变 |
| 4 | Element 对原始 Markdown 渲染差 | Matrix adapter 已同时输出 `org.matrix.custom.html` formatted body + plain text fallback |
| 5 | `/diff` 像 executor preview，不适合人工审批 | 已改为人类审批页，由 `VaultOperation` payload 渲染 create / move 信息 |
| 6 | Knowledge draft tag 不符合 `Meta/Tagging.md` | 已切到 `knowledge-vault` profile 的 `type/knowledge` / `status/draft` / `status/needs-review`，并由 policy gate 校验 |

## 2026-05-14 / 15 真实 vault 验证

真实 vault + deterministic organizer 与真实 vault + OpenAI-compatible LLM 均已跑到 `/diff` 后 reject。完整结论与后续 2026-05-19 approve/apply 闭环、2026-05-20 4C.1 high-risk proposal smoke，全部归并到 `progress.md` "验证状态"。

## 真实 vault debug 隔离

切到真实 vault 前的本机 SQLite / Matrix session 隔离脚本与说明，统一记录在 `phase-4-wiki-agent-workflow.md` "下一步真实环境验证顺序" 一节，不再在本页重复。
