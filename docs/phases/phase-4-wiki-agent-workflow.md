# Phase 4 Wiki Agent Workflow

状态：已收束。Phase 4A / 4B / 4B.5 / 4C.1 / 4C.2 全部落地，4C.3 / 4C.4 主动取消，Phase 4 至此收尾。默认仍使用 deterministic planner，真实 provider 需要显式启用。各子阶段验证状态以 [`docs/progress.md`](../progress.md) 为准；完整设计弧线见 [`docs/architecture/openwhisker-v1-review.md`](../architecture/openwhisker-v1-review.md)。

这是 Phase 3 完成 sync-aware approval execution 之后，下一阶段值得推进的设计边界。

当前实现状态：Phase 1 到 Phase 3 已经验证了低风险 raw capture、中风险 approval/diff/hash guard，以及 approval apply 前后的 Headless one-shot sync。Phase 4A 已经补上 Matrix IM 入口 MVP 和长期 daemon；Phase 4B 已经补上可显式启用的真实 LLM-backed `organize last` 最小路径，并接入第一版 agent output policy gate 和 Raw/Processed processing note。Phase 4C.1 / 4C.2 已落地（详见下方子阶段）。默认仍使用 deterministic planner，真实 provider 优先按 OpenAI-compatible Chat Completions endpoint 接入，需要 `--organizer=openai-compatible` 和本地 API key。

近期代码 / 验证进展以 [`docs/progress.md`](../progress.md) 为准；本文不再镜像 commit 级别 changelog。

最小启用方式：

```sh
export OPENWHISKER_LLM_API_KEY=...
export OPENWHISKER_LLM_BASE_URL=https://your-compatible-endpoint.example/v1
export OPENWHISKER_LLM_MODEL=your-model
go run ./cmd/openwhisker organize last --organizer=openai-compatible
go run ./cmd/openwhisker matrix poll-once --organizer=openai-compatible
go run ./cmd/openwhisker matrix daemon --organizer=openai-compatible
```

可选配置：

```sh
export OPENWHISKER_LLM_ORG_ID=...
export OPENWHISKER_LLM_PROJECT_ID=...
```

旧的 `OPENWHISKER_OPENAI_*` 和 `OPENAI_API_KEY` 只作为兼容 fallback 保留；新文档、新配置和 `.env.local` 模板应优先使用 `OPENWHISKER_LLM_*`。官方 OpenAI 只是 OpenAI-compatible endpoint 的一种可选实现，不是 Phase 4 的默认口径。

## 设计收敛：Profile 分析与 Skill 驱动

Skill-driven adapter architecture 的原则（OpenWhisker 固定安全流程，外部对象的本地协作方式由 `VaultProfile` + `VaultSkill` 描述）以 [`architecture/overview.md`](../architecture/overview.md) "Skill-driven 外部对接" 一节为准，本文不再重复。

当前代码已经把 minimal context 调整为：

```text
raw note
  + OpenWhisker/VaultRawOrganizerSkill.md
  + OpenWhisker/VaultProfile.md
```

本地预览当前配置会得到的 Profile / Skill bundle：

```sh
go run ./cmd/openwhisker vault profile preview --vault-profile knowledge-vault
```

从 vault 本地规则生成候选 Profile / Skill 应使用外部 vault-local Skill，模板见 [`docs/skills/vault-profile-analyzer/SKILL.md`](../skills/vault-profile-analyzer/SKILL.md)。

## 下一步真实环境验证顺序

Phase 4 代码闭环已经进入真实环境验证。当前已完成真实 Matrix + test vault deterministic 闭环、真实 vault + deterministic diff/reject、test vault + OpenAI-compatible provider smoke、真实 vault + OpenAI-compatible LLM diff/reject，以及 2026-05-19 完成的真实 vault + OpenAI-compatible LLM **approve/apply** 闭环（CLI 路径，DeepSeek，`--sync=off`）。Matrix 路径下的 LLM approve/apply 已于 2026-05-20 在 `testdata/vault` 上跑通（Matrix daemon + DeepSeek）。

真实环境验证顺序保持保守：

1. **真实 Matrix + test vault**：先运行 `matrix daemon --vault testdata/vault --organizer=deterministic`，验证 IM 收发、event 去重、outbox 投递、`/organize last`、`/diff`、`/approve` 和 `since` token 持久化，不污染真实 vault。
2. **真实 vault + deterministic organizer**：再切到 `/Users/wang/Documents/KnowLedge`，验证 `Raw/Inbox`、`Raw/Processed`、`Knowledge/Drafts`、approval apply 和 Headless Sync 行为，保持输出稳定可控。真实 vault 验证应使用独立本地 SQLite，例如 `OPENWHISKER_DEBUG_DB=data/openwhisker-real.db`，避免和 test vault 的 job / plan / operation log 混在一起。
3. **真实 vault + OpenAI-compatible LLM**：diff/reject 已验证。2026-05-19 完成 approve/apply 闭环（CLI 路径，DeepSeek `deepseek-v4-flash`，`--sync=off`，独立 `data/openwhisker-real-llm-approve.db`），覆盖 `direct_fs_executor` 真实写入、`move_note` Raw/Processed processing note、`vault_operation_logs` 两条 `applied`、`move_note.before_hash` 命中 ingest 时记录的 raw hash。这次验证暴露 Raw Organizer 仍在用 `response_format: json_schema`，与 DeepSeek 不兼容；按 intent classifier 已有的迁移路径切到 `json_object` + 客户端 `validateRawOrganizerOutput` 硬校验，并补单次空 content 重试。中风险 plan 仍必须走 diff、approval、hash guard 和 `VaultExecutor`，不直接写正式 Knowledge note。

本地 ignored debug 脚本可用于这三步验证：

```sh
scripts/local/matrix-debug.sh status
scripts/local/matrix-debug.sh daemon

OPENWHISKER_DEBUG_VAULT=/Users/wang/Documents/KnowLedge \
OPENWHISKER_DEBUG_DB=data/openwhisker-real.db \
scripts/local/matrix-debug.sh daemon
```

`OPENWHISKER_MATRIX_SESSION_FILE` 和 `OPENWHISKER_MATRIX_SINCE_FILE` 可以继续复用；前者保存 Matrix 登录 session，后者避免 daemon 重启后重复消费旧消息。`OPENWHISKER_DEBUG_DB` 建议按 vault 或验证阶段拆分。

进入第 3 步前必须先做一次合成 raw 或 test vault 的 provider smoke，确认兼容 endpoint、模型名、key 权限和 structured JSON 输出可用。不要在权限、模型分组或输出格式尚未确认时，把真实 vault raw/context 发送到外部 LLM endpoint。

外发前可先在本地预览真实 organizer 会读取的上下文。默认 `minimal` 只包含 raw note、由当前 `VaultProfile` 编译出的 `VaultRawOrganizerSkill` 和当前 `VaultProfile` 摘要；如需调试完整 vault 规则，可显式传 `--context-mode=vault-rules`：

```sh
go run ./cmd/openwhisker organize preview-context \
  --db data/openwhisker-real.db \
  --vault /Users/wang/Documents/KnowLedge \
  --vault-profile knowledge-vault
```

该命令只读本地 SQLite 和 vault，不调用 LLM、不生成 plan、不写 vault。

Phase 4 默认不把完整 vault 规则全文发送给外部 LLM。OpenWhisker 只固定执行安全契约：source trace、plan-before-write、risk、approval、path safety 和 deterministic executor。具体 vault 的 raw inbox、processed raw、草稿区、tag 清单等属于 `VaultProfile`，并通过任务 `VaultSkill` 进入 LLM 上下文，不能硬编码成所有 vault 的通用 schema。当前 `knowledge-vault` profile 只是 `/Users/wang/Documents/KnowLedge` 的本地范式；默认 `generic` profile 保持更少假设。其他 vault 可以通过 `OPENWHISKER_RAW_INBOX_DIR`、`OPENWHISKER_RAW_PROCESSED_DIR`、`OPENWHISKER_KNOWLEDGE_DIR`、`OPENWHISKER_KNOWLEDGE_DRAFT_DIR` 和 `OPENWHISKER_REQUIRED_DRAFT_TAGS` 覆盖本地约定。

真实 vault + LLM 的规划链路已通。Knowledge Expander 与 high-risk proposal policy 已分别在 4C.2 / 4C.1 落地；剩余的真实 vault apply 复核为可选 follow-up，明细见 [`docs/progress.md`](../progress.md) 的"验证队列"。

## Phase 4B.5：IM Intent Router

partial implementation。在 Matrix Adapter 和 Core Adapter API 之间增加 `IM Intent Router` 中间件，把"整理刚才 / 预览一下 / 写进去 / 先不写"等自然语言归一化为现有受控命令；slash 命令继续 passthrough 作为 debug / fallback。第一版边界、rules / hybrid / off 三模式、source-scoped binding、approval guard 和 audit 隐私边界见 [`phase-4-im-intent-router.md`](phase-4-im-intent-router.md) 和 [`architecture/im-intent-router.md`](../architecture/im-intent-router.md)。

Intent Router 不直接写 vault、不生成 `VaultPlan`、不调用 `VaultExecutor`、不绕过 policy / approval；所有执行仍由 Core、Plan、Policy 和 Executor 决定。小模型使用独立 `OPENWHISKER_INTENT_*` 配置，不复用 Raw Organizer 的 `OPENWHISKER_LLM_*`。

## 本次推进结论

Phase 4 主线是真实 LLM + Matrix IM approval workflow，拆成三个连续切片：

1. **Phase 4A：Core Adapter API + Matrix Adapter MVP + Agent Host Contract**。先定义 Core 给 IM adapter 使用的稳定入口、Matrix 入站/出站最小闭环、Wiki Agent Host 的输入输出边界、vault context 读取范围、结构化 plan 校验和 fake/fixture agent 验证方式。
2. **Phase 4B：Real LLM-backed Raw Organizer**。接入真实 LLM provider，让 Matrix `/organize last` 从 raw note、VaultRawOrganizerSkill、VaultProfile 摘要、可选 vault rules 和已有 wiki context 中生成可审批 `VaultPlan`，并通过 Matrix 完成 diff、approval、reject 和结果回传。当前已完成最小 OpenAI-compatible provider 接入，后续补齐更完整的 validator、上下文检索和 Raw/Processed 处理记录。
3. **Phase 4C：Knowledge Expander + Proposal Policy**。让 Knowledge Expander 支持扩展、拆分建议、hub / child note proposal；高风险 split / merge 默认只生成 proposal note。

Phase 4 必须包含真实 LLM provider 接入；fake / fixture agent 只作为 contract、validator 和 regression test 的替身。Provider 细节必须被隔离在 Agent Host 边界内。Core、Policy、Executor 和 Matrix Adapter 只消费结构化 job、plan、outbox 或 command result，不依赖具体模型或 SDK。

## 目标

构建 Wiki Agent workflow 计划层，使 OpenWhisker 可以：

1. 通过 Matrix IM 接收 raw input、命令、approval 和 reject；
2. 通过 Core Adapter API 把 IM event 转换为受控 `WikiJob`、`VaultPlan` 生命周期操作和 `OutboxMessage`；
3. 为 agent 组装受控 vault context；
4. 使用由 vault-specific `VaultProfile` 编译出的 task-specific Raw Organizer Skill / contract 约束 LLM 输出；
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
  -> Vault Context Builder reads raw + VaultRawOrganizerSkill + VaultProfile
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
- 定义 context 读取规则：默认 raw note + task-specific VaultRawOrganizerSkill + VaultProfile；完整 vault rules context 只作为显式调试模式，不作为真实外发默认值。
- 定义 fake / fixture agent 验证方式，确保 adapter、contract 和 plan validation regression 不依赖真实模型。
- 定义 agent output 的校验要求：合法 operation、clean relative path、source refs、target paths、risk、reason 和 metadata。

范围外：

- 真实 LLM provider 的完整生产化；Phase 4B 已有最小 OpenAI-compatible provider，后续补齐重试、更多 validator 和真实 Matrix 环境验证。
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

- 选择并接入第一版真实 LLM provider；模型、密钥、超时和结构化输出解析隔离在 Wiki Agent Host 内。第一版优先接入 OpenAI-compatible Chat Completions API。结构化输出契约：`response_format: {"type": "json_object"}`，schema 约束（字段名、`raw_kind` enum、字符串/数组类型）以英文规则 + JSON 示例形式写在 system prompt 里，依赖客户端 `validateRawOrganizerOutput` 做硬校验；不使用 OpenAI 的 strict `json_schema`，以兼容 DeepSeek 等只支持 `json_object` 的 OpenAI-compatible 端点。空 content 在 `OrganizeRaw` 的 `createWithRetry` helper 中做一次同请求体重试（无指数退避），两次都空则返回 `empty content after one retry` 错误。该实现与 [`docs/architecture/intent-router-model-contract.md`](../architecture/intent-router-model-contract.md) 里的 intent classifier 迁移备注同源（见 commit 890013a 与 2026-05-19 验证记录）。
- CLI `organize last` 和 Matrix `/organize last` 都可以通过 `--organizer=openai-compatible` 使用 Raw Organizer 生成 plan；默认仍保持 deterministic，避免无意触发外部模型调用。
- Matrix `/diff <job_id>`、`/approve <job_id>`、`/reject <job_id>` 返回用户可读的审批和执行结果。
- 支持 `raw_kind` 推断：`concept-seed`、`web-clip`、`todo-list`、`llm-chat`、`mixed`。
- 计划必须保留 raw source，并在完成后写入 `Raw/Processed/` 处理记录和 output links。第一版已通过 `move_note.processing_note` 写入。
- Knowledge 输出必须包含 frontmatter、controlled tags、source link、`待核查` 或 needs-review 标记。
- mixed raw 默认优先生成拆分建议，只有边界清楚时才生成多个输出。

范围外：

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

## Phase 4C：Knowledge Expander + Proposal Policy

目标：让 agent 可以维护长期 `Knowledge/`，但对结构性重构保持保守。

范围内：

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

Phase 4C 进一步拆为两个子阶段，每个子阶段独立可 commit、独立可验收。

### 4C.1：High-risk proposal-only policy

目标：在引入新 LLM agent 之前，先把 high-risk plan 的安全出口铺好。让 4C.2 在判断不准时可以稳定降级为 proposal，而不是被迫硬写或拒绝。

范围内：

- 在 policy 层固化 high-risk 判定枚举：`split` / `merge` / `rename`（已有正式 Knowledge note）/ 大规模 retag / 大规模 link rewrite。第一版以静态规则识别 plan 内 operation 组合，无需新 LLM 调用。
- plan lifecycle 新增 `proposal_written` 终态：high-risk plan 在 `approve` 时不进入 `direct_fs_executor` apply 路径，而是生成一篇结构化 proposal note 写入受控路径，原始 plan 操作不落 vault。
- proposal note 默认写入 `Raw/Agent-Proposals/`，frontmatter / 必填章节由 [`docs/architecture/proposal-note-schema.md`](../architecture/proposal-note-schema.md) 定义。
- `vault_operation_logs` 增加可区分 `applied` / `proposed` 的字段（或新增 row type），保留 hash chain 完整性。
- `plan diff` 对 high-risk plan 展示「将生成 proposal note 而非直接写入」的明确提示，避免用户以为是普通 medium-risk 审批。
- 现有 medium-risk Raw Organizer 闭环行为完全不变（回归测试覆盖）。

范围外：

- 自动检测「我应该把这俩 note 合并」之类的 high-risk **建议**生成。本阶段只处理「plan 已经长成 high-risk 形状时怎么办」，建议生成由 4C.2 负责。
- proposal note 上的二次审批 / 自动执行。proposal 是给人看的终态，不再走 OpenWhisker apply。

验收：

```text
给一个合成 high-risk plan（包含 rename 现有 Knowledge note + 大规模 retag），approve 后：
- Knowledge/ 没有任何写入；
- Raw/Agent-Proposals/ 出一篇符合 proposal-note-schema 的 proposal note；
- vault_operation_logs 中该 plan 状态为 proposed，hash chain 保持完整；
- 现有 medium-risk Raw Organizer end-to-end 测试全绿，行为不变。
```

### 4C.2：Knowledge Expander（仅 append + 高风险打报告）

状态：已落地（瘦身版）。`KnowledgeExpander` 接口、`OpenAIKnowledgeExpander`、`expand <path>` CLI、`prepareHighRiskApprovalPlan` 高风险出口均已实现并测试通过。真实 Matrix / 真实 vault + DeepSeek 的端到端验证待后续。

目标：新增 LLM agent，针对已有 thin Knowledge note，生成**末尾 append** 扩展 plan；拿不准（需要新建子 note / split / merge / rename / 批量改 tag 或 link）时一律通过 4C.1 的 proposal 出口降级，由人在外部强工具（Codex / Claude Code 等）中实际执行。

**设计取向**：Knowledge Expander 是非关键模块，主干 policy / executor 不为它做让步。OpenWhisker 自接的开源 / 小厂 LLM 在工程能力 + 联网搜索能力上天然不如闭源旗舰；与其让 Expander 端到端把活做完，不如把它瘦成"只做最确定有用的 append + 给闭源工具生成高质量入口文档（proposal）"。

范围内：

- 新建 `KnowledgeExpander` LLM agent，契约对齐 Raw Organizer：OpenAI-compatible `json_object` 响应、客户端 `validateKnowledgeExpanderOutput` 硬校验、单次空 content 重试。
- 输入边界：一篇目标 Knowledge note（必填）+ 可选 source trace 关联的 raw / processed note（第一版只通过 `--context-mode=vault-rules` 显式扩展）。
- 输出仅两种 kind：
  - `append`（medium-risk）：对已有 Knowledge note 末尾追加 H2 章节，必须有 `before_hash`；
  - `propose_restructure`（high-risk）：split / merge / rename / bulk-retag / bulk-link-rewrite，由 4C.1 写一份 proposal note 到 `Raw/Agent-Proposals/`，由人在外部工具中实际执行。**新建子 note 也归到这条路径**（建议 `proposal_kind = split`）。
- CLI 入口最小形态：`expand <knowledge_path>`。Knowledge Expander 保持 CLI-only，不接入 Matrix 自然语言入口。
- 契约文档：[`docs/architecture/knowledge-expander-model-contract.md`](../architecture/knowledge-expander-model-contract.md)。

范围外：

- **直接新建子 note**。第一版有意不支持 `create_child_note` kind。任何新建 note 的需求都走 `propose_restructure`。这避免了 expander 输出落到 `Knowledge/Drafts/` 这种 policy 例外。
- 主动「扫描整个 vault 找扩展机会」。本阶段只在用户显式指定 Knowledge note 时工作。
- 自动选择关联 raw。第一版只用 source trace 中已写入的反向引用。
- IM 自然语言入口。Knowledge Expander 的产出（append plan / proposal note）本身就是普通文档，会自然回流到既有 capture / review 链路，不需要为非关键模块单独做 Matrix 交互 UX。

验收：

```text
在真实 vault 上：
- 选一篇 thin Knowledge note，运行 expand <path> --organizer=openai-compatible 产出 medium-risk append plan；
- plan diff / approve --sync=off 能正常落地，append 命中 before_hash；
- 若 LLM 输出 split/merge/rename/bulk-* 类操作，plan 被判定 high-risk 并走 4C.1 proposal 出口；
- 单测覆盖 schema 校验（含 create_child_note 已被拒）、空 content 重试、high-risk 降级三条路径。
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
- `organize today` 批量整理入口已移除：capture bucket 已在输入期由用户完成 topic 分组，再在整理期做一次机器聚类会与捕获期决策冲突。整理链路统一为 capture bucket（`OrganizeCaptureBucket`）和 source-last（`OrganizeSourceLast`）两条。原计划的 4C.3（organize today 多 topic 分组）因此一并取消。
- Knowledge Expander 保持 CLI-only。其产出只是一份文档建议（append plan 或 proposal note），会自然回流到既有 capture / review 链路；不为非关键模块单独构建 Matrix 自然语言 UX。原计划的 4C.4（IM 自然语言入口）因此一并取消。

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
go run ./cmd/openwhisker plan diff <plan_id|job_id>
go run ./cmd/openwhisker plan approve <plan_id|job_id>
go run ./cmd/openwhisker plan reject <plan_id|job_id>
```

Matrix 文档层面的目标命令：

```text
普通文本
/raw <text>
/organize last
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
- mixed raw 在边界不清时生成 proposal，而不是直接创建多个长期 note。
- high-risk split / merge 只写 proposal note，不修改正式 Knowledge note。
- Phase 3 sync-aware approval 行为继续保持：pre-sync failure 不写 vault，post-sync failure 只产生 warning。

## Review 问题（Phase 4 收束时均已解决）

- **真实 LLM provider 配置**：不单独立 ADR，统一用 `OPENWHISKER_LLM_*` 环境变量配置，OpenAI-compatible Chat Completions 为第一版接入口径。
- **Core Adapter API 形态**：采用进程内接口，未暴露本机 HTTP API。
- **proposal note 默认路径**：定为 `Raw/Agent-Proposals/`（见 [`architecture/proposal-note-schema.md`](../architecture/proposal-note-schema.md)），不使用 `Meta/Proposals/` 或 `Knowledge/Drafts/`。
- **`Knowledge/Drafts/` 的归宿**：继续作为中风险整理的草稿区；high-risk agent 产出改走 `Raw/Agent-Proposals/`。
- **`expand` 入口**：定为 CLI `expand <path>`，Knowledge Expander 保持 CLI-only。
- **frontmatter / controlled tags 硬闸门**：固定在 policy 层；provider 侧友好错误可作为补充，不替代 policy。
- **`/replan`**：Phase 4 未实现，按真实使用驱动的原则在需要时再单独立项。
