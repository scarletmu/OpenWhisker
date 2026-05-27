# Phase 8 Memory Recall Service

状态：**待实现**。Phase 7 v1 已合并 main（commit `2558928`），联合评审条件满足，7 条原待决问题已于 2026-05-27 收为决议（见"决议"小节）。本文沿用 Phase 5/6/7 的格式，定义"把 Phase 7 enrich 沉淀的 vault 原生 tag 归属，整理成跨调用方共享的 memory recall 服务"这一增量的设计意图、范围边界和决议。

## 概述

Phase 8 不引入新的 vault 写入面、不引入新的存储依赖、不引入向量索引。它把以下两件事捆成一个独立模块 `internal/memory/`：

- **已知 tag 词表服务**：派生自 vault frontmatter 的 `topic/*` / `skill/*` 集合，daemon 启动时全量 rescan，executor Apply 成功时增量更新。Phase 7 enrich 用它做 tag 候选校验；未来其它写入侧也共用同一份词表。
- **Recall 核心 API**：`Recall(ctx, RecallRequest) -> RecallResult` 的纯 Go 入口，回答"按 tag 或 query 找相关 Raw / Knowledge 笔记"。读 frontmatter 索引 + sqlite cache + 现有 `vault_text_search`，不直接调 LLM。
- **vault 链接图索引**：派生自 vault frontmatter `related` 字段（含 enrich 自己写回的那部分），用于一跳扩散召回——tag 命中候选的 `related` 邻居自然带回，链接图不被 tag 框架废掉。

每个消费者按自己的需要包一层薄壳——LLM agent 包成 Phase 6 工具集中的 `recall_memory` 第七件工具；enrich job、未来的 intent router / scheduler skill 直接 Go 调用，无需各自再造一套检索。

核心立场：

- **memory 不绑死某个 agent**：核心 API 是 Go 函数，调用方决定上下文与展示形态；
- **不引入新真值源**：索引完全派生自 vault frontmatter（enrich 写入的 vault 原生 `tags` / `related` 字段 + 词表派生）+ vault 文本，frontmatter 是源，sqlite 索引只是 cache；
- **不黑盒**：每条召回结果带 `source` 标签（`tag_match` / `text_match` / `related_link`），可追溯；
- **简单优先**：v1 索引就是 tag 反向表 + 单跳 related 邻接表，不引入多跳图遍历、不引入向量、不引入个性化排序。

## 背景

Phase 7 计划让 enrich agent 给每条新落盘的 Raw 在 vault 已有的受控 tag 词表里定位，把 1–4 个 `topic/` / `skill/` 项追加到 frontmatter `tags`。enrich 自己需要两份基础能力：

1. **当前 vault 的"已知 tag 词表"**，否则没法判断"这个 tag 是不是合法"——vault 已有规则禁止自创 tag；
2. **在 Knowledge/ 子树里按 query 找候选关联**，目前 Phase 6 只有 `vault_text_search` 一条工具能用，纯字面匹配。

同时，Phase 6 的 ask / vault-qa agent 在回答"我之前在 X 上写过什么"时，唯一能用的也是 `vault_text_search`，字面命中率低、tool budget 浪费——而 vault 里 enrich 已经写好的 `tags` 字段恰好是这类问题的最佳索引入口（"找所有带 `skill/golang` 的 Raw"），却没有 agent 友好的工具去消费。

更关键的是：

- **词表派生**和 **tag 反向索引** 在物理上几乎是同一件事——都要扫 frontmatter `tags`，区别只是聚合方式不同（一个聚合成 "已出现过的 tag 集合"，一个聚合成 "tag → 笔记列表"）；
- 这件事**不该由 enrich 自己实现一遍**——未来 intent router 想用、scheduler skill 想用、ask agent 想用，每家都重写一遍是浪费。

Phase 8 把这块缺失的 "memory 读取侧 + 词表派生" 补齐，并定位成**跨调用方的共享基础设施**而非某个 agent 的专属工具。

## 核心结论

Phase 8 引入的能力：

- 新模块 `internal/memory/`，对外两个主入口：
  - `memory.KnownTags(ctx, prefix) -> []string`：已知 tag 词表查询，支持前缀过滤（`topic/` / `skill/`）；
  - `memory.Recall(ctx, RecallRequest) -> RecallResult`：tag/query 召回，自带一跳 related 邻居扩散。
- 新 sqlite 表：
  - `memory_tag_index`（`tag, note_path, updated_at`）：tag 反向索引 cache；
  - `memory_link_index`（`src_path, dst_path, updated_at`）：vault `related` wikilink 邻接表 cache，双向都可查；
  - `memory_known_tags`（`tag, first_seen_at, last_seen_at`）：词表 cache；
  - `memory_recalls`（`caller_kind, query/tags 概要, result_count, latency_ms, called_at`）：轻量调用 trace；
- 索引维护：daemon 启动时全量 rescan，executor Apply 成功且 plan 触及 frontmatter `tags` 或 `related` 时增量更新；无独立守护 goroutine、无文件系统 watcher；
- Phase 6 工具集追加第 7 件工具 `recall_memory`，参数面向 agent 友好（query / tags / limit）；
- Phase 7 enrich 的"词表查询"段切到 `memory.KnownTags()` 上；enrich 落地若早于 Phase 8，则先在 enrich 内部做最小实现，Phase 8 落地时迁移；
- 召回结果每条携带 `source` 标签：`tag_match` / `text_match` / `related_link`；
- CLI `openwhisker memory reindex` 用于手动全量重扫，幂等。

Phase 8 明确不引入的能力：

- 不引入向量 / embedding 索引，不引入外部检索依赖；
- 不召回 agent 对话历史（`agent_runs` 表），episodic memory 是另一命题，不在本期；
- 不做 auto-inject——recall 必须由调用方主动调，不会在 agent 不调工具时偷偷塞 context；
- 不引入重排模型 / 学习排序，v1 score 是简单加权（source 类型 + 命中数 + 文本匹配分）；
- 不跨 vault；
- 不提供反馈闭环（thumbs up/down），没有数据先不造闭环；
- 不主动剔除"曾经出现但当前不再出现"的 tag（已知词表只增不减；剔除留给手动 reindex）；
- 不新增任何 vault 写工具——memory 是只读侧，唯一的"写"是索引 cache 的内部维护，不暴露给调用方。

## 与 Phase 7 的关系（落地顺序）

Phase 7 与 Phase 8 在能力上互补：Phase 7 是 vault tag 标注的**写入器**，Phase 8 是**索引 + 词表 + 共享 API**。落地顺序定为：

**Phase 7 先按现稿落地，Phase 8 在其上做抽离与扩张**，理由：

- Phase 7 的词表查询和检索段是 memory 服务的事实原型。先让它在 enrich 内部跑通，再抽出共享服务，比先造 memory service 再让 enrich 消费要低风险——抽离的是已经验证过的代码，而非凭空猜出来的 API；
- Phase 7 frontmatter `tags` 写入是 Phase 8 反向索引的输入源。Phase 7 不落地，Phase 8 的索引就没数据可填，验收无法闭环；
- Phase 8 落地时，对 Phase 7 的改动**只动两点**：(1) `internal/tagvocab/` 模块迁入 `internal/memory/`；(2) enrich 调用 `memory.KnownTags()` 代替自己的最小实现。enrich 的 agent prompt、frontmatter 写入字段、policy guard 全部不动。

落地后两阶段的稳定形态：

```text
              ┌───────────────────────────────────────────┐
              │  internal/memory                          │
              │  - KnownTags(prefix)                      │
              │  - Recall(req)                            │
              │  (Go API, 无 LLM 依赖)                     │
              └────────────┬──────────────────────────────┘
                           │
   ┌──────────────────┬────┴─────┬──────────────────┬──────────────┐
   ▼                  ▼          ▼                  ▼              ▼
recall_memory   Phase 7 enrich  intent router    scheduler skill   ...
(LLM 工具)      (Go 直调)        (Go 直调, 未来)   (Go 直调, 未来)
```

## Workflow

### 读路径（Recall）

```text
Caller (agent tool / enrich job / future router)
  └── memory.Recall(ctx, RecallRequest{
        Query, Tags, ScopeDirs, Since, Limit, CallerKind, ExpandRelated
      })
        ├── Pass 1: 若 Tags 非空 → tag 反向索引召回
        │     SELECT note_path FROM memory_tag_index
        │       WHERE tag IN Tags
        │       (多 tag 取交集 or 并集，由 RecallRequest.TagMode 控制)
        │     → source = "tag_match"
        ├── Pass 2: 若 Query 非空 → vault_text_search(Query) 兜底
        │     → source = "text_match"
        ├── Pass 3: 若 ExpandRelated（默认 true）→ 对 Pass 1/2 命中集做一跳 related 扩散
        │     SELECT dst_path FROM memory_link_index WHERE src_path IN <seeds>
        │     UNION
        │     SELECT src_path FROM memory_link_index WHERE dst_path IN <seeds>
        │     → source = "related_link"（仅当该 path 未被 Pass 1/2 命中时使用此 source）
        ├── 合并 + 去重 + 简单加权打分
        ├── ScopeDirs / Since 过滤（默认排除 Archive/）
        ├── Limit 截断（由调用方传，核心 API 不内置 magic number）
        ├── INSERT memory_recalls (...) 轻量 trace
        └── return RecallResult{ Items: [...] }
```

### 读路径（KnownTags）

```text
Caller (enrich job, agent tool 也可用)
  └── memory.KnownTags(ctx, prefix)
        ├── SELECT tag FROM memory_known_tags WHERE tag LIKE prefix || '%'
        └── return []string
```

### 写路径（索引 + 词表维护，对调用方透明）

```text
executor.Apply(plan)  // 成功
  └── 若 plan 含 rewrite_note 且 diff 触及 frontmatter `tags`:
  │     ├── memoryIndex.UpdateTagIndex(note_path, new_tags)
  │     │     ├── DELETE FROM memory_tag_index WHERE note_path = ?
  │     │     └── INSERT (tag, note_path) per new_tags
  │     └── memoryIndex.UpdateKnownTags(new_tags)
  │           └── UPSERT memory_known_tags (tag, last_seen_at) per topic/* | skill/*
  └── 若 plan 含 rewrite_note 且 diff 触及 frontmatter `related`:
        └── memoryIndex.UpdateLinkIndex(note_path, new_related_paths)
              ├── DELETE FROM memory_link_index WHERE src_path = ?
              └── INSERT (src_path, dst_path) per resolved wikilink → vault 相对路径
```

启动时：daemon boot → `memoryIndex.RescanAll(vaultRoot)`，串行扫一次 `Knowledge/` / `Interview/` / `Life/` 下所有 `.md` 的 frontmatter，幂等重建 `memory_tag_index` 与 `memory_link_index`（全量覆盖）以及 `memory_known_tags`（仅追加，不删——保持只增不减语义）。无独立守护 goroutine，无文件系统 watcher。

链接解析约定：`related` 字段的 `[[...]]` wikilink 走 Obsidian 默认解析规则（短名或带子路径），落到 `memory_link_index` 时统一规一化为 vault 相对路径；解析失败的 wikilink 计 warning 日志，不入表。

## 设计

### API 形状（草案）

```go
type RecallRequest struct {
    Query         string      // 可选；文本查询
    Tags          []string    // 可选；tag 锚点（如 ["skill/golang", "topic/system-design"]）
    TagMode       string      // "any" | "all"，默认 "any"
    ScopeDirs     []string    // 可选；空则全 vault 减 Archive/
    Since         *time.Time
    Limit         int         // 调用方必传，核心层不兜底默认值
    ExpandRelated bool        // 是否对 Pass 1/2 命中集做一跳 related 扩散；默认 true
    CallerKind    string      // "agent" | "enrich" | "router" | "scheduler"
}

type RecallItem struct {
    Path       string   // vault 相对路径
    Score      float64  // 0~1
    Source     string   // "tag_match" | "text_match" | "related_link"
    Excerpt    string   // 命中 snippet（text_match 时非空）
    MatchedOn  []string // 命中的 tag 或匹配位置
}

type RecallResult struct {
    Items []RecallItem
}
```

`Query` 和 `Tags` 至少要有一个非空，否则 reject。两者都给时分别走两条 pass 再合并。

### 评分（v1 简单加权）

每条候选独立计算：

- base：来自 `memory_tag_index` 命中的条目，base = 命中 tag 数 / `len(Tags)`；
- base：来自 `vault_text_search` 的条目，base = 文本匹配分（沿用现有实现的 score）；
- base：来自 `memory_link_index` 一跳扩散的条目，base = seed 的最高分 × 0.7（一跳衰减）；
- source 权重：`tag_match` × 1.0，`text_match` × 0.6，`related_link` × 0.4；
- 同路径多 source 命中：取最高分 source 作主排序，并在 `MatchedOn` 里聚合所有命中来源；`related_link` 不覆盖已有的 `tag_match` / `text_match` 主标签，仅作为补充来源。

v1 不引入时间衰减、不引入个性化、不引入学习排序。规则简单且可解释。

### 工具层 `recall_memory`（Phase 6 工具集 +1）

```json
{
  "name": "recall_memory",
  "parameters": {
    "query":      { "type": "string" },
    "tags":       { "type": "array",   "items": { "type": "string" } },
    "tag_mode":   { "type": "string",  "enum": ["any","all"], "default": "any" },
    "limit":      { "type": "integer", "default": 10 },
    "scope_dirs": { "type": "array",   "items": { "type": "string" } },
    "since":      { "type": "string",  "format": "date-time" }
  }
}
```

工具内部固定 `CallerKind = "agent"`、`limit` 兜底 10（核心 API 不兜底，工具层兜）。返回结果转成 agent 易消化的紧凑 markdown 列表（不直接吐 JSON）。

### 索引一致性

- vault frontmatter 是源；`memory_tag_index` / `memory_link_index` / `memory_known_tags` 都是派生 cache；
- 用户在 Obsidian 里手改 frontmatter 不会立即触达索引，**直到下一次 daemon 重启的全量 rescan 或手动 `openwhisker memory reindex`**——v1 不引入文件 watcher / Obsidian Sync 钩子，避免引入异步 race；
- `memory_known_tags` 仅追加，不主动剔除"曾经出现但现在不再出现"的 tag。剔除语义留给手动 reindex（reindex 会清空表后从 vault 重新构建，自然达成剔除）。

### 失败语义

- 索引未就绪（启动 rescan 进行中）：`Recall` 返回部分结果（仅 `text_match`，标 `degraded=true`），`KnownTags` 阻塞等待 rescan 完成（enrich 必须拿到完整词表，否则可能误判 needs-review）；
- sqlite 故障：两个入口都返回错误，不 fallback；caller 自行决定是否兜底；
- 文本检索失败但 tag 索引成功：返回 tag 索引结果，错误降级为 warning 日志，不抛给 caller。

## 范围

包含：

- `internal/memory/`（新建）：`KnownTags`、`Recall`、`RecallRequest`、`RecallResult`、`memoryIndex.Rescan/UpdateTagIndex/UpdateLinkIndex/UpdateKnownTags` 等公开 API；
- `internal/memory/sqlite_store.go`（新建）：四张表的 schema 与 DAO；
- `internal/memory/wikilink_resolver.go`（新建）：把 `related` 字段的 `[[...]]` 规一化为 vault 相对路径，供 link 索引和 Recall 共用；
- `internal/executor/`：Apply 成功后对触及 frontmatter `tags` 或 `related` 的 plan 触发索引 + 词表增量更新（hook 而非 fork）；
- `internal/agent/tools/recall_memory.go`（新建）：Phase 6 工具集 +1；
- `internal/core/enrich.go`（Phase 7 落地后修改）：词表查询切到 `memory.KnownTags()`；可选地把 enrich 内部检索段也接 `memory.Recall()`，但不强制；
- `cmd/openwhisker/main.go`：`openwhisker memory reindex` 子命令；
- `docs/phases/phase-8-memory-recall.md`（本文）；
- `CHANGELOG.md`、`docs/progress.md`：落地时联动更新。

不包含：

- 修改 Phase 7 enrich 的 frontmatter 写入字段、policy guard；
- 修改 Phase 6 其它六件工具的语义；
- 任何新的写工具或写路径；
- 向量 / embedding 集成；
- Obsidian Sync / 文件 watcher；
- intent router / scheduler skill 的实际接入（接入面留给后续 Phase；Phase 8 只保证 API 形状能承载）。

## 验收

| 维度 | 验收要点 | 方式 |
|---|---|---|
| 单元测试 | `Recall` 在 query-only / tags-only / 两者皆有 / 两者皆空 四种入参下的行为；`TagMode = any/all` 切换正确；评分合并去重正确；`ScopeDirs` 排除 `Archive/` 默认生效 | `go test ./internal/memory/...` |
| 单元测试 | `Recall` 一跳 related 扩散：seed 命中 → 邻居以 `related_link` 出现；`ExpandRelated=false` 时不扩散；双向（src→dst 与 dst→src）都被命中 | `go test ./internal/memory/...` |
| 单元测试 | wikilink 解析：短名 / 带子路径 / 含 alias `|` 三种形态都能规一化到 vault 相对路径；解析失败计 warning 不入表 | `go test ./internal/memory/...` |
| 单元测试 | `KnownTags(prefix)` 在 fixture vault 上返回正确集合；`topic/*` 与 `skill/*` 分别命中 | `go test ./internal/memory/...` |
| 单元测试 | 索引增量：执行一个改动 frontmatter `tags` 的 rewrite_note plan 后，`memory_tag_index` 与 `memory_known_tags` 变化符合预期；改动 `related` 的 plan 同样触发 `memory_link_index` 增量；不触及任一字段的 rewrite 不更新索引 | `go test ./internal/executor/...` |
| 集成测试 | daemon 启动→rescan 完成→`Recall` 返回完整结果；rescan 进行中调用 `Recall` 返回 `degraded=true`，`KnownTags` 阻塞等待 | `go test ./internal/memory/...` |
| 工具测试 | agent 通过 `recall_memory` 工具召回，返回 markdown 列表能被 agent 正确解析并在下一步 `read_vault_note` 中使用 | Phase 6 工具集集成测试扩展 |
| Demo | 真实 vault：先跑 Phase 7 enrich 至少 30 条 Raw 沉淀 tag，再用 `recall_memory` 在 ask 路径 / `openwhisker memory reindex` 后人工检验召回质量 | 类似 Phase 6/7 demo 形式 |
| 回归 | Phase 7 enrich 切到 `memory.KnownTags()` 后，原有验收（tag 命中、needs-review、new_tag_candidate 三类样本）通过率不下降 | 复跑 Phase 7 demo |

## 决议（实现前对齐）

以下 7 条为 2026-05-27 收掉的原待决问题，落地时按此执行。

1. **enrich 切换的时机 = 同步切换**：Phase 8 落地的同一 PR 内，把 Phase 7 `internal/tagvocab/` 迁入 `internal/memory/`、enrich 词表查询切到 `memory.KnownTags()`。理由：避免"memory 已有 API 但 enrich 还用自己最小实现"的过渡形态；按 [[feedback_prefer_fewer_larger_commits]]，端到端落地比"中间状态可独立 review"更重要。
2. **enrich 内部检索不切**：Phase 7 enrich agent 找候选 Knowledge 关联仍用 `vault_text_search`，不替换为 `memory.Recall()`。理由：enrich 是 LLM 调用方，已熟悉现有工具；`memory.Recall` 设计的目标受众是 Go 直调方（intent router / scheduler skill）和 `recall_memory` LLM 工具，不是已经跑通的 enrich agent。强切要改 prompt，无收益。
3. **索引一致性容忍上限 = 接受重启或手动 reindex 才生效**：v1 不引入文件 watcher / Obsidian Sync 钩子。理由：手改 frontmatter 是低频事件；`openwhisker memory reindex` 是逃生口；watcher 异步 race 风险高于其收益。Follow-up 留作"真出现痛点再做"。
4. **`recall_memory` 默认 limit = 10**：核心 API 不兜底，工具层兜 10。理由：v1 没有 demo 数据，先用文档原数；调用方可以显式覆盖。
5. **`TagMode` 默认值 = `any`**：理由：enrich 写入端限制 1–4 个 tag，召回端默认严格交集（`all`）会让多 tag 查询返回空；`any` 召回多噪音高，但有 `Score` 排序兜底，比"空结果"用户体验好。`all` 留给 agent 显式指定。
6. **`memory_known_tags` 只增不减**：剔除语义留给手动 reindex（清表后从 vault 重建自然达成剔除）。理由：自动剔除策略（如 "N 天未见"）会让 enrich 在跨长时间窗的 Raw 上误判 needs-review；手动 reindex 已经够用。若长期使用后已知词表过大导致 enrich 输入 token 占用问题再开。
7. **`memory_recalls` trace 永久保留**：v1 不引入滚动清理。理由：没有数据用例之前先保留全量，简单优先；表本身是窄表，单条目极小，长期增长可承受。

## PR 边界

**单 PR + 单 commit**，沿用 Phase 7 决议 #11 的同源理由。范围 = `internal/memory/` 全量（含 `wikilink_resolver.go` / `sqlite_store.go` / `KnownTags` / `Recall`）+ 四张表 schema migration + executor Apply hook + `recall_memory` 工具 + enrich 切换 + `openwhisker memory reindex` CLI + 单测 + 文档同步（progress.md / CHANGELOG.md）。中间 commit 都无法独立 demo，端到端跑通才是验收硬要求。

## Follow-up（不阻塞 Phase 8 落地）

- intent router 接入 `memory.Recall()`，把"这条消息是否延续某个 tag 主题"作为分类信号；
- 周回顾 / 未闭环检查类 scheduler skill 接入；
- 向量检索作为 Pass 4 兜底（保持 enrich frontmatter 仍为权威，向量只补召回）；
- 多跳 related 扩散（v1 固定单跳；若召回质量需要再放开，加跳数参数与衰减函数）；
- 召回质量反馈闭环（agent 调用后用户回复 thumbs up/down，落 `memory_recalls` 表用于离线分析）；
- Obsidian Sync 钩子或文件 watcher，减少手改 frontmatter 后的索引延迟；
- 词表演化辅助：把 `openwhisker_new_tag_candidates` 字段累计的建议聚合成一份"待词表评审"摘要，辅助你周期性扩词表。
