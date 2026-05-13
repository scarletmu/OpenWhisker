# Phase 4 Wiki Agent Workflow

状态：推进中。Phase 4A 已完成第一版代码切片；Phase 4B 已接入真实 LLM-backed Raw Organizer 的最小版本，并补上第一版 agent output policy gate、长期 Matrix daemon 和 `organize today` grouped plan。默认仍使用 deterministic planner，真实 provider 需要显式启用。

这是 Phase 3 完成 sync-aware approval execution 之后，下一阶段值得推进的设计边界。

当前实现状态：Phase 1 到 Phase 3 已经验证了低风险 raw capture、中风险 approval/diff/hash guard，以及 approval apply 前后的 Headless one-shot sync。Phase 4A 已经补上 Matrix IM 入口 MVP 和长期 daemon；Phase 4B 已经补上可显式启用的真实 LLM-backed `organize last` 最小路径。默认仍使用 deterministic planner，真实 provider 优先按 OpenAI-compatible Chat Completions endpoint 接入，需要 `--organizer=openai-compatible` 和本地 API key。

2026-05-13 代码进展：

- 已新增 Core Adapter API：支持普通文本 raw capture、`/raw`、`/organize last`、`/diff`、`/approve`、`/reject`、`/status`、`/jobs`。
- 已新增 Matrix event 去重表和 outbox polling / delivery ack 存储能力。
- 已把 `organize last` 的 planner 抽象为 `RawOrganizer` 合同；当前默认实现仍是 deterministic planner，OpenAI-compatible provider 已接入这个边界并可显式启用。
- 已新增 Matrix Adapter MVP：支持 Matrix `/sync` 单次轮询、文本消息入站、调用 Core Adapter API、从 outbox 回发 Matrix 文本消息。
- 已新增 CLI 入口 `openwhisker matrix poll-once`，用于手动验证 Matrix 单次轮询。
- 已新增 CLI 入口 `openwhisker matrix daemon`，用于长期运行 Matrix `/sync` 循环；支持 `since` token 本地持久化、interrupt / SIGTERM 正常退出、错误退避和 pending outbox 继续投递。
- 已新增第一版 agent output policy gate：medium-risk plan 必须有 source refs、target paths、operation reason/payload，Knowledge draft 必须有 frontmatter、受控 tag、`needs_review` 和 Raw/Processed 正文链接。
- 已新增 `organize today` 最小 grouped plan：当天仍在 `Raw/Inbox` 的 raw captures 会生成一个 grouped Knowledge draft 和多条 approved raw move。

2026-05-13 追加代码进展：

- 已新增 `internal/agent` LLM reasoning adapter 边界，明确禁止 provider 直接写 vault、执行 shell 或调用 Obsidian CLI。
- 已新增 OpenAI-compatible Chat Completions Raw Organizer 最小实现：读取受控 raw context，要求 structured JSON 输出，并把结果归一化为 medium-risk `VaultPlan`。
- 已新增 Raw Organizer Context Builder：读取 raw note、vault root `AGENTS.md`、`Meta/README.md`、`Meta/Tagging.md`、`Raw/AGENTS.md` 和 `Knowledge/AGENTS.md`；不读取隐藏路径、`.obsidian`、`.git`、secrets 或 vault root 外文件。
- CLI `organize last`、Matrix `poll-once` 和 Matrix `daemon` 已支持 `--organizer deterministic|openai-compatible`；默认 deterministic，启用兼容 endpoint 时优先使用 `OPENWHISKER_LLM_API_KEY`、`OPENWHISKER_LLM_BASE_URL` 和 `OPENWHISKER_LLM_MODEL`。
- 已扩展 `move_note` payload：approved raw move 会在 `Raw/Processed/` note 末尾追加 OpenWhisker processing note，包含 plan job、raw job、处理时间、Raw/Processed 路径、Knowledge output link 和剩余 review 项。
- 已新增 provider contract tests、Chat Completions request tests、context builder tests 和 CLI organizer selection tests。
- 已新增 policy regression tests，覆盖缺 source refs、缺 frontmatter、缺 Raw/Processed 正文链接、move destination 未列入 target paths 等 agent 输出拒绝场景。
- 尚未完成 Knowledge Expander。

最小启用方式：

```sh
export OPENWHISKER_LLM_API_KEY=...
export OPENWHISKER_LLM_BASE_URL=https://your-compatible-endpoint.example/v1
export OPENWHISKER_LLM_MODEL=your-model
go run ./cmd/openwhisker organize last --organizer=openai-compatible
go run ./cmd/openwhisker organize today --organizer=openai-compatible
go run ./cmd/openwhisker matrix poll-once --organizer=openai-compatible
go run ./cmd/openwhisker matrix daemon --organizer=openai-compatible
```

可选配置：

```sh
export OPENWHISKER_LLM_ORG_ID=...
export OPENWHISKER_LLM_PROJECT_ID=...
```

旧的 `OPENWHISKER_OPENAI_*` 和 `OPENAI_API_KEY` 只作为兼容 fallback 保留；新文档、新配置和 `.env.local` 模板应优先使用 `OPENWHISKER_LLM_*`。官方 OpenAI 只是 OpenAI-compatible endpoint 的一种可选实现，不是 Phase 4 的默认口径。

## 下一步真实环境验证顺序

Phase 4 代码闭环已经可以进入真实环境验证，但验证顺序应保持保守：

1. **真实 Matrix + test vault**：先运行 `matrix daemon --vault testdata/vault --organizer=deterministic`，验证 IM 收发、event 去重、outbox 投递、`/organize last`、`/organize today`、`/diff`、`/approve` 和 `since` token 持久化，不污染真实 vault。
2. **真实 vault + deterministic organizer**：再切到 `/Users/wang/Documents/KnowLedge`，验证 vault `AGENTS.md` context、`Raw/Inbox`、`Raw/Processed`、`Knowledge/Drafts`、approval apply 和 Headless Sync 行为，保持输出稳定可控。
3. **真实 vault + OpenAI-compatible LLM**：最后启用 `--organizer=openai-compatible`，验证真实整理质量。中风险 plan 仍必须走 diff、approval、hash guard 和 `VaultExecutor`，不直接写正式 Knowledge note。

这三步通过后，再继续推进 Knowledge Expander、high-risk proposal policy、真实 Matrix 部署固化和多房间 / room-scoped outbox。

Phase 4 的目标不是绕过现有 executor，也不是先实现 desktop Obsidian CLI executor 或 scheduler，而是打通真实端到端 vertical slice：Matrix IM 作为首期核心交互入口，Wiki Agent Host 使用真实 LLM provider 生成结构化 `VaultPlan`，中风险变更继续经过 policy、diff、approval、hash guard 和 sync-aware executor。LLM 可以读取受控 vault context 并生成计划，但仍然不能直接写 vault、不能执行 shell、不能自由调用 Obsidian CLI；Matrix Adapter 也不能直接调用 LLM 或写 vault。

## 本次推进结论

Phase 4 主线是真实 LLM + Matrix IM approval workflow，拆成三个连续切片：

1. **Phase 4A：Core Adapter API + Matrix Adapter MVP + Agent Host Contract**。先定义 Core 给 IM adapter 使用的稳定入口、Matrix 入站/出站最小闭环、Wiki Agent Host 的输入输出边界、vault context 读取范围、结构化 plan 校验和 fake/fixture agent 验证方式。
2. **Phase 4B：Real LLM-backed Raw Organizer**。接入真实 LLM provider，让 Matrix `/organize last` 从 raw note、vault rules、已有 wiki context 中生成可审批 `VaultPlan`，并通过 Matrix 完成 diff、approval、reject 和结果回传。当前已完成最小 OpenAI-compatible provider 接入，后续补齐更完整的 validator、上下文检索和 Raw/Processed 处理记录。
3. **Phase 4C：Organize Today + Knowledge Expander + Proposal Policy**。让 `organize today` 支持多 raw 分组整理，并让 Knowledge Expander 支持扩展、拆分建议、hub / child note proposal；高风险 split / merge 默认只生成 proposal note。

Phase 4 必须包含真实 LLM provider 接入；fake / fixture agent 只作为 contract、validator 和 regression test 的替身。Provider 细节必须被隔离在 Agent Host 边界内。Core、Policy、Executor 和 Matrix Adapter 只消费结构化 job、plan、outbox 或 command result，不依赖具体模型或 SDK。

## 目标

构建 Wiki Agent workflow 计划层，使 OpenWhisker 可以：

1. 通过 Matrix IM 接收 raw input、命令、approval 和 reject；
2. 通过 Core Adapter API 把 IM event 转换为受控 `WikiJob`、`VaultPlan` 生命周期操作和 `OutboxMessage`；
3. 为 agent 组装受控 vault context；
4. 读取最近适用的 vault `AGENTS.md` 和 workflow 文档；
5. 让真实 LLM-backed Raw Organizer 生成可审计的 `VaultPlan`；
6. 让 Knowledge Expander 生成扩展计划或 proposal；
7. 在 plan 进入执行前校验 source traceability、frontmatter、controlled tags 和风险等级；
8. 对 medium-risk plan 继续走 Phase 2 / Phase 3 的 diff、approval、hash guard 和 sync-aware apply；
9. 对 high-risk restructuring 默认只写 proposal 或 report；
10. 保持 Matrix Adapter、LLM reasoning 与 deterministic vault write 的安全隔离。

## 初始支持的 workflow

Raw Organizer：

```text
Matrix /organize last or CLI organize last
  -> Core Adapter API / command router
  -> Raw/Inbox or Raw/Sources note
  -> WikiJob(type=organize_raw)
  -> Vault Context Builder reads raw + nearest AGENTS.md + limited wiki context
  -> Wiki Agent Host / Raw Organizer generates VaultPlan
  -> policy validates source traceability, paths, metadata, risk
  -> direct_fs_executor prepares diff + before_hash
  -> plan status awaiting_approval
  -> approve / reject
  -> sync-aware VaultExecutor apply
  -> Raw/Processed record + output links
  -> operation log + outbox result
  -> Matrix reply when invoked from Matrix
```

Organize today：

```text
Matrix /organize today or CLI organize today
  -> unprocessed Raw/Inbox notes from current day
  -> grouped WikiJob(type=organize_raw_today)
  -> Raw Organizer generates grouped draft or deterministic grouped fallback
  -> one grouped VaultPlan
  -> approval prompt with grouped summary
  -> Matrix approval prompt when invoked from Matrix
```

Knowledge Expander：

```text
Matrix command, CLI command, or selected topic
  -> WikiJob(type=expand_knowledge)
  -> Vault Context Builder reads target note + local AGENTS.md + related notes
  -> Knowledge Expander generates append / create draft / proposal plan
  -> policy classifies risk
  -> medium risk waits for approval
  -> high risk writes proposal note only
```

## Phase 4A：Core Adapter API + Matrix Adapter MVP + Agent Host Contract

目标：先把 IM 入口、Core command 边界和 agent reasoning 边界固定下来，让后续真实 LLM-backed workflow 可以测试、审计和替换。

范围内：

- 定义 Core 给 adapter 使用的稳定入口：raw input、command、approval、reject、status 和 outbox polling / delivery ack。
- 实现 Matrix Adapter MVP：bot `/sync` 入站、Matrix event 去重、普通文本 raw capture、基础 `/status` / `/jobs` / `/diff` / `/approve` / `/reject` 命令、outbox 回传。
- Matrix Adapter 只调用 Core 入口，不直接写 vault、不直接调用 LLM、不执行 shell、不调用 Obsidian CLI。
- 定义 Wiki Agent Host 的稳定职责：接收 job、user intent、受控 vault context，返回结构化 `VaultPlan` 或 proposal result。
- 定义 Raw Organizer 和 Knowledge Expander 的最小输入输出契约。
- 定义 vault context 读取规则：raw note、最近适用 `AGENTS.md`、`Meta/README.md`、`Meta/Tagging.md`、必要的 destination guide 和有限 wiki search 结果。
- 定义 fake / fixture agent 验证方式，确保 adapter、contract 和 plan validation regression 不依赖真实模型。
- 定义 agent output 的校验要求：合法 operation、clean relative path、source refs、target paths、risk、reason 和 metadata。

范围外：

- 真实 LLM provider 的完整生产化；Phase 4B 已有最小 OpenAI-compatible provider，后续补齐重试、更多 validator 和真实 Matrix 环境验证。
- `organize today`。
- Knowledge Expander。
- scheduler 和 maintenance agent。
- sandbox runtime。
- arbitrary tool use。
- 自动执行 high-risk restructure。

验收：

```text
Matrix 普通文本可以创建 low-risk raw capture job，并通过 outbox 回 Matrix。
Matrix /approve 和 /reject 可以推进已有 awaiting_approval plan。
Matrix event 重试不会重复创建 job，outbox 重试不会重复发送同一业务消息。
fixture agent 输出的合法 VaultPlan 可以进入现有 approval pipeline。
缺 source_refs、越界路径、未知 operation、缺 metadata 的 agent 输出会被拒绝。
vault context 不读取 .obsidian、.git、secrets、隐藏路径或 vault root 外文件。
```

## Phase 4B：Real LLM-backed Raw Organizer

目标：接入真实 LLM provider，替换 `organize last` 的 deterministic Phase 2 planner，让 raw 整理能力遵守 vault 的真实组织规则，并通过 Matrix 完成审批体验。

范围内：

- 选择并接入第一版真实 LLM provider；模型、密钥、超时和结构化输出解析隔离在 Wiki Agent Host 内。第一版优先接入 OpenAI-compatible Chat Completions API，使用 structured JSON 输出；重试策略仍待补齐。
- CLI `organize last` 和 Matrix `/organize last` 都可以通过 `--organizer=openai-compatible` 使用 Raw Organizer 生成 plan；默认仍保持 deterministic，避免无意触发外部模型调用。
- Matrix `/diff <job_id>`、`/approve <job_id>`、`/reject <job_id>` 返回用户可读的审批和执行结果。
- 支持 `raw_kind` 推断：`concept-seed`、`web-clip`、`todo-list`、`llm-chat`、`mixed`。
- 计划必须保留 raw source，并在完成后写入 `Raw/Processed/` 处理记录和 output links。第一版已通过 `move_note.processing_note` 写入。
- Knowledge 输出必须包含 frontmatter、controlled tags、source link、`待核查` 或 needs-review 标记。
- mixed raw 默认优先生成拆分建议，只有边界清楚时才生成多个输出。

范围外：

- 更细粒度的 `organize today` 自动主题聚类和 proposal 拆分策略。
- 把 Raw 直接复制进 `Knowledge/`。
- 未审批地 patch 正式 Knowledge note。
- 外部网页抓取和当前资料核验的完整自动化；需要外部验证时先标记 review 或在后续工具阶段处理。

验收：

```text
真实 LLM-backed organize last 可以根据 raw 内容生成比 Phase 2 deterministic draft 更具体的 plan。已完成最小 provider contract 和 fake HTTP regression；真实网络调用需本地密钥配置后手动验证。
Matrix /organize last 可以收到 approval prompt，并用 /diff、/approve、/reject 推进同一个 plan。代码路径已支持 `matrix poll-once --organizer=openai-compatible` 和 `matrix daemon --organizer=openai-compatible`；真实长期环境仍需要本地 Matrix 配置后验证。
Raw/Processed 记录包含原始来源、产出链接、处理说明和剩余 review 项。第一版已落地到 approved `move_note` 的 processing note append。
fixture agent 仍可在测试中替代真实 provider。
```

## Phase 4C：Organize Today + Knowledge Expander + Proposal Policy

目标：让 agent 可以批量整理当天 raw 并维护长期 `Knowledge/`，但对结构性重构保持保守。

范围内：

- `organize today` 为多条 raw 生成分组摘要、目标路径和审批计划。第一版已生成单个 grouped plan。
- Matrix `/organize today` 返回 grouped approval prompt。第一版已接入 Core Adapter API。
- 扩展现有 thin Knowledge note。
- 为已有 topic 生成 append plan 或 child note proposal。
- 支持 hub / child note 结构建议。
- 对 broad topic、split、merge、rename、大规模 link rewrite 生成 proposal note。
- proposal 必须说明来源、目标结构、影响路径、建议操作和需要人工确认的问题。

范围外：

- 自动执行 high-risk split / merge。
- 自动大规模 rename 或 backlink rewrite。
- 直接修改 `.obsidian` 配置或插件数据。
- 让 LLM 自由调用 Obsidian CLI。

验收：

```text
中风险 Knowledge 扩展可以生成 diff 并等待 approval。
高风险 split / merge 只生成 proposal note，不改写正式 Knowledge note。
proposal note 能让用户理解结构变化、来源依据、影响路径和下一步审批方式。
```

## 已确认的 Phase 4 决策

- Phase 4 主线是真实 LLM + Matrix IM approval workflow。
- Matrix Adapter 是首期核心 IM 入口，但只负责交互、命令路由、去重和 outbox 投递。
- Matrix Adapter 不直接调用 LLM、不写 vault、不执行 shell、不调用 Obsidian CLI。
- Phase 4 不替代 Phase 1 到 Phase 3 的 safety pipeline；agent 只替换 reasoning / planning 层，adapter 只替换人工 CLI 交互层的一部分。
- Phase 4 必须接入真实 LLM provider；fake / fixture agent 只作为测试替身。
- LLM 只生成结构化 `VaultPlan` 或 proposal，不直接写 vault。
- LLM 不获得 shell，不自由调用 Obsidian CLI，不直接调用 Headless `ob`。
- Core、Policy、Executor 不依赖具体 LLM provider。
- 默认 vault target 仍是本地 test vault；真实 vault 仍需用户显式传入。
- 真实 vault approval 继续继承 Phase 3 的 sync-aware apply 行为。
- scheduler、maintenance agent 和 sandbox runtime 留给后续阶段或独立文档。

## 范围外

- Wiki Reader 问答体验。
- Maintenance Agent。
- Scheduler Agent。
- Desktop Obsidian CLI executor。
- Obsidian plugin 工作。
- Headless Sync setup、login、logout、config、unlink。
- Arbitrary shell 或任意 tool execution。
- CubeSandbox runtime 接入。
- 高风险 vault restructuring 自动执行。
- 写入旧 FlashBang 仓库。

## 最小 policy

Phase 4 继续沿用 plan-before-write 的安全边界，并补充 agent output 约束。

允许：

- low-risk raw capture 自动执行；
- Matrix 普通文本和受控命令进入 Core，不绕过 job / plan lifecycle；
- medium-risk Raw Organizer 和 Knowledge Expander plan 进入 approval；
- high-risk restructuring 生成 proposal note 或 report；
- proposal / report 写入受控路径；
- approved plan 通过现有 `direct_fs_executor` 执行。

必须满足：

- plan 必须包含 `source_refs`；
- durable note 必须包含 frontmatter 和 controlled tags；
- Raw-derived note 必须链接回 `Raw/Processed/`；
- uncertain、version-sensitive 或 inferred 内容必须标记 review；
- operation target path 必须是 clean relative path；
- append / move / patch 类操作必须有 `before_hash`；
- high-risk split / merge 不得直接执行。

继续阻止：

- Matrix Adapter 直接写 vault、直接调用 LLM、执行 shell 或调用 Obsidian CLI；
- LLM 直接写文件；
- LLM 直接执行 shell；
- LLM 任意调用 Obsidian CLI 或 Headless `ob`；
- 写出 vault root；
- 修改 `.obsidian`、`.git`、secrets 或 hidden path；
- 无 source trace 的 durable Knowledge 写入；
- 未审批 medium-risk 写入；
- critical-risk action。

## CLI 与 Matrix 验收

Phase 4 继续保留 CLI regression 命令：

```sh
go run ./cmd/openwhisker organize last
go run ./cmd/openwhisker organize today
go run ./cmd/openwhisker plan diff <plan_id|job_id>
go run ./cmd/openwhisker plan approve <plan_id|job_id>
go run ./cmd/openwhisker plan reject <plan_id|job_id>
```

Matrix 文档层面的目标命令：

```text
普通文本
/raw <text>
/organize last
/organize today
/diff <job_id>
/approve <job_id>
/reject <job_id>
/status [job_id]
/jobs
```

Knowledge Expander 的用户入口可以在实施前再确认命令形态，例如：

```sh
go run ./cmd/openwhisker knowledge expand <path-or-topic>
```

本文不把该命令视为已确认接口。

## 测试场景

- Matrix 普通文本能创建 low-risk raw capture job，并收到 result outbox。
- Matrix `/organize last` 能触发真实 Raw Organizer workflow，并收到 approval prompt。
- Matrix `/diff`、`/approve`、`/reject` 能推进 Core 中同一个 plan。
- Matrix event 去重和 outbox retry 不产生重复 job 或重复业务消息。
- fake / fixture agent 输出合法 plan 时，可以进入 prepare diff 和 approval。
- 真实 LLM provider 输出合法 plan 时，可以进入 prepare diff 和 approval。
- agent 输出非法 JSON 或缺必需字段时，job failed，outbox 返回可见错误。
- agent 输出越界路径、hidden path、absolute path 或未知 operation 时，被 policy 拒绝。
- agent 输出缺 source trace 的 Knowledge plan 时，被 policy 拒绝。
- `organize last` 保留 Phase 2 approval、reject、conflict regression。
- `organize today` 能把多条 raw 分组，并保留每条 raw 的 source trace。第一版已落地为单 grouped plan。
- mixed raw 在边界不清时生成 proposal，而不是直接创建多个长期 note。
- high-risk split / merge 只写 proposal note，不修改正式 Knowledge note。
- Phase 3 sync-aware approval 行为继续保持：pre-sync failure 不写 vault，post-sync failure 只产生 warning。

## Review 问题

- 真实 LLM provider 的第一版选择、模型、密钥来源和配置方式是否需要单独 ADR。
- Core Adapter API 是先做进程内接口，还是同时暴露本机 HTTP API 供 Matrix Adapter 调用。
- Matrix Adapter MVP 与 Synapse / Caddy / Docker Compose 部署是否放在同一 Phase 4A PR，还是拆成运行部署文档和 adapter 代码两步。
- proposal note 的默认路径是 `Meta/Proposals/`，还是继续使用 `Knowledge/Drafts/` 作为 staging 区。
- `Knowledge/Drafts/` 在 Phase 4 后是保留为草稿区，还是只服务 Phase 2 deterministic workflow。
- `organize today` 第一版已生成单个 grouped plan；后续是否按主题自动拆成多个 plan 仍需在真实使用后评估。
- `expand knowledge` 的第一版入口是 CLI topic/path，还是只作为内部 workflow。
- frontmatter 和 controlled tags 的第一版硬闸门已放在 policy 层；后续可在 provider 侧补更早的友好错误，但不能替代 policy。
- `/replan` 是否应在 Phase 4B 一起设计，还是等 agent workflow 稳定后再加入。
