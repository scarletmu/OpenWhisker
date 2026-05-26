# Phase 6 Agent-Driven Vault Knowledge Response

状态：**实现完成（待 OPEN-1 demo 验证）**。本文定义 Phase 6 的设计意图、范围边界和待决问题。代码已落地（详见 CHANGELOG 2026-05-26 「Phase 6 落地」条目），但 OPEN-1 要求的 ≥9/10 deepseek-chat 多轮稳定性 demo 需用户在本地真实 vault 上自行验证后才能合 deploy/main。

> 命名说明：phase 文件名沿用历史名 `phase-6-scheduler-skill-creator`，避免外部链接断裂；但 Phase 6 的主题已从"只把 scheduler 变聪明"扩展为"让 OpenWhisker 具备 agent 化的 vault 知识查询响应能力"。scheduler 在新主题下退居为众多触发方式之一。
>
> 术语澄清：phase 名字里的 "skill-creator" 是**泛指 vault Skill 创作能力链路**，包括 schema、`openwhisker skill lint`、vault `Agent/Skills/skill-creator/` Skill 三者的整体。当本文出现首字母小写、加引号的 `skill-creator` 时，仅指 vault 层那个具体 Skill；不要混淆。

## 概述

Phase 6 要解决的本质问题是：**让用户能在主动提出问题或设定目标时，让 LLM 自主从 vault 抽取相关知识做出响应**。响应的触发方式可以是：

- **用户主动提问**（Matrix 自然语言 @skill_id / CLI `openwhisker ask`）；
- **按预设节律触发**（scheduler cron）。

Phase 5 已经搭好了 read-only Skill Scheduler 的基础，但 LLM 推理层是"Host 预拉静态白名单数据 + 单轮总结"，无法支持"agent 自己决定要看 vault 哪里"。Phase 6 在 Phase 5 之上补三件事：

1. **Agent runtime 能力**：把 LLM 推理层升级为"自主调用受限工具集 + 多轮收集 + 自主收尾"的 ReAct 循环，并保留 Phase 5 的 capability 声明边界不破。
2. **多 host 接入**：把 agent runtime 从 SchedulerService 解耦为通用 `AgentRunner`，让 Matrix Bot、CLI、Scheduler 三个 host 共用同一套 engine / 工具 / budget / trace。
3. **Authoring 能力**：给用户提供受规范约束的 vault Skill 创建流程，包括 vault 级别的 `skill-creator` Skill（走主线 `WikiJob` 写入）和 CLI 级别的 `openwhisker skill lint`（read-only 校验）。

Phase 6 不改变只读 vault 边界。所有 agent runtime 增量都在 read-only 工具范围内；任何写 vault 的能力（包括"新增 vault Skill"本身）必须走主线 `WikiJob → VaultPlan → Policy → Approval → VaultExecutor`。

## 背景

Phase 5 第一版让 Scheduler 可以按 cron 触发 vault-local Skill，但 LLM 推理层有两个结构性限制：

- **数据收集是 Host 侧的静态白名单**。`SCHEDULE.md.vault_context` 必须预先列出具体路径，`external_info_sources` 在 schedule load 时就锁定，Host 在调用 engine 前已经把所有声明的内容拉满拼成一份 prompt blob。LLM 没有"决定要看什么"的能力。
- **当 vault 数据面不可枚举时模型崩塌**。典型场景：用户问"我最近笔记里有没有讲过 X" 或 "按当前 vault 状态生成简报或雷达"，都无法把整个 vault 列进 `vault_context`。要么爆 context，要么只能粗暴选一小段静态目录，丢失语义命中能力。

同时，Phase 5 把 LLM 推理只挂在 scheduler 路径上，没有"用户主动发起一次 vault 查询"的入口。而 Knowledge Bot 已经有 hybrid intent router（规则 + LLM 分类，覆盖 raw/organize/diff/approve/reject 五类意图），缺一个 `vault_query` 类别接入 agent runtime。

最后，随着字段增加（capabilities / vault_context / external_info_sources / skill_config / delivery / cron），`SCHEDULE.md` 手写难度上升，registry loader 错误信息只指出"哪里 load 失败"，对用户写新 Skill 体验不友好。没有 schema、没有脚手架、没有 lint。

Phase 6 同时解决"LLM 自主收集信息"、"用户主动发起 vault 查询"和"用户创建规范化 Skill"三件事，因为它们共享一个前提：**必须先为 vault Skill 定义机器可读的 schema 并把 agent runtime 解耦为通用组件**，否则 tool-calling 工具白名单、多 host 接入和 lint 都没有基准。

## 核心结论

Phase 6 引入的能力：

- 在现有 `SkillEngine` 接口下新增 `ToolCallingEngine` 实现，与 `StaticSkillEngine` / OpenAI-compatible single-turn engine 并列；
- 引入通用 `AgentRunner` 抽象，承接 ToolCallingEngine 调度 + budget + trace + sanitize；scheduler / matrix bot / CLI 三个 host 共用同一个 runner；
- 暴露五个 read-only vault 工具给 LLM：`list_vault_dir`、`read_vault_note`、`vault_outlinks`、`vault_backlinks`、`vault_text_search`，加上一个内置终止工具 `submit_result`；
- 引入 vault 链接图索引子系统，由 daemon 启动时全量构建并通过 fsnotify 增量维护；
- 引入运行时 budget 机制：单 run 最大 tool call 次数、累计返回字节数、wall clock 上限；
- 引入 tool call trace：把 `scheduler_runs` 重命名为 `agent_runs` 并新增 `tool_trace_json` + `trigger_kind` 两列；LLM 完整 message 序列仅在 `--debug` 模式下写入磁盘 ndjson；
- 在 Matrix Bot 现有 intent router 中加入 `vault_query` 意图分支：当用户消息以 `@skill_id` 前缀显式 mention 一个 vault Skill 时，dispatch 到 AgentRunner；
- 新增 CLI `openwhisker ask --skill <id> "<query>"`：本地终端发起一次 ad-hoc query，结果走 stdout，trace 默认落 `agent_runs` 表；
- 在 vault `Skills/` 下新增 `skill-creator` Skill，用户手动调用，通过主线 `WikiJob` 写入新的 `SKILL.md`（必填）和 `SCHEDULE.md`（可选，仅当需要 cron 触发时）；
- 新增 `openwhisker skill lint` CLI，read-only 校验 vault Skill 目录是否符合 schema 和 profile 边界。

Phase 6 明确不引入的能力：

- 不引入 langgraph 或任何外部 Python reasoning 框架；
- 不让 agent runtime 获得任何 vault 写工具；
- 不让 scheduler 自动 propose 新 Skill / 新 schedule；
- 不让 ad-hoc query 路径成为新的 vault 写入入口（即使是 medium-risk approval 链路也不复用，agent runtime 永远 read-only）；
- 不在第一版引入 wikilink alias 解析、嵌入式向量检索或反向链接 SQLite 持久化；
- 不引入工具调用的并发执行，每个 run 内工具调用严格串行；
- 不引入 Matrix Bot 的"intent classifier 自动推断该走哪个 Skill"机制，第一版强制 @skill_id 显式指定。

## 不可破的边界

下列声明是 Phase 6 实现必须满足的硬约束：

```text
1.  Agent runtime 的工具集永远不包含 vault 写工具。
2.  Agent runtime 不得创建、修改、删除、移动任何 vault 文件，无论触发来源是 scheduler / matrix / cli。
3.  Agent runtime 不得 propose 新的 SKILL.md / SCHEDULE.md。
4.  新 vault Skill 只能通过 vault 主线 WikiJob 路径创建，由用户显式调用 skill-creator 或手动编辑触发。
5.  Tool call 的每次访问都必须在 Go 侧重新校验 path 在 vault_scope 内，校验失败即工具调用失败。
6.  LLM 不得通过 tool 间接获得任意 HTTP / 任意 shell / 任意文件读写能力。
7.  ToolCallingEngine 的工具集是编译期闭集（5 vault 工具 + 1 内置 submit_result），第一版不提供 plugin / dynamic registration / Skill 自带 tool 实现机制。
8.  Trace 中所有从 vault 内容派生的字段（摘要片段、错误信息、参数回显）必须经过与 outbox payload 同等的脱敏规则。
9.  vault_scope 校验在任何情况下都拒绝以 .obsidian/、.git/、.trash/、.DS_Store 为前缀的路径，即使用户在 VaultProfile.scheduler.read_only_vault_roots 中手滑写入这些路径，registry loader 也应拒绝并返回明确错误。
10. Matrix Bot ad-hoc 路径的 sender 必须通过 Adapter.AllowedSenders / Adapter.IgnoredUserIDs 白名单（与 Phase 5 Knowledge Bot 复用同一份白名单）；CLI 路径只接受本地 stdin 调用。
11. @skill_id 路由必须是显式 mention，不接受 intent classifier 推断；未 mention 任何 Skill 的自由文本继续走 Phase 5 既有 intent 分支，不进入 AgentRunner。
```

这些约束在文档之外还必须在代码层面体现（registry capability 黑名单已存在；ToolCallingEngine 的工具注册表是闭集；fsnotify watcher 不暴露给 LLM；脱敏规则复用 RSS adapter 现有 `sanitizedFeedURLList` / `isSensitiveConfigKey` 思路并抽取为通用 `internal/sanitize/` 包；vault_scope 校验在 `cleanRelativePath` 之上新增黑名单前缀检查）。

**关于手动编辑路径的 by-design 声明**：第 4 条同时承认两条 Skill 变更路径 —— skill-creator 走主线 `WikiJob` approval，用户手动编辑 vault 内 `SKILL.md` / `SCHEDULE.md` 直接生效（agent runtime 在下一次调用 reconcile 加载）。这是有意设计而非缺陷：vault 是用户自治空间，OpenWhisker 不应阻止用户直接编辑自己的文件；skill-creator 的 approval 价值在于"agent 辅助生成时强制人审"，不在于"垄断 Skill 变更路径"。

## 总体架构

```text
                ┌─────────────────────────────┐
                │  vault Agent/Skills/<id>/   │  Skill 是核心组织单元 (Phase 6 新分集)
                │    SKILL.md                 │  必填: 任务描述 / vault_scope / vault_tools / budget / engine
                │    SCHEDULE.md              │  可选: cron / timezone / enabled (仅 cron 触发时需要)
                └──────────┬──────────────────┘
                           │ load + lint
                           ▼
              ┌─────────────────────────┐
              │      AgentRunner         │  通用 agent runtime, 与 host 无关
              │  ┌───────────────────┐   │  - 加载 Skill, 校验 vault_scope / vault_tools / budget
              │  │ ToolCallingEngine │   │  - 调度 ToolCallingEngine 多轮循环
              │  │ + 5 vault tools   │   │  - 收集 trace, 写 agent_runs
              │  │ + submit_result   │   │  - 调用 sanitize 包脱敏 trace
              │  │ + budget enforce  │   │  - 命中 force-finalize / hard-fail / protocol-failed
              │  │ + trace collector │   │    时给出正确 run status
              │  └───────────────────┘   │
              └─┬───────────┬───────────┘
                │           │
       ┌────────┘           │           └────────┐
       ▼                    ▼                    ▼
┌──────────────┐   ┌─────────────────┐   ┌──────────────┐
│  Scheduler   │   │ Matrix Bot      │   │     CLI      │
│  (cron tick) │   │ (@skill_id 自然 │   │ openwhisker  │
│              │   │  语言路由)       │   │ ask --skill  │
│ trigger_kind │   │                 │   │              │
│ = scheduler  │   │ trigger_kind    │   │ trigger_kind │
│              │   │ = adhoc_matrix  │   │ = adhoc_cli  │
└──────────────┘   └─────────────────┘   └──────────────┘
       │                    │                    │
       │ outbox             │ matrix room reply  │ stdout
       ▼                    ▼                    ▼
   既有 outbox          既有 reply helper       终端
```

**关键架构原则**：

- **Skill 是核心组织单元**。`SKILL.md` 是必填的"agent 任务定义"，包括任务描述、prompt 偏好、vault_scope、vault_tools 白名单、budget、engine。`SCHEDULE.md` 降级为**可选的 cron 触发挂载**，仅当用户希望该 Skill 按节律自动执行时才需要。
- **AgentRunner 是 host-agnostic 的 runtime**。它接收 `(skill, query, trigger_context)` 三元组，返回 `AgentRunResult`（含 title / summary / payload / trace）。host 不参与 LLM 协议或工具执行，只负责把 host-specific 的输入翻译成 query 字符串、把结果格式化回 host-specific 的输出通道。
- **trigger_kind 是 first-class metadata**。`agent_runs` 表的每一行都带 trigger 来源，scheduler / matrix / cli 共用同一份审计入口（`openwhisker agent runs <run_id> --trace` 或 vault 内 outbox 摘要）。

## Skill 定义

### vault 分集

Phase 6 agent skill 走独立分集 `Agent/Skills/<skill-id>/`，**不复用**根级 `Skills/` 目录（后者是 Phase 5 之前就存在的"主线 LLM prompt 文档"分集，含 `Skills/vault-raw-organizer/` 等，不带 agent runtime frontmatter，本 Phase 不改动这些）。

```text
<vault-root>/Agent/Skills/<skill-id>/
  SKILL.md      (必填)
  SCHEDULE.md   (可选, 仅当需要 cron 触发)
  README.md     (可选, 人类向说明)
```

**与 Phase 5 `Scheduler/Skills/` 的关系**：

- Phase 5 老分集 `Scheduler/Skills/<id>/SCHEDULE.md` 是本分集的前身；Phase 6 升级语义后改名为 `Agent/Skills/`，因为 scheduler 已退居为众多触发方式之一；
- registry loader 在 Phase 6 binary 首次运行时**同时扫两个路径**：`Agent/Skills/` 视为主目录，`Scheduler/Skills/` 视为 backwards-compat 加载源并给出 deprecation warning，建议用户手动迁移目录；
- 两个路径下同名 Skill id 冲突时 reject 加载，明确指出冲突的两个目录；
- 用户也可保留两个路径长期共存；deprecation warning 不阻塞加载。

**Skill id 全局唯一**：在 vault 内（含 `Agent/Skills/` 和兼容期的 `Scheduler/Skills/`）所有 SKILL.md 的 `id` 全局唯一。registry loader 启动加载时扫整个 Skill 树收集 id，发现重复立即 reject（明确指出冲突的两个目录），不容忍带歧义的 `@skill-id` 路由。`openwhisker skill lint` 在加载 vault 上下文时也做同样检测。

> 设计权衡：选目录分集而非 frontmatter `kind` 字段，是为了与 Phase 5 既有的 `Scheduler/Skills/` 分集做法对齐；目录本身就是天然的 kind 标签，避免引入 frontmatter 字段后既有 vault `Skills/` 下 Skill 还要补加。

### SKILL.md（必填）

`SKILL.md` 定义 agent 任务本身，与触发方式无关。frontmatter 包含 agent runtime 配置，body 是给 LLM 的任务描述。

```yaml
---
id: vault-qa
name: Vault 知识问答
description: 针对用户提出的问题, 从 vault 抽取相关知识给出回答
engine: tool-calling
capabilities:
  - vault_read
  - vault_link_graph_read
  - vault_text_search
  - agent_run_log_write
vault_tools:
  - list_vault_dir
  - read_vault_note
  - vault_outlinks
  - vault_backlinks
  - vault_text_search
vault_scope:
  - Raw/
  - Knowledge/
  - Interview/
  - Meta/
external_info_sources: []
budget:
  max_tool_calls: 8
  max_total_bytes: 204800
  max_wall_clock_seconds: 60
delivery:
  - matrix     # 用于 cron 触发时的输出通道; ad-hoc 触发时被忽略
  - outbox
skill_config:
  detail: normal
---

# Vault 知识问答

你是 OpenWhisker 的 vault 知识问答 agent。用户会通过 query 提出问题, 你需要:

1. 在 vault_scope 范围内自主搜集相关 note
2. 优先利用 wikilink 网络扩展上下文
3. 给出准确、有出处引用的回答 (引用形如 [[note-title]])
4. 不要编造 vault 内不存在的信息; 如果信息不足, 在 summary 中明说

通过 submit_result 工具收尾, payload 形态:
{ "answer": string, "cited_notes": [string], "confidence": "high" | "medium" | "low" }
```

字段语义：

- `engine`：可选（**建议显式声明**）。可选值 `static` / `openai-compatible` / `tool-calling`。
  - **隐式推断规则**：未显式声明 `engine` 时，registry loader 按 `vault_tools` 推断：非空 → `tool-calling`；为空或缺失 → `static`；
  - **冲突 reject**：显式 `engine: static`（或 `openai-compatible`）+ 非空 `vault_tools` 视为配置冲突，registry loader 拒绝加载并给出可读错误；
  - **CLI flag 适用范围**：daemon flag `--scheduler-engine` 仅在 SKILL.md / Phase 5 SCHEDULE.md 都没声明 `engine` 且也没能从 `vault_tools` 推断时作为兜底；Phase 6 主流路径（agent skill 必带 vault_tools）下该 flag 实际不参与决策，保留是为 Phase 5 老 SCHEDULE.md 兼容；
  - **backwards-compat**：Phase 5 老 Skill 的 SCHEDULE.md 中既无 `engine` 又无 `vault_tools` 时，视为 `engine: static`，行为保持 Phase 5 不变，零回归。
- `vault_tools`：tool name 字符串数组，必须是上面五工具子集；声明 `tool-calling` engine 时必填且非空；registry loader 加载时按字典序规范化存储，避免顺序差异产生不同 `registry_hash`。
- `vault_scope`：根级路径数组，必须落在 `VaultProfile.scheduler.read_only_vault_roots` 内（profile 字段名沿用 Phase 5）。每次 tool call 的 path 都按此校验；同样按字典序规范化。
- `budget`：可选，缺省按全局默认值；任何字段不得超过全局上限（避免 Skill 自抬权限）；任一字段必须 > 0，`0` 视为非法值，registry loader 拒绝加载。
- `delivery`：仅在 cron 触发场景下消费（决定 scheduler tick 完成后把结果丢到 outbox 还是直接发 matrix）；ad-hoc 触发时被忽略（matrix 路径直接 reply，CLI 路径直接 stdout）。
- `external_info_sources`：与 Phase 5 语义不变，可与 `vault_tools` 并存；**第一版处理方式**：AgentRunner 启动 run 时仍按 Phase 5 行为预拉 `external_info_sources`，作为 initial prompt 的一部分注入；不把 RSS 等包装成 tool 暴露给 LLM；后续如需"LLM 决定何时拉 feed"形态，再讨论 `fetch_external_feed` 工具化设计。

### SCHEDULE.md（可选）

仅当该 Skill 需要按 cron 自动触发时才需要。所有 agent runtime 字段（engine / vault_tools / vault_scope / budget / capabilities）**都从同目录 SKILL.md 继承**，`SCHEDULE.md` 只保留触发相关字段：

```yaml
---
id: daily-vault-radar          # 必须等于父目录的 skill id
enabled: true
cron_expr: "0 9 * * *"
timezone: Asia/Shanghai
---
```

字段语义：

- `id`：必须与同目录 SKILL.md 的 id 一致，registry loader 校验；
- `enabled`：开关 cron 触发；为 false 时 scheduler 跳过，但 ad-hoc 触发仍可用；
- `cron_expr` / `timezone`：与 Phase 5 语义不变。

**SCHEDULE.md 不再独立声明 agent runtime 字段**。这意味着同一个 Skill 在 cron / matrix / cli 三种触发下 vault_scope、vault_tools、budget 行为完全一致，避免"cron 触发能看的范围比 matrix 触发更大"这种隐式权限差异。

### JSON Schema

正式 schema 文件以 JSON Schema 形式存放，registry loader 和 `openwhisker skill lint` 共享同一份 schema：

```text
docs/phases/phase-6-scheduler-skill-creator/skill-schema.json
docs/phases/phase-6-scheduler-skill-creator/schedule-schema.json
```

## 触发路径

### 路径 A：Matrix Bot（@skill_id 自然语言）

Matrix Bot 在现有 intent router（`internal/core/intent_router.go`）的规则匹配前置阶段加一条优先级最高的检测：

```text
if message starts with "@<skill-id> " (or just "@<skill-id>" + newline):
    intent = "vault_query"
    skill_id = parsed
    query = remainder of message (everything after @<skill-id>)
    dispatch to AgentRunner with trigger_kind = adhoc_matrix
    return  // 不进入后续 hybrid intent classifier
```

如果消息没有以 `@skill-id` 前缀开头，**完全不进入 vault_query 分支**（硬约束 #11）。这样 Phase 5 的 raw/organize/diff/approve/reject 意图行为零回归。

- `<skill-id>` 必须是 vault 内 `Agent/Skills/<skill-id>/SKILL.md`（或兼容期 `Scheduler/Skills/<skill-id>/SKILL.md`）存在且未禁用的 Skill；不存在时 bot 回复"未找到 Skill: <skill-id>，可用 Skill 列表: ..."；
- query 长度上限：单条 Matrix 消息原生上限即可；不额外截断；
- 输出：AgentRunner 完成后，bot 走既有 reply helper 直接回到原 room；`AgentRunResult.payload` 按 Skill 定义的形态格式化（默认 `summary + cited_notes` 拼成 Markdown）；长 summary 沿用 Phase 5 既有 reply helper 的长度限制行为（超长截断并附 `[trace: <run_id>]` 引用）；
- 失败情况：vault_scope 越界 / budget 超限 / protocol_failed 都按"agent 执行失败"回帖一条简短错误描述（trace 落 SQLite，详情走 `openwhisker agent runs --trace`）；
- 权限：sender 必须通过现有 `Adapter.AllowedSenders` / `Adapter.IgnoredUserIDs` 白名单（硬约束 #10）；不引入 per-skill ACL；
- 去重：matrix event_id 复用现有去重逻辑，**不做 query 内容去重**；用户连续发两条同内容 `@<skill-id> <query>` 会跑两次 agent run 并产生两条 trace；
- **`@<skill-id>` 与 Matrix 原生 user mention 的关系**：`@<skill-id>` 是 bot 侧的纯文本约定，**不依赖** Matrix mention 协议。部分 Matrix client（如 Element）在用户输入 `@xxx` 时可能尝试自动补全为 user mention（mxid 形如 `@user:server`）；如果用户接受了补全，bot 收到的文本会变成 `@user:server` 形式，bot 视为"未指定 Skill"按非 vault_query 分支处理。**用户教育**：建议在 vault `Agent/Skills/skill-creator/SKILL.md` 文档里说明"输入 `@skill-id` 时按 ESC 或不接受 client 补全建议"；如未来 UI 重合问题明显影响体验，再 Phase 7+ 评估换语法（如 `!<skill-id>` 或 slash 命令）。

### 路径 B：CLI（`openwhisker ask`）

```text
openwhisker ask --skill <skill-id> "<query>" [--json] [--debug]
```

- `--skill <id>` 必填；
- `<query>` 是位置参数，整条 query 字符串；
- 默认输出：`AgentRunResult.summary` 走 stdout，结尾追加一行 `[trace: <run_id>]` 方便查；
- `--json` 输出完整 `AgentRunResult`（含 trace 引用、cited_notes、payload）；
- `--debug` 启用 `--debug` 模式（LLM 完整 message 序列落 `data/agent-debug/<run_id>.ndjson`）；
- 输入只接受命令行参数 + 本地 stdin，不开 HTTP 端口（硬约束 #10）；
- CLI 路径下的链接图索引采用同步现建 ad-hoc 模式（见「链接图索引子系统」节软门槛保护）。

### 路径 C：Scheduler（cron）

Phase 5 既有路径基本不变，只是 SchedulerService 改为通过 AgentRunner 调度：

```text
scheduler tick (existing Phase 5 flow)
  -> load SCHEDULE.md (Phase 6 schema, 仅触发字段)
  -> load 同目录 SKILL.md (Phase 6 schema, agent runtime 字段)
  -> 通过 AgentRunner.Run(skill, query="", trigger_kind=scheduler)
     - query 为空字符串时, AgentRunner 不向 LLM 注入 "user query" 段, 仅用 SKILL.md 任务描述
  -> AgentRunner 返回 AgentRunResult
  -> SchedulerService 按 Skill.delivery 字段决定走 outbox / matrix
```

scheduler 路径与 Phase 5 的差异：

- 不再从 `SCHEDULE.md` 读 vault_context（字段移除）；
- 不再从 `SCHEDULE.md` 读 capabilities / vault_tools / vault_scope / budget；这些都从同目录 SKILL.md 读；
- `engine` 选择优先级：SKILL.md.engine ?? CLI `--scheduler-engine` flag ?? VaultProfile.scheduler.default_engine ?? 编译期默认（static）；
- 如果选中的 engine 不可用（例如 tool-calling 需要 LLM API key 缺失），run status = failed，outbox surfaces explicit error，**不静默降级到 static engine**。

## ToolCallingEngine

新增 `SkillEngine` 实现，与 `StaticSkillEngine` / OpenAI-compatible engine 并列。原有两个 engine 行为完全不变，避免回归。

底层协议使用 OpenAI Chat Completions 的 `tools` / `tool_choice` / `tool_calls` 标准格式。Phase 6 第一版目标 provider：

- 主要：`deepseek-chat`（与 v1 主线对齐）；
- 备选：任何 OpenAI-compatible function calling 兼容 provider；
- `deepseek-reasoner` 是否支持 function calling 未在官方文档明确，视为 OPEN 项；
- ToolCallingEngine 主体 + 5 工具 + 链接图索引完整落地后，在真实 vault 上跑 OPEN-1 多轮稳定性 demo（≥9/10 通过为 merge 硬门槛）；不达标必须调整 prompt / 协议 / 必要时接 fallback provider 后重跑，详见「待决问题 OPEN-1」。

引擎与 Host 的接口扩展：

```go
// 概念草稿, 实际 struct 字段名 / tag 在实现时定。
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

// AgentExecutionRequest 是 Phase 5 SkillExecutionRequest 的扩展:
//   Skill             *VaultSkill      // 来自 SKILL.md
//   Query             string           // ad-hoc 触发时为用户原文; scheduler 触发时为 ""
//   TriggerKind       string           // "scheduler" | "adhoc_matrix" | "adhoc_cli"
//   ToolCatalog       []ToolDescriptor
//   Budget            BudgetLimits
//   LinkIndexProvider LinkIndexReader
//   ToolExecutor      ToolExecutor      // Host 提供, engine 只用不实现
//
// 返回类型 AgentRunResult (title / summary / payload + trace ref)。
// trace 由 AgentRunner 在 engine 返回后从 ToolCallRecorder 提取, 写入 agent_runs。
```

工具的执行实体不在 engine 内，而是 AgentRunner 提供的受信函数集；engine 只负责协议层（解析 tool_call、回填 tool_result、循环至终止条件）。这样 capability 校验、size limit、trace 记录全部留在 Go Host 侧，engine 无法绕过。

### System prompt 模板

第一版定义固定的 prompt 拼装模板，避免每个 Skill 用户自己拼出风格各异的 prompt：

```text
[system message]
You are running as vault Skill "{skill.id}" inside OpenWhisker Agent Runtime.
Trigger kind: {trigger_kind}
Skill definition (verbatim from SKILL.md body):

<SKILL.md body content, frontmatter stripped>

Vault scope (the only paths you may access via tools):
- {vault_scope[0]}
- {vault_scope[1]}
...

Available tools: {tool_name_list}.

To finalize, call the submit_result tool with title/summary/payload. Do not produce
a final natural-language message; the only way to terminate is submit_result.

[budget] tool_calls_left=N, bytes_left=M, wall_clock_left=Ts

[user message]
{user query, if any; otherwise "Now produce the scheduled output as defined by the skill."}
```

每轮 LLM 调用前 Host 在 system message 末尾刷新 `[budget]` 行。

### 终止协议：submit_result tool

LLM 不通过"返回不带 tool_call 的最终消息"来终止，而是**必须调用一个名为 `submit_result` 的特殊工具**：

```text
submit_result(title: string, summary: string, payload: object)
```

- `submit_result` 由 AgentRunner 注册，永远在 `ToolCatalog` 里，不受 `SKILL.md.vault_tools` 白名单约束（它不访问 vault）；
- LLM 调用 `submit_result` → AgentRunner 提取参数填入 `AgentRunResult` → engine 退出循环 → run 标记 `ok`；
- 强制结构化输出，不依赖 DeepSeek `json_object` 兼容性（DeepSeek 只支持 `json_object`，不支持 `json_schema`）；
- 撞 budget force-finalize 时，AgentRunner 在最后一轮 system message 追加 "you must call submit_result now with what you have"，强制收尾；
- LLM 在未调用 `submit_result` 就吐自由文本时，AgentRunner 视为协议错误，记录 trace，再给一次机会，连续两次仍不合规则 = run failed。

### Message history 策略

第一版**全保留**：每轮 LLM 调用都带完整 message history（含所有历次 tool_call + tool_result）。

- tool_result 已有单次 size limit（`read_vault_note` 128 KiB 上限），总长度受 `max_total_bytes` budget 间接约束；
- 不引入历史压缩 / 摘要化 / 滚动窗口；
- 等真撞 LLM context token 上限或成本问题再上"历史摘要化"，留作后续优化。

## 工具集

第一版固定 5 个工具，对应 5 个 Go 函数。每个工具的 capability 在 SKILL.md `vault_tools` 里按名字白名单声明。

| 工具名 | 入参 | 返回 | 依赖 capability |
| --- | --- | --- | --- |
| `list_vault_dir` | `path: string` | 目录条目列表（文件名 / 子目录名），带 truncated 标记 | `vault_read` |
| `read_vault_note` | `path: string` | 文件全文（截到 size limit），带 truncated 标记 | `vault_read` |
| `vault_outlinks` | `path: string` | 结构化返回 `{wikilinks: [...], frontmatter_related: [...]}` 两类区分；wikilinks 是正文 `[[...]]` 解析结果，frontmatter_related 是 frontmatter `related:` 字段显式声明 | `vault_read` + `vault_link_graph_read` |
| `vault_backlinks` | `path: string` | 反查链接图，返回所有链入该 note 的 source 路径列表 | `vault_link_graph_read` |
| `vault_text_search` | `query: string, scope_subset?: []string` | ripgrep 文本搜索，每项返回 path + 单行匹配片段 | `vault_read` + `vault_text_search` |

实现方式约束：

- 五个工具的实现**全部为 Go 内函数**，不依赖 `obsidian-cli` 或任何外部 CLI subprocess；
- 这条约束的理由：agent runtime 既可能在 daemon 24/7 后台跑（scheduler tick），也可能在 Matrix Bot poll 时跑、CLI 单次跑，全程不能假设 Obsidian 桌面 app 在前台；obsidian-cli 多数实现需要 Obsidian app 在跑且自身带 vault 写 + JS 执行能力，与 agent runtime 的 read-only + 闭集硬约束冲突；
- obsidian-cli 在 Phase 6 的角色限定在 skill-creator 路径上，见后文 `skill-creator` 章节。

调用约束：

- 所有路径参数在 Go 侧调用 `cleanRelativePath` + `IsForbiddenScopePath` + `isUnderAnyRoot(path, scope)` 重新校验；
- `read_vault_note` 使用 **tool-side 独立上限**：默认 128 KiB，可通过 daemon flag `--scheduler-tool-read-bytes` 调整（flag 名沿用 scheduler 前缀避免引入新前缀；语义实际作用于 agent runtime），硬上限 256 KiB；超过则截断并标记 truncated。Phase 5 的 `maxVaultContextFileBytes = 20 KiB` 是为已移除的静态 `vault_context` 路径设计，Phase 6 移除该字段后视为 dead constant；
- `list_vault_dir` 复用 `maxVaultContextEntries = 50` 上限；
- `vault_text_search` 单次返回上限：max 50 命中，每命中只回 1 行上下文片段；`scope_subset` 可选参数必须是 `SKILL.md.vault_scope` 的真子集，校验失败即工具调用失败；整体响应 size 受全局 budget 约束；
- `vault_text_search` 第一版固定 **literal 匹配 + case-insensitive**，不暴露 regex / case 开关；`query` 最小长度 2 字符（防 LLM 单字符全 vault 扫底烧 budget）；
- `vault_outlinks` 第一版只解析精确路径和文件名命中，**不解析 Obsidian alias**。无法解析的 wikilink 在返回中标记 `unresolved=true`，列出原始文本。这是已知限制，留待后续扩展；
- `vault_outlinks` 返回值显式区分 `wikilinks` 和 `frontmatter_related` 两个数组，让 LLM 能区分"作者显式声明的强关联"和"正文偶发提及"；
- `vault_backlinks` 完全依赖内存链接图索引，索引尚未建好时返回明确 error，提示 LLM 退化到 `vault_text_search`。

新增 capability 名（registry loader 允许列表扩展）：

```text
allowed (新增):
  - vault_link_graph_read
  - vault_text_search
  - agent_run_log_write   # 替换 Phase 5 的 scheduler_run_log_write 同义词; 二者都允许以兼容旧 SCHEDULE.md
```

`vault_write`、`vault_executor_apply`、`auto_approve`、`external_api_side_effect`、`arbitrary_shell`、`arbitrary_http`、`arbitrary_file_write` 维持禁用。

## Budget

三个独立上限，任一先撞到即触发"超限"动作：

| 维度 | 默认值（初始估算） | 行为 |
| --- | --- | --- |
| `max_tool_calls` | 8 | force finalize |
| `max_total_bytes` | 200 KiB | force finalize |
| `max_wall_clock_seconds` | 60 | hard fail |

每轮 LLM 调用前，AgentRunner 在 system message 里 append 当前剩余预算快照：

```text
[budget] tool_calls_left=5, bytes_left=148 KiB, wall_clock_left=42s
```

让 LLM 自主决定何时收尾。撞 call 数或 bytes 上限时进入 force finalize：禁止再发普通 tool_call（`submit_result` 仍允许），system message 追加 "you must call submit_result now"，LLM 收尾后结果标记 `status=partial`，trace 记录终止原因。

撞 wall clock 时走 hard fail：AgentRunner 通过 `context.Deadline` 在工具执行和 LLM 调用两侧同时强制 cancel，run 结果标记 `status=failed`，输出通道（outbox / matrix reply / stdout）报错。Wall clock 检查发生在 (a) 每轮 LLM 调用前、(b) 每次工具执行 ctx 内、(c) HTTP client timeout 三处。

**Run status 枚举**（全局口径）：

- `ok`：natural 终止，LLM 通过 `submit_result` 正常收尾；
- `partial`：force finalize 后 LLM 通过 `submit_result` 收尾，但 trace 记录撞了 call 数或 bytes 上限；
- `failed`：wall clock 超限 / protocol_failed / 工具执行错误未恢复 / engine 不可用等任何无法产出有效 AgentRunResult 的情况；
- `skipped`：Phase 5 既有语义（同 schedule 已有 running run 时本次 tick 跳过）；ad-hoc 触发路径不会产生 `skipped`。

**与 Phase 5 SQLite 字段值的关系**：`ok` 是本 doc / CLI 输出层的别名，SQLite `agent_runs.status` 字段值**沿用 Phase 5 的 `done`** 表示"自然终止成功"，**不引入 status 值 migration**。新增 `partial` 是真正的新枚举值，需要在 `model.SchedulerRunStatus*` 常量加一项；`failed` / `skipped` 继续复用 Phase 5 既有值。CLI / outbox 在渲染时把 `done` 显示为 `ok`，trace 内部以原值入库便于审计。

全局默认值通过 daemon flag 配置（命名统一前缀 `--scheduler-budget-`，沿用 Phase 5 命名空间）：

```text
--scheduler-budget-tool-calls     (默认 8)
--scheduler-budget-bytes          (默认 204800)
--scheduler-budget-wall-clock     (默认 60s)
```

SKILL.md `budget` 字段可在全局上限内向下覆盖，不允许向上越权（registry loader 加载时校验，超限即 reject）。

## Trace

数据库层：把 Phase 5 的 `scheduler_runs` 重命名为 `agent_runs`，并新增两列。

```text
-- migration (向后兼容, 详见「数据模型变更」节):
ALTER TABLE scheduler_runs RENAME TO agent_runs;
ALTER TABLE agent_runs ADD COLUMN tool_trace_json TEXT;
ALTER TABLE agent_runs ADD COLUMN trigger_kind TEXT NOT NULL DEFAULT 'scheduler';
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

脱敏实现：抽取 RSS adapter 现有 `sanitizedFeedURLList` / `isSensitiveConfigKey` / `SanitizedSkillConfigJSON` 思路为通用 `internal/sanitize/` 包，扩展 `SanitizeFreeText(s string) string` 覆盖私有主机名 / 邮箱 / token / Matrix ID / 内部命名空间。RSS adapter 改为 thin wrapper 调用 sanitize 包。

LLM 完整 message 仅在 `--debug` 模式下落盘：

```text
data/agent-debug/<run_id>.ndjson
```

debug 模式由 `openwhisker scheduler tick --debug` / `openwhisker ask --debug` / daemon flag 开启。生产部署默认关闭。

CLI 查看入口：

```text
openwhisker agent runs <run_id> --trace
openwhisker agent runs list [--trigger-kind scheduler|adhoc_matrix|adhoc_cli]
```

输出工具调用时间线（seq / tool / 关键参数摘要 / 大小 / 截断 / 剩余预算）。

**Phase 5 命令兼容**：`openwhisker scheduler runs ...` 命令保留为 `openwhisker agent runs --trigger-kind scheduler ...` 的语法糖，避免脚本回归。

Matrix 不在通知里默认推 trace，可加 `@<skill-id> /trace <run_id>` 按需查。outbox 默认摘要只显示终止原因和计数（例如 "扫描 12 个 note，命中 3 项相关内容；终止：natural"）。计数由 AgentRunner 在 finalize 时从 trace 中统计 `list_vault_dir` / `read_vault_note` / `vault_outlinks` / `vault_backlinks` / `vault_text_search` 的调用次数与命中数得到，不需要 LLM 自己报告。

## 链接图索引子系统

新增内部子系统 `internal/vault/linkindex/`，daemon 进程内单例。

**归属层声明**：链接图索引位于通用 `internal/vault/` 命名空间下，**不是 scheduler 私有子系统**。Phase 6 中 AgentRunner 是首个消费者；后续 WikiJob plan / 主线 organize 路径 / Knowledge Expander 等也可复用同一索引接口（例如 plan 阶段判断"新增 note 会破坏哪些已有 backlink"）。Phase 6 不实现这些消费方，但**接口设计应预留**给非 agent runtime 的调用方。

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

- daemon 启动时**异步**全量扫描 vault root（排除 `.obsidian/`、`.git/`、`.trash/`、`.DS_Store`、隐藏目录），解析所有 `.md` 的 wikilink 和 frontmatter `related:`；daemon 启动**不阻塞**等待索引就绪；
- 异步构建期间：daemon 其他能力（Matrix poll / scheduler tick / outbox 投递）正常运行；ToolCallingEngine 仍可启动；只有 `vault_outlinks` / `vault_backlinks` 两个依赖索引的工具在未就绪期间返回明确 `link_index_not_ready` error，提示 LLM 退化到 `vault_text_search`；其他三个工具（`list_vault_dir` / `read_vault_note` / `vault_text_search`）不受索引就绪状态影响；
- 索引首次就绪后 daemon 在 stderr / 日志记录一条 `link_index ready: N notes, M links, elapsed Xs`，便于运维确认；
- 全量扫描完成后启动 fsnotify watcher 监听 vault 文件变化：create / modify → 重解析该文件并更新 outlinks 和反向 backlinks；delete → 移除条目；rename → delete + create；
- 不跟随指向 vault root 外的 symlink（防止 LLM 间接读 vault 外文件）；vault 内部 symlink 跟随但解析后的 path 必须仍在 vault root 内；
- watcher 事件做 debounce（默认 200ms）合并连续保存，处理编辑器常见的 "tmp file → rename" 原子保存模式；
- 索引坏掉或事件溢出时（macOS kqueue FD 上限触发），daemon 记录 stderr error，下次 tick 前触发全量重建（同样异步），重建期间 `vault_backlinks` 仍返回 not_ready error；
- 内存预期：典型 `.md` 平均 5-20 个 link，10k notes 量级时 LinkIndex 占用应在数十 MB 量级；超过 50k notes 视为未验证范围，需要补充实测。

Snapshot 语义：

- 单 run 内的 tool call 不持有索引 snapshot，每次调用读最新索引（最终一致）；
- 每次 tool call 在 trace 中记录读取时的 `link_index_version`，让审计能判断 run 是否跨越了索引更新；
- 不引入 per-run snapshot 复制（vault 规模下成本和复杂度都不划算）。

索引不落 SQLite：

- daemon 重启 = 全量重建，可接受（典型 vault 几秒内重建完成）；
- 持久化引入一致性问题且收益有限。

CLI tick / CLI ask 进程的索引行为：

- `openwhisker scheduler tick` 和 `openwhisker ask` 都是与 daemon 完全独立的进程，仅共享 SQLite，不通过 IPC 访问 daemon 内存中的链接图；
- 这两类 CLI 进程启动时**同步**构建一次 ad-hoc 链接图索引，本次进程内使用，进程退出即丢弃；
- CLI 进程**不启动 fsnotify watcher**（无意义，进程是一次性的）；
- 软门槛：当 vault `.md` 文件数 > `5000` 或全量扫描时间 > `10s` 时，CLI 进程拒绝执行并提示用户加 `--force-build-index` 显式确认，避免在大 vault 上意外承担长延迟；
- 软门槛阈值通过 daemon flag 可调（`--cli-index-file-warn` / `--cli-index-time-warn`）；
- 这意味着 CLI 路径在 tool-calling engine 下的启动延迟显著高于 daemon 内执行；这是 by design，CLI 是调试/手动验证手段，不是 Skill 的主执行入口。

## skill-creator（vault 级）

位置：

```text
<vault-root>/Agent/Skills/skill-creator/
  SKILL.md
  README.md（可选）
```

放在 Phase 6 新分集 `Agent/Skills/` 下，与其他 agent skill 同级。注意它**不**与现有 `Skills/vault-raw-organizer/` / `Skills/vault-knowledge-expander/` 同级 —— 那两个属于 Phase 5 之前就存在的"主线 LLM prompt 文档"分集，Phase 6 不动它们的位置。

调用路径：

- 用户通过 Knowledge Bot `@skill-creator` 自然语言路径触发（自身也走 Matrix Bot 路径 A），或 `openwhisker ask --skill skill-creator` 触发；
- **不通过 scheduler 触发**（硬约束 #3，scheduler 不得创建任何新 Skill）；
- 输出形态：一份 `VaultPlan`，包含一个新文件 `Agent/Skills/<new-skill-id>/SKILL.md`（必填），可选追加 `Agent/Skills/<new-skill-id>/SCHEDULE.md`（仅当用户希望 cron 触发时）；
- 经 Policy / Diff / Risk 检查后进入 approval 流程；
- 因为是"新增 agent 自动化行为"，**默认风险等级 medium**，必须用户显式 approve；
- approve 后 `VaultExecutor` 写入文件，下次 AgentRunner 调用 reconcile 时加载新 Skill；
- **风险自动升级到 high（产 proposal note 而非直接 approval）的触发条件**：(a) `vault_scope` 覆盖根超过 3 个或包含整个 vault；(b) `budget` 任一字段超过全局默认值的 50%；(c) `external_info_sources` 引入新的尚未使用过的源类型；(d) 同时创建 SCHEDULE.md 且 cron 频率高于每小时一次。满足任一条 → high-risk，走 proposal note 路径让用户先在 vault 里讨论再 approve。

skill-creator 自身的 prompt 应：

- 询问用户意图（任务描述 / 是否需要 cron 触发 / 需要的 vault 区域 / 需要的工具 / 输出形态）；
- 区分"只接受 ad-hoc 触发"和"同时接受 cron 触发"两种创建模式（前者只生成 SKILL.md，后者额外生成 SCHEDULE.md）；
- 生成符合 schema 的 `SKILL.md`（engine / capabilities / vault_tools / vault_scope / budget / 任务描述 body）；
- 生成 `SCHEDULE.md`（如需，仅 id / enabled / cron_expr / timezone）；
- 在 `VaultPlan` 提交前调用 `openwhisker skill lint` 做最后一次自检；
- 如果 lint 不通过，进入修订循环而不是直接 propose；
- propose 前给用户展示 dry-run preview：将要创建的文件路径、frontmatter 摘要、预期下次触发时间（如有 cron）、风险等级、升级原因（若有），让用户能在不读全文的情况下判断是否 approve。

**obsidian-cli 的有限角色**：

skill-creator 路径**允许**调用 `obsidian-cli` 完成辅助任务，因为该路径是用户主动触发、运行时 Obsidian 桌面 app 几乎必然在前台。允许的辅助场景：

- 探查用户现有 vault 结构（已有哪些 zone、哪些 Skill），辅助 prompt 生成时给出合理默认值；
- 校验新生成的 `SKILL.md` / `SCHEDULE.md` 在 Obsidian 中能正确渲染（wikilink 是否解析、frontmatter 是否被识别）；
- approve 后在 Obsidian 中跳转打开新建的 Skill 目录给用户确认。

**禁止**用 obsidian-cli 执行以下任何动作：

- 任何 vault 写入 / 创建 / 修改（写入仍由 `VaultExecutor` 完成，唯一写入路径）；
- 任何 JS 执行 / plugin reload / 任意命令；
- 任何 agent runtime 路径上的调用（agent runtime 永远不依赖 obsidian-cli）。

skill-creator 的核心生成与校验逻辑（schema 套用、frontmatter 拼装、lint 调用）仍是 Go 实现，obsidian-cli 只是可选的辅助层；obsidian-cli 不可用时 skill-creator 应能降级运行，不阻塞主流程。

skill-creator 的详细设计文档放在 vault 内，不在本 phase doc 展开：

```text
<vault-root>/Agent/Skills/skill-creator/SKILL.md
```

本 phase doc 仅声明它的归属、调用边界和与 `openwhisker skill lint` 的协作关系。

## openwhisker skill lint

CLI read-only 校验工具：

```text
openwhisker skill lint <path>
```

`<path>` 是一个 vault Skill 目录、单个 `SKILL.md` 文件、或单个 `SCHEDULE.md` 文件。当传入目录时，会同时校验目录内的 SKILL.md（必填）和 SCHEDULE.md（如存在）。

校验项：

**SKILL.md**：

- frontmatter 是否合 JSON Schema；
- `id` 是否与已有 Skill 冲突；
- `capabilities` 是否在 Phase 6 允许列表内（无禁用项）；
- `vault_tools` 是否在固定五工具集内，`engine` 与 `vault_tools` 是否一致；
- `vault_scope` 是否落在当前 `VaultProfile.scheduler.read_only_vault_roots` 内，且不包含被禁前缀（`.obsidian/` 等）；
- `budget` 各字段是否未超全局上限且都 > 0；
- body 是否非空、至少包含一个 `# heading` 用于 outbox 标题回退。

**SCHEDULE.md**（如存在）：

- frontmatter 是否合 JSON Schema；
- `id` 是否与同目录 SKILL.md 的 id 一致；
- `cron_expr` 是否合法；
- `timezone` 是否可解析；
- **Phase 6 新引入字段**（`vault_tools` / `vault_scope` / `budget` / `engine`）出现在 SCHEDULE.md → **error**（这些字段从未在 Phase 5 SCHEDULE.md 出现过，写在 SCHEDULE.md 就是位置错；lint 给修复建议"迁到同目录 SKILL.md"）；
- **Phase 5 旧字段**（`capabilities` / `external_info_sources` / `skill_config` / `delivery`）出现在 SCHEDULE.md → **deprecation warning**（向后兼容加载，但建议迁到 SKILL.md 统一管理）；
- Phase 5 移除字段 `vault_context` → **error**（字段已彻底移除）。

`--strict` 模式追加（warning 不 error）：

- `id` 是否仅包含 ASCII 小写字母、数字、连字符（推荐风格）；
- `cron_expr` 是否在 daemon 重启常见时段（如 `0 0 * * *`）触发，可能与重启窗口冲突；
- `vault_scope` 是否覆盖整个 vault（高风险但合法）；
- `external_info_sources` 是否含未在 profile 显式启用过的 source 类型。

错误输出格式应**指出文件名、行号、字段名和可读修复建议**，而不是只 "load failed"。

退出码：`0` = 全部通过；`1` = 至少一个 error；`2` = 仅 warning（仅 `--strict` 模式可能返回）。

lint 不写任何东西，可在任何环境运行。skill-creator 在 propose 前会调用它（默认非 `--strict`，避免风格 warning 阻塞 propose）。

## 数据模型变更

```sql
-- Phase 6 migration (daemon 首次启动 Phase 6 binary 时执行)
ALTER TABLE scheduler_runs RENAME TO agent_runs;
ALTER TABLE agent_runs ADD COLUMN tool_trace_json TEXT;
ALTER TABLE agent_runs ADD COLUMN trigger_kind TEXT NOT NULL DEFAULT 'scheduler';
```

`scheduler_runtime` 表保持不变（仍然只承载 cron 触发态，ad-hoc 触发无需 runtime 表）。

**迁移策略**：

- daemon 首次启动 Phase 6 binary 时，schema migration 检查表名 `scheduler_runs` 是否存在：
  - 存在 → 执行 RENAME 序列；
  - 不存在 → 直接 `CREATE TABLE agent_runs (...)`；
- 无 down migration（向 Phase 5 binary 回退时 `agent_runs` 不被识别，scheduler 会报错；这是有意的，避免静默回退导致 trace 列丢失）；
- Phase 5 已写入的 `scheduler_runs` 行经 RENAME 后全部携带 `trigger_kind = 'scheduler'`、`tool_trace_json = NULL`，CLI `--trace` 显示 "no trace (legacy or non-tool-calling run)"。

**SQL 调用方批量改名**：所有 store 层的 `scheduler_runs` 字面量必须改为 `agent_runs`；store 接口方法可保留 `SchedulerRun` 命名以减少调用方改动，但加 `TriggerKind` 字段。

**Phase 5 CLI 兼容**：`openwhisker scheduler runs ...` 命令保留为兼容入口，内部转发到 `openwhisker agent runs --trigger-kind scheduler ...`。

**VaultProfile.scheduler 字段扩展**（向后兼容字段，缺省值不破坏 Phase 5 行为）：

```yaml
scheduler:
  tool_budget_defaults:
    max_tool_calls: 8
    max_total_bytes: 204800
    max_wall_clock_seconds: 60
  default_engine: static       # 可选, Phase 5 profile 不写视为 static
```

CLI flag 优先级：CLI > profile > 编译期默认值。Phase 5 profile 文件未声明 `tool_budget_defaults` 或 `default_engine` 时，全部回退到编译期默认值，行为与 Phase 5 完全一致。

字段名沿用 `VaultProfile.scheduler.*` 不改为 `agent.*`，避免 profile 文件破坏性变更；语义上视为"agent runtime 配置寄居在 scheduler profile 节点下"，未来若有专门的 agent 配置项再考虑节点重命名。

## Workflow

```text
AgentRunner 单 run (任一 trigger):

  AgentRunner.Run(skill, query, trigger_kind, deadline):

    Host: prepare initial prompt (固定模板, 见 ToolCallingEngine 章节)
      -> SKILL.md body content
      -> skill metadata (id, name, vault_scope, vault_tools, trigger_kind)
      -> tool catalog (JSON Schema for each whitelisted tool + submit_result)
      -> initial budget snapshot
      -> 如果 query != "": 追加 [user message] = query
                          否则: 追加 [user message] = "Now produce the scheduled output as defined by the skill."

    loop:
      LLM -> response
        -> if response 包含 submit_result tool_call:
             提取 title/summary/payload 填入 AgentRunResult
             break with status=ok (termination=natural)
        -> if response.tool_calls 为空 (LLM 吐自由文本):
             记录协议违反, 给一次纠正提示重试
             连续两次违反 → break with status=failed (termination=protocol_failed)
        -> for each tool_call:
             validate tool name in vault_tools (submit_result 例外, 总在 catalog)
             validate path args in vault_scope (含 .obsidian/.git/.trash 黑名单)
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
      persist tool_trace_json to agent_runs (含 trigger_kind)
      return AgentRunResult to caller

  Caller-specific output:
    scheduler:    AgentRunResult -> outbox -> matrix (复用 Phase 5 路径)
    matrix bot:   AgentRunResult -> reply helper -> 原 room
    cli ask:      AgentRunResult -> stdout (含 trace_id 引用)
```

## 首版范围

范围内：

- `AgentRunner` 通用抽象，承接 ToolCallingEngine + budget + trace + sanitize；
- `ToolCallingEngine` 实现（Go，OpenAI-compatible function calling，固定 system prompt 模板，`submit_result` 终止协议）；
- 五个 vault 工具实现 + `submit_result` 内置工具；
- 链接图索引子系统（fsnotify-based，内存，daemon 启动时全量构建 + CLI 进程 ad-hoc 同步构建 + 软门槛）；
- Budget 三上限（命名前缀 `--scheduler-budget-*`）+ force finalize / hard fail / protocol_failed 行为 + run status 枚举；
- `agent_runs` 表（含 `scheduler_runs` 自动 RENAME 迁移）+ `tool_trace_json` + `trigger_kind` 两列 + `--debug` ndjson + trace 脱敏规则；
- `SKILL.md` / `SCHEDULE.md` schema JSON 文件 + registry loader 重组（agent runtime 字段从 SCHEDULE 迁移到 SKILL，含隐式 engine 推断、字典序规范化、backwards-compat）；
- Matrix Bot 新增 `vault_query` 意图分支（@skill_id 显式路由，最高优先级，未 mention 不进入此分支）；
- CLI `openwhisker ask --skill <id> "<query>"` + `--json` / `--debug`；
- `openwhisker agent runs list/<id> --trace` CLI（Phase 5 `scheduler runs` 保留为兼容入口）；
- `openwhisker skill lint` CLI（含 `--strict` 模式 + 三档退出码 + 跨 SKILL.md / SCHEDULE.md 联合校验）；
- `Agent/Skills/skill-creator/` 设计入口（不展开 Skill 内部 prompt 设计；含 medium → high 风险升级规则 + dry-run preview + 区分 ad-hoc only / cron+ad-hoc 两种创建模式）；
- `internal/sanitize/` 通用脱敏包；
- schema migration（自动 RENAME + ALTER TABLE，无 down migration）。

范围外：

- langgraph 或其他外部框架；
- agent runtime 写 vault 任何形态；
- 自动 propose 新 Skill / 新 schedule；
- Matrix Bot intent classifier 推断 vault_query（强制 @skill_id 显式 mention）；
- wikilink alias 解析；
- 向量检索 / 嵌入式语义搜索；
- 反向链接 SQLite 持久化；
- 多 worker 并发执行同一 run；
- 工具间并行调用；
- per-skill ACL（继续复用 adapter 级 AllowedSenders / IgnoredUserIDs）；
- 用户级别的 budget 反馈 UI（未来 Android Console 范围）。

## Policy

Phase 6 在 Phase 5 policy 基础上追加：

```text
allowed (新增):
  - vault_link_graph_read
  - vault_text_search
  - agent_run_log_write

requires_user_confirmation (新增):
  - create_vault_skill  (skill-creator 走主线 WikiJob, 默认 medium-risk approval)

disabled (新增显式):
  - scheduler_initiated_skill_creation
  - scheduler_initiated_schedule_modification
  - agent_runtime_initiated_skill_creation     # ad-hoc 路径同样禁用 (覆盖硬约束 #3)
  - tool_call_chained_external_http
  - tool_call_chained_shell_execution
```

## 验收

Phase 6 完成后应满足：

```text
AgentRunner 是 host-agnostic 的通用 runtime, scheduler / matrix bot / cli 三个 host 共用同一份 engine / 工具 / budget / trace。
ToolCallingEngine 可以按 SKILL.md 声明的 vault_tools 白名单运行多轮 tool calling。
LLM 通过 submit_result 工具终止 run, 非通过自由文本结束; 连续两次违反协议视为 protocol_failed。
五个工具的每次调用都在 Go 侧重新校验 path 在 vault_scope 内 (含 .obsidian/.git/.trash 等禁入前缀的硬黑名单)。
LLM 无法通过任何 tool 间接读到 vault_scope 外的内容。
LLM 无法通过任何 tool 写 vault、调用 shell、发起任意 HTTP, 无论触发来源是 scheduler / matrix / cli。
budget 撞限按 force finalize / hard fail / protocol_failed 规则终止, 并在 trace 中记录原因; run status 落入 ok / partial / failed / skipped 之一。
agent_runs 表完整保存每次 tool call 的元数据 (不含全文), trigger_kind 列正确区分来源, 所有 vault 派生字段经 internal/sanitize 脱敏。
--debug 模式下完整 LLM message 序列落 data/agent-debug/<run_id>.ndjson。
链接图索引在 daemon 启动时全量构建, 运行期通过 fsnotify 增量维护; CLI 进程 (tick / ask) 同步现建 ad-hoc 索引并受软门槛保护。
vault_backlinks 在索引未就绪时返回明确 error, 不静默返回错误数据。
Matrix Bot 接收 "@<skill-id> <query>" 格式消息时正确路由到 AgentRunner; 未 @ 任何 Skill 的自由文本继续走 Phase 5 既有 intent 分支, 零回归。
openwhisker ask --skill <id> "<query>" 在本地终端正确运行, 默认 stdout 输出 summary, --json 输出完整 result, --debug 落 ndjson。
openwhisker skill lint 对违反 schema / capability / scope 的 SKILL.md / SCHEDULE.md 给出可读错误 (含行号 + 修复建议), --strict 模式追加风格 warning; 联合校验同目录两文件的 id 一致性。
skill-creator 创建新 vault Skill 时走主线 WikiJob, 默认进入 medium-risk approval; 满足风险升级条件时走 high-risk proposal note; 支持仅创建 SKILL.md (ad-hoc only) 和 同时创建 SKILL.md + SCHEDULE.md (cron+ad-hoc) 两种模式。
Phase 6 binary 启动时自动 RENAME scheduler_runs → agent_runs 并 ADD COLUMN 两列, 对已有 Phase 5 run 行无破坏。
Phase 5 已有 schedule 行为不受 Phase 6 引入的回归影响 (无 engine + 无 vault_tools 默认视为 engine: static; SCHEDULE.md 仅允许保留 Phase 5 旧字段 capabilities / external_info_sources / skill_config / delivery, registry loader 加载但给 deprecation warning; Phase 6 新引入字段 vault_tools / vault_scope / budget / engine 出现在 SCHEDULE.md 会被 reject)。
openwhisker scheduler runs ... CLI 保留为 openwhisker agent runs --trigger-kind scheduler ... 的兼容别名。
```

## 待决问题

落入实现前需要解决：

- **OPEN-1**：DeepSeek `deepseek-chat` 多轮 function calling 稳定性需要一次最小 demo 验证。
  - **demo 数据源**：跑在真实 vault `~/Documents/KnowLedge` 上，不构造 fixture vault；
  - **工具实现**：使用 Phase 6 真实实现（链接图索引 + 5 工具完整落地），不 mock；
  - **trigger 覆盖**：demo 至少覆盖 (a) scheduler tick 触发（query=""）、(b) CLI ask 触发（query=自然语言问题）两种入口，验证两种模式下 LLM 行为都稳定；matrix 路径与 CLI 路径共用 query 注入逻辑，可由 CLI 路径代表；
  - **验收标准**：连续 10 次 demo run（5 次 scheduler + 5 次 CLI ask），每次 3-4 轮 tool call，要求 (a) 每轮模型返回合法的 `tool_call` 或调用 `submit_result`，无 schema 错误；(b) 无 stuck loop（同一参数反复调同一 tool 超过 2 次）；(c) 最终通过 `submit_result` 返回合理的 title/summary/payload；
  - **门槛性质**：≥9/10 通过是 Phase 6 **merge 硬门槛**，不通过不允许合入 deploy/main。不达标 → 调整 prompt / 协议 / 必要时引入 fallback provider，重跑直到通过；不接受"标记 caveat 后合并"或"限定发布范围"的妥协；
  - **入仓约束**：demo 脚本存 `docs/phases/phase-6-scheduler-skill-creator/demo/`，**实际命中的 vault 内容不入仓**，只留脱敏后的结果摘要（通过率、撞限分布、协议错误次数）。
- ~~**OPEN-2**~~：已确认 budget 默认值 8 / 200 KiB / 60s 作为初始估算保留，转入「运行后观察项」节按两周窗口回顾，不阻塞 Phase 6 实现。
- ~~**OPEN-3**~~：已确认 Phase 6 第一版不解析 Obsidian wikilink alias；`vault_outlinks` 对无法解析的 wikilink 返回 `unresolved=true` + 原文；LLM 退化使用 `vault_text_search` 查 alias 字面量；alias 解析作为 Phase 7+ 增量。
- ~~**OPEN-4**~~：已确认 `vault_text_search` 第一版固定为 literal 匹配 + case-insensitive，不暴露 regex / case 模式开关给 LLM；`query` 参数最小长度 2 字符（防 LLM 用单字符全 vault 扫底烧 budget）；保留既有的 50 命中 + 单行片段上限。
- ~~**OPEN-5**~~：已确认 `skill-creator` Skill prompt 设计落 vault `Agent/Skills/skill-creator/SKILL.md`，本 phase doc 只声明边界；
- ~~**OPEN-6**~~：已确认 fsnotify 溢出处理为 daemon stderr error + 下次 tick 前触发全量重建，重建期间 `vault_backlinks` 返回 error；50k+ notes 规模视为未验证范围需补充实测。
- ~~**OPEN-7**~~：已确认 Matrix Bot 第一版强制 `@skill-id` 显式 mention 路由，不引入 intent classifier 推断；ad-hoc query 必须挂载到一个预定义 Skill，不允许裸 prompt。
- ~~**OPEN-8**~~：已确认 ad-hoc query 与 scheduler run 共用 `agent_runs` 表（原 `scheduler_runs` 表 RENAME），通过 `trigger_kind` 列区分来源。

## 运行后观察项

Phase 6 发布后需要在运行中验证的项目，不阻塞实现：

- **budget 默认值调参**：当前默认 `max_tool_calls=8` / `max_total_bytes=200 KiB` / `max_wall_clock_seconds=60s` 为初始估算。首个 tool-calling Skill 上线后两周内，daemon 在 stderr 记录每 run 的 budget 使用率分布；触发回顾的阈值：(a) 单维度 P90 使用率 > 80%，或 (b) 撞限（force finalize + hard fail）次数占总 run > 10%。满足任一条件即评审是否调整默认值或允许 Skill 级 override 范围。回顾结论写入 CHANGELOG。
- **ad-hoc / scheduler 触发比例**：观察 `agent_runs.trigger_kind` 分布。如果 ad-hoc 占比显著高于 scheduler，说明用户更多以"主动提问"方式使用 agent runtime，下一阶段应优先优化 Matrix Bot 体验（例如更友好的 @skill-id 自动补全、多 Skill 列表查询命令）；反之则优先优化 cron / 通知形态。
- **Matrix `@skill-id` 误触率**：观察用户消息中 `@skill-id` 实际不指向已知 Skill 的频次。若高，考虑给出 "你是不是想用 @<相近 skill-id>" 类提示，但**不引入自动 fallback 路由**（保持显式语义）。

## 已确认决策

- 不引入 langgraph，第一版用 Go 内 ToolCallingEngine + OpenAI-compatible function calling 协议；
- Phase 5 `vault_context` 字段移除，不保留兼容路径；Phase 5 `maxVaultContextFileBytes = 20 KiB` 成为 dead constant；
- **Phase 6 主题从"让 scheduler 变聪明"扩展为"agent 化的 vault 知识查询响应"**；Skill 成为核心组织单元，触发方式有三：scheduler cron / Matrix Bot @skill-id 自然语言 / CLI `openwhisker ask`；
- **Agent runtime 抽象为通用 `AgentRunner`**，scheduler / matrix bot / cli 三个 host 共用；
- **SCHEDULE.md 降级为可选触发配置**，仅保留 cron / timezone / enabled / id 字段；所有 agent runtime 字段（engine / vault_tools / vault_scope / budget / capabilities）迁移到必填的 SKILL.md；
- **Matrix Bot 强制 `@skill-id` 显式 mention 路由**，第一版不引入 intent classifier 推断；
- **CLI 入口为 `openwhisker ask --skill <id> "<query>"`**；--json / --debug 选项；
- **`scheduler_runs` 表 RENAME 为 `agent_runs`** 并新增 `tool_trace_json` + `trigger_kind` 两列；`openwhisker scheduler runs` 保留为兼容别名；
- 链接图索引使用 daemon 启动全量构建 + fsnotify 增量维护方案；归属 `internal/vault/linkindex/` 通用层，AgentRunner 是首个消费者；
- CLI 进程（tick / ask）与 daemon 是独立进程，同步现建 ad-hoc 索引并受软门槛（5000 文件 / 10s）保护；
- Trace 落 `agent_runs.tool_trace_json` 列，不引入独立 `agent_tool_calls` 表；trace 字段经 `internal/sanitize/` 通用脱敏；
- LLM 完整 message 仅在 `--debug` 模式下落磁盘 ndjson，不进 SQLite；
- `skill-creator` 归属 vault `Skills/`，通过主线 `WikiJob` 写入，agent runtime 不参与；默认 medium-risk approval，满足升级条件转 high-risk proposal note；支持仅创建 SKILL.md 或同时创建 SKILL.md + SCHEDULE.md 两种模式；
- `openwhisker skill lint` 是 read-only CLI，与 skill-creator 共用 schema；含 `--strict` 模式 + 三档退出码 + 联合校验同目录两文件；
- `vault_tools` 按工具粒度白名单声明，不引入 bundle 形态；顺序无意义按字典序规范化存储；
- Budget 行为：撞 call 数 / bytes 走 force finalize；撞 wall clock 走 hard fail；命名前缀 `--scheduler-budget-*`（沿用避免引入新前缀）；
- Run status 枚举：ok / partial / failed / skipped；trace.termination：natural / call_count_exceeded / bytes_exceeded / wall_clock_exceeded / protocol_failed / error；
- 工具执行体留在 Go Host 侧，engine 只承担协议层；
- LLM 通过内置 `submit_result` 工具终止 run，不通过自由文本结束；
- Engine 隐式推断：非空 `vault_tools` 未声明 `engine` 时自动推断为 `tool-calling`；显式冲突（如 `engine: static` + 非空 `vault_tools`）reject；
- 工具集是编译期闭集（5 + 1 内置 `submit_result`），不提供 plugin / dynamic registration；
- vault_scope 校验在任何情况下拒绝 `.obsidian/`、`.git/`、`.trash/`、`.DS_Store` 前缀；不跟随指向 vault root 外的 symlink；
- `read_vault_note` 使用 tool-side 独立上限（默认 128 KiB，硬上限 256 KiB），与 Phase 5 常量解耦；
- `vault_outlinks` 返回区分 `wikilinks` 和 `frontmatter_related` 两类；Phase 6 第一版不解析 Obsidian wikilink alias，未解析项返回 `unresolved=true` + 原文，LLM 退化用 `vault_text_search` 查 alias 字面量；alias 解析留 Phase 7+ 增量；
- `search_vault` 改名为 `vault_text_search`，保留可选 `scope_subset` 参数；第一版固定 literal 匹配 + case-insensitive，不暴露 regex / case 开关；`query` 最小长度 2 字符，防 LLM 单字符全 vault 扫底烧 budget；
- 手动编辑 vault `SKILL.md` / `SCHEDULE.md` 直接生效是 by design 行为，与 skill-creator approval 并存，不视为不一致；
- Schema 迁移：daemon 首次启动检查 `scheduler_runs` 表存在则 RENAME + ADD COLUMN，无 down migration；
- **vault 分集升级**：Phase 6 agent skill 走 `Agent/Skills/<id>/SKILL.md`（升级 Phase 5 `Scheduler/Skills/` 而非新开第三个分集）；registry loader 同时扫两个路径作为 backwards-compat，旧路径加 deprecation warning；不引入 frontmatter `kind` 字段，目录就是天然的 kind 标签；现有 `Skills/vault-raw-organizer/` 等 Phase 5 之前的"主线 LLM prompt 文档"零变更，不进入 agent runtime 加载路径；
- **Skill id 全局唯一**：在 vault 内（含 `Agent/Skills/` 和兼容期 `Scheduler/Skills/`）所有 agent skill SKILL.md 的 id 全局唯一，registry loader 启动加载冲突即 reject；
- **`engine` 字段可选**：未声明时按 `vault_tools` 推断（非空 → `tool-calling`；空 → `static`）；显式冲突 reject；CLI `--scheduler-engine` flag 仅 Phase 5 老 SCHEDULE.md 兼容兜底；
- **SCHEDULE.md 字段处理两档**：Phase 6 新引入字段（vault_tools / vault_scope / budget / engine）在 SCHEDULE.md 出现 = lint error；Phase 5 旧字段（capabilities / external_info_sources / skill_config / delivery）在 SCHEDULE.md 出现 = deprecation warning；`vault_context` = error（字段已移除）；
- **Run status `ok` 是 doc / CLI 渲染层别名**：SQLite 字段值沿用 Phase 5 的 `done`，不引入 status 值 migration；`partial` 是真正新增的枚举值，需要加入 `model.SchedulerRunStatus*` 常量；
- **链接图索引异步构建**：daemon 启动**不阻塞**等待索引就绪；未就绪期间 `vault_outlinks` / `vault_backlinks` 返回 `link_index_not_ready` error，其他三个工具不受影响；索引就绪后日志记一条 ready；
- **Matrix `@<skill-id>` 保留为文本约定**：不依赖 Matrix mention 协议；用户接受 client mention 自动补全后 bot 视为"未指定 Skill"按 Phase 5 既有 intent 分支处理；UI 重合作为 acknowledge 写入文档，Phase 7+ 视情况评估换语法。
