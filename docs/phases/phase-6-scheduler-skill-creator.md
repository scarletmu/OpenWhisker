# Phase 6 Scheduler Skill Creator

状态：草稿。本文定义 Phase 6 的设计意图、范围边界和待决问题，尚未进入实现。

> 术语澄清：phase 名字里的 "skill-creator" 是**泛指 scheduled Skill 创作能力链路**，包括 schema、`openwhisker skill lint`、vault `Skills/skill-creator/` Skill 三者的整体。当本文出现首字母小写、加引号的 `skill-creator` 时，仅指 vault 层那个具体 Skill；不要混淆。

Phase 6 在 Phase 5 已落地的 read-only Skill Scheduler 之上，补齐两件事：

1. **Runtime 能力**：把 scheduler 内的 LLM 推理层从"Host 预拉全部声明数据 + 单轮总结"升级为"LLM 主动调用受限工具集 + 多轮收集 + 自主收尾"，并保留现有 capability 声明边界不破。
2. **Authoring 能力**：给用户提供受规范约束的 scheduled Skill 创建流程，包括 vault 级别的 `skill-creator` Skill（走主线 `WikiJob` 写入）和 CLI 级别的 `openwhisker skill lint`（read-only 校验）。

Phase 6 不改变 Scheduler 的只读 vault 边界。所有 scheduler runtime 增量都在 read-only 工具范围内；任何写 vault 的能力（包括"新增 scheduled Skill"本身）必须走主线 `WikiJob → VaultPlan → Policy → Approval → VaultExecutor`。

## 背景

Phase 5 第一版让 Scheduler 可以按 cron 触发 vault-local Skill，但 LLM 推理层有两个结构性限制：

- **数据收集是 Host 侧的静态白名单**。`SCHEDULE.md.vault_context` 必须预先列出具体路径，`external_info_sources` 在 schedule load 时就锁定，Host 在调用 engine 前已经把所有声明的内容拉满拼成一份 prompt blob。LLM 没有"决定要看什么"的能力。
- **当 vault 数据面不可枚举时模型崩塌**。典型场景：一个想"按当前 vault 状态生成简报或雷达"的 skill，无法把整个 vault 列进 `vault_context`。要么爆 context，要么只能粗暴选一小段静态目录，丢失语义命中能力。

同时，随着字段增加（capabilities / vault_context / external_info_sources / skill_config / delivery / cron），`SCHEDULE.md` 手写难度上升，registry loader 错误信息只指出"哪里 load 失败"，对用户写新 skill 体验不友好。没有 schema、没有脚手架、没有 lint。

Phase 6 同时解决"LLM 自主收集信息"和"用户创建规范化 skill"两件事，因为它们共享一个前提：**必须先为 SCHEDULE.md / SKILL.md 定义机器可读的 schema**，否则 tool-calling 工具白名单和 lint 都没有基准。

## 核心结论

Phase 6 引入的能力：

- 在现有 `SkillEngine` 接口下新增 `ToolCallingEngine` 实现，与 `StaticSkillEngine` / OpenAI-compatible single-turn engine 并列；
- 暴露五个 read-only vault 工具给 LLM：`list_vault_dir`、`read_vault_note`、`vault_outlinks`、`vault_backlinks`、`vault_text_search`；
- 引入 vault 链接图索引子系统，由 daemon 启动时全量构建并通过 fsnotify 增量维护；
- 引入运行时 budget 机制：单 run 最大 tool call 次数、累计返回字节数、wall clock 上限；
- 引入 tool call trace：在 `scheduler_runs` 表加 `tool_trace_json` 列，记录每次工具调用的关键元数据；LLM 完整 message 序列仅在 `--debug` 模式下写入磁盘 ndjson；
- 在 vault `Skills/` 下新增 `skill-creator` Skill，用户手动调用，通过主线 `WikiJob` 写入新的 `SCHEDULE.md` + `SKILL.md`；
- 新增 `openwhisker skill lint` CLI，read-only 校验 scheduled Skill 目录是否符合 schema 和 profile 边界。

Phase 6 明确不引入的能力：

- 不引入 langgraph 或任何外部 Python reasoning 框架；
- 不让 scheduler 获得任何 vault 写工具；
- 不让 scheduler 自动 propose 新 schedule；
- 不在第一版引入 wikilink alias 解析、嵌入式向量检索或反向链接 SQLite 持久化；
- 不引入工具调用的并发执行，每个 run 内工具调用严格串行。

## 不可破的边界

下列声明是 Phase 6 实现必须满足的硬约束：

```text
Scheduler tool catalog 永远不包含 vault 写工具。
Scheduler runtime 不得创建、修改、删除、移动任何 vault 文件。
Scheduler runtime 不得 propose 新的 SCHEDULE.md 或 SKILL.md。
新 scheduled Skill 只能通过 vault 主线 WikiJob 路径创建，由用户显式调用 skill-creator 或手动编辑触发。
Tool call 的每次访问都必须在 Go 侧重新校验 path 在 vault_scope 内，校验失败即工具调用失败。
LLM 不得通过 tool 间接获得任意 HTTP / 任意 shell / 任意文件读写能力。
ToolCallingEngine 的工具集是编译期闭集（第一版固定 5 个），第一版不提供 plugin / dynamic registration / Skill 自带 tool 实现 机制。
Trace 中所有从 vault 内容派生的字段（摘要片段、错误信息、参数回显）必须经过与 outbox payload 同等的脱敏规则（私有主机名、邮箱、token、Matrix ID、内部命名空间等不得明文落 SQLite）。
vault_scope 校验在任何情况下都拒绝以 .obsidian/、.git/、.trash/、.DS_Store 为前缀的路径，即使用户在 VaultProfile.scheduler.read_only_vault_roots 中手滑写入这些路径，registry loader 也应拒绝并返回明确错误。
```

这些约束在文档之外还必须在代码层面体现（registry capability 黑名单已存在；ToolCallingEngine 的工具注册表是闭集；fsnotify watcher 不暴露给 LLM；脱敏规则复用 RSS adapter 现有 `sanitizedFeedURLList` / `isSensitiveConfigKey` 思路并扩展到 trace 写入路径；vault_scope 校验在 `cleanRelativePath` 之上新增黑名单前缀检查）。

**关于手动编辑路径的 by-design 声明**：第 4 条同时承认两条 schedule 变更路径 —— skill-creator 走主线 `WikiJob` approval，用户手动编辑 `SCHEDULE.md` 直接生效（scheduler 下一次 tick reconcile 加载）。这是有意设计而非缺陷：vault 是用户自治空间，OpenWhisker 不应阻止用户直接编辑自己的文件；skill-creator 的 approval 价值在于"agent 辅助生成时强制人审"，不在于"垄断 schedule 变更路径"。未来若引入 schedule 来源审计需求，应作为独立可选机制（例如 audit-only 日志），不应改变手动编辑直接生效的基础语义。

## 总体架构

```text
daemon startup
  -> build vault link index (full scan, async-safe at boot)
  -> start fsnotify watcher on vault root

cli tick (openwhisker scheduler tick)
  -> open SQLite (no IPC to daemon)
  -> build ad-hoc vault link index synchronously
     if file_count > threshold or build_time > threshold:
       require --force-build-index flag, else abort with guidance
  -> no fsnotify watcher (one-shot process)
  -> run single tick then exit

scheduler tick (existing Phase 5 flow)
  -> load SCHEDULE.md
  -> resolve same-dir SKILL.md
  -> select SkillEngine
       priority: SCHEDULE.engine
                  ?? CLI --scheduler-engine flag
                  ?? VaultProfile.scheduler.default_engine
                  ?? compile-time default (static)
  -> if selected engine is unavailable (例如 tool-calling 需要 LLM API key 缺失):
       run status = failed
       outbox surfaces explicit error
       不静默降级到 static engine
  -> dispatch to engine

ToolCallingEngine run
  -> build initial prompt from SKILL.md + schedule metadata
  -> open multi-turn loop with LLM (OpenAI-compatible function calling)
     -> LLM emits tool_call
     -> Go side validates tool name in SCHEDULE.vault_tools whitelist
     -> Go side validates path arguments against vault_scope
     -> Go side executes tool (list / read / outlinks / backlinks / search)
     -> apply per-call sanitization, size limit
     -> deduct from budget, append remaining budget to next system message
     -> return tool_result to LLM
  -> termination:
     -> LLM calls submit_result tool → natural (run status = ok)
     -> budget exceeded → force finalize (强制要 LLM 立刻调用 submit_result)
     -> wall clock exceeded → hard fail (no chance for final summary)
     -> LLM 连续两轮违反协议(吐自由文本 / 未调用 submit_result) → protocol failed
  -> persist tool_trace_json to scheduler_runs
  -> emit SkillRunResult (title / summary / payload) as before
```

## 工具集

第一版固定 5 个工具，对应 5 个 Go 函数。每个工具的 capability 在 SCHEDULE.md `vault_tools` 里按名字白名单声明。

| 工具名 | 入参 | 返回 | 依赖 capability |
| --- | --- | --- | --- |
| `list_vault_dir` | `path: string` | 目录条目列表（文件名 / 子目录名），带 truncated 标记 | `vault_read` |
| `read_vault_note` | `path: string` | 文件全文（截到 size limit），带 truncated 标记 | `vault_read` |
| `vault_outlinks` | `path: string` | 结构化返回 `{wikilinks: [...], frontmatter_related: [...]}` 两类区分；wikilinks 是正文 `[[...]]` 解析结果，frontmatter_related 是 frontmatter `related:` 字段显式声明 | `vault_read` + `vault_link_graph_read` |
| `vault_backlinks` | `path: string` | 反查链接图，返回所有链入该 note 的 source 路径列表 | `vault_link_graph_read` |
| `vault_text_search` | `query: string, scope_subset?: []string` | ripgrep 文本搜索，每项返回 path + 单行匹配片段 | `vault_read` + `vault_text_search` |

实现方式约束：

- 五个工具的实现**全部为 Go 内函数**，不依赖 `obsidian-cli` 或任何外部 CLI subprocess；
- 这条约束的理由：scheduler daemon 运行假设是 24/7 后台 launchd 服务，可能在 Obsidian 桌面 app 未运行的时段 tick；obsidian-cli 多数实现需要 Obsidian app 在跑且自身带 vault 写 + JS 执行能力，与 scheduler 的 read-only + 闭集硬约束冲突；
- obsidian-cli 在 Phase 6 的角色限定在 skill-creator 路径上，见后文 `skill-creator` 章节。

调用约束：

- 所有路径参数在 Go 侧调用 `cleanRelativePath` + `isUnderAnyRoot(path, scope)` 重新校验；
- `read_vault_note` 使用 **tool-side 独立上限**：默认 128 KiB，可通过 daemon flag `--scheduler-tool-read-bytes` 调整，硬上限 256 KiB；超过则截断并标记 truncated。Phase 5 的 `maxVaultContextFileBytes = 20 KiB` 是为已移除的静态 `vault_context` 路径设计，Phase 6 移除该字段后视为 dead constant；
- `list_vault_dir` 复用 `maxVaultContextEntries = 50` 上限；
- `vault_text_search` 单次返回上限：max 50 命中，每命中只回 1 行上下文片段；`scope_subset` 可选参数必须是 `SCHEDULE.md.vault_scope` 的真子集，校验失败即工具调用失败；整体响应 size 受全局 budget 约束；
- `vault_outlinks` 第一版只解析精确路径和文件名命中，**不解析 Obsidian alias**。无法解析的 wikilink 在返回中标记 `unresolved`，列出原始文本。这是已知限制，留待后续扩展；
- `vault_outlinks` 返回值显式区分 `wikilinks` 和 `frontmatter_related` 两个数组，让 LLM 能区分"作者显式声明的强关联"和"正文偶发提及"；
- `vault_backlinks` 完全依赖内存链接图索引，索引尚未建好时返回明确 error，提示 LLM 退化到 `vault_text_search`。

新增 capability 名（registry loader 允许列表扩展）：

```text
allowed (新增):
  - vault_link_graph_read
  - vault_text_search
```

`vault_write`、`vault_executor_apply`、`auto_approve`、`external_api_side_effect`、`arbitrary_shell`、`arbitrary_http`、`arbitrary_file_write` 维持禁用。

## SCHEDULE.md schema 演进

Phase 5 的 `vault_context` 字段（具体路径列表）**直接移除**，无历史包袱保留。新字段：

```yaml
---
id: daily-vault-radar
name: 每日 vault 雷达
enabled: true
cron_expr: "0 9 * * *"
timezone: Asia/Shanghai
delivery:
  - outbox
capabilities:
  - vault_read
  - vault_link_graph_read
  - vault_text_search
  - scheduler_run_log_write
  - outbox_notify
engine: tool-calling
vault_tools:
  - list_vault_dir
  - read_vault_note
  - vault_outlinks
  - vault_backlinks
  - vault_text_search
vault_scope:
  - Raw/
  - Knowledge/
  - Meta/
external_info_sources: []
budget:
  max_tool_calls: 8
  max_total_bytes: 204800
  max_wall_clock_seconds: 60
skill_config:
  detail: normal
---
```

字段语义：

- `engine`：第一版可选 `static` / `openai-compatible` / `tool-calling`。
  - 缺省按 CLI `--scheduler-engine` 推断；
  - **隐式推断规则**：声明了非空 `vault_tools` 而未声明 `engine` 时，registry loader 自动推断为 `engine: tool-calling`，避免用户写错；
  - **冲突 reject**：显式 `engine: static`（或 `openai-compatible`）+ 非空 `vault_tools` 视为配置冲突，registry loader 拒绝加载并给出可读错误；
  - **backwards-compat**：既无 `engine` 又无 `vault_tools` 的 Phase 5 老 schedule，视为 `engine: static`，行为保持 Phase 5 不变，零回归；
- `vault_tools`：tool name 字符串数组，必须是上面五工具子集；声明 `tool-calling` engine 时必填且非空；
  - **顺序无意义**：registry loader 加载时按字典序规范化存储，避免用户写两份顺序不同的 SCHEDULE.md 产生不同 `registry_hash`；
- `vault_scope`：根级路径数组，必须落在 `VaultProfile.scheduler.read_only_vault_roots` 内。每次 tool call 的 path 都按此校验；同样按字典序规范化；
- `budget`：可选，缺省按全局默认值；任何字段不得超过全局上限（避免 skill 自抬权限）；
  - **任一字段必须 > 0**，`0` 视为非法值，registry loader 拒绝加载；
  - 想完全禁止 tool call 应改用 `engine: static` 或干脆不声明 `vault_tools`；
- `external_info_sources` 与 Phase 5 语义不变，可与 `vault_tools` 并存；
  - **第一版处理方式**：ToolCallingEngine 启动时仍按 Phase 5 行为预拉 `external_info_sources`，作为 initial prompt 的一部分注入；不把 RSS 等包装成 tool 暴露给 LLM；
  - 后续如需"LLM 决定何时拉 feed"形态，再讨论 `fetch_external_feed` 工具化设计。

正式 schema 文件以 JSON Schema 形式存放：

```text
docs/phases/phase-6-scheduler-skill-creator/schedule-schema.json
docs/phases/phase-6-scheduler-skill-creator/skill-schema.json
```

`SCHEDULE.md` registry loader 和 `openwhisker skill lint` 共享同一份 schema。

## ToolCallingEngine

新增 `SkillEngine` 实现，与 `StaticSkillEngine` / OpenAI-compatible engine 并列。原有两个 engine 行为完全不变，避免回归。

CLI 入口：

```text
openwhisker daemon --scheduler-engine tool-calling
openwhisker scheduler tick --engine tool-calling
```

`SCHEDULE.md` 内 `engine` 字段优先级高于 CLI flag。

底层协议使用 OpenAI Chat Completions 的 `tools` / `tool_choice` / `tool_calls` 标准格式。Phase 6 第一版目标 provider：

- 主要：`deepseek-chat`（与 v1 主线对齐）；
- 备选：任何 OpenAI-compatible function calling 兼容 provider；
- `deepseek-reasoner` 是否支持 function calling 未在官方文档明确，视为 OPEN 项；
- 在写代码前应做一次最小多轮 demo（3-4 轮 tool call），验证 DeepSeek function calling 多轮稳定性。

引擎与 Host 的接口扩展：

```go
// 概念草稿，实际 struct 字段名 / tag 在实现时定。
type ToolDescriptor struct {
    Name        string          // e.g. "read_vault_note"
    Description string          // 给 LLM 看的简短说明
    Parameters  json.RawMessage // JSON Schema, 直接喂给 OpenAI tools.function.parameters
}

type BudgetLimits struct {
    MaxToolCalls         int
    MaxTotalBytes        int
    MaxWallClockSeconds  int
}

// SkillExecutionRequest 在 Phase 5 基础上新增:
//   ToolCatalog       []ToolDescriptor
//   Budget            BudgetLimits
//   LinkIndexProvider LinkIndexReader
//   ToolExecutor      ToolExecutor  // Host 提供, engine 只用不实现
//
// 返回类型 SkillRunResult 不变 (title / summary / payload)。
// trace 由 Host 在 engine 返回后从 ToolCallRecorder 提取, 写入 run log。
```

工具的执行实体不在 engine 内，而是 Host 提供的受信函数集；engine 只负责协议层（解析 tool_call、回填 tool_result、循环至终止条件）。这样 capability 校验、size limit、trace 记录全部留在 Go Host 侧，engine 无法绕过。

### System prompt 模板

第一版定义固定的 prompt 拼装模板，避免每个 skill 用户自己拼出风格各异的 prompt：

```text
[system message]
You are running as scheduled skill "{schedule.id}" inside OpenWhisker Scheduler.
Skill definition (verbatim from SKILL.md):

<SKILL.md content>

Vault scope (the only paths you may access via tools):
- {vault_scope[0]}
- {vault_scope[1]}
...

Available tools: {tool_name_list}.

To finalize, call the submit_result tool with title/summary/payload. Do not produce
a final natural-language message; the only way to terminate is submit_result.

[budget] tool_calls_left=N, bytes_left=M, wall_clock_left=Ts

[user message]
Now produce the scheduled output as defined by the skill.
```

正文中 `<SKILL.md content>` 包含 frontmatter 之外的 markdown 全文。每轮 LLM 调用前 Host 在 system message 末尾刷新 `[budget]` 行。

### 终止协议：submit_result tool

LLM 不通过"返回不带 tool_call 的最终消息"来终止，而是**必须调用一个名为 `submit_result` 的特殊工具**：

```text
submit_result(title: string, summary: string, payload: object)
```

- `submit_result` 由 Host 注册，永远在 `ToolCatalog` 里，不受 `SCHEDULE.md.vault_tools` 白名单约束（它不访问 vault）；
- LLM 调用 `submit_result` → Host 提取参数填入 `SkillRunResult` → engine 退出循环 → run 标记 `ok`；
- 强制结构化输出，不依赖 DeepSeek `json_object` 兼容性（DeepSeek 只支持 `json_object`，不支持 `json_schema`，见 memory `project_deepseek_target_provider`）；
- 撞 budget force-finalize 时，Host 在最后一轮 system message 追加 "you must call submit_result now with what you have"，强制收尾；
- LLM 在未调用 `submit_result` 就吐自由文本时，Host 视为协议错误，记录 trace，再给一次机会，连续两次仍不合规则 = run failed。

### Message history 策略

第一版**全保留**：每轮 LLM 调用都带完整 message history（含所有历次 tool_call + tool_result）。

- tool_result 已有单次 size limit（`read_vault_note` 128 KiB 上限），总长度受 `max_total_bytes` budget 间接约束；
- 不引入历史压缩 / 摘要化 / 滚动窗口；
- 等真撞 LLM context token 上限或成本问题再上"历史摘要化"，留作后续优化。

## Budget

三个独立上限，任一先撞到即触发"超限"动作：

| 维度 | 默认值（待验证） | 行为 |
| --- | --- | --- |
| `max_tool_calls` | 8 | force finalize |
| `max_total_bytes` | 200 KiB | force finalize |
| `max_wall_clock_seconds` | 60 | hard fail |

每轮 LLM 调用前，Host 在 system message 里 append 当前剩余预算快照：

```text
[budget] tool_calls_left=5, bytes_left=148 KiB, wall_clock_left=42s
```

让 LLM 自主决定何时收尾。撞 call 数或 bytes 上限时进入 force finalize：禁止再发普通 tool_call（`submit_result` 仍允许），system message 追加 "you must call submit_result now"，LLM 收尾后结果标记 `status=partial`，trace 记录终止原因。

撞 wall clock 时走 hard fail：Host 通过 `context.Deadline` 在工具执行和 LLM 调用两侧同时强制 cancel，run 结果标记 `status=failed`，outbox 报错。Wall clock 检查发生在 (a) 每轮 LLM 调用前、(b) 每次工具执行 ctx 内、(c) HTTP client timeout 三处。

**Run status 枚举**（全局口径）：

- `ok`：natural 终止，LLM 通过 `submit_result` 正常收尾；
- `partial`：force finalize 后 LLM 通过 `submit_result` 收尾，但 trace 记录撞了 call 数或 bytes 上限；
- `failed`：wall clock 超限 / protocol_failed / 工具执行错误未恢复 / engine 不可用等任何无法产出有效 SkillRunResult 的情况；
- `skipped`：Phase 5 既有语义，同 schedule 已有 running run 时本次 tick 跳过。

全局默认值通过 daemon flag 配置（命名统一前缀 `--scheduler-budget-`）：

```text
--scheduler-budget-tool-calls     (默认 8)
--scheduler-budget-bytes          (默认 204800)
--scheduler-budget-wall-clock     (默认 60s)
```

SCHEDULE.md `budget` 字段可在全局上限内向下覆盖，不允许向上越权（registry loader 加载时校验，超限即 reject）。

## Trace

新增 SQLite 列：

```text
ALTER TABLE scheduler_runs ADD COLUMN tool_trace_json TEXT;
```

`tool_trace_json` 内容形态（每条 tool call 一项 + 终止信息）：

```json
{
  "termination": "natural | call_count_exceeded | bytes_exceeded | wall_clock_exceeded | protocol_failed | error",
  "link_index_version": "monotonic generation counter (int64, incremented on every index mutation event)",
  "calls": [
    {
      "seq": 1,
      "tool": "list_vault_dir",
      "args": {"path": "Raw/"},
      "result_bytes": 412,
      "result_summary_sha256": "...",
      "duration_ms": 7,
      "truncated": false,
      "error": null,
      "budget_after": {"tool_calls_left": 7, "bytes_left": 204388, "wall_clock_left": 58}
    }
  ],
  "llm_total_tokens": 0
}
```

不存工具返回原文，只存大小 + hash + 摘要片段（不超过 200 字节）。trace 不包含 LLM 完整 message 序列。

**Trace 脱敏规则**（呼应硬约束 #8）：

- `args` 中的 `path` 参数：path 本身不脱敏（vault 路径不是敏感数据）；
- `args` 中的 `query` / `summary` / 自由文本参数：按 outbox payload 同等脱敏（私有主机名、邮箱、token、Matrix ID、内部命名空间用占位符替换）；
- `error` 字段：剥离任何包含 vault 内容的 backtrace 行，只保留错误类型 + 安全描述；
- `result_summary_sha256` 是工具返回原文的 sha256（用于跨 run 判断"读到的是不是同一份内容"），不脱敏；
- 摘要片段（≤200 字节）必须经过脱敏后再存。

脱敏实现复用 RSS adapter 现有 `sanitizedFeedURLList` / `isSensitiveConfigKey` 思路，抽取为通用 `internal/sanitize/` 包供 trace 写入路径调用。

LLM 完整 message 仅在 `--debug` 模式下落盘：

```text
data/scheduler-debug/<run_id>.ndjson
```

debug 模式由 `openwhisker scheduler tick --debug` 或 daemon flag 开启。生产部署默认关闭。

CLI 查看入口：

```text
openwhisker scheduler runs <run_id> --trace
```

输出工具调用时间线（seq / tool / 关键参数摘要 / 大小 / 截断 / 剩余预算）。Matrix 不在通知里默认推 trace，可加 `/scheduler trace <run_id>` 按需查。outbox 默认摘要只显示终止原因和计数（例如 "扫描 12 个 note，命中 3 项相关内容；终止：natural"）。计数由 Host 在 finalize 时从 trace 中统计 `list_vault_dir` / `read_vault_note` / `vault_outlinks` / `vault_backlinks` / `vault_text_search` 的调用次数与命中数得到，不需要 LLM 自己报告。

## 链接图索引子系统

新增内部子系统 `internal/vault/linkindex/`，daemon 进程内单例。

**归属层声明**：链接图索引位于通用 `internal/vault/` 命名空间下，**不是 scheduler 私有子系统**。Phase 6 中 scheduler 是首个消费者；后续 WikiJob plan / 主线 organize 路径 / Knowledge Expander 等也可复用同一索引接口（例如 plan 阶段判断"新增 note 会破坏哪些已有 backlink"）。Phase 6 不实现这些消费方，但**接口设计应预留**给非 scheduler 的调用方。

数据结构：

```text
notePath     = vault-relative path, forward-slash normalized (e.g. "Knowledge/CS/locks.md")
ResolvedLink:
  TargetPath   notePath           // 解析成功时的目标路径; 失败为空
  SourceKind   "wikilink" | "frontmatter_related"
  OriginalText string             // wikilink 原文如 "[[Foo|别名]]" 或 related: 条目
  Resolved     bool               // false = unresolved (Phase 6 第一版不支持 alias)

LinkIndex:
  outlinks   map[notePath][]ResolvedLink
  backlinks  map[notePath][]notePath
  generation int64   // 每次增量更新自增, 用于 trace.link_index_version
  mu         sync.RWMutex
```

`backlinks` 派生自 `outlinks`（反向映射），增量更新时两者一起维护。

构建与维护：

- daemon 启动时全量扫描 vault root（排除 `.obsidian/`、`.git/`、`.trash/`、`.DS_Store`、隐藏目录），解析所有 `.md` 的 wikilink 和 frontmatter `related:`；
- 不跟随指向 vault root 外的 symlink（防止 LLM 间接读 vault 外文件）；vault 内部 symlink 跟随但解析后的 path 必须仍在 vault root 内；
- fsnotify watcher 监听 vault 文件变化：create / modify → 重解析该文件并更新 outlinks 和反向 backlinks；delete → 移除条目；rename → delete + create；
- watcher 事件做 debounce（默认 200ms）合并连续保存，处理编辑器常见的 "tmp file → rename" 原子保存模式；
- 索引构建期间 tool call `vault_backlinks` 返回明确 error，提示退化到 `vault_text_search`；
- 索引坏掉或事件溢出时（macOS kqueue FD 上限触发），daemon 记录 stderr error，下次 tick 前触发全量重建，重建期间 `vault_backlinks` 仍返回 error；
- 内存预期：典型 `.md` 平均 5-20 个 link，10k notes 量级时 LinkIndex 占用应在数十 MB 量级；超过 50k notes 视为未验证范围，需要补充实测。

Snapshot 语义：

- 单 run 内的 tool call 不持有索引 snapshot，每次调用读最新索引（最终一致）；
- 每次 tool call 在 trace 中记录读取时的 `link_index_version`，让审计能判断 run 是否跨越了索引更新；
- 不引入 per-run snapshot 复制（vault 规模下成本和复杂度都不划算）。

索引不落 SQLite：

- daemon 重启 = 全量重建，可接受（典型 vault 几秒内重建完成）；
- 持久化引入一致性问题且收益有限。

CLI tick 进程的索引行为：

- `openwhisker scheduler tick` 是与 daemon 完全独立的进程，仅共享 SQLite，不通过 IPC 访问 daemon 内存中的链接图；
- CLI tick 进程启动时**同步**构建一次 ad-hoc 链接图索引，本次 tick 内使用，进程退出即丢弃；
- CLI tick 进程**不启动 fsnotify watcher**（无意义，进程是一次性的）；
- 软门槛：当 vault `.md` 文件数 > `5000` 或全量扫描时间 > `10s` 时，CLI tick 拒绝执行并提示用户加 `--force-build-index` 显式确认，避免在大 vault 上意外承担长延迟；
- 软门槛阈值通过 daemon flag 可调（`--cli-index-file-warn` / `--cli-index-time-warn`）；
- 这意味着 CLI tick 在 tool-calling engine 下的启动延迟显著高于 daemon 内 tick；这是 by design，CLI tick 是调试/手动验证手段，不是 schedule 的主执行入口。

## skill-creator（vault 级）

位置：

```text
<vault-root>/Skills/skill-creator/
  SKILL.md
  README.md（可选）
```

与现有 `Skills/vault-raw-organizer/` 和 `Skills/vault-knowledge-expander/` 同级，遵循 vault Skills 约定（参见 `Meta/Knowledge-Base-Data-Organization-Process.md` 的 `Skills/` 区域定义）。

调用路径：

- 用户通过 Knowledge Bot slash command 或 `openwhisker` CLI 显式触发；
- **不通过 scheduler 触发**（scheduler 不得自动创建 skill）；
- 输出形态：一份 `VaultPlan`，包含两个新文件 `Scheduler/Skills/<new-skill-id>/SCHEDULE.md` + `SKILL.md`；
- 经 Policy / Diff / Risk 检查后进入 approval 流程；
- 因为是"新增自动化行为"，**默认风险等级 medium**，必须用户显式 approve；
- approve 后 `VaultExecutor` 写入文件，下次 scheduler tick reconcile 时加载新 schedule；
- **风险自动升级到 high（产 proposal note 而非直接 approval）的触发条件**：(a) `vault_scope` 覆盖根超过 3 个或包含整个 vault；(b) `budget` 任一字段超过全局默认值的 50%；(c) `external_info_sources` 引入新的尚未使用过的源类型；满足任一条 → high-risk，走 proposal note 路径让用户先在 vault 里讨论再 approve。

skill-creator 自身的 prompt 应：

- 询问用户意图（任务描述 / 触发时机 / 需要的 vault 区域 / 需要的工具）；
- 生成符合 schema 的 `SCHEDULE.md`（cron / capabilities / vault_tools / vault_scope / budget）；
- 生成 `SKILL.md`（任务描述 / 输出形态 / 是否需要 suggested raw capture）；
- 在 `VaultPlan` 提交前调用 `openwhisker skill lint` 做最后一次自检；
- 如果 lint 不通过，进入修订循环而不是直接 propose；
- propose 前给用户展示 dry-run preview：将要创建的文件路径、frontmatter 摘要、预期下次触发时间、风险等级、升级原因（若有），让用户能在不读全文的情况下判断是否 approve。

**obsidian-cli 的有限角色**：

skill-creator 路径**允许**调用 `obsidian-cli` 完成辅助任务，因为该路径是用户主动触发、运行时 Obsidian 桌面 app 几乎必然在前台。允许的辅助场景：

- 探查用户现有 vault 结构（已有哪些 zone、哪些 Skill），辅助 prompt 生成时给出合理默认值；
- 校验新生成的 `SKILL.md` / `SCHEDULE.md` 在 Obsidian 中能正确渲染（wikilink 是否解析、frontmatter 是否被识别）；
- approve 后在 Obsidian 中跳转打开新建的 Skill 目录给用户确认。

**禁止**用 obsidian-cli 执行以下任何动作：

- 任何 vault 写入 / 创建 / 修改（写入仍由 `VaultExecutor` 完成，唯一写入路径）；
- 任何 JS 执行 / plugin reload / 任意命令；
- 任何 scheduler runtime 路径上的调用（scheduler 永远不依赖 obsidian-cli）。

skill-creator 的核心生成与校验逻辑（schema 套用、frontmatter 拼装、lint 调用）仍是 Go 实现，obsidian-cli 只是可选的辅助层；obsidian-cli 不可用时 skill-creator 应能降级运行，不阻塞主流程。

skill-creator 的详细设计文档放在 vault 内，不在本 phase doc 展开：

```text
<vault-root>/Skills/skill-creator/SKILL.md
```

本 phase doc 仅声明它的归属、调用边界和与 `openwhisker skill lint` 的协作关系。

## openwhisker skill lint

CLI read-only 校验工具：

```text
openwhisker skill lint <path>
```

`<path>` 是一个 scheduled Skill 目录或单个 `SCHEDULE.md` 文件。

校验项：

- frontmatter 是否合 JSON Schema；
- `id` 是否与已有 schedule 冲突；
- `cron_expr` 是否合法；
- `timezone` 是否可解析；
- `capabilities` 是否在 Phase 6 允许列表内（无禁用项）；
- `vault_tools` 是否在固定五工具集内，`engine` 与 `vault_tools` 是否一致；
- `vault_scope` 是否落在当前 `VaultProfile.scheduler.read_only_vault_roots` 内，且不包含被禁前缀（`.obsidian/` 等）；
- `budget` 各字段是否未超全局上限且都 > 0；
- 同目录 `SKILL.md` 是否存在、非空、至少包含一个 `# heading` 用于 outbox 标题回退。

`--strict` 模式追加（warning 不 error）：

- `id` 是否仅包含 ASCII 小写字母、数字、连字符（推荐风格）；
- `cron_expr` 是否在 daemon 重启常见时段（如 `0 0 * * *`）触发，可能与重启窗口冲突；
- `vault_scope` 是否覆盖整个 vault（高风险但合法）；
- `external_info_sources` 是否含未在 profile 显式启用过的 source 类型。

错误输出格式应**指出文件名、行号、字段名和可读修复建议**，而不是只 "load failed"。

退出码：`0` = 全部通过；`1` = 至少一个 error；`2` = 仅 warning（仅 `--strict` 模式可能返回）。

lint 不写任何东西，可在任何环境运行。skill-creator 在 propose 前会调用它（默认非 `--strict`，避免风格 warning 阻塞 propose）。

## 数据模型变更

```text
ALTER TABLE scheduler_runs ADD COLUMN tool_trace_json TEXT;
```

`scheduler_runtime` 不变。

**迁移策略**：daemon 首次启动 Phase 6 binary 时，schema migration 检查 `scheduler_runs` 是否含 `tool_trace_json` 列，缺则 ALTER TABLE 添加。无 down migration（向 Phase 5 binary 回退时该列被忽略，Phase 5 不读不写它）。Phase 5 已写入的 `scheduler_runs` 行其 `tool_trace_json` 为 NULL，CLI `--trace` 显示 "no trace (legacy or non-tool-calling run)"。

`VaultProfile.scheduler` 增加（向后兼容字段，缺省值不破坏 Phase 5 行为）：

```yaml
scheduler:
  tool_budget_defaults:
    max_tool_calls: 8
    max_total_bytes: 204800
    max_wall_clock_seconds: 60
  default_engine: static       # 可选, Phase 5 profile 不写视为 static
```

CLI flag 优先级：CLI > profile > 编译期默认值。Phase 5 profile 文件未声明 `tool_budget_defaults` 或 `default_engine` 时，全部回退到编译期默认值，行为与 Phase 5 完全一致。

## Workflow

```text
ToolCallingEngine 单 run:

  Host: prepare initial prompt (固定模板, 见 ToolCallingEngine 章节)
    -> SKILL.md content
    -> schedule metadata (id, name, vault_scope, vault_tools)
    -> tool catalog (JSON Schema for each whitelisted tool + submit_result)
    -> initial budget snapshot

  loop:
    LLM -> response
      -> if response 包含 submit_result tool_call:
           提取 title/summary/payload 填入 SkillRunResult
           break with status=ok (termination=natural)
      -> if response.tool_calls 为空 (LLM 吐自由文本):
           记录协议违反, 给一次纠正提示重试
           连续两次违反 → break with status=failed (termination=protocol_failed)
      -> for each tool_call:
           validate tool name in vault_tools (submit_result 例外, 总在 catalog)
           validate path args in vault_scope
           execute via Go function
           sanitize, size-limit, record trace
           deduct from budget
           append tool_result to message history (全保留, 不压缩)
           if any budget < threshold: enter force-finalize mode
                                       (system message 追加"must call submit_result now")
      -> if wall_clock_exceeded:
           abort with status=failed (termination=wall_clock_exceeded)
      -> append [budget] system note for next turn

  finalize:
    persist tool_trace_json
    enqueue outbox message (existing flow)
```

## 首版范围

范围内：

- `ToolCallingEngine` 实现（Go，OpenAI-compatible function calling，固定 system prompt 模板，`submit_result` 终止协议）；
- 五个 vault 工具实现 + `submit_result` 内置工具；
- 链接图索引子系统（fsnotify-based，内存，daemon 启动时全量构建 + CLI tick 进程 ad-hoc 同步构建 + 软门槛）；
- Budget 三上限（命名前缀 `--scheduler-budget-*`）+ force finalize / hard fail / protocol_failed 行为 + run status 枚举；
- `tool_trace_json` 列 + `--debug` ndjson + trace 脱敏规则（呼应硬约束 #8）；
- `SCHEDULE.md` schema JSON 文件 + registry loader 适配（含隐式 engine 推断、字典序规范化、backwards-compat）；
- `openwhisker skill lint` CLI（含 `--strict` 模式 + 三档退出码）；
- `Skills/skill-creator/` 设计入口（不展开 Skill 内部 prompt 设计；含 medium → high 风险升级规则 + dry-run preview）；
- `internal/sanitize/` 通用脱敏包；
- CLI / Matrix trace 查看入口；
- schema migration（ALTER TABLE 自动检测，无 down migration）。

范围外：

- langgraph 或其他外部框架；
- scheduler 写 vault 任何形态；
- 自动 propose 新 schedule；
- wikilink alias 解析；
- 向量检索 / 嵌入式语义搜索；
- 反向链接 SQLite 持久化；
- 多 worker 并发执行同一 run；
- 工具间并行调用；
- 用户级别的 budget 反馈 UI（未来 Android Console 范围）。

## Policy

Phase 6 在 Phase 5 policy 基础上追加：

```text
allowed (新增):
  - vault_link_graph_read
  - vault_text_search

requires_user_confirmation (新增):
  - create_scheduled_skill  (skill-creator 走主线 WikiJob, 默认 medium-risk approval)

disabled (新增显式):
  - scheduler_initiated_skill_creation
  - scheduler_initiated_schedule_modification
  - tool_call_chained_external_http
  - tool_call_chained_shell_execution
```

## 验收

Phase 6 完成后应满足：

```text
ToolCallingEngine 可以按 SCHEDULE.md 声明的 vault_tools 白名单运行多轮 tool calling。
LLM 通过 submit_result 工具终止 run，非通过自由文本结束；连续两次违反协议视为 protocol_failed。
五个工具的每次调用都在 Go 侧重新校验 path 在 vault_scope 内（含 .obsidian/.git/.trash 等禁入前缀的硬黑名单）。
LLM 无法通过任何 tool 间接读到 vault_scope 外的内容。
LLM 无法通过任何 tool 写 vault、调用 shell、发起任意 HTTP。
budget 撞限按 force finalize / hard fail / protocol_failed 规则终止，并在 trace 中记录原因；run status 落入 ok / partial / failed / skipped 之一。
scheduler_runs.tool_trace_json 完整保存每次 tool call 的元数据（不含全文），所有 vault 派生字段经 internal/sanitize 脱敏。
--debug 模式下完整 LLM message 序列落 data/scheduler-debug/<run_id>.ndjson。
链接图索引在 daemon 启动时全量构建，运行期通过 fsnotify 增量维护；CLI tick 进程同步现建 ad-hoc 索引并受软门槛保护。
vault_backlinks 在索引未就绪时返回明确 error，不静默返回错误数据。
openwhisker skill lint 对违反 schema / capability / scope 的 SCHEDULE.md 给出可读错误（含行号 + 修复建议），--strict 模式追加风格 warning。
skill-creator 创建新 scheduled Skill 时走主线 WikiJob，默认进入 medium-risk approval；满足风险升级条件时走 high-risk proposal note。
Phase 6 binary 启动时自动 ALTER TABLE 添加 tool_trace_json 列，对已有 Phase 5 run 行无影响。
Phase 5 已有 schedule 行为不受 Phase 6 引入的回归影响（无 engine + 无 vault_tools 默认视为 engine: static）。
```

## 待决问题

落入实现前需要解决：

- **OPEN-1**：DeepSeek `deepseek-chat` 多轮 function calling 稳定性需要一次最小 demo 验证。验收标准：连续 10 次 demo run，每次 3-4 轮 tool call，要求 (a) 每轮模型返回合法的 `tool_call` 或调用 `submit_result`，无 schema 错误；(b) 无 stuck loop（同一参数反复调同一 tool 超过 2 次）；(c) 最终通过 `submit_result` 返回合理的 title/summary/payload。≥9/10 通过视为稳定；否则将 DeepSeek 标记为 supported with caveat，要求 fallback provider 配置。
- **OPEN-2**：budget 默认值（8 / 200 KiB / 60s）为初始估算，需要在首批 skill 落地后两周内观察实际 run 决定是否调整；
- **OPEN-3**：wikilink alias 解析是否纳入 Phase 6 第一版（当前倾向不纳入，作为后续增量）；
- **OPEN-4**：`vault_text_search` 是否暴露 case sensitivity / regex 模式开关给 LLM，还是固定为 literal + case-insensitive。当前倾向固定后者，简化工具语义；
- ~~**OPEN-5**~~：已确认 `skill-creator` Skill prompt 设计落 vault `Skills/skill-creator/SKILL.md`，本 phase doc 只声明边界；
- ~~**OPEN-6**~~：已确认 fsnotify 溢出处理为 daemon stderr error + 下次 tick 前触发全量重建，重建期间 `vault_backlinks` 返回 error；50k+ notes 规模视为未验证范围需补充实测。

## 已确认决策

- 不引入 langgraph，第一版用 Go 内 ToolCallingEngine + OpenAI-compatible function calling 协议；
- Phase 5 `vault_context` 字段移除，不保留兼容路径；Phase 5 `maxVaultContextFileBytes = 20 KiB` 成为 dead constant；
- 链接图索引使用 daemon 启动全量构建 + fsnotify 增量维护方案；归属 `internal/vault/linkindex/` 通用层，scheduler 是首个消费者；
- CLI tick 与 daemon 是独立进程，CLI tick 同步现建 ad-hoc 索引并受软门槛（5000 文件 / 10s）保护；
- Trace 落 `scheduler_runs.tool_trace_json` 列，不引入独立 `scheduler_tool_calls` 表；trace 字段经 `internal/sanitize/` 通用脱敏；
- LLM 完整 message 仅在 `--debug` 模式下落磁盘 ndjson，不进 SQLite；
- `skill-creator` 归属 vault `Skills/`，通过主线 `WikiJob` 写入，scheduler 不参与；默认 medium-risk approval，满足升级条件转 high-risk proposal note；
- `openwhisker skill lint` 是 read-only CLI，与 skill-creator 共用 schema；含 `--strict` 模式 + 三档退出码；
- `vault_tools` 按工具粒度白名单声明，不引入 bundle 形态；顺序无意义按字典序规范化存储；
- Budget 行为：撞 call 数 / bytes 走 force finalize；撞 wall clock 走 hard fail；命名前缀 `--scheduler-budget-*`；
- Run status 枚举：ok / partial / failed / skipped；trace.termination：natural / call_count_exceeded / bytes_exceeded / wall_clock_exceeded / protocol_failed / error；
- 工具执行体留在 Go Host 侧，engine 只承担协议层；
- LLM 通过内置 `submit_result` 工具终止 run，不通过自由文本结束；
- Engine 隐式推断：非空 `vault_tools` 未声明 `engine` 时自动推断为 `tool-calling`；显式冲突（如 `engine: static` + 非空 `vault_tools`）reject；
- 工具集是编译期闭集（5 + 1 内置 `submit_result`），不提供 plugin / dynamic registration；
- vault_scope 校验在任何情况下拒绝 `.obsidian/`、`.git/`、`.trash/`、`.DS_Store` 前缀；不跟随指向 vault root 外的 symlink；
- `read_vault_note` 使用 tool-side 独立上限（默认 128 KiB，硬上限 256 KiB），与 Phase 5 常量解耦；
- `vault_outlinks` 返回区分 `wikilinks` 和 `frontmatter_related` 两类，但 Phase 6 第一版不解析 Obsidian alias（OPEN-3）；
- `search_vault` 改名为 `vault_text_search`，保留可选 `scope_subset` 参数；
- 手动编辑 vault `SCHEDULE.md` 直接生效是 by design 行为，与 skill-creator approval 并存，不视为不一致；
- Schema 迁移：daemon 首次启动检查 `tool_trace_json` 列缺则 ALTER TABLE，无 down migration。
