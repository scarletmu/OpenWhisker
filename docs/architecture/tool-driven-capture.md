# 工具驱动的捕获（Tool-Driven Capture）设计

状态：**设计稿，未落代码**。本文是一次架构梳理的产物，定义"让捕获/输入环节具备现状感知与记忆感知"的目标形态、已锁定的设计决策与共用骨架契约。除作为对照基线引用的 `enrich`（Phase 7，已实现）外，本文描述的捕获骨架尚未实现，不要假设任何能力已存在。

## 1. 背景与动机

vault 已经基本建成并能完整运行。当前的捕获/整理链路在做判断时，依赖 host 把全量 context 一次性塞进 prompt，让模型从零重新判断一条新材料该如何归位、打标、关联。这有两个问题：

- 每次都全量加载、重新判断，既不增量也不利用 vault 现状与已有记忆。
- 这种"单次结构化输出"范式（见第 2 节）与 Phase 6 之后引入的多轮 tool-calling（ReAct）范式并存，形成了双轨。

目标：**让捕获环节变成"先查现状、查记忆，再下增量判断"的过程**——agent 主动用只读工具去问"vault 里有没有相关笔记？有哪些 tag？这条该挂到哪、链到谁？"，而不是靠 host 喂全量。

## 2. 核心洞察：走法 A 的捕获引擎，enrich 已经是了

项目里并存两套 LLM 推理范式：

- **范式 A — 单次结构化输出适配器**（Phase 4–5）：`OpenAIRawOrganizer`、`OpenAIKnowledgeExpander`、`OpenAIIntentClassifier`、`OpenAISchedulerEngine`。共享 `OpenAICompatibleClient` + `json_object` + 客户端硬校验 + 单次空响应重试。host 预取全部 context，模型一次转换 input→output，**无工具、不迭代**。
- **范式 B — 多轮 tool-calling（ReAct）循环**（Phase 6+）：`ToolCallingEngine` + `AgentRunner` + `VaultToolExecutor`。模型自己 *推理 → 调只读工具 → 看结果 → 再推理 → `submit_result` 收尾*，带 budget 闸门、protocol 守卫、force-finalize、全程 trace。被 scheduler（`engine=tool-calling`）与 ad-hoc（`ask`/Matrix）使用。

把四个现有适配器按"是否改写正文"对照本设计选定的走法 A（只路由不改写）重新归位：

| 现有适配器 | 对正文做什么 | 归档 |
|---|---|---|
| **enrich**（Phase 7） | 只改 frontmatter，body 一字不动（`body_guard` 冻结） | **就是走法 A**，且已是 ReAct + 工具驱动 + 现状感知 |
| `OpenAIRawOrganizer` | 把原始文本生成成草稿正文 | 走法 B（authored body），且仍是单次 |
| `OpenAIKnowledgeExpander` | 扩写——往正文加段落 | 走法 B / 或高风险出 proposal note |
| `OpenAIIntentClassifier` | 不碰 vault | 天然单次分类，不动 |

**结论**：本设计想要的"根据现状与记忆、增量判断去向"的走法-A 能力，`enrich` 已经实现了约 90%。它现在就会用 `vault_text_search` / `vault_outlinks` 查证现状、用受控词表打 tag、产出 `related` 链接、并算出 `route_suggestion`。它差的最后一步是：**enrich 只把 `route_suggestion` 当一段 frontmatter 文字记下来（不行动）**。走法-A 版捕获的真正增量，就是——**当置信度够高时，把 route 变成一个真正的 `move_note` 操作，走 medium-risk 审批落地**。

因此本设计的方向不是"再造一个 Raw Organizer"，而是**把捕获的现状感知路由统一收敛到 enrich 骨架上并扩展它**，避免两套系统重复做同一件 vault-state-aware 的打标与归位。

## 3. 已锁定的设计决策

1. **写入桥用 enrich 模式**：工具驱动的 agent 只通过 `submit_result.payload` 交受限的**结构化建议**，绝不交 raw 正文；从建议到磁盘的每一步都是确定性 Go 代码。operation 类型与 risk 由 host 钉死，agent 无权决定。
2. **走法 A——只路由不改写**：agent 只决定去向 / 并入 / tag / 链接；**正文一字不改**，沿用 enrich 的 `body_guard`（before/after 正文 hash 必须相等），并将其升格为整个捕获骨架的全局不变量。
3. **共用骨架优先**：先抽出两个适配器共用的捕获骨架，再各自特化。
4. **Knowledge Expander 在走法 A 下基本无事可做**：扩写=写正文，天然不属于走法 A，留在其现有路径，本设计第一版不动它（见第 8 节）。

## 4. 走法 A 的全局不变量与允许操作

走法 A 下所有允许的操作都不碰正文，于是 `body_guard` 成为整个骨架的全局不变量。允许的操作只有两类：

| 操作 | 改什么 | risk | 审批 |
|---|---|---|---|
| `rewrite_note` | frontmatter 追加 `tags` / `related` / provenance 字段 | low | 自动放行（同 enrich） |
| `move_note` | 重定位文件，正文不变 | medium | **需人工审批**（设计哲学：medium 知识变更需批准） |

与 enrich 的唯一结构性差异：enrich 永远 low-risk 静默 apply；捕获骨架必须能产出 **medium-risk + `requires_approval` 的 `move_note` plan**，并接回主链路的审批出口（enrich 是绕过审批的低风险特例）。

## 5. 共用骨架（七件套，抽自 enrich `RunOne`）

enrich 的编排本质是一个七件套，把 ReAct agent 安全接到了写入主链路上。这七件就是捕获骨架的共用底座：

| # | 部件 | enrich 对应 | 共用化角色 |
|---|---|---|---|
| 1 | 只读 ReAct loop | `AgentRunner` + Phase 6 引擎 | 直接复用，agent 只能调 5+1 个只读工具 |
| 2 | in-code Skill | `buildEnrichSkill` | 每适配器一份 in-code skill，**刻意不入 vault `Skills/`，防 prompt 漂移脱离 policy 契约** |
| 3 | 结构化建议 schema | `EnrichResult` | 每适配器定义自己的建议信封；agent 只填它 |
| 4 | host 确定性 plan builder | `buildPlan` | 每适配器一个 builder，op 类型 / risk 由 host 钉死 |
| 5 | content hash 守卫 | pre_hash / cur_hash 二次读 | 不变，TOCTOU 防御 |
| 6 | 专用 policy 校验器 | `CheckEnrichPlan` | 每适配器一份，安全边界，工作量大头 |
| 7 | executor apply | 同一个 `DirectFS` | 不变 |

可提取为公共 `capture` helper 的样板：ReAct runner 装配、in-code skill 构造模板、hash 守卫、executor 调用、`wiki_job` 行生命周期、agent 失败 / 并发编辑的 finish 状态机。

每适配器无法共用、必须各写一份的：建议 schema、plan builder、policy 校验器、skill prompt body。

### 5.1 建议信封（设计示意，非最终接口）

```text
CaptureSuggestion （agent 经 submit_result.payload 只填这个，绝不交正文）
  ├─ tags         []string                              // 受控词表，append-only，复用 enrich 校验
  ├─ related      []wikilink                            // 工具查证后才填，append-only
  ├─ placement    { target_dir, confidence, reason }    // 高置信 → 触发 move_note
  ├─ merge_into   *wikilink                             // 可选：建议并入某条已有笔记（仅记录，不自动合并）
  └─ needs_review bool
```

### 5.2 host 流程（设计示意）

```text
vocab / 现状就绪
  → 读原文 + pre_hash
  → ReAct run（只读工具）
  → 解析 CaptureSuggestion
  → buildCapturePlan（op 类型 / risk 由 host 钉死）
  → pre / cur hash 守卫（并发编辑则丢弃）
  → CheckCapturePlan（body 冻结 + 字段白名单 + vocab + 路径/目标合法）
  → 若 medium：入审批出口（Matrix / CLI 提示）；若 low：直接 apply → executor
```

## 6. policy 校验器 `CheckCapturePlan`（契约要点）

复用 enrich 校验器的结构，但因**禁止 body 改写**，不需要 enrich 那套为 body 改写设计的复杂逻辑。要点：

- `body_guard`：before / after 正文 hash 必须相等（全局不变量）。
- 字段白名单：frontmatter 仅允许 `tags` / `related` / provenance 字段变化；其余 top-level 字段 blob 必须逐字节不变。
- `tags` / `related`：append-only，逐项校验前缀与受控词表成员资格（直接复用 enrich 的 `enrichValidateTagsAppend` / `enrichValidateRelatedAppend` 思路）。
- 新增 `move_note` 校验：目标目录必须落在允许集合内；move 操作不得改动正文；risk 必须为 medium 且 `RequiresApproval=true`。
- 路径校验：复用 `validateRelativeVaultPath` 与 hash 守卫。

按 policy 包惯例，**policy 校验先于 executor 改动落地**（启用新写入行为时的固定顺序）。

## 7. 与 enrich 的收敛关系

- **enrich** 升级为走法-A 捕获引擎本体；新增"高置信 `route_suggestion` → approval-gated `move_note`"能力。这是本设计的主要落点。
- 避免另起一个并行的 Raw Organizer 做同样的 vault-state-aware 打标。捕获的现状感知路由统一在 enrich 骨架上扩展。

## 8. 四个现有适配器的最终处置

- **enrich** → 升级为走法-A 捕获引擎，新增 approval-gated `move_note`。
- **`OpenAIRawOrganizer`（走法 B 草稿生成）** → 不并入共用骨架，保留现状；日后若需要再单独按走法 B 处理。它与走法 A 正交。
- **`OpenAIKnowledgeExpander`** → 扩写=写正文，不属于走法 A，留在现有"medium 草稿 / 高风险 proposal"路径，第一版不动。
- **`OpenAIIntentClassifier`** → 天然单次分类，不动。

诚实边界：走法 A 真正受益并改造的是**捕获/路由这条线（以 enrich 为载体）**；Knowledge Expander 在走法 A 里基本无事可做。这不是缩水，而是"只路由不改写"的固有边界。

## 9. 次要决策（默认值）

1. **同步 vs 异步**：沿用 enrich 的异步队列产出 plan；但 `move_note` 类 medium-risk plan 进**审批出口**（Matrix / CLI 提示），而非 enrich 那种静默 apply。
2. **skill 放哪**：同 enrich，**in-code**，prompt 不进 vault，防漂移。
3. **置信度阈值**：move 触发沿用 enrich 的 `≥ 0.7`，先复用现有常量。

## 10. 未决与后续

- **走法 B（结构化草稿）**：若日后希望捕获能在受限 typed 字段下生成正文，需单独决策；policy 校验器要从"body 冻结"改为"模板结构 + 字段约束"，护栏更复杂。本设计为其预留概念位，但不预先实现。
- **Knowledge Expander 工具化**：必然走法 B，是独立决策。
- **范式 A 中 `OpenAISchedulerEngine` 与 ReAct 路径的重叠**：是另一处可独立梳理的双轨，不在本设计范围内。

## 参考

- 写入主链路与风险分级：[设计哲学](design-philosophy.md)
- 已实现的走法-A 对照基线：[Phase 7 Inbox Enrichment](../phases/phase-7-inbox-enrichment.md)
- ReAct 引擎与工具：[Phase 6 Scheduler Skill Creator](../phases/phase-6-scheduler-skill-creator.md)
- 受控记忆与召回：[Phase 8 Memory Recall](../phases/phase-8-memory-recall.md)
- frontmatter 解析契约：[Frontmatter 解析](frontmatter-parsing.md)
