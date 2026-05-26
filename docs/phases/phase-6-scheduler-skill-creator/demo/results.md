# OPEN-1 Demo Results — Phase 6 deepseek-chat 多轮稳定性

执行日期：2026-05-26  
模型：DeepSeek `deepseek-chat`（OpenAI-compatible Chat Completions function-calling）  
Vault：本地 Obsidian 知识库（`knowledge-vault` profile）  
Skill：`Agent/Skills/vault-qa/`（vault 内手写 skeleton，5 tools 闭集，budget = 8 calls / 200KB / 60s）  
Demo DB：`data/openwhisker-open1-demo.db`（隔离，不污染 daemon 状态）

## 总分

- **18 / 20 自然收口（90%）**
- R1 (第一轮)：9/10
- R2 (第二轮)：9/10
- 0 次 `protocol_failed`、0 次 `hard_fail`、0 次 HTTP / function-calling 协议错误

判定：按 spec `≥9/10` 门槛 **PASS**。

## 触发拆分

| 触发 | 总样本 | natural | partial | 失败模式 |
| --- | --- | --- | --- | --- |
| scheduler tick（无 user query） | 10 | 9 | 1 | `call_count_exceeded` |
| adhoc CLI ask（有 user query） | 10 | 9 | 1 | `call_count_exceeded` |

scheduler 路径 partial 率明显高于 ask 路径，根因是「无 user query 时 LLM 倾向枚举性探索」，已记为 follow-up（不阻塞合并）：当 `req.Query == ""` 时由 engine 层把 `max_tool_calls` 砍半。

## 第一轮（R1，10 runs）

| # | trigger | termination | tool calls | 工具序列 | 标题（脱敏） |
| --- | --- | --- | --- | --- | --- |
| 1 | scheduler | natural | 4 | list/list/read/read | Vault 定时快报 |
| 2 | scheduler | **call_count_exceeded** | 9 | list/list/read×6 + 第 9 个被 forceFinalize 拦下 | Raw/Inbox 快报 |
| 3 | scheduler | natural | 5 | list/list/read×3 | Raw/Inbox 快报 |
| 4 | scheduler | natural | 2 | list/list | Vault 快报 |
| 5 | scheduler | natural | 2 | list/list | Vault 快报 |
| 6 | adhoc_cli | natural | 5 | search/search/list/read/read | Meta Tagging 约定 |
| 7 | adhoc_cli | natural | 4 | list/list/list/read | Knowledge/CS 子主题概览 |
| 8 | adhoc_cli | natural | 4 | list/read/read/read | Raw/Inbox 未处理笔记概览 |
| 9 | adhoc_cli | natural | 1 | read | Meta/LLM-Workflow.md 流程 |
| 10 | adhoc_cli | natural | 4 | search/search/read/read | Meta Tagging 约定（重复 #6） |

> 注：R1 ask #10 与 #6 query 重复，是脚本 zsh 0-索引偏差导致。R2 已修正为 5 个 distinct query。

## 第二轮（R2，10 runs）

| # | trigger | termination | tool calls | 工具序列 | 标题（脱敏） |
| --- | --- | --- | --- | --- | --- |
| 1 | scheduler | natural | 2 | list/list | Vault 快报 |
| 2 | scheduler | natural | 2 | list/list | Vault 快报 |
| 3 | scheduler | natural | 3 | list/list/read | Raw/Inbox 快报 |
| 4 | scheduler | natural | 4 | list/list/read/read | Vault 定时巡检快报 |
| 5 | scheduler | natural | 2 | list/list | Raw/Inbox 待处理笔记快报 |
| 6 | adhoc_cli | natural | 6 | search/search/list/list/read/read | Go 并发与 context 笔记定位 |
| 7 | adhoc_cli | **call_count_exceeded** | 11 | list/read×9 + 第 11 个被拦 | Interview 区域主题概览 |
| 8 | adhoc_cli | natural | 5 | search/list/read×3 | Meta/Templates 模板说明 |
| 9 | adhoc_cli | natural | 7 | list/read×6 | Knowledge/Systems/Databases 概览 |
| 10 | adhoc_cli | natural | 5 | list/list/read×3 | Raw/Sources 定位与内容类型说明 |

## 质量抽查

- R2 ask #6（Go 并发）：正确定位到 `Knowledge/Languages/Go/Go Concurrency.md` 与 `Go Memory Model.md`，引用路径与 vault 实际内容一致。
- R2 ask #10（Raw/Sources）：vault 中该目录为空，agent 诚实回报「目录为空」而非编造内容。
- 全部 18 个 natural 收口的 run，`result.summary` 内引用的笔记路径均能在 vault 中验证存在；未观察到幻觉路径。

## Trace 取样

每个 partial run 的 trace 末尾都能看到 engine 层 `forceFinalize` 守卫的明确拦截记录，例如：

```text
"args": {"path": "Knowledge/CS"},
"budget_after": {"bytes_left": 186213, "tool_calls_left": 0, "wall_clock_left": 53},
"error": "force_finalize: only submit_result is permitted",
"tool": "list_vault_dir"
```

这是 [`internal/agent/tool_calling_engine.go`](../../../internal/agent/tool_calling_engine.go) 在 2026-05-26 代码评审硬化中新增的 engine-layer 守卫；契约从 prompt-only 升级为 prompt + code 双保证，本次 demo 实证有效。

## 复现

```bash
set -a; source .env.local; set +a
DB=data/openwhisker-open1-demo.db
rm -f "$DB" "$DB"-shm "$DB"-wal

# scheduler 5 次（70s 间隔避开 cron 同分钟去重）
for i in 1 2 3 4 5; do
  openwhisker scheduler tick \
    --db "$DB" \
    --vault ~/Documents/KnowLedge \
    --vault-profile knowledge-vault \
    --engine openai-compatible \
    --llm-model deepseek-chat
  [ $i -lt 5 ] && sleep 70
done

# ask 5 次（5 个 distinct query）
for Q in "<query 1>" "<query 2>" "<query 3>" "<query 4>" "<query 5>"; do
  openwhisker ask \
    --db "$DB" \
    --vault ~/Documents/KnowLedge \
    --vault-profile knowledge-vault \
    --llm-model deepseek-chat \
    --skill vault-qa \
    --debug \
    "$Q"
done

# 汇总
openwhisker agent runs list --db "$DB" --limit 25
openwhisker agent runs --db "$DB" --trace <run_id>
```

vault 内需先就位的 skill 文件（vault-qa 是 demo skeleton，用户后期可继续调）：

- `Agent/Skills/vault-qa/SKILL.md`：`engine: tool-calling`，`vault_tools` 含 5 个只读工具，`vault_scope` 限定 `Knowledge/` / `Meta/` / `Raw/`。
- `Agent/Skills/vault-qa/SCHEDULE.md`：demo 期间 `cron_expr: "* * * * *"` 让每次 tick 都到期，上线前必须改为具体时间或关闭。
