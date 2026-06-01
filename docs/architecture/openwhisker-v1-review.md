# OpenWhisker v1 设计回顾

本文回顾 OpenWhisker 从 FlashBang 起点到 Phase 4C 收束的完整设计弧线。

这是一份**收官回顾**，不引入新决策、不改变任何行为。职责分工保持不变：长期架构以 `architecture/` 为准，当前进度与遗留以 [`progress.md`](../progress.md) 为准，跨阶段决策以 [`project-decisions.md`](../project-decisions.md) 为准，完整设计方向以 [`design-philosophy.md`](design-philosophy.md) 为准。本文只负责一件事：把分散在四个 Phase 文档里的动机、取舍和教训，收敛成一条可读的主线。

阅读对象是想理解"OpenWhisker 为什么长成现在这样"的人，包括未来的自己。

## 1. 起点与核心赌注

FlashBang 是一个本地优先的捕获服务：SQLite 捕获存储、本地规则分类、手动 LLM digest、profile 驱动的 sync action、一个 Obsidian 插件 MVP。它能把输入收下来，但没有回答一个问题——当 LLM 真正参与知识库整理时，怎么让它**有用**又**不危险**。

OpenWhisker 从这个起点出发，押下一个贯穿全程、从未动摇的核心赌注：

> LLM 生成计划，不直接写库。

它展开成系统的第一架构约束：

```text
LLM reasoning -> structured plan -> policy and approval -> deterministic execution
```

这条约束在 Phase 1 就立起来，到 Phase 4 接入真实 LLM 时仍然成立——agent 只替换了 reasoning 层，没有任何一个 Phase 为了"让 LLM 方便一点"而破例。这是整条弧线最该被记住的成功点：能力一路在长，安全边界一格没退。

## 2. 弧线回顾

四个 Phase 各解决一段问题，每一段都把下一段需要的前置铺好。

### Phase 1 最小切片

- **解决**：用最低风险的一条链路（raw text → `Raw/Inbox` note）把 plan-before-write 骨架立起来，并且可测试。
- **关键决策**：CLI 入口、SQLite 持久化、默认写本地 test vault、不接 LLM。保守默认值优先于功能广度。
- **留给下一段**：approval / diff / 中风险写入全部明确推迟到 Phase 2，骨架先于能力。

### Phase 2 审批与 Diff

- **解决**：中风险链路——`organize_raw` → `VaultDiff` + `before_hash` → 人工 approve/reject → vault lock 保护下 apply。
- **关键决策**：planner 仍是 deterministic，不接 LLM；Knowledge 只写 `Knowledge/Drafts/`，不碰长期正式 note；conflict 时整体失败，不留下部分写入。
- **留给下一段**：`Knowledge/Drafts/` 是否长期保留为 staging 区——一个一直挂到 Phase 4 才回答的 Review 问题。

### Phase 3 Sync-Aware Approval Execution

- **解决**：apply 前后接入 Headless `ob` 的 one-shot sync，让真实 vault 的写入是 sync-aware 的。
- **关键决策**（值得单独记住）：**不新增能独立写 vault 的 executor**。sync 只作为受控 client 包在 approval 编排的外层，vault mutation 仍只由 `direct_fs_executor` 完成。Sync 是同步层，不是事务系统；冲突判断仍归 `before_hash` + vault lock + path guard。
- **留给下一段**：desktop Obsidian CLI executor 明确不作为 Phase 4 的前置条件。

### Phase 4 Wiki Agent Workflow

主线是真实 LLM + Matrix IM approval workflow，拆成四段连续切片：

- **4A — Core Adapter API + Matrix Adapter MVP + Agent Host Contract**：先把 IM 入口和 agent reasoning 边界钉死，让后续真实 LLM workflow 可被测试、审计、替换。
- **4B — 真实 LLM-backed Raw Organizer**：`organize last` 的 reasoning 层从 deterministic planner 换成真实 provider，但默认仍 deterministic，真实 provider 需 `--organizer=openai-compatible` 显式启用。
- **4B.5 — IM Intent Router**：在 Matrix Adapter 和 Core 之间加一层中间件，把"整理刚才 / 写进去 / 先不写"等自然语言归一成受控命令；slash 命令继续 passthrough 作为 debug / fallback。引入 capture bucket、source-scoped binding、pending clarification 状态机。
- **4C — Knowledge Expander + Proposal Policy**：再分两个子阶段——
  - **4C.1**：先铺 high-risk proposal-only 出口。新增 `proposal_written` 终态，high-risk plan 在 approve 时不进 apply 路径，改写一篇 proposal note 到 `Raw/Agent-Proposals/`。
  - **4C.2**：Knowledge Expander 瘦身版落地。只保留 `append`（medium）+ `propose_restructure`（high）两个出口，CLI-only。

注意 4C 的次序：**先铺安全出口（4C.1），再放能力（4C.2）**。这样 Expander 判断不准时可以稳定降级为 proposal，而不是被迫硬写或拒绝。

## 3. 贯穿全程的架构约束

整条弧线由一组跨阶段约束撑住，它们不属于任何单个 Phase。完整版见 [`project-decisions.md`](../project-decisions.md)，这里只点名：

1. **VaultPlan before Vault Write**：`VaultPlan` 是 LLM reasoning 和 vault mutation 之间的审计边界；LLM 不直接写文件、不执行 shell、不任意调用 Obsidian CLI。
2. **早期阶段保守默认值**：Phase 1–3 一律 CLI + SQLite + test vault，真实 vault 必须用户显式传入。
3. **Knowledge 写入先进 Draft**：中风险整理不直接 patch 长期正式 note。
4. **Sync 不是事务系统**：Headless `ob` 只作 sync client，path guard / lock / hash guard / operation log 仍由 executor 自己负责。
5. **外部对接节点优先 Skill-driven**：程序固定接入、计划、审批、执行、审计边界；外部对象的本地协作方式由 `VaultProfile` + `VaultSkill` 描述，不硬编码进通用代码。

这五条加上第一架构约束，是"能力一路在长、安全边界一格没退"的具体保障。

## 4. 主动收窄的边界

一份诚实的回顾要记录被砍掉的东西。OpenWhisker 在收官前主动收窄了几处范围，逻辑是一致的：

- **4C.3（`organize today` 多 topic 分组）取消，并删除整条 `organize today` 链路**。capture bucket 已经在**输入期**由用户完成 topic 分组，整理期再做一次机器聚类会与捕获期决策冲突。这不是"做不动"，是"发现重复了"。整理链路因此收敛为 bucket 驱动的 `organize last` 与 `expand` 两条。
- **4C.4（Knowledge Expander 的 Matrix 自然语言入口）取消**。Expander 是非关键模块，其产出（append plan / proposal note）本身就是普通文档，会自然回流既有 capture / review 链路，不值得为它单独造一套 Matrix 交互 UX。
- **`create_child_note` kind 不做**。任何新建 note 的需求统一走 `propose_restructure`，避免 expander 输出落到 `Knowledge/Drafts/` 这种 policy 例外。
- **`Knowledge/Drafts/` 的归宿**。这个从 Phase 2 挂到 Phase 4 的 Review 问题，最终以"high-risk agent 产出统一写 `Raw/Agent-Proposals/`"收尾——agent 产出等于未被人处理的输入，归 `Raw/`，`Meta/` 只放规则模板。

收窄的共同逻辑：**主干（policy / executor / 安全契约）不为非关键模块弯腰；发现职责重复就删一边，而不是两边都留。**

## 5. 经验与教训

1. **Provider 兼容是 schema 设计的输入，不是事后补丁。** Raw Organizer 和 intent classifier 都先用了 OpenAI 的 strict `json_schema`，撞上 DeepSeek 只支持 `json_object`，被迫迁移到 `json_object` + 客户端硬校验 + 单次空 content 重试。DeepSeek 是官方目标 provider，json_object-first 本应是设计起点而不是迁移终点。
2. **不要把控制流挂在 LLM 自评的软标签上。** medium-confidence 澄清分支最初挂在 classifier 自评的 `confidence_label=medium` 上，但小模型几乎从不自选 medium（唯一的 few-shot 示例还偏向 high），整条分支从未触发。改挂结构信号 `bucket_relation=unclear` + active bucket 在场之后才真正可达。
3. **deterministic-default 是对的。** 每个 Phase 都保持 deterministic planner 为默认、真实 provider 显式启用。回归测试因此不依赖外部模型，也杜绝了把真实 vault 内容误发到外部 endpoint。
4. **安全出口先于能力。** 4C 先做 4C.1 的 proposal 出口，再做 4C.2 的 Expander。能力上线时，降级路径已经在那儿等着了。
5. **roadmap-driven 有尽头。** 4C.3 / 4C.4 被取消揭示：当主干弧线闭合后，继续按预排 phase 往下做会滑向过度设计。识别"主线已经完整"这件事本身，和实现功能一样重要。

## 6. 当前状态与下一步

- **设计弧线状态**：主干链路 `Capture → WikiJob → VaultPlan → Policy/Diff/Risk → Approval → Executor → Sync → Log` 功能上已闭合，`go test ./...` 全绿，已在 Matrix + DeepSeek + `testdata/vault` 上跑通整链。
- **验证边界**：testdata smoke 视为验证充分；真实 vault apply 的若干复核是 nice-to-have，列在 [`progress.md`](../progress.md) 的"验证队列"，需要时再单独安排。
- **已知遗留**：5 项，明细见 [`progress.md`](../progress.md) 的"关键已知遗留"。
- **工作模式转变**：Phase 4C 收束后没有预排的 4C.5。后续推进从 roadmap-driven 改为**真实使用驱动**——新能力按真实使用中暴露的摩擦单独立项，不再凭空往下排 phase。

至此，OpenWhisker 的 v1 设计弧线收束。它没有做成一个"全能知识库 agent"，而是做成了一条**可审计、可降级、安全边界从不退让**的整理流水线——这正是 FlashBang 起点上想要回答的那个问题的答案。
