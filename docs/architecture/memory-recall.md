# 记忆召回

本文描述 `internal/memory/`：一个**跨调用方共享**的只读 memory 服务。它不引入新的 vault 写入面、不引入新的存储依赖、不引入向量索引，只把以下两件事捆成一个独立模块：

- **已知 tag 词表服务**：派生自 vault frontmatter 的 `topic/*` / `skill/*` 集合，daemon 启动时全量 rescan，executor Apply 成功时增量更新。[收件箱自动 enrich](inbox-enrichment.md) 用它做 tag 候选校验；其它写入侧也共用同一份词表。
- **Recall 核心 API**：`Recall(ctx, RecallRequest) -> RecallResult` 的纯 Go 入口，回答"按 tag 或 query 找相关 Raw / Knowledge 笔记"。读 frontmatter 索引 + sqlite cache + 现有 `vault_text_search` + [Agent 工具调用](agent-tooling.md)里的 `internal/vault/linkindex` 内存邻接图，不直接调 LLM。

每个消费者按需包一层薄壳：LLM agent 包成第 7 件工具 `recall_memory`；enrich job、未来的 intent router / scheduler skill 直接 Go 调用，无需各自再造一套检索。

核心立场：

- **memory 不绑死某个 agent**：核心 API 是 Go 函数，调用方决定上下文与展示形态；
- **不引入新真值源**：索引完全派生自 vault frontmatter（enrich 写入的 `tags` / `related` + 词表派生）+ vault 文本，frontmatter 是源，sqlite 索引 / linkindex 内存图都是 cache；链接图真值源与 agent runtime 同源，避免双写；
- **不黑盒**：每条结果带 `source` 标签（`tag_match` / `text_match` / `related_link`），可追溯；
- **简单优先**：v1 索引就是 tag 反向表 + 复用 linkindex 的单跳 related 邻接读，不引入多跳遍历、向量、个性化排序。

## 为什么是共享基础设施

**词表派生**和 **tag 反向索引** 物理上几乎是同一件事——都要扫 frontmatter `tags`，区别只是聚合方式（一个聚合成"已出现过的 tag 集合"，一个聚合成"tag → 笔记列表"）。这件事不该由 enrich 自己实现一遍：未来 intent router、scheduler skill、ask agent 各重写一遍是浪费。所以 memory 定位成跨调用方的共享基础设施，而非某个 agent 的专属工具。

```text
              internal/memory  (Go API, 无 LLM 依赖)
              - KnownTags(prefix)
              - Recall(req)
                     │
   ┌─────────────┬───┴────────┬──────────────────┬──────────────┐
   ▼             ▼            ▼                  ▼              ▼
recall_memory  enrich      intent router     scheduler skill   ...
(LLM 工具)     (Go 直调)    (Go 直调, 未来)    (Go 直调, 未来)
```

## 对外入口

- `memory.KnownTags(ctx, prefix) -> []string`：已知 tag 词表查询，支持前缀过滤（`topic/` / `skill/`）；
- `memory.Recall(ctx, RecallRequest) -> RecallResult`：tag / query 召回，自带一跳 related 邻居扩散（邻居来自注入的 `LinkGraph` 接口，daemon 用 `internal/vault/linkindex` 实现）。

新 sqlite 表：

- `memory_tag_index`（`tag, note_path, updated_at`）：tag 反向索引 cache；
- `memory_known_tags`（`tag, first_seen_at, last_seen_at`）：词表 cache；
- `memory_recalls`（`caller_kind, query/tags 概要, result_count, latency_ms, called_at`）：轻量调用 trace（v1 不滚动清理）。

明确不引入：向量 / embedding 索引、agent 对话历史召回（episodic memory）、auto-inject（recall 必须由调用方主动调）、重排 / 学习排序、跨 vault、反馈闭环；不主动剔除"曾出现但当前不再出现"的 tag（只增不减，剔除留给手动 reindex）；不新增任何 vault 写工具（memory 是只读侧，唯一的"写"是索引 cache 内部维护，不暴露给调用方）。

## 读路径（Recall）

```text
memory.Recall(ctx, RecallRequest{Query, Tags, TagMode, ScopeDirs, Since, Limit, ExpandRelated, CallerKind})
  ├── Pass 1: Tags 非空 → tag 反向索引召回
  │     SELECT note_path FROM memory_tag_index WHERE tag IN Tags   (any/all 由 TagMode 控制)
  │     → source = "tag_match"
  ├── Pass 2: Query 非空 → vault_text_search(Query) 兜底
  │     → source = "text_match"
  ├── Pass 3: ExpandRelated(默认 true) 且 linkindex.Ready() → 对 Pass 1/2 命中集做一跳 related 扩散
  │     neighbors += LinkGraph.OutlinksRelated(seed) + BacklinksRelated(seed)   // frontmatter related 双向
  │     → source = "related_link" (仅当该 path 未被 Pass 1/2 命中时)
  │     linkindex 未就绪时跳过 Pass 3, 结果标 degraded=true
  ├── 合并 + 去重 + 简单加权打分
  ├── ScopeDirs / Since 过滤 (默认排除 Archive/)
  ├── Limit 截断 (调用方传, 核心 API 不内置 magic number)
  ├── INSERT memory_recalls 轻量 trace
  └── return RecallResult{Items}
```

`Query` 和 `Tags` 至少一个非空，否则 reject；两者都给时分别走两条 pass 再合并。

```go
type RecallRequest struct {
    Query         string
    Tags          []string
    TagMode       string      // "any" | "all", 默认 "any"
    ScopeDirs     []string    // 空则全 vault 减 Archive/
    Since         *time.Time
    Limit         int         // 调用方必传, 核心层不兜底
    ExpandRelated bool        // 默认 true
    CallerKind    string      // "agent" | "enrich" | "router" | "scheduler"
}

type RecallItem struct {
    Path      string
    Score     float64   // 0~1
    Source    string    // "tag_match" | "text_match" | "related_link"
    Excerpt   string    // text_match 时非空
    MatchedOn []string
}
```

## 评分（v1 简单加权）

每条候选独立计算 base：

- tag 命中条目：base = 命中 tag 数 / `len(Tags)`；
- 文本命中条目：base = `vault_text_search` 现有 score；
- 一跳扩散条目：base = seed 最高分 × 0.7（一跳衰减）。

乘 source 权重：`tag_match` ×1.0 / `text_match` ×0.6 / `related_link` ×0.4。同路径多 source 命中取最高分作主排序，`MatchedOn` 聚合所有来源；`related_link` 不覆盖已有的 `tag_match` / `text_match` 主标签，仅作补充来源。v1 不引入时间衰减 / 个性化 / 学习排序。

## 写路径（索引维护，对调用方透明）

```text
executor.Apply(plan) 成功:
  若 plan 含 rewrite_note 且 diff 触及 frontmatter `tags`:
    UpdateTagIndex(note_path, new_tags)   // DELETE WHERE note_path, 再按 new_tags INSERT
    UpdateKnownTags(new_tags)             // UPSERT memory_known_tags 的 topic/* | skill/*

// frontmatter `related` 的变更不走 memory 模块——linkindex 在 fsnotify 路径上
// 自己感知 vault 文件变化(含 executor 写出的 rewrite 结果), Pass 3 现读内存图即可。
```

启动时 daemon boot → `RescanTagIndex(vaultRoot)`，串行扫一次 `Knowledge/` / `Interview/` / `Life/` 下所有 `.md` 的 frontmatter `tags`，幂等重建 `memory_tag_index`（全量覆盖）与 `memory_known_tags`（仅追加，保持只增不减）。链接图全量扫由 linkindex 在既有 daemon 启动路径完成，memory 模块自身不引入独立守护 goroutine 与文件 watcher。链接解析沿用 linkindex 的 `resolveTarget`（已覆盖短名 / 带子路径 / 含 alias `|` 三种形态）。

## 链接图复用

Pass 3 一跳扩散通过 `LinkGraph` 接口消费，daemon 注入既有 `internal/vault/linkindex` 适配器，**不新建 `memory_link_index`**。linkindex 已是内存图 + fsnotify 实时同步 + 覆盖三种 wikilink 形态；新建 sqlite 镜像会引入第二份链接真值源、第二套 wikilink 解析、与 `vault_outlinks/backlinks` 工具的潜在不一致。复用后 memory 模块自身仍不引入 watcher——watcher 是复用而非新增。适配器只透出 `SourceKindFrontmatterRelated` 边：正文 `[[...]]` 不作为 related diffusion 的 seed-neighbor。

## 工具层 recall_memory

agent 工具集 +1：

```json
{ "name": "recall_memory", "parameters": {
  "query": {"type":"string"}, "tags": {"type":"array"},
  "tag_mode": {"enum":["any","all"],"default":"any"}, "limit": {"type":"integer","default":10},
  "scope_dirs": {"type":"array"}, "since": {"format":"date-time"} } }
```

工具内部固定 `CallerKind="agent"`、`limit` 兜底 10（核心 API 不兜底，工具层兜），返回转成紧凑 markdown 列表而非 JSON。

## 索引一致性与失败语义

- vault frontmatter 是源；`memory_tag_index` / `memory_known_tags` 是派生 cache；`related` 邻接图复用 linkindex 内存图；
- 用户在 Obsidian 手改 frontmatter `tags`：不会立即触达 memory cache，**直到下次 daemon 重启全量 rescan 或手动 `openwhisker memory reindex`**（memory 模块 v1 不引入文件 watcher）；
- 手改 frontmatter `related`：linkindex fsnotify 秒级感知，Pass 3 自然跟随（新鲜度高于 tag 路径，是 linkindex 复用的副效益）；
- `memory_known_tags` 仅追加，剔除语义留给手动 reindex（清表后从 vault 重建自然达成剔除）；
- memory 自有索引未就绪（启动 rescan 中）：`Recall` 返回部分结果（仅 `text_match`，标 `degraded=true`），`KnownTags` 阻塞等待 rescan 完成（enrich 必须拿到完整词表，否则可能误判 needs-review）；
- linkindex 未就绪：跳过 Pass 3，其余照常返回，标 `degraded=true`；
- sqlite 故障：两个入口都返回错误，不 fallback；文本检索失败但 tag 索引成功时返回 tag 结果，错误降级为 warning 日志。

`TagMode` 默认 `any`：enrich 写入端限制 1–4 个 tag，召回端默认严格交集（`all`）会让多 tag 查询常返回空；`any` 噪音高但有 `Score` 排序兜底，比空结果体验好，`all` 留给 agent 显式指定。

## CLI

`openwhisker memory reindex`：手动全量重扫（清表后从 vault 重建），幂等。
