# 收件箱自动 enrich

本文描述 inbox enrichment：在 raw 输入落盘后，叠加一层**异步的 enrichment agent run**，让 LLM 用只读 vault 工具把刚落盘的 Raw 在你 vault **已有的 `topic/` / `skill/` tag 词表**里定位，把归属结果写回 Raw 的 frontmatter。

它复用 [Agent 工具调用](agent-tooling.md)的同一套 runtime 与 5+1 read-only 工具，写回仍走主线 `VaultPlan → Policy → Executor`（见[设计哲学](../20-architecture/design-philosophy.md)），词表与召回侧依赖[记忆召回](memory-recall.md)。

核心立场：

- **不阻塞 capture**：inbox 仍先落盘、先回执，enrichment 是落盘之后异步触发；
- **不替用户做整理**：只给出 tag 归属信号，不移动文件、不创建 Knowledge 笔记、不动 Raw Text；
- **不发明 vault 概念**：完全复用 vault 已有受控词表和 `status/needs-review` 协议，不引入新前缀；
- **不引入新写入入口**：写回复用 `rewrite_note` 操作类型，写入面由 policy 层物理约束。

判断轴是 **"这条 Raw 在已有知识图谱里属于哪个 topic / skill"**，不是 "Raw 之间是否重复"——Raw 是流水账，知识点反复出现是常态。操作层的"用户重复贴同一段"由 CaptureBucket 处理，不归 enrichment 管。

## 能力边界

引入：

- 新作业类型 `JobTypeEnrichRaw`，由 `IngestRaw` 落盘成功后入队；
- 复用 agent runtime + 同款只读工具集，输入是新落盘 Raw 的 path + 内容摘要 + **当前 vault 已知 `topic/` / `skill/` 词表**，输出结构化 `EnrichResult`；
- 写回唯一允许的 op 是 `rewrite_note`，target_path 严格限定为本 enrich job 对应的 inbox 文件，由 policy 硬保证；
- 低风险 auto-apply（三 guard 通过），不进 approval 队列；
- 失败 / 超时静默丢弃 + 一条 debug 级 outbox，Raw 本身永不受影响；
- CLI `openwhisker enrich <rawJobID>` 手动重跑；Matrix `/no-enrich`（一次性）与 source_key blocklist 关闭 enrichment。

不引入：

- 不在 Raw 之间做"重复度"判断（不出 `duplicate_of`）；
- 不修改 Raw Text、不在正文追加 callout；
- 不自动移动或重命名 inbox 文件，路由仅作 frontmatter 建议字段；
- **不自创 tag**：写入 `tags` 的项必须命中派生自当前 vault 的已知词表，否则只能走 `openwhisker_new_tag_candidates` 字段记录；
- 不引入新工具集合、不引入向量检索、不跨 vault 匹配。

## Workflow

```text
IngestRaw (现有, 同步)
  ├── buildRawPlan → Apply → Raw/Inbox/{jobID}.md
  ├── outbox: "Raw capture saved to ..."
  └── enrichQueue.Enqueue(raw_path)          ← 事件路径主入口
        │   写入 sqlite 表 enrich_jobs
        ▼
  daemon scan tick (5min)                    ← scan 兜底 (独立路径)
    扫 Raw/Inbox/, 过滤后入队:
      无 openwhisker_enriched_at AND filename != *_bucket.md
      AND NOT has tag raw/bucket AND mtime ≤ now−10min AND attempts < 5
        │
        ▼
EnrichWorker (daemon goroutine, 串行消费 enrich_jobs)
  ├── 读取当前 vault 已知 tag 词表
  ├── pre_hash = sha256(file)
  ├── AgentRunner.Run(skill=inbox-enrich, input={raw_path, excerpt, source, known_*_tags})
  │     tools: list_vault_dir / read_vault_note / vault_text_search /
  │            vault_outlinks / vault_backlinks / submit_result(EnrichResult)
  ├── buildEnrichPlan(EnrichResult) → VaultPlan{op: rewrite_note, target: 同一 Raw 文件}
  ├── cur_hash = sha256(file); if != pre_hash → skipped_concurrent_edit, attempts++
  ├── policy.Check(path_guard / field_guard / body_guard)
  ├── executor.Apply → done
  └── outbox(可选): route_suggestion / new_tag_candidate
```

## 触发模型

事件路径主 + scan 兜底，入队点在 `IngestRaw` 末尾，持久化用 sqlite 表 `enrich_jobs`。

**事件路径（主）**：`IngestRaw` 落盘成功后、回 outbox 之前写入 `enrich_jobs`，daemon enrich worker goroutine 串行消费。例外：Matrix 最近一条消息含 `/no-enrich`、配置 `source_key_blocklist` 命中、CaptureBucket 的 `AppendRawBucket` 路径——这三类跳过入队（bucket 在 vault 语义里是"还没决定怎么分"的容器，给它打 tag 违背语义，v1 永不补 bucket-level enrich）。

**scan 兜底（辅）**：daemon 周期 tick（默认 5min）扫 `Raw/Inbox/`，把满足全部条件的文件入队（无 `openwhisker_enriched_at`；非 `*_bucket.md`、无 `raw/bucket` tag；mtime ≤ now−10min 的**静默期过滤**避开活跃编辑；对应 `attempts < 5` 的**上限熔断**）。覆盖三类丢失：入队后 worker 跑前 daemon crash、用户手动拷文件进 `Raw/Inbox/`、用户在 Obsidian 删了 `openwhisker_enriched_at`。

```sql
CREATE TABLE enrich_jobs (
  raw_path      TEXT PRIMARY KEY,   -- UNIQUE, scan 重复入队自动去重
  parent_job_id TEXT,               -- IngestRaw 来源; scan 路径为 NULL
  state         TEXT NOT NULL,      -- pending | running | done
                                    --   | skipped_concurrent_edit | attempts_exhausted | failed
  attempts      INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT,
  enqueued_at   DATETIME NOT NULL,
  updated_at    DATETIME NOT NULL
);
```

worker 按 `state IN ('pending','skipped_concurrent_edit') AND attempts < 5` 升序取，单 goroutine 串行，不并发（避开 provider rate limit 与同文件双开）。

## Agent 契约

输入：`raw_path` / `raw_job_id` / `source` / `source_key` / `created_at` / `excerpt`（前 N 字符）/ `known_topic_tags` / `known_skill_tags`。agent 可 `read_vault_note(raw_path)` 拿全文、`vault_text_search` 在 Knowledge/ 找候选关联、`vault_outlinks/backlinks` 翻邻居。

输出（`submit_result` payload）：

```json
{
  "tags": ["topic/system-design", "skill/golang"],
  "related": ["[[Knowledge/Languages/Golang/concurrency|Go 并发]]"],
  "route_suggestion": { "target_dir": "Interview/...", "confidence": 0.8, "reason": "..." },
  "new_tag_candidates": [ { "tag": "topic/work-stealing", "reason": "现有词表无对应主题" } ],
  "needs_review": false,
  "notes": "可选, outbox debug"
}
```

- `tags`：1–4 个，必须全部命中 `known_topic_tags ∪ known_skill_tags`，否则 reject；
- `related`：可选 wikilink，作为辅助线索（非主信号）；
- `route_suggestion`：可选，只在判断该 Raw 应归 Interview/Life/ 而非保留 Inbox 时填，confidence ≥ 0.7 才写 frontmatter。**宽容解析**：部分 provider 偶发把该 optional 字段输出成裸字符串而非对象，解析时退化为存入 `reason`、`confidence` 留 0（故不写 frontmatter），不让一个 optional 字段的格式偏差硬失败整条 `EnrichResult`；
- `new_tag_candidates`：词表里没有但 enrich 觉得"该有"的候选，**不**进 `tags`；
- `needs_review`：`tags` 找不到合适项 / `new_tag_candidates` 非空 / LLM 拿不准时为 true，policy 据此追加 `status/needs-review`；
- 全部字段允许为空；空对象是合法收口，policy 仍写 `_enriched_at` / `_enrich_run_id` 留痕。

## 写回字段（frontmatter）

| 字段 | 写入规则 |
|---|---|
| `tags`（vault 原生） | **仅追加**：`topic/*` / `skill/*` 必须命中词表；可追加 `status/needs-review`（needs_review 时）与 `raw/*`；禁追加 `type/*` / `interview/*` / 词表外项；禁删已有项 |
| `related`（vault 原生） | 仅追加，最多新增 3 项 |
| `openwhisker_route_suggestion` | confidence ≥ 0.7 才写 |
| `openwhisker_new_tag_candidates` | 仅当给出候选时写（`{tag, reason}` 数组） |
| `openwhisker_enriched_at` | 总是写——scan 兜底通过其缺失判断"需要 enrich" |
| `openwhisker_enrich_run_id` | 总是写，便于回溯 trace |
| `openwhisker_enrich_attempts` | 同步当前 `enrich_jobs.attempts`，vault 自洽 |

confidence 阈值（`route_suggestion.confidence ≥ 0.7`、tag 严格在词表内）写死代码常量。Raw Text 区块及上表以外的 frontmatter 一概不动。

## Policy 边界

三道 guard，全部通过则 risk 降为 low、auto-apply；任一不通过 → plan 直接 reject + outbox debug，不进 approval 队列：

- `path_guard`：enrichment plan 的 `target_path` 必须等于 enrich job 的 `parent_raw_path`；
- `field_guard`：rewrite_note payload 差分检查——只允许上表 7 个字段变化，其余 frontmatter before/after 必须相等；`tags` 满足 `after ⊇ before`（不删）且新增项满足前缀规则，`topic/*` / `skill/*` 必须在词表内；`related` 满足 `after ⊇ before` 且新增 ≤ 3；
- `body_guard`：rewrite 后 Raw Text 区块 sha256 必须等于 rewrite 前。

## 执行通道与 budget

走 `agentdispatch` 统一入口，daemon 端单独 enrich worker goroutine 串行消费，trace 落 `agent_runs`（`trigger_kind="enrich"`）。enrich 用**独立 budget**：`max_tool_calls=8` / `wall_clock=20s`，在 dispatch 处硬编码传入，不读 yaml。enrich 是系统自动触发、调用频率比 ask 高一个量级、任务面更窄（典型路径 `vault_text_search` 1–2 次 + `read_vault_note` 数次 + `submit_result`），独立 budget 是成本兜底。

## 失败语义

| state | 触发 | 处置 |
|---|---|---|
| `pending` | 入队 / scan 入队初始态 | worker 拉起 |
| `running` | worker 取走 | 防并发取重 |
| `done` | Apply 成功 | 终态 |
| `skipped_concurrent_edit` | pre_hash ≠ cur_hash | attempts++；不立即重试；scan 下一轮（过静默期后）重新捡 |
| `failed` | LLM 失败 / budget 超限 / 解析失败 / policy reject | attempts++；scan 下一轮重试；debug outbox |
| `attempts_exhausted` | attempts ≥ 5 后任何失败 | 终态；scan 不再捡；debug outbox |

enrich 写的字段全是 append-only 或 OpenWhisker 私有，"丢失"语义不存在；并发编辑用 hash guard + drop + scan 兜底 + mtime 静默期 + attempts 上限即可，不引入 merge 策略。Obsidian 自带的外部修改检测作为最后兜底。`openwhisker enrich <rawJobID>` 是逃生口：把对应 `enrich_jobs` 行重置为 pending、清零 attempts、绕过静默期与 5 次上限。

scan 周期（5min）、mtime 静默期（10min）、attempts 上限（5）三个常量写死代码，不外置 yaml——与 budget 同源，待跑批数据再决定是否外置。

## 与 CaptureBucket 的关系

不替代，职责互补：CaptureBucket 做同一 `source_key` 短窗口内连续输入的确定性聚合（落同一 Raw 文件）；enrichment 做跨来源跨时间的 tag 归属定位（输出元数据）。enrichment 若判断当前 Raw 与某 active bucket 主题高度一致，**只**在 outbox 提示，不自动合并。
