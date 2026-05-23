# Phase 5 Read-only Skill Scheduler

状态：minimal slice implemented，RSS / Atom info-only adapter implemented。

本文定义 Phase 5 的阶段边界。Phase 5 的目标不是让 OpenWhisker 定时自动修改 Obsidian vault，而是引入一个按时间运行的 read-only Skill Host：它读取 vault 上下文和外部信息源，生成个人简报、提醒、候选输入或后续动作建议，并通过 OpenWhisker outbox 推送给用户。

当前实现已经落地首个最小切片：

- `VaultProfile.scheduler` 可以声明 scheduler 是否启用、registry 路径、scheduler 专用 Skill 根目录、默认 delivery、只读 vault roots 和允许的外部信息源；
- `knowledge-vault` 配置 profile 默认启用 `Scheduler/Skills/*/SCHEDULE.md`；
- runtime 只读取 profile 声明的 registry path，并要求 `SCHEDULE.md` 绑定同目录 `SKILL.md`；
- `SCHEDULE.md` frontmatter 支持 `id`、`name`、`enabled`、`cron_expr`、`timezone`、`delivery`、`capabilities`、`vault_context`、`external_info_sources`、`skill_config`；
- registry loader 会拒绝 `vault_write`、`auto_approve`、`external_api_side_effect`、`arbitrary_shell`、`arbitrary_http` 等 Scheduler 禁用能力；
- `vault_context` 只能落在 profile 声明的 `read_only_vault_roots` 内，当前最小 runner 会读取显式声明的文件内容或目录清单；
- cron 使用 5 字段表达式和 timezone 计算 due window；
- SQLite 保存 `scheduler_runtime` 和 `scheduler_runs`；
- `openwhisker scheduler tick` 可以执行一次手动 tick，把 due schedule 的结果写入 run log 和 outbox；
- `openwhisker scheduler status` / `scheduler runs` 可以查看 runtime、next run 和最近 run；Matrix 支持 `/scheduler status` / `/scheduler runs [limit]`；
- `openwhisker scheduler schedules list|enable|disable` 可以通过 SQLite override 管理 schedule 生效状态，不直接改 vault 中的 `SCHEDULE.md`；
- 同一 schedule 已有 `running` run 时，本次 due tick 记录 `skipped`；
- 首版 Scheduler Host 已拆出可替换的 `SkillEngine` 和 info-only `ExternalInfoAdapter` 接口：默认 engine 仍只生成 generic outbox 摘要；CLI 可显式选择 OpenAI-compatible Scheduler Engine；`knowledge-vault` profile 允许 `rss` 外部信息源，CLI 配置了只读 RSS / Atom adapter，可读取 RSSHub route、普通 RSS feed 或 Atom feed。
- outbox 已有 `actor` 字段：Scheduler 产生的消息标记为 `scheduler` actor，既有知识处理链路默认是 `knowledge` actor；Matrix delivery 可以按 actor 选择 Knowledge Bot / Scheduler Bot 发送身份，并忽略同 room 内两个 bot 自己发出的消息。
- `payload.suggested_raw_captures` 已有显式确认入口：`openwhisker scheduler accept <run_id>` 和 Matrix `/scheduler accept <run_id> [item_number]` 会读取指定 scheduler run 的指定建议条目，并转入既有 low-risk raw capture workflow；Scheduler 本身仍不写 vault。
- 独立 `openwhisker daemon` 已实现 scheduler tick loop、可选 Matrix poll loop、tick 后 outbox delivery、状态文件和 `openwhisker daemon status`；macOS launchd 部署模板见 `docs/deployment/launchd.md`。

## 背景

V1 主线已经收束为：

```text
Capture / Command
  -> WikiJob
  -> VaultPlan
  -> Policy / Diff / Risk
  -> Approval
  -> VaultExecutor
  -> Vault Files
  -> Operation Log / Outbox
```

这条链路解决的是“用户触发后，如何安全地把知识变更写入 vault”。

Phase 5 要解决的是另一类问题：系统如何在合适的时间主动把该看的信息推到用户面前，例如每日简报、外部动态摘要、未处理事项提醒、vault 健康雷达和跨来源信息汇总。

因此 Scheduler 应该被建模为新的输入和观察层，而不是新的写入层。

## 核心结论

Scheduler 是：

- 定时触发器；
- vault-local Skill Host；
- 只读 vault context provider；
- 外部信息源编排器；
- run state / outbox 记录器。

Scheduler 不是：

- `VaultExecutor`；
- approval bypass；
- 自动 raw capture 入口；
- 自动 organizer；
- 自动 maintenance writer；
- 任意外部 API 执行器。

第一版必须遵守：

```text
Scheduler can read vault.
Scheduler can read external information sources through approved adapters.
Scheduler can write OpenWhisker internal run log and outbox.
Scheduler cannot write vault.
Scheduler cannot call VaultExecutor.
Scheduler cannot auto-approve or auto-apply VaultPlan.
Scheduler cannot perform external API side effects.
```

## 总体架构

```text
Clock / daemon tick
  -> Scheduler Registry
  -> Scheduler Host
  -> load vault-local Skill
  -> VaultReader(read-only)
  -> External API adapters(info-only)
  -> Skill result envelope
  -> Scheduler run log
  -> Outbox / Matrix notify
```

Scheduler Host 负责运行协议，不负责业务判断。业务判断属于 vault-local Skill。

## 交互身份：Scheduler Bot

Scheduler 在交互层应该有独立的角色身份。这里的“独立”指 bot identity / actor identity，不是新的写入权限主体。

目标形态是同一个 Matrix room 里可以同时存在两个 OpenWhisker 相关 bot：

```text
Matrix room
  -> Knowledge Bot
     handles user-initiated capture / organize / expand / diff / approve / reject

  -> Scheduler Bot
     sends scheduled briefing / RSSHub watch / vault radar / reminder / suggested capture prompts
```

这两个 bot 可以共享同一个 OpenWhisker 后端、SQLite 状态和 vault policy，但对用户和未来客户端来说应是两个不同角色：

- Knowledge Bot 面向“我主动给你一段材料，请帮我处理知识”；
- Scheduler Bot 面向“系统定时观察到了这些信息，提醒你看看或确认是否收录”。

Scheduler Bot 仍然遵守 Scheduler 的只读边界。它可以发送摘要、提醒和“是否收录这条信息”的自然确认文案，但不能写 vault、不能调用 `VaultExecutor`、不能自动 approve，也不能执行外部 API 副作用。

用户确认 Scheduler Bot 发出的 suggested raw capture 后，OpenWhisker 可以在内部转入既有 raw capture / knowledge workflow。这个转入可以由 Knowledge Bot 继续展示，也可以由后端保持同一确认链路，但最终写入仍必须走既有 `WikiJob -> VaultPlan / policy -> VaultExecutor` 边界。

后续客户端也应该按 actor 区分展示：

```text
Scheduler Feed / Bot
  scheduled briefing, reminders, suggested captures

Knowledge Workbench / Bot
  capture, organize, diff, approval, execution result
```

因此 outbox / Matrix delivery 使用 `actor` 字段把同一 room 内的消息投递给不同 bot identity：Scheduler 的消息归属是 `scheduler` actor，知识处理消息归属是 `knowledge` actor。没有配置 Scheduler Bot 凭据时，Matrix delivery 会退回到现有 Knowledge Bot client 发送；配置 Scheduler Bot 后，`scheduler` actor 消息由 Scheduler Bot 身份发送。

## Skill-driven 组织方式

Scheduled workflow 由 `VaultProfile` 开启，并由 vault-local Skills 组织。OpenWhisker runtime 不应该把某个 vault 的每日简报、维护检查或外部信息源规则硬编码进通用代码，也不应该自己扫描整个 vault 去发现可运行的 scheduled Skill。

`VaultProfile` 是 Scheduler 的入口声明。它应该体现：

- Scheduler 是否启用；
- scheduler registry 可以读取哪些 vault-local 路径；
- scheduled Skills 允许位于哪些 scheduler 专用 skill root；
- 默认 delivery target，例如 Matrix / outbox；
- 默认只读 vault context 范围；
- 默认外部信息源 capability 范围。

概念示例：

```yaml
scheduler:
  enabled: true
  registry_paths:
    - Scheduler/Skills/*/SCHEDULE.md
  scheduled_skill_roots:
    - Scheduler/Skills/
  default_delivery:
    - outbox
  read_only_vault_roots:
    - Raw/
    - Knowledge/
    - Interview/
    - Life/
    - Meta/
  external_info_sources:
    - rss
```

Scheduler registry loader 只读取 `VaultProfile` 声明允许的位置。Profile 没有开启 scheduler，或没有声明 registry path 时，Scheduler 不应自动扫描 `Skills/**`、`Scheduler/**`、`Meta/**` 或整个 vault。

一个 scheduled Skill 应描述：

- 它是什么任务；
- 它适合被什么 trigger 调用；
- 它需要哪些只读 vault context；
- 它需要哪些外部信息源；
- 它输出什么粒度的信息；
- 哪些输出只适合提醒，哪些输出可以作为内部 suggested raw capture（建议收录项）；
- 哪些信息必须在用户确认后才能进入既有 OpenWhisker workflow。

示例方向：

```text
vault Scheduler/
  Skills/
    daily-briefing/
      SKILL.md
      SCHEDULE.md
    weekly-review/
      SKILL.md
      SCHEDULE.md
    release-watch/
      SKILL.md
      SCHEDULE.md
    calendar-briefing/
      SKILL.md
      SCHEDULE.md
```

这些名称只是方向示例，不是 Phase 5 的内置固定 job type。首版实现应支持“每个 scheduled Skill 自带一份 `SCHEDULE.md`”，而不是把 daily / weekly 逻辑写死在 Core。

第一版约束：

- `VaultProfile.scheduler.registry_paths` 只支持 skill-local `SCHEDULE.md` 形态，例如 `Scheduler/Skills/*/SCHEDULE.md`；
- 第一版不支持集中 registry 文件，例如 `OpenWhisker/Scheduler.md`；
- `SCHEDULE.md` 必须位于 `VaultProfile.scheduler.scheduled_skill_roots` 允许的目录下；
- `SCHEDULE.md` 隐式使用同目录的 `SKILL.md`，不提供跨目录 Skill 引用机制；
- scheduled Skill manifest 复用同目录 `SKILL.md`，不引入新的 `skill.yaml`、`manifest.json` 或 schedule-specific manifest；
- `SCHEDULE.md` 只承载调度声明、delivery 和 `skill_config`；
- Scheduler 不读取普通 `Skills/`，避免和面向 TUI、通用 agent workflow 或其他工具的 vault Skills 混淆。

## 权限边界

### Vault 能力

Scheduler 对 vault 只有读取能力。

允许：

- 读取 `VaultProfile` 明确声明允许的目录或文件；
- 读取 `VaultProfile` / Skill 所需的只读摘要；
- 扫描 metadata、tags、links、pending review 状态；
- 生成提醒、摘要、候选输入或后续动作建议。

禁止：

- 创建、修改、移动或删除 vault note；
- 调用 `direct_fs_executor`；
- 调用 `VaultExecutor.Apply`；
- 自动创建 `VaultPlan` 并进入 approval；
- 自动把外部 API 内容写入 `Raw/Inbox`；
- 自动把 report 写回 `Meta/Reports` 或 daily note。

如果某个 scheduled Skill 发现外部信息值得保存，它只能生成内部 suggested raw capture（建议收录项），由 outbox 以自然的人读方式请求用户确认。用户确认后，OpenWhisker 内部调用既有 raw capture 或 command workflow；具体 `WikiJob`、policy 和 executor 细节不暴露给用户。

### 外部 API 能力

Scheduler 场景下的外部 API 只提供信息。

允许：

- 读取日历、任务、RSS、GitHub、邮件摘要或其他用户授权的信息源；
- 搜索、分页、拉取变更摘要；
- 把外部信息整理为 Skill result。

禁止：

- 创建或修改外部系统对象；
- 发消息、改日历、创建 issue、关闭任务等副作用；
- 把外部 API response 直接落入 vault；
- 让 Skill 获得任意 shell 或任意 HTTP 能力。

后续如果需要外部写操作，应作为独立 capability 和独立审批模型设计，不属于 Phase 5 第一版。

## 结果模型

Scheduler 只定义最小结果 envelope。具体 payload schema 由 Skill 和它的配置决定。

概念模型：

```text
SchedulerRunResult:
  run_id
  schedule_id
  skill_id
  status
  started_at
  finished_at
  title
  summary
  payload
  error
```

其中：

- `title` 和 `summary` 用于 outbox 的默认人读展示；
- `payload` 是 Skill-owned JSON；
- Scheduler 不解析 payload 来执行 vault 写入；
- 第一版 outbox renderer 只做 generic 展示，不提供 Skill-specific renderer；
- outbox renderer 不能把 payload 当成命令执行。

Skill 可以根据自己的配置输出不同粒度的信息，例如：

- 一段日报式摘要；
- 外部信息条目列表；
- 风险或优先级雷达；
- suggested raw capture（建议收录项）；
- 候选 command suggestion；
- 需要用户继续追问或确认的问题。

这些输出都不等于已经进入 OpenWhisker 写入链路。只有用户显式确认后，才可以转入既有 raw capture、organize、expand、diff 或 approval workflow。

## Registry 与运行状态

Phase 5 不把 OpenWhisker SQLite 建模为唯一 registry。真正的 scheduler registry 入口来自 `VaultProfile`：Profile 开启 scheduler，并声明 registry loader 可以读取哪些 vault-local schedule 文件和 scheduler 专用 skill roots。

分层如下：

```text
VaultProfile scheduler declaration
  human-owned, reviewed, read from confirmed profile context
  enables scheduler and declares readable registry paths / scheduled skill roots

Vault-local schedule registry
  human-owned, read-only to OpenWhisker
  declares each scheduled Skill's trigger and skill-specific config

OpenWhisker scheduler runtime state
  runtime-owned, stored in SQLite
  stores resolved schedule identity, source hash, next_run, last_run and locks

OpenWhisker scheduler run log
  runtime-owned, stored in SQLite
  stores each execution result and outbox linkage
```

Vault-local schedule registry 的概念字段：

```text
SCHEDULE.md frontmatter:
  id
  name
  capabilities
  vault_context
  external_info_sources
  skill_config
  cron_expr
  timezone
  enabled
  delivery
```

`SCHEDULE.md` 第一版使用 Markdown + frontmatter。Frontmatter 承载机器可读 schedule declaration，正文承载给人看的说明。

概念示例：

```md
---
id: daily-briefing
name: 每日简报
enabled: true
cron_expr: "0 8 * * *"
timezone: Asia/Shanghai
delivery:
  - outbox
capabilities:
  - vault_read
  - scheduler_run_log_write
  - outbox_notify
vault_context:
  - Raw/
external_info_sources: []
skill_config:
  detail: normal
---

# 每日简报调度

每天早上生成个人简报摘要。具体输出粒度由同目录 `SKILL.md` 和 `skill_config` 决定。
```

第一版不提供 `skill_ref` 机制。`SCHEDULE.md` 隐式使用同目录的 `SKILL.md`，不允许引用其他目录的 Skill。`SCHEDULE.md` 本身必须匹配 `VaultProfile.scheduler.registry_paths`，且所在目录必须位于 `VaultProfile.scheduler.scheduled_skill_roots` 允许的位置内。

时间规则第一版直接使用 cron 表达，不再定义 daily / weekly 语义层，避免后续迁移配置格式。人读友好的说明可以放在 `name` 或 Skill 文档中，运行时以 `cron_expr + timezone` 为准。

`delivery` 第一版只支持 `outbox`。Matrix 投递继续通过现有 outbox adapter 完成，schedule declaration 不直接绑定 Matrix room、user 或 adapter-specific destination。outbox message 使用 actor identity 表达投递身份：Scheduler 产生的通知归属 `scheduler` actor，并可由 Scheduler Bot 身份投递；知识处理链路的通知归属 `knowledge` actor，并可由 Knowledge Bot 身份投递。两个 bot 可以在同一个 Matrix room 内共存。

`capabilities` 是运行边界声明，不是授权绕过。第一版只允许 `vault_read`、`external_info_read`、`scheduler_run_log_write` 和 `outbox_notify`；`vault_write`、`vault_executor_apply`、`auto_approve`、`external_api_side_effect`、`arbitrary_shell`、`arbitrary_http` 和 `arbitrary_file_write` 会在 registry load 阶段被拒绝。

`vault_context` 是 Skill 希望读取的 vault-local 路径列表。路径必须是相对路径，不能包含 parent traversal，不能使用 wildcard，并且必须位于 `VaultProfile.scheduler.read_only_vault_roots` 允许的 root 内。当前最小 runner 对文件读取带大小上限，对目录只读取直接子项清单，不递归扫描整个 vault。

`external_info_sources` 是 Skill 希望读取的外部信息源名称列表。名称必须出现在 `VaultProfile.scheduler.external_info_sources` 中。当前 Scheduler Host 已有 info-only adapter 接口，但默认 CLI 没有配置真实 adapter，因此声明了外部信息源的 schedule 会得到明确失败结果和 outbox error；后续接入 adapter 时仍只能提供 info-only read。

### 一期外部信息源候选：RSS / Atom

Phase 5 的第一个真实外部信息源候选优先考虑 RSS / Atom，而不是直接把某个上游产品写成 Scheduler Core 的概念。

建议的一期抽象是：

```text
Scheduler
  -> rss info-only adapter
  -> read feed URLs from scheduled Skill config
  -> parse RSS / Atom items
  -> pass external_info to SkillEngine
  -> Skill summarizes / filters
  -> outbox notifies user
```

这里的 `rss` 是能力名和 adapter 类型。RSSHub、Folo 或普通站点 feed 都只是 `rss` 的上游来源：

- RSSHub 更像 feed 生成器 / 路由服务；只要它产出标准 RSS / Atom，Scheduler 不需要理解 RSSHub 路由语义；
- Folo 如果作为订阅管理或聚合服务，第一期不深接账号 API，也不读取用户账号里的完整订阅状态；
- 如果 Folo 能导出 feed、OPML 或用户手动给出 feed URL，第一期可以把它当成普通 feed 来源；
- Scheduler Core 不应该出现 `rsshub`、`folo` 这样的内置业务分支，除非后续明确需要专属鉴权、分页或限流模型。

一期配置方向仍由 `VaultProfile.scheduler.external_info_sources` 显式允许，再由具体 `SCHEDULE.md` 声明使用：

```yaml
external_info_sources:
  - rss

skill_config:
  feed_urls: "https://rsshub.example/routes/example,https://example.com/feed.xml"
  feed_limit: "10"
```

当前代码已实现 `rss` adapter。RSSHub 验收边界是：用户在 `SCHEDULE.md` 的 `skill_config.feed_urls` 填入 RSSHub route URL，例如 `https://rsshub.example/routes/example`；Scheduler 只把它当成标准 RSS / Atom feed URL 拉取和解析，不理解也不硬编码具体 RSSHub route 语义。

实现保持以下边界：

- 只允许 adapter 读取 profile 已允许的 `rss` source；
- feed URL 由 scheduled Skill 的 `skill_config` 提供，不先做中心订阅库；
- adapter 只做 `GET http/https`、大小限制、条数限制和字段裁剪；
- adapter 只解析 RSS / Atom，返回信息快照，不写 vault，不创建 raw capture；
- adapter 不发送 cookie、authorization header 或其他用户凭据；
- adapter payload 中的 feed URL 会去掉 query、fragment 和 userinfo，避免把 token 型 URL 直接落入 run log；
- Skill 可以在 payload 里给出“建议收录”的人读内容，但确认和 raw capture 仍走 OpenWhisker 既有内部 workflow；
- RSSHub / Folo 的账号 API、订阅变更、标记已读、收藏、关注 / 取消关注等副作用不属于一期。

`SkillEngine` 是 Scheduler Host 和实际 reasoning 层之间的接口。Host 负责加载同目录 `SKILL.md`、构造受限 `vault_context`、调用 info-only external adapters，并把这些输入交给 engine。默认 engine 只生成 generic result；OpenAI-compatible engine 只能消费 Host 给出的受限输入，返回 `title`、`summary` 和 opaque JSON `payload`，不能自行读 vault、调用任意 HTTP、执行 shell、调用 `VaultExecutor` 或写文件。

`skill_config` 对 Scheduler Core 是 opaque payload。Scheduler 只负责解析、保存和传给同目录 Skill，不解释其中的业务字段，也不根据 `skill_config` 执行 vault 写入或外部 API 副作用。

SQLite 中的 runtime state 只保存解析后的运行状态，不是人类配置事实来源：

```text
scheduled_runtime:
  id
  schedule_id
  registry_path
  registry_hash
  skill_dir
  skill_path
  last_run_at
  next_run_at
  updated_at
```

Scheduler run log 记录每次运行：

```text
scheduler_runs:
  id
  schedule_id
  runtime_id
  skill_dir
  skill_path
  status
  started_at
  finished_at
  result_json
  error
  outbox_message_id
```

第一版 runtime state 和 run log 属于 OpenWhisker 内部状态，不写 vault。它用于排查失败、避免重复运行、支撑 outbox 重试和给用户展示最近运行状态。

如果 vault-local registry 被用户修改，Scheduler 下次 tick 时应只读重新加载并 reconcile 到 runtime state。OpenWhisker 不应自动把 runtime state、`last_run_at` 或 `next_run_at` 回写到 vault-local registry。

## Workflow

### Daemon

Phase 5 之后的长期运行入口从 adapter-specific `matrix daemon` 收敛到独立 `openwhisker daemon`。它的语义是 OpenWhisker workflow host 常驻进程，而不是某个 IM adapter 的附属循环。

目标结构：

```text
openwhisker daemon
  -> scheduler tick loop
  -> outbox delivery loop
  -> optional Matrix poll loop
  -> future sync radar / vault health / maintenance proposal loops
```

设计边界：

- `openwhisker daemon` 是当前长期运行主入口；
- `openwhisker matrix daemon` 保留为兼容入口和 Matrix adapter 调试入口，短期不删除；
- Scheduler 不依赖 Matrix。没有 Matrix 配置时，daemon 仍可运行 scheduler tick，把 outbox 留在 pending；
- Matrix 是可选 delivery / command adapter。配置存在时，daemon 可以同时 poll Matrix 输入并 delivery outbox；
- Scheduler tick 后应尽快触发 outbox delivery，但 delivery 失败不应回滚 scheduler run log；
- daemon 不改变 Scheduler 的权限边界：tick loop 仍只运行 read-only Scheduler Host，确认后的 raw capture 仍走显式 `scheduler accept` / `/scheduler accept` 和既有 low-risk raw workflow。

第一版 `openwhisker daemon` 已按以下行为实现：

```text
startup:
  -> open SQLite store
  -> load selected VaultProfile
  -> initialize scheduler service
  -> initialize Matrix adapter only when matrix mode enables it

scheduler loop:
  every scheduler_tick_interval:
    -> SchedulerService.Tick()
    -> if Matrix delivery enabled: DeliverOutbox()

matrix loop:
  while running:
    -> MatrixAdapter.PollOnce()
    -> PollOnce handles Matrix input and pending outbox delivery

shutdown:
  SIGINT / SIGTERM cancels both loops
```

当前配置：

```text
openwhisker daemon
  --db data/openwhisker.db
  --vault testdata/vault
  --vault-profile generic|knowledge-vault
  --scheduler-tick-interval 1m
  --status-file data/daemon-status.json
  --scheduler-engine static|openai-compatible
  --matrix auto|on|off
```

默认值：

- `--scheduler-tick-interval 1m`；
- `--matrix auto`；
- `auto` 模式下，Matrix homeserver / user or token / room 配置完整才启用 Matrix loop；
- `on` 模式下，Matrix 配置不完整应启动失败；
- `off` 模式下，不初始化 Matrix，scheduler run 只写 run log 和 pending outbox；
- 当 selected `VaultProfile.scheduler.enabled=false` 时，scheduler loop 可以继续 tick，但每次结果应快速返回 disabled，不执行 registry scan。

并发模型：

- 第一版可以使用两个 goroutine：一个 scheduler ticker，一个 Matrix long-poll loop；
- 两个 loop 可以共享同一个 `storage.Store`，SQLite 连接已限制为单连接；
- 不引入复杂 supervisor，不做多 worker，不并发执行同一个 schedule；
- scheduler tick error 写入 daemon stderr，并等待下一轮 tick，不退出进程；
- Matrix poll error 沿用 adapter 现有 backoff；
- delivery error 不影响已经完成的 scheduler run，只保留 outbox pending，等待下一轮 delivery。

后续如果引入 Android Console / Web UI / 多 adapter，`openwhisker daemon` 应继续作为主进程，Matrix adapter 只保留为其中一个输入/输出插件，不再承载系统级 daemon 语义。

### Tick

```text
scheduler tick
  -> load confirmed VaultProfile scheduler declaration
  -> read declared vault-local registry paths
  -> resolve each SCHEDULE.md to its same-directory SKILL.md
  -> reconcile scheduler runtime state
  -> find due schedules
  -> create scheduler run
  -> invoke Scheduler Host
  -> write run result
  -> enqueue outbox message
  -> update last_run_at / next_run_at
```

`tick` 应是幂等的。同一个 schedule 在同一个 due window 内不能因为 daemon 重启或重复调用而创建多次有效 run。

同一个 schedule 不并发运行。如果上一次 run 仍在 `running`，下一次 due tick 第一版应跳过本次运行，记录 `skipped` 状态，并通过 run log 保留原因。

### Skill Run

```text
Scheduler Host
  -> load vault-local Skill
  -> validate declared capabilities
  -> build read-only vault context
  -> call info-only external adapters
  -> invoke SkillEngine
  -> receive Skill result envelope
  -> persist run result
  -> notify through outbox
```

Skill run 失败也应写入 run log，并生成 outbox error 摘要。错误摘要只包含 schedule id、Skill 名称 / 路径和安全的错误信息，不包含 secrets、tokens、完整外部 API response 或未脱敏 payload。

### External Information To Raw

```text
Skill sees external information worth preserving
  -> emits internal suggested raw capture in payload
  -> outbox asks user to confirm in human-facing wording
  -> user confirms through IM / CLI
  -> OpenWhisker internally calls existing raw capture workflow
  -> existing low-risk policy and executor handle Raw/Inbox write
```

Scheduler 本身不执行最后一步。

## 首版范围

范围内：

- 定义 `SCHEDULE.md` -> 同目录 `SKILL.md` 的隐式解析模型；
- 定义 Scheduler Host 的只读 vault 和 info-only external API 边界；
- 定义最小 run result envelope；
- 定义 run log 和 outbox 作为第一版持久化 / 通知出口；
- 支持手动 `tick` 或 daemon tick 的设计；
- 支持个人简报中枢这一主场景，但不固定首批 Skill 名单。

范围外：

- 自动写 vault report；
- 自动 raw capture 外部 API 内容；
- 自动 organize、expand 或 approve；
- 自动修复 broken links、tags 或 frontmatter；
- 外部 API 写操作；
- 任意 shell、任意 HTTP、任意 Obsidian CLI；
- 把 Skill payload 当成命令直接执行。

## Policy

Phase 5 第一版的 policy 可以先按 capability 处理，而不是按 vault operation 处理：

```text
allowed:
  - vault_read
  - external_info_read
  - scheduler_run_log_write
  - outbox_notify

requires_user_confirmation:
  - accept_suggested_raw_capture
  - run_candidate_command
  - start_existing_organize_or_expand_workflow

disabled:
  - vault_write
  - vault_executor_apply
  - auto_approve
  - external_api_side_effect
  - arbitrary_shell
  - arbitrary_file_write
```

如果用户确认某个候选动作，后续动作离开 Scheduler 边界，由 OpenWhisker 内部调用既有 workflow，并继续受 `VaultPlan -> Policy -> Approval -> Executor` 约束。

Suggested raw capture 是内部结果类型，不是用户需要理解的操作流程。对外只展示“是否收录这条信息”一类自然确认文案；确认后的 raw capture 调用由 OpenWhisker 内部完成。当前确认入口只走显式 CLI 或 slash command：`openwhisker scheduler accept <run_id>` / `/scheduler accept <run_id> [item_number]`。它只从 `payload.suggested_raw_captures` 中抽取 string 条目，或 object 条目的 `text` / `raw_text` / `content` / `body` / `summary` 字段，不把 payload 当成命令执行。后续如果要接入 Matrix Intent Router，也必须保持“用户显式确认后才进入 raw capture”的边界。

## 验收

第一版文档和后续实现应满足：

```text
Scheduler 可以按 schedule 运行 vault-local Skill。
Skill 可以读取被允许的 vault context。
Skill 可以读取被允许的外部信息源。
Skill result 进入 OpenWhisker run log。
用户通过 outbox / Matrix 收到简报或提醒。
Scheduler 不写 vault。
Scheduler 不调用 VaultExecutor。
Scheduler 不自动创建或 approve VaultPlan。
外部 API 内容不会在未确认时进入 Raw/Inbox。
Skill payload 的粒度由 Skill/config 决定，而不是由 Scheduler Core 写死。
```

## 已确认决策

- Registry 第一版只支持 skill-local `SCHEDULE.md`，不支持集中 registry 文件。
- Scheduled Skill manifest 复用同目录 `SKILL.md`。
- `SCHEDULE.md` 使用 Markdown + frontmatter。
- `SCHEDULE.md` 隐式使用同目录 `SKILL.md`，不提供 `skill_ref` 或跨目录引用机制。
- 时间规则使用 `cron_expr + timezone`。
- `delivery` 第一版只支持 `outbox`。
- `capabilities` 第一版只允许 read-only / run-log / outbox 类能力，禁用 vault 写入、自动审批、外部副作用、任意 shell、任意 HTTP 和任意文件写。
- `vault_context` 必须落在 profile 声明的 `read_only_vault_roots` 内。
- `external_info_sources` 必须落在 profile 声明的允许列表内；当前代码已有 info-only adapter 接口，但默认 CLI 尚未配置真实 adapter。
- Scheduler Host 已有 `SkillEngine` / `ExternalInfoAdapter` 接口；CLI 默认使用 safe static engine，可显式使用 OpenAI-compatible Scheduler Engine。
- `skill_config` 对 Scheduler Core 是 opaque payload。
- Outbox 第一版只做 generic 展示，不提供 Skill-specific renderer。
- Suggested raw capture 是内部结果类型；对外只展示自然确认文案，确认后由 OpenWhisker 内部调用既有 raw capture。
- 同一个 schedule 不并发运行；上一次仍在 `running` 时，本次 due tick 记录 `skipped`。
