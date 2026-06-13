# 只读定时 Skill 调度

本文描述 OpenWhisker 的 Scheduler 子系统：一个按时间触发的 **read-only Skill Host**。它读取 vault 上下文和外部信息源，生成个人简报、提醒、候选输入或后续动作建议，并通过 outbox 推送给用户。

写入主链路解决的是"用户触发后如何安全地把知识变更写入 vault"；Scheduler 解决的是另一类问题：系统如何在合适的时间主动把该看的信息推到用户面前。因此 Scheduler 被建模为新的**输入和观察层**，而不是新的写入层。

完整写入边界与风险分级见[设计哲学](../20-architecture/design-philosophy.md)；agent 运行时见[Agent 工具调用](agent-tooling.md)。

## 定位

Scheduler 是：定时触发器、vault-local Skill Host、只读 vault context provider、外部信息源编排器、run state / outbox 记录器。

Scheduler 不是：`VaultExecutor`、approval bypass、自动 raw capture 入口、自动 organizer、自动 maintenance writer、任意外部 API 执行器。

不可破的边界：

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
  -> VaultReader (read-only)
  -> External API adapters (info-only)
  -> Skill result envelope
  -> Scheduler run log
  -> Outbox / Matrix notify
```

Scheduler Host 负责运行协议，不负责业务判断；业务判断属于 vault-local Skill。

## 交互身份：Scheduler Bot

Scheduler 在交互层有独立的 bot identity（不是新的写入权限主体）。同一个 Matrix room 里可以同时存在两个 bot：

- **Knowledge Bot** 处理用户主动触发的 capture / organize / expand / diff / approve / reject；
- **Scheduler Bot** 发送定时简报、RSS 观察、提醒和"是否收录这条信息"的确认文案。

两个 bot 共享同一个后端、SQLite 状态和 vault policy。outbox / Matrix delivery 用 `actor` 字段区分投递身份：Scheduler 消息归属 `scheduler` actor，知识处理消息归属 `knowledge` actor；未配置 Scheduler Bot 凭据时退回 Knowledge Bot 发送，delivery 忽略两个 bot 自己发出的消息。Scheduler Bot 仍遵守 Scheduler 的只读边界。

## Skill-driven 组织方式

Scheduled workflow 由 `VaultProfile.scheduler` 开启，并由 vault-local Skills 组织。runtime 不把某个 vault 的简报 / 维护检查 / 外部信息源规则硬编码进通用代码，也不自己扫描整个 vault 去发现可运行的 Skill。

`VaultProfile.scheduler` 声明：是否启用、registry 可读取的 vault-local 路径、scheduled skill roots、默认 delivery、只读 vault roots、允许的外部信息源。

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

registry loader 只读取 profile 声明允许的位置。Profile 未开启 scheduler 或未声明 registry path 时，Scheduler 不自动扫描整个 vault。

约束：

- registry 只支持 skill-local `SCHEDULE.md` 形态（如 `Scheduler/Skills/*/SCHEDULE.md`），不支持集中 registry 文件；
- `SCHEDULE.md` 必须位于 `scheduled_skill_roots` 允许的目录下，隐式绑定同目录 `SKILL.md`，无跨目录 Skill 引用机制；
- scheduled Skill manifest 复用同目录 `SKILL.md`，不引入新的 manifest；
- Scheduler 不读取普通 `Skills/`，避免和面向通用 agent workflow 的 vault Skills 混淆。

## SCHEDULE.md

Markdown + frontmatter：frontmatter 承载机器可读的 schedule declaration，正文承载人读说明。

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

字段语义：

- 时间规则使用 `cron_expr`（5 字段）+ `timezone`，不另定义 daily / weekly 语义层；
- `delivery` 只支持 `outbox`；Matrix 投递通过 outbox adapter 完成，并以 actor identity 选择投递 bot；
- `capabilities` 是运行边界声明，不是授权绕过。只允许 `vault_read` / `external_info_read` / `scheduler_run_log_write` / `outbox_notify`；`vault_write` / `vault_executor_apply` / `auto_approve` / `external_api_side_effect` / `arbitrary_shell` / `arbitrary_http` / `arbitrary_file_write` 在 registry load 阶段被拒绝；
- `vault_context` 是相对路径列表，不能含 parent traversal / wildcard，必须落在 `read_only_vault_roots` 内；文件读取带大小上限，目录只读直接子项清单，不递归扫描；
- `external_info_sources` 必须出现在 `VaultProfile.scheduler.external_info_sources` 中；
- `skill_config` 对 Scheduler Core 是 opaque payload，只解析、保存、传给同目录 Skill，不据此执行写入或外部副作用。

## 外部信息源：RSS / Atom

第一个真实外部信息源是 `rss`（能力名兼 adapter 类型）。RSSHub、Folo 或普通站点 feed 都只是 `rss` 的上游来源——只要产出标准 RSS / Atom，Scheduler 不理解上游路由语义，Core 不出现 `rsshub` / `folo` 内置业务分支。

```text
Scheduler
  -> rss info-only adapter
  -> read feed URLs from scheduled Skill config
  -> parse RSS / Atom items
  -> pass external_info to SkillEngine
  -> Skill summarizes / filters
  -> outbox notifies user
```

feed URL 由具体 `SCHEDULE.md` 的 `skill_config` 提供（`feed_urls` / `feed_limit`），不先做中心订阅库。adapter 边界：

- 只读取 profile 已允许的 `rss` source；
- 只做 `GET http/https` + 大小限制 + 条数限制 + 字段裁剪，只解析 RSS / Atom 返回信息快照，不写 vault、不创建 raw capture；
- 不发送 cookie / authorization header / 用户凭据；
- payload 中的 feed URL 去掉 query / fragment / userinfo，避免把 token 型 URL 落入 run log；
- 账号 API、订阅变更、标记已读、收藏、关注等副作用不在范围内。

## SkillEngine

`SkillEngine` 是 Scheduler Host 与实际 reasoning 层之间的接口。Host 加载同目录 `SKILL.md`、构造受限 `vault_context`、调用 info-only external adapters，把输入交给 engine。

- 默认 **static engine** 只生成 generic outbox 摘要；
- 可显式启用 **OpenAI-compatible Scheduler Engine**，只能消费 Host 给出的受限输入，返回 `title` / `summary` / opaque JSON `payload`，不能自行读 vault、调用任意 HTTP、执行 shell、调用 `VaultExecutor` 或写文件；
- 声明了 `vault_tools` 的 Skill 走 tool-calling 路径（见[Agent 工具调用](agent-tooling.md)）；engine 选择优先级为 `SKILL.md.engine` → CLI flag → profile default → 编译期默认（static）。选中的 engine 不可用时 run 直接 failed，**不静默降级**。

## 结果模型与运行状态

最小 run result envelope（具体 payload schema 由 Skill 决定）：

```text
SchedulerRunResult:
  run_id / schedule_id / skill_id / status
  started_at / finished_at
  title / summary / payload / error
```

`title` / `summary` 用于 outbox 默认人读展示；`payload` 是 Skill-owned JSON；第一版 outbox renderer 只做 generic 展示，**不把 payload 当命令执行**。

分层持久化：

```text
VaultProfile scheduler declaration   human-owned, 从确认后的 profile 读取
Vault-local schedule registry        human-owned, 对 OpenWhisker 只读
scheduler_runtime (SQLite)           runtime-owned, 解析后的 schedule identity / hash / next_run / last_run / lock
scheduler_runs    (SQLite)           runtime-owned, 每次执行结果与 outbox linkage
```

runtime state 不写 vault；vault-local registry 被用户修改时，下次 tick 只读重新加载并 reconcile，不回写 `last_run_at` / `next_run_at` 到 vault。

## Tick 与并发

```text
scheduler tick
  -> load confirmed VaultProfile scheduler declaration
  -> read declared vault-local registry paths
  -> resolve each SCHEDULE.md to its same-directory SKILL.md
  -> reconcile scheduler runtime state
  -> find due schedules
  -> create scheduler run -> invoke Scheduler Host -> write run result
  -> enqueue outbox message -> update last_run_at / next_run_at
```

`tick` 幂等：同一 schedule 在同一 due window 内不因 daemon 重启或重复调用产生多次有效 run。同一 schedule 不并发运行；上一次仍在 `running` 时，本次 due tick 记 `skipped`。Skill run 失败也写 run log + outbox error 摘要，错误摘要只含 schedule id / Skill 名称路径 / 安全错误信息，不含 secrets / tokens / 完整外部 response / 未脱敏 payload。

## Daemon

长期运行主入口是独立 `openwhisker daemon`（语义是 OpenWhisker workflow host 常驻进程，而非某个 IM adapter 的附属循环）：

```text
openwhisker daemon
  -> scheduler tick loop
  -> outbox delivery loop
  -> optional Matrix poll loop
```

- Scheduler 不依赖 Matrix：无 Matrix 配置时 daemon 仍 tick，outbox 留 pending；
- Matrix 是可选 delivery / command adapter；`--matrix auto|on|off` 控制启用；
- scheduler tick 后尽快触发 outbox delivery，但 delivery 失败不回滚 scheduler run log；
- 并发模型：scheduler ticker + Matrix long-poll 两个 goroutine 共享单连接 `storage.Store`，不引入 supervisor / 多 worker，不并发执行同一 schedule；tick error 写 stderr 并等下一轮，不退出进程。

部署模板见[部署指南](../50-deployment/README.md)（macOS launchd / Linux systemd）。

## suggested_raw_capture：外部信息转 Raw

Skill 认为某条外部信息值得保存时，只能在 payload 里给出**内部 suggested raw capture**，由 outbox 以人读文案请求用户确认。Scheduler 本身不执行写入：

```text
Skill emits internal suggested raw capture in payload
  -> outbox asks user to confirm
  -> user confirms via IM / CLI
  -> OpenWhisker internally calls existing raw capture workflow
  -> existing low-risk policy and executor handle Raw/Inbox write
```

确认入口走显式命令：`openwhisker scheduler accept <run_id>` / Matrix `/scheduler accept <run_id> [item_number]`。它只从 `payload.suggested_raw_captures` 抽取 string 条目或 object 条目的 `text` / `raw_text` / `content` / `body` / `summary` 字段，不把 payload 当命令执行，确认后的 raw capture 仍受 `WikiJob → VaultPlan → Policy → Approval → VaultExecutor` 约束。

## 可观测与管理

- `openwhisker scheduler status` / `scheduler runs [limit]`、Matrix `/scheduler status` / `/scheduler runs`：查看 runtime、next run 和最近 run；
- `openwhisker scheduler schedules list|enable|disable`：通过 SQLite override 管理 schedule 生效状态，不直接改 vault 中的 `SCHEDULE.md`；
- `openwhisker daemon status`：daemon 状态文件。
