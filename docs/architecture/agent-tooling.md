# Agent 工具调用运行时

本文描述 OpenWhisker 的 agent runtime：一套"自主调用受限工具集 + 多轮收集 + 自主收尾"的 ReAct 循环，让 LLM 在用户提问或按节律触发时，自己决定去 vault 哪里抽取知识做出响应。

它解决的本质问题是：**让 LLM 在主动提问或设定目标时，自主从 vault 抽取相关知识做出响应**，而不是由 Host 预拉一份静态白名单数据再单轮总结。触发方式有三种：

- 用户主动提问（Matrix `@<skill-id>` 自然语言、CLI `openwhisker ask`）；
- 按预设节律触发（scheduler cron，见[只读定时 Skill 调度](scheduler.md)）；
- 系统事件触发（inbox enrich，见[收件箱自动 enrich](inbox-enrichment.md)）。

agent runtime **永远 read-only**。任何写 vault 的能力（包括"新增 vault Skill"本身）必须走主线 `WikiJob → VaultPlan → Policy → Approval → VaultExecutor`，见[设计哲学](design-philosophy.md)。

## 不可破的边界

```text
1.  Agent runtime 的工具集永远不包含 vault 写工具。
2.  Agent runtime 不创建、修改、删除、移动任何 vault 文件，无论触发来源。
3.  Agent runtime 不 propose 新的 SKILL.md / SCHEDULE.md。
4.  新 vault Skill 只能通过主线 WikiJob 创建（skill-creator 或用户手动编辑）。
5.  每次 tool call 的 path 都在 Go 侧重新校验在 vault_scope 内，失败即工具调用失败。
6.  LLM 不通过任何 tool 间接获得任意 HTTP / shell / 文件读写能力。
7.  工具集是编译期闭集（5 vault 工具 + 1 内置 submit_result），无 plugin / dynamic registration。
8.  Trace 中所有从 vault 内容派生的字段必须经过与 outbox 同等的脱敏。
9.  vault_scope 校验在任何情况下拒绝 .obsidian/ .git/ .trash/ .DS_Store 前缀。
10. Matrix 路径 sender 必须通过 Adapter 白名单；CLI 路径只接受本地 stdin。
11. @skill-id 路由必须是显式 mention，不接受 intent classifier 推断。
```

## 总体架构

`AgentRunner` 是 host-agnostic 的通用 runtime，scheduler / Matrix bot / CLI 三个 host 共用同一套 engine / 工具 / budget / trace。

```text
              ┌─────────────────────────┐
              │      AgentRunner         │  通用 agent runtime, 与 host 无关
              │  - 加载 Skill, 校验 scope/tools/budget
              │  - 调度 ToolCallingEngine 多轮循环
              │  - 收集 trace, 写 agent_runs, 调 sanitize 脱敏
              │  - 命中 force-finalize / hard-fail / protocol-failed 时给出正确 status
              └─┬───────────┬───────────┬─┘
                │           │           │
       ┌────────┘           │           └────────┐
       ▼                    ▼                    ▼
   Scheduler           Matrix Bot              CLI
   (cron tick)         (@skill-id 路由)        openwhisker ask
   trigger_kind        trigger_kind            trigger_kind
   = scheduler         = adhoc_matrix          = adhoc_cli
       │                    │                    │
   既有 outbox          room reply              stdout
```

关键原则：

- **Skill 是核心组织单元**。`SKILL.md` 是必填的 agent 任务定义（任务描述 / vault_scope / vault_tools / budget / engine），`SCHEDULE.md` 降级为可选的 cron 触发挂载。
- **AgentRunner host-agnostic**。它接收 `(skill, query, trigger_context)`，返回 `AgentRunResult`（title / summary / payload / trace）；host 只负责把输入翻译成 query 字符串、把结果格式化回各自输出通道。
- **trigger_kind 是 first-class metadata**。`agent_runs` 表每行带 trigger 来源，三个 host 共用同一审计入口。

## Skill 定义

agent skill 走独立分集 `Agent/Skills/<skill-id>/`，不复用根级 `Skills/`（后者是早于 agent runtime 的"主线 LLM prompt 文档"分集，如 `Skills/vault-raw-organizer/`，不带 agent runtime frontmatter）。registry loader 同时扫 `Agent/Skills/`（主）和 `Scheduler/Skills/`（backwards-compat，加 deprecation warning）；Skill `id` 在 vault 内全局唯一，冲突即 reject。

### SKILL.md（必填）

定义 agent 任务本身，与触发方式无关。frontmatter 是 runtime 配置，body 是给 LLM 的任务描述。

```yaml
---
id: vault-qa
name: Vault 知识问答
engine: tool-calling
capabilities: [vault_read, vault_link_graph_read, vault_text_search, agent_run_log_write]
vault_tools: [list_vault_dir, read_vault_note, vault_outlinks, vault_backlinks, vault_text_search]
vault_scope: [Raw/, Knowledge/, Interview/, Meta/]
budget:
  max_tool_calls: 8
  max_total_bytes: 204800
  max_wall_clock_seconds: 60
delivery: [matrix, outbox]
---

# Vault 知识问答
你是 OpenWhisker 的 vault 知识问答 agent ...
通过 submit_result 工具收尾, payload: { "answer": string, "cited_notes": [string], "confidence": ... }
```

字段语义：

- `engine`：`static` / `openai-compatible` / `tool-calling`。未声明时按 `vault_tools` 推断（非空 → `tool-calling`，空 → `static`）；显式冲突（如 `engine: static` + 非空 `vault_tools`）reject；
- `vault_tools`：五工具子集，`tool-calling` 时必填非空，按字典序规范化存储；
- `vault_scope`：根级路径数组，必须落在 `VaultProfile.scheduler.read_only_vault_roots` 内，每次 tool call 按此校验；
- `budget`：可选，缺省取全局默认；不得超过全局上限，任一字段必须 > 0；
- `delivery`：仅 cron 触发场景消费，ad-hoc 触发被忽略。

### SCHEDULE.md（可选）

仅当 Skill 需要 cron 自动触发时才需要。所有 runtime 字段都从同目录 SKILL.md 继承，`SCHEDULE.md` 只保留触发字段：

```yaml
---
id: daily-vault-radar   # 必须等于父目录 skill id
enabled: true
cron_expr: "0 9 * * *"
timezone: Asia/Shanghai
---
```

同一 Skill 在 cron / matrix / cli 三种触发下 vault_scope / vault_tools / budget 行为完全一致，避免隐式权限差异。

## ToolCallingEngine

`SkillEngine` 的一个实现，与 static / OpenAI-compatible single-turn engine 并列；底层用 OpenAI Chat Completions 的 `tools` / `tool_choice` / `tool_calls` 标准格式（目标 provider 与主线对齐）。工具执行体不在 engine 内，而是 AgentRunner 提供的受信 Go 函数集；engine 只承担协议层（解析 tool_call、回填 tool_result、循环至终止），capability 校验 / size limit / trace 全部留在 Go Host 侧，engine 无法绕过。

### 终止协议：submit_result

LLM **不通过返回自由文本终止**，而必须调用内置工具 `submit_result(title, summary, payload)`：

- `submit_result` 由 AgentRunner 注册，永远在工具目录里，不受 `vault_tools` 白名单约束（它不访问 vault）；
- 强制结构化输出，不依赖 provider 的 `json_schema` 兼容性；
- LLM 在未调用 `submit_result` 就吐自由文本时记协议错误并给一次纠正机会，连续两次违反 = `protocol_failed`。

### System prompt 模板

固定模板拼装，避免风格各异：注入 skill id / trigger_kind / SKILL.md body（frontmatter stripped）/ vault_scope 列表 / 可用工具名 / 每轮刷新的 `[budget]` 行 / user query（ad-hoc 触发为用户原文，scheduler 触发为空时用"produce the scheduled output as defined by the skill"）。message history 第一版全保留，不压缩。

## 工具集

5 个 read-only Go 工具（不依赖 obsidian-cli 或任何外部 CLI subprocess），加 1 个内置 `submit_result`：

| 工具 | 入参 | 返回 |
| --- | --- | --- |
| `list_vault_dir` | `path` | 目录条目列表（带 truncated 标记，上限 50 项） |
| `read_vault_note` | `path` | 文件全文（tool-side 上限默认 128 KiB / 硬上限 256 KiB，超限截断标记） |
| `vault_outlinks` | `path` | `{wikilinks, frontmatter_related}` 两类区分；无法解析的 wikilink 标 `unresolved=true`（第一版不解析 alias） |
| `vault_backlinks` | `path` | 链入该 note 的 source 路径列表（依赖链接图索引；未就绪返回明确 error） |
| `vault_text_search` | `query, scope_subset?` | literal + case-insensitive 搜索，max 50 命中、每命中 1 行片段；`query` 最小 2 字符；`scope_subset` 必须是 `vault_scope` 真子集 |

每个路径参数在 Go 侧重新校验（`cleanRelativePath` + 禁入前缀黑名单 + `isUnderAnyRoot`）。新增允许的 capability：`vault_link_graph_read` / `vault_text_search` / `agent_run_log_write`；写类 capability 维持禁用。

## 链接图索引子系统

`internal/vault/linkindex/`：daemon 进程内单例，归属通用 `internal/vault/` 命名空间（AgentRunner 是首个消费者，接口预留给非 agent 调用方）。

- daemon 启动时**异步**全量扫描 vault（排除 `.obsidian/` `.git/` `.trash/` `.DS_Store` 等隐藏目录），解析所有 `.md` 的 wikilink 和 frontmatter `related:`；启动**不阻塞**等待索引就绪；
- 异步构建期间其它能力正常运行；仅 `vault_outlinks` / `vault_backlinks` 在未就绪期返回 `link_index_not_ready`，其余三个工具不受影响；
- 就绪后启动 fsnotify watcher 增量维护（create/modify → 重解析，delete → 移除，rename → delete+create），事件 debounce（默认 200ms）；不跟随指向 vault root 外的 symlink；
- 不落 SQLite（daemon 重启全量重建）；事件溢出时下次 tick 前触发全量重建；
- 每次 tool call 在 trace 记录读取时的 `generation`（`link_index_version`），最终一致，不做 per-run snapshot；
- **CLI 进程**（`scheduler tick` / `ask`）与 daemon 独立，仅共享 SQLite，启动时**同步**现建一次 ad-hoc 索引，进程退出即丢弃，不启 watcher；软门槛保护：`.md` 文件数 > 5000 或全量扫描 > 10s 时拒绝执行并提示加 `--force-build-index`。

## Budget

三个独立上限，任一先撞到即触发超限动作：

| 维度 | 默认 | 行为 |
| --- | --- | --- |
| `max_tool_calls` | 8 | force finalize |
| `max_total_bytes` | 200 KiB | force finalize |
| `max_wall_clock_seconds` | 60 | hard fail |

每轮 LLM 调用前在 system message append 剩余预算快照，让 LLM 自主决定收尾。撞 call / bytes 上限进入 force finalize：禁止再发普通 tool_call（`submit_result` 仍允许），追加"must call submit_result now"，收尾后 `status=partial`。撞 wall clock 走 hard fail：通过 `context.Deadline` 在工具执行和 LLM 调用两侧 cancel，`status=failed`。

**scheduler 无 query 时收紧**：`trigger_kind == scheduler` 且 query 为空时，AgentRunner 把该 run 的 `max_tool_calls` 砍半（floor 1 / cap 4），仅作用于本次副本，不改 registry 缓存值；调整记入 `AgentTrace.budget_note`。

Run status：`ok`（自然 `submit_result` 收尾）/ `partial`（force finalize 后收尾）/ `failed`（wall clock 超限 / protocol_failed / 工具错误未恢复 / engine 不可用）/ `skipped`（同 schedule 已有 running run）。`ok` 是渲染层别名，SQLite `agent_runs.status` 沿用 `done`，不引入 status migration。

## Trace

`scheduler_runs` 表重命名为 `agent_runs`，新增 `tool_trace_json` + `trigger_kind` 两列（自动 RENAME 迁移，Phase 5 历史行携带 `trigger_kind='scheduler'` / `tool_trace_json=NULL`，无 down migration）。

`tool_trace_json` 每条 tool call 记 seq / tool / args / result_bytes / `result_summary_sha256` / duration / truncated / error / budget_after，加终止信息（`termination` ∈ natural / call_count_exceeded / bytes_exceeded / wall_clock_exceeded / protocol_failed / error）与 `link_index_version`。**不存工具返回原文**，只存大小 + hash + ≤200 字节摘要片段。

脱敏（呼应硬约束 #8）：path 不脱敏（不是敏感数据）；`query` / `summary` / 自由文本与摘要片段按 outbox 同等脱敏（私有主机名 / 邮箱 / token / Matrix ID / 内部命名空间→占位符）；`error` 剥离含 vault 内容的 backtrace。脱敏实现统一在 `internal/sanitize/`。LLM 完整 message 仅 `--debug` 模式落 `data/agent-debug/<run_id>.ndjson`，不进 SQLite。

查看入口：`openwhisker agent runs list [--trigger-kind ...]` / `openwhisker agent runs <run_id> --trace`；`openwhisker scheduler runs` 保留为 `--trigger-kind scheduler` 的兼容别名。

## 触发路径

- **Matrix Bot**：消息以 `@<skill-id> ` 前缀开头时，在 intent router 规则匹配前最高优先级检测，`intent=vault_query`，query 取前缀之后的剩余文本，dispatch 到 AgentRunner（`trigger_kind=adhoc_matrix`）后直接 return，不进后续 hybrid classifier；未 mention 任何 Skill 的自由文本走既有 intent 分支（零回归）。`@<skill-id>` 是 bot 侧纯文本约定，不依赖 Matrix mention 协议；用户若接受 client 自动补全为 `@user:server` 则视为"未指定 Skill"。
- **CLI**：`openwhisker ask --skill <id> "<query>" [--json] [--debug]`，summary 走 stdout 并附 `[trace: <run_id>]`，只接受命令行参数 + 本地 stdin。
- **Scheduler**：cron tick 通过 AgentRunner 调度，`query=""` 时不注入 user query 段，仅用 SKILL.md 任务描述；按 `delivery` 字段决定 outbox / matrix。

## Skill 创作

- `Agent/Skills/skill-creator/`（vault 级 Skill）：用户通过 `@skill-creator` 或 `openwhisker ask --skill skill-creator` 触发，**不通过 scheduler 触发**（硬约束 #3）。输出一份 `VaultPlan`（新 `SKILL.md` 必填、可选 `SCHEDULE.md`），经 Policy / Diff / Risk 后进 approval，默认 **medium-risk**；满足升级条件（vault_scope 覆盖根 > 3 或整库 / budget 任一字段超全局默认 50% / 引入未用过的外部源 / cron 高于每小时一次）转 **high-risk proposal note**。skill-creator 在 propose 前调用 `skill lint` 自检，preview 展示文件路径 / frontmatter 摘要 / 下次触发时间 / 风险等级。obsidian-cli 在该路径仅作可选辅助（探查结构 / 校验渲染），不可用时降级运行；写入仍只由 `VaultExecutor` 完成。
- `openwhisker skill lint <path>`：read-only 校验 SKILL.md / SCHEDULE.md 是否合 JSON Schema、id 冲突、capability / vault_tools / vault_scope / budget 边界、`engine` 与 `vault_tools` 一致性，并联合校验同目录两文件 id 一致。SCHEDULE.md 中出现 runtime 字段（vault_tools / vault_scope / budget / engine）= error，旧字段（capabilities / external_info_sources / skill_config / delivery）= deprecation warning，已移除的 `vault_context` = error。`--strict` 追加风格 warning；退出码 `0` 通过 / `1` error / `2` 仅 warning。skill-creator 与 lint 共用同一份 schema。
