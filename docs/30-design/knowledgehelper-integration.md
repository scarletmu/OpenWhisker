# KnowledgeHelper 对接设计

> 本文是**设计方向稿，未落代码**。它描述 OpenWhisker 与 KnowledgeHelper（仓库内部代号 InterviewPolice，下称 KH）如何打通。核心立场：**KH 不是一次性的"外部系统集成"，而是挂在 OpenWhisker 现有 Core Adapter API 下的第二个 adapter**，与 [Matrix Adapter](matrix-adapter.md) 同类。具体能力是否已实现以 [`project-status.md`](../00-overview/project-status.md) 为准；与[设计哲学](../20-architecture/design-philosophy.md)一样，不要假设这里描述的每项能力都已落地。

KH 是一款移动端优先、离线优先的**面试题卡复习 PWA**：把面试题整理成「题卡 + 题目 + 标签」，配合 AI 实时批改（`GradeResult`）、AI 讲解（`ExplainResult`）与 TTS，追踪掌握度、易错题和复盘历史。后端 Go + Gin + GORM，AI 走厂商中立的 OpenAI 兼容接口（dev 指向 DeepSeek），契约以 `openapi/openapi.yaml` 为单一真相。

## 0. 已确认决策

| 决策 | 取值 | 出处 |
|---|---|---|
| 方向 A 触发 | **显式命令触发**（scheduler 版为后续演进） | §5 |
| 映射键 | **adapter 自有 binding 表 + 投影即对账**（不往 vault 注入机器标记） | §7 |
| 方向 B 传输 | **scheduler 纯只读 pull + OW 侧幂等去重**；ack 不挂 scheduler（越只读边界），KH 打点降为可选优化 | §6 |
| outbox 路由 | **按 kind 订阅**：adapter 各认领自己的 kind；`review_cards`→KH，`human_notification`→泛支持（所有人类通知 adapter） | §4 |
| reasoning/Skill | **只方向 A 需新 vault Skill；方向 B 复用 enrichment/Expander；无 LLM 向 KH Profile** | §8 |
| 顶层模型 | **KH = 第二个 Core Adapter** | §4 |
| binding 对齐 | **相似度自动认题（"让它自己猜"），模糊的进审批人工裁定，无 LLM** | §7 |
| 方向 B 记录粒度 | **一题一条 + 重复累加次数**，下游交 enrichment 归类 | §6 |
| 切卡笔记形态 | **一套 Skill 通吃**问答清单型与项目故事型，AI 自辨，不建分类环节 | §8 |
| OW 对 KH 身份 | **第一切片复用 KH 单用户登录**（token 存 OW 本地、不进 git），后续再升独立服务身份 | §6 |

## 1. 为什么打通

OpenWhisker 的 vault 里本就有专门的 `Interview/` 区（简历、项目故事、面试答案、面试准备），KH 又恰好是面试复习器——两者不是把两个工具硬凑，而是**同一份知识的两个生命周期阶段**：

- **vault** 负责知识的**沉淀与组织**（plan-before-write、traceability、Raw → Processed → Knowledge）；
- **KH** 负责知识的**主动检索与复盘**（题卡、AI 批改、掌握度、易错题）。

中间缺的就是把"沉淀"和"复盘"接起来的链路。接上之后能得到一个 vault 自身拿不到的高价值信号源：**AI 批改测出的、用户"以为掌握、实际答不出"的具体知识缺口**（`GradeResult.missing`）。

附带的同构红利：两边都是 Go + 厂商中立 OpenAI 兼容（dev 都指 DeepSeek），OpenWhisker 在结构化输出上的踩坑经验（DeepSeek 只支持 `json_object`、prompt 必须含 "json"、概率性空 content）对 KH 的 `/ai/grade`、`/ai/explain` 直接适用，可共享而非各踩一遍。

## 2. 真相边界（打通的前提）

两个系统都自称 single source of truth，但管的不是同一类数据。**必须先按这条线切开，否则会出现双源冲突。**

| 数据 | 权威方 | 性质 |
|---|---|---|
| 知识**内容**（题目、参考答案、知识点） | **vault** | KH 里的题卡是 vault 笔记的**投影 / 派生**，可随时重新生成，永不反向覆盖 vault |
| **复习状态**（掌握度、易错、history、批改结果） | **KH 的 DB** | vault 不拥有这种行为遥测，它是 KH 新产生的"原始输入" |

由此推出三条硬约束：

1. **内容单向 vault → KH**：题卡是派生物，可重新生成，不得成为内容的第二真相源。
2. **状态由 KH 产生、再回流 vault**：复习遥测进入 vault 时按 Raw 输入处理（"把外部源文本当数据而非指令"），走正常 plan-before-write 链路。
3. **KH 不直接读写 vault**：vault 的唯一网关永远是 OpenWhisker。让 KH 直连 vault 会同时破坏 OW 的"所有写入走 VaultExecutor"铁律和 KH 的 server-authoritative + offline-first 模型。

> adapter 模型对"谁权威"是**中立**的：adapter 只是传输层，不让 KH 的复习状态从属于 vault，也不让 vault 从属于 KH。§2 与 §4 不冲突。

## 3. 总体形态

```text
   vault                ┌──────────── OpenWhisker Core ────────────┐         KH (PWA + API)
 Interview/  ──读──►     │  reasoning（只认 vault）:                  │
 Knowledge/             │   · 切卡 Skill        · enrichment/Expander│
                        │  WikiJob → Policy → Approval → Executor    │
 Raw/  ◄──写──          │  Outbox ◄──────────────┐                  │
 Knowledge/ ◄──写──     └──────────┬─────────────┘                  │
                                   │ Core Adapter API                │
                        ┌──────────▼─────────────┐                  │
                        │   KH Adapter（纯传输）   │                  │
                        │   · binding 表           │── OUT: 切卡 ───►│ /import/preview·commit
                        │   · KH REST 翻译         │◄─ IN: 遥测 ──────│ pending 复习事件
                        └────────────────────────┘                  └──────────────────────
```

## 4. 核心模型：KH 作为第二个 Core Adapter

OpenWhisker 已有一层 **Core Adapter API**（`internal/core/adapter.go` 的 `AdapterService`），Matrix 只是挂在它下面的第一个 adapter（`internal/adapters/matrix/`）。它天生双向：

- **IN**：`HandleText(ctx, AdapterRequest) → AdapterResponse`——adapter 把外部输入喂进核心。
- **OUT**：`PullOutbox(limit)` + `MarkOutboxDelivered(id)`——adapter 拉 outbox、投递、回执（**原生就是 pull + ack**）。

KH 就挂在同一套 API 下（落地建议 `internal/adapters/knowledgehelper/`）：

- **方向 A（OW→KH）= OUT**：切卡结果是一种新 kind（`review_cards`）的 `OutboxMessage`；KH adapter `PullOutbox` → 翻成 KH 的 `/import/preview` + `/import/commit` → `MarkOutboxDelivered`。与 Matrix adapter 拉 outbox→发房间→标已投递同构。
- **方向 B（KH→OW）= IN**：复习遥测经 `AdapterRequest` 喂进 `HandleText`（或结构化的兄弟入口）→ 变 capture/WikiJob → Raw → enrichment/Expander。与一条 Matrix 消息进来同构。

**outbox 路由按"能力/kind 订阅"（多 adapter 后的关键机制）**：outbox 消息已有的 `Kind` 字段就是挂点——**adapter 声明自己认领哪些 kind、各收各的**，skill / 命令不写死目标 adapter。`review_cards` 只有 KH adapter（将来还有 Anki adapter）认领；面向人的 `human_notification`（简报 / 提醒 / 确认文案）是**泛支持**类，所有人类通知 adapter 都收。这样换/加卡片系统不改 skill，兑现"KH 可替换"。**此为跨 adapter 的通用机制，不限 KH**（scheduler、命令路径、未来任何 adapter 都适用）；本文只就 KH 用到的 kind 说明，通用机制的权威描述见[架构概览的 Outbox 路由](../20-architecture/system-overview.md#outbox)。

**这个模型的核心收益是把 transport 和 reasoning 彻底分开：**

- **adapter = 纯传输/翻译**（KH REST ↔ 核心中立类型 + binding 表），不含 LLM；
- **reasoning = 核心侧、且只认 vault**（切卡 Skill / enrichment / Expander）。

推论：**LLM 根本不需要知道 KH 存在**，它永远只对 vault 推理，KH 的形状全锁在 adapter 里。于是 **KH 是可替换的**——同一份切卡 outbox + 切卡 Skill，换个 adapter 就能打到 Anki 或任何卡片系统，reasoning 一行不改。

**两点张力（KH-adapter 比 Matrix-adapter "重"，需显式承认）：**

1. Matrix 的 OUT 是**发完即忘的人类通知**；KH 的 OUT 是**写进另一个系统的权威存储**，所以必须幂等/对账（见 §7）。KH adapter 是个**有状态**的 adapter。
2. KH 的 IN 与 OUT 是**同一批实体在往返**（卡出去、关于这些卡的遥测回来），耦合比 Matrix 紧——binding 表是**承重件**，不是可选项。

## 5. 方向 A = OUT（切卡 → outbox）

把 `Interview/`（先只做这一窄切片）里的笔记投影成 KH 的题卡。

- **触发：显式命令**（如 IM 发一句 `export interview cards`）。零额外状态、不依赖 binding 即可跑通"投影 + traceability"；scheduler 周期投影是它的超集，待 §7 binding 稳了之后再做。触发入口倾向 IM（Matrix），与 KH 移动端调性一致，CLI 作本地备用。
- **它走审批，因为是对外副作用**：切卡 → core 产出 `VaultPlan` 之外的一种 outbox（外发内容、可逆性低，定**中风险**）。KH 现成的 `/import/preview` → `/import/commit` 两段式天然映射 OW 的 diff → 人工确认 → 执行：
  1. KH adapter 调 `POST /import/preview` 拿预览（KH 已支持逐卡 warning）；
  2. OW 把预览 + binding 对账方案（见 §7）渲染成审批面，等用户确认；
  3. 确认后 `POST /import/commit` 落库，`MarkOutboxDelivered`。
- **已是 `Card N｜` 结构的笔记**可走 KH 确定性 importer 当快路径，自由格式笔记才上切卡 Skill（见 §8）。

## 6. 方向 B = IN（遥测 → capture）

把 KH 复盘中产生的行为遥测送回 vault，驱动知识补全。

- **传输：scheduler 纯只读 pull + OW 侧幂等去重**。KH 暴露一个只读遥测端点（易错题 + 批改 `missing` + history），OW 复用 **scheduler** 周期拉。
  - **为什么 ack 不挂 scheduler**：scheduler 的 charter 是只读、**不得有对外副作用**（见 [scheduler](scheduler.md)）。"回写 KH 标 delivered"是一次对外写，**越界**。因此 scheduler 只 pull、不 ack。
  - **正确性靠 OW 侧幂等**：OW **按稳定事件 id 幂等地**落成 Raw，重复拉到的同一事件被去重，无害。KH 那个 `pending/delivered` 打点因此**降为可选优化**（省流量），而非正确性依赖；真要 ack，也由 ingest 路径（落 Raw 那段普通 job）做，不挂 scheduler 时钟。
- **OW 对 KH 的身份**：第一切片**复用 KH 单用户登录**（KH 现单用户、v1 桌面单机，OW 以用户身份操作风险可接受），token 仅存 OW 本地配置、**绝不进 git**。走出单机或要把"只读遥测"与"可导入"拆开管时，再升级成 KH 侧的独立服务身份 / API key。
- **当 Raw 输入处理（一题一条 + 重复累加）**：每道答错的题落成一条干净的 Raw capture（一条只讲一件事，便于下游精准归类）；**同一道题反复答错不刷屏——在已有那条上累加次数**而非新建。例如：

  > 复习信号：题目 X 答错（累计 3 次，last verdict=wrong）；AI 批改标记遗漏要点：{missing}；源题：{binding 解析出的 [[Interview/...]]}。

  "按知识点聚合"不在这一步做——题→知识点的归类交给下游 enrichment（它本就基于 vault 词表做归属），避免送回这步提前硬聚合。

- **走标准链路**：Raw 落盘（低风险自动放行）→ [收件箱 enrichment](inbox-enrichment.md) 挂回源笔记的 `topic/` / `skill/` 词表 → 需要补知识时由 [Knowledge Expander](../40-api/knowledge-expander-model-contract.md) 生成 `VaultPlan`（写 `Knowledge/` 为中风险走审批；大改结构为 high-risk 出 proposal note）。
- **`missing` 是核心价值**：它是结构化、具体到知识点的"差距清单"。普通笔记系统只知道"你写了什么"，这条回流让 vault 知道"你以为掌握、实际答不出的是什么"，精确驱动补全——这是 OW 现有 capture / enrichment 拿不到的输入。

## 7. binding 表（adapter 自有状态）

映射键的归属：**它是 KH adapter 自己的 binding 状态**，与 Matrix adapter 的 `source-scoped binding` / `adapterSourceKey` 同类，不是一次性的"外部集成"机器。决策是**不往 vault 笔记注入任何机器标记**（保 vault 纯净），因此 binding 落在 adapter 侧（OW sqlite）。

代价：没有笔记内锚点，binding 就不能是"确定性精确键查找"，必须升级成**每次投影时跑一遍对账（reconciliation）**——而对账正好骑在方向 A 已有的审批闸上。

- **表里存什么**：每个 card/question 存 `{OW 发的稳定 questionId, 上次投影时的源路径, 上次投影时的题目文本快照}`。
- **Card 级断链（Obsidian 里手动重命名/移动）**：OW 自己执行的移动顺手更新表；只有"绕过 OW 的手动移动"会路径失配。处理——投影时发现映射路径已不存在、却冒出一篇未映射且标题/内容高度相似的新笔记 → 在预览里提示「`Interview/A.md` 像是被重命名成 `Interview/B.md`，把它的 12 张卡重新挂过去？」，人确认即愈合。
- **Question 级断链（改字 / 重排 / 增删题）**：投影时拿当前题目集与表里的文本快照做**相似度对齐**（文本相似为主、顺序作 tiebreaker），产出"匹配 / 新增 / 删除 / 改写"方案，塞进方向 A 那个本来就要人确认的预览。改字不丢复习状态（仍能匹配上），重排不错位。
- **残余风险**：相似度对齐在"两道题几乎一样""一道题被大改到像删了重写"的**模糊尾部**可能错配。兜底——**模糊的永不自动决议，一律进预览让人裁定**；最坏只是那条尾部题目的复习状态重置（与纯 hash 一个量级），只发生在真正歧义处。
- **方向 B 的 `source`**：KH 的 `Question.source` 只存 **OW 发的不透明 questionId**（稳定），回流时由 adapter 用 binding 解析成当时的活笔记路径。KH 这边也保持干净、不感知 vault 路径。
- **附带好处**：这份"上次投影快照"正好是将来 scheduler 版（§5）算"自上次以来改了啥"所需的状态，顺手铺路。

## 8. reasoning 与 Skill

adapter 是纯传输（§4），LLM 那部分全在核心侧、且**只认 vault**：

- **方向 A 的切卡**：把 `Interview/` 一篇自由格式笔记（项目故事 / 问答清单）**理解并切成 Card/Question**，是真正的 vault 形态 LLM 任务（KH 的 `Card N｜` 行解析吃不下自由格式）。**需要一份新的 vault-local Skill**（类比 vault 里的 `vault-raw-organizer` / `vault-knowledge-expander`），可叫 `vault-review-card-extractor`，在用户 vault 侧生产 / 审阅、runtime 只消费。
  - **一套 Skill 通吃两种笔记形态**：指令里把"问答清单型"（照拆，1:1）与"项目故事型"（从叙述凭空出题）都举例说清，由 AI 自辨，**OW 不建"判断笔记类型"的前置分类环节**——这样也能吃下"前段叙述 + 后段几道题"的混合笔记。
- **方向 B 的回流**：遥测 → Raw 是**确定性模板**（adapter 传输，不是 LLM）；"用 missing 扩写 Knowledge"直接**复用现有 Knowledge Expander + enrichment**，它们各有模型契约 / Skill。**方向 B 不另编 Skill。**
- **无 LLM 向 KH Profile**：因为 LLM 永不感知 KH，不需要给它一份"KH Profile"。KH 的 schema / 端点等事实只是 adapter 的实现知识，不进 LLM 上下文。

## 9. 闭环

```text
vault 沉淀 ──切卡 Skill→outbox(A)──► KH 复习 / AI 批改 ──测出 missing(B)──►
   ▲                                                                      │
   └──── Expander 扩写 Knowledge ◄── Raw 回流 enrichment ◄── pull 遥测 ────┘
```

study（vault）→ test（KH）→ 测出缺口（grading `missing`）→ 捕获缺口（vault Raw）→ 扩写知识（vault Knowledge）→ re-test。每步都落在 OW 已有机制（Core Adapter / outbox / enrichment / Expander）上，不为闭环新发明执行通路。

## 10. 契约与影响面（blast radius）

| 改动点 | 位置 | 说明 |
|---|---|---|
| `Question.source` | KH `openapi/openapi.yaml` | 存 OW 发的不透明 questionId，供方向 B 回流解析源笔记。改 spec 后双端 codegen 联动（`oapi-codegen` + `openapi-typescript`） |
| 复习事件 `pending/delivered` | KH 后端 + 只读遥测端点 | KH 打点 + 暴露 pending 增量（weak + `missing` + history），挂 JWT，沿用"后端是 AI/数据唯一出口"鉴权 |
| KH Adapter | OW `internal/adapters/knowledgehelper/` | 挂 Core Adapter API；OUT=切卡 outbox→import，IN=遥测→capture；持有 binding 表 |
| 切卡 outbox kind + 切卡 Skill | OW core + vault-local Skill | 新 OutboxMessage kind；`vault-review-card-extractor` Skill |
| 遥测拉取 | OW scheduler | 周期 pull pending + ack |

**不改动**：KH 始终是干净的 PWA + API，不感知 vault、不引入 vault 依赖、不主动出站；OW 不新增绕过 VaultExecutor 的写入面；LLM 不感知 KH。

## 11. 边界与非目标

- **不让 KH 读写 vault**（§2 约束 3）。
- **题卡不是内容真相源**：只投影、可再生、不反向覆盖（§2 约束 1）。
- **第一步只做 Interview 切片 + 只做方向 A**：先验证投影 + binding 对账跑通，再加方向 B。
- **不往 vault 笔记注入机器标记**：binding 落 adapter 侧（§7）。
- **不把 KH 复习状态镜像进 vault 正文**：vault 只接收可转化为知识动作的"缺口信号"，不复制 KH 进度表。

## 12. 演进路径（建议顺序）

1. **契约打底**：KH 给 `Question` 加 `source`、复习事件加 `pending/delivered`，跑通双端 codegen。
2. **KH Adapter 骨架**：挂 Core Adapter API，打通 OUT（import）与 IN（遥测）两条传输 + binding 表。
3. **方向 A 最小切片**：显式命令 → 切卡 Skill 只覆盖 `Interview/` → preview→审批（含 binding 对账）→ commit。
4. **方向 B 回流**：scheduler pull pending → 幂等落 Raw → enrichment 挂回源笔记。
5. **补全闭环**：基于 `missing` 走 Expander 扩写 / 重测建议（中风险审批）。
6. **扩面**：从 `Interview/` 扩到选定的 `Knowledge/` 子集；视情况上 scheduler 周期投影（方向 A）。

## 13. 留待实现期 / 后续演进

设计层面的问题已在 §0 决策表收口。以下是实现期调参或明确推后的演进项，不是悬而未决的设计分歧：

- **binding 相似度阈值**（判 carry / new / orphan 的具体分数线）：实现期标定起步取保守值、可调，反正每次都有审批预览兜底。
- **只读遥测端点的 DTO 形状**：weak + `missing` + history 的具体字段，随 KH `openapi.yaml` 一并定。
- **scheduler 周期投影（方向 A）**：推后到 binding 稳定后，作为显式命令触发的超集（§5）。
- **OW 对 KH 的独立服务身份**：推后到走出单机或需拆分读/写权限时（§6）。
- **扩面到 `Knowledge/` 子集**：第一切片只做 `Interview/`，验证后再扩（§12）。
