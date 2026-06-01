# Phase 7 Inbox Enrichment

状态：**v1 已实现（待真实 vault demo 验证）**。本文沿用 Phase 5/6 的格式，定义"利用 Phase 6 agent-driven 工具调用能力，对刚落盘的 inbox Raw 笔记按 vault 现有 tag 词表做归属定位并写回元数据"这一增量的设计意图、范围边界和决议。testdata 烟测全部通过；真实 vault 联调放在独立验证队列，不挂顶部 priority。

## 概述

Phase 7 在 Phase 1 已有的 `IngestService.IngestRaw` 低风险自动 apply 流之后，叠加一层**异步的 enrichment agent run**，让 LLM 用 Phase 6 的同一套只读 vault 工具，把刚落盘的 Raw 在你 vault **已有的 `topic/` / `skill/` tag 词表**里定位，结果写回 Raw 的 frontmatter。

核心立场：

- **不阻塞 capture**：inbox 仍然先落盘、先回执，enrichment 是落盘之后异步触发；
- **不替用户做整理**：enrichment 只给出 tag 归属信号，不移动文件、不创建 Knowledge 笔记、不动 Raw Text；
- **不发明 vault 概念**：完全复用 vault 已有的 tag 受控词表（见 `~/Documents/KnowLedge/Meta/Tagging.md`）和 `status/needs-review` 协议，不引入新前缀；
- **不引入新写入入口**：enrichment 的写回仍然走主线 `VaultPlan → Policy → Executor`，复用 `rewrite_note` 操作类型，写入面被 policy 层物理约束。

判断轴是 **"这条 Raw 在你已有的知识图谱里属于哪个 topic / skill"**，不是 "Raw 之间是否重复"——Raw 是流水账，知识点本来就会反复出现，把 Raw 之间的重复当主信号无意义。操作层的"用户重复贴同一段"由 CaptureBucket 处理，不归 enrichment 管。

## 背景

Phase 1 的 inbox 写入是无状态的：每条 Raw 都生成 `Raw/Inbox/{jobID}.md`，frontmatter 只记录 source / source_key / created_at / job_id，没有任何与 vault 已有内容的关联。CaptureBucket（Phase 4B 引入）按 `source_key + TTL` 把同一会话短窗口内的连续输入聚到同一 Raw 文件，但匹配维度只有 source_key，不涉及内容。

Phase 6 引入了 `AgentRunner` + 5 + 1 read-only vault 工具（`list_vault_dir` / `read_vault_note` / `vault_outlinks` / `vault_backlinks` / `vault_text_search` / `submit_result`）和 budget / trace 基础设施，让 LLM 可以自主决定去 vault 哪里查信息。这套能力当前只接在"用户主动 ask"和"scheduler cron tick"两个 host 上，没有覆盖"事件驱动"——也就是 inbox 落盘这一类系统主动产生的触发。

与此同时，vault 早就存在一套成熟的 tag 体系：

- 受控前缀 `type/` / `status/` / `topic/` / `skill/` / `interview/` / `raw/`，**禁止为一次性上下文创建新标签**；
- `topic/` 和 `skill/` 每篇限 1–4 个；
- 不确定时已有协议：加 `status/needs-review`，正文/外部记录原因。

Phase 7 把同一套 agent runtime 接到 inbox 落盘事件上，目的是让 agent 给 Raw 加一层 **vault 原生 tag 归属**：这条 Raw 该挂哪些 `topic/` / `skill/`，是否更适合走 Interview/Life/ 而不是默认的 Raw/Inbox/，以及——如果在现有词表里找不到合适归属，按 vault 已有的 `needs-review` 协议挂起，并把建议的 tag 候选记下来留给后续整理流程。

## 核心结论

Phase 7 引入的能力：

- 新增 `JobTypeEnrichRaw` 作业类型，由 `IngestRaw` 在落盘成功之后 enqueue；
- 复用 Phase 6 `AgentRunner` + 同款只读 vault 工具集，agent 输入是新落盘 Raw 的 path + 内容摘要 + **当前 vault 已知 `topic/` / `skill/` tag 词表**，输出是结构化 `EnrichResult`；
- enrichment 写回走主线 `VaultPlan`，唯一允许的 op 是 `rewrite_note` 且 target_path 严格限定为本 enrich job 对应的那一份 inbox 文件，由 policy 层硬保证；
- 允许修改的 frontmatter 字段：
  - **vault 原生 `tags`**：仅允许**追加** `topic/...` / `skill/...` / `status/needs-review` / `raw/...`，不允许删除已有 tag，不允许追加 `type/` / `interview/` / 词表外的项；
  - **vault 原生 `related`**：仅允许**追加** wikilink 数组项，作为辅助线索；
  - **OpenWhisker 私有字段**：`openwhisker_route_suggestion`、`openwhisker_new_tag_candidates`、`openwhisker_enriched_at`、`openwhisker_enrich_run_id`；
- enrichment job 走低风险 auto-apply（前提是 policy 三 guard 通过），不进 approval 队列；
- 失败/超时静默丢弃 + 写一条 debug 级 outbox 消息，Raw 本身永远不受影响；
- 新增 CLI `openwhisker enrich <jobID>` 用于手动重跑某条 Raw 的 enrichment；
- 新增 Matrix 命令 `/no-enrich`（一次性，作用于下一条 Raw）和 source_key 白名单关闭 enrichment 的能力。

Phase 7 明确不引入的能力：

- 不在 Raw 之间做"重复度"判断；Raw 间是否重复不是 enrichment 的工作；
- 不修改 Raw Text 区块，不在正文追加 callout；
- 不自动移动或重命名 inbox 文件，路由仅作为 frontmatter 建议字段；
- **不自创 tag**：所有写入 `tags` 的项必须出现在派生自当前 vault 的"已知 tag 词表"中，否则只能走 `openwhisker_new_tag_candidates` 字段记录，等待人工/整理流程纳入词表；
- 不引入新的工具集合，复用 Phase 6 的 5 + 1 read-only 工具；
- 不引入向量检索，匹配走 `vault_text_search` + `vault_backlinks` + LLM 排序；
- 不引入跨 vault 匹配；
- 不让 enrichment 路径成为新的 vault 写入面，rewrite_note 的写回边界由 policy 层物理约束。

## Workflow

```text
IngestRaw (现有, 同步)
  ├── createJob (JobTypeIngestRaw)
  ├── buildRawPlan → Apply → Raw/Inbox/{jobID}.md
  ├── outbox: "Raw capture saved to ..."
  └── enrichQueue.Enqueue(raw_path)  ←  事件路径主入口 (新增)
        │   写入 sqlite 表 enrich_jobs(raw_path, attempts, state, ...)
        ▼
                    ─── scan 兜底 (新增, 独立路径) ───
                    daemon scan tick → 扫 Raw/Inbox/
                      └── 过滤：无 openwhisker_enriched_at
                              AND filename != *_bucket.md
                              AND NOT has tag raw/bucket
                              AND mtime ≤ now − 10min
                              AND attempts < 5
                          → enrichQueue.Enqueue(raw_path)（按 raw_path UNIQUE 去重）
                    ─────────────────────────────────
        │
        ▼
EnrichWorker (新增, daemon goroutine, 串行消费 enrich_jobs)
  ├── 取一条 pending job
  ├── createJob (JobTypeEnrichRaw, parent_job_id={原 Raw job})
  ├── 读取当前 vault 已知 tag 词表 (派生自 frontmatter，详见"已知 tag 词表")
  ├── pre_hash = sha256(file)   ← hash guard 起点
  ├── AgentRunner.Run(skill=inbox-enrich, input={
  │     raw_path, excerpt, source, source_key, created_at,
  │     known_topic_tags: [...], known_skill_tags: [...]
  │   })
  │     └── tools: list_vault_dir / read_vault_note / vault_text_search /
  │                vault_outlinks / vault_backlinks / submit_result(EnrichResult)
  ├── buildEnrichPlan(EnrichResult) → VaultPlan{op: rewrite_note, target: 同一 Raw 文件}
  ├── cur_hash = sha256(file)   ← hash guard 终点
  │   if cur_hash != pre_hash:
  │     enrich_jobs.state = skipped_concurrent_edit
  │     attempts++; return（不立即重试，scan 下一轮捡）
  ├── policy.Check (
  │     path_guard:  target_path == parent_raw_path;
  │     field_guard: 仅允许在 7 字段白名单内变更，tags / related 仅允许追加，
  │                  tags 追加项必须满足前缀规则且 topic/ + skill/ 项必须命中已知词表;
  │     body_guard:  Raw Text 区块 sha256 不变
  │   )
  ├── executor.Apply
  ├── enrich_jobs.state = done
  └── outbox(可选): "Suggest route: ..." / "New tag candidate: ..."
```

## 设计

### 触发时机

Phase 7 落地的触发模型是 **M3 = 事件路径 + scan 兜底**，入队点 P1 = `IngestRaw` 末尾，持久化 Q2 = sqlite 表 `enrich_jobs`。

**事件路径（主）**

`IngestRaw` 落盘 Apply 成功之后、回 outbox 之前，把 `(raw_path, parent_job_id)` 写入 `enrich_jobs` 表，daemon 端的 enrich worker goroutine 串行消费。事件路径例外：

- Matrix 端最近一条用户消息含 `/no-enrich` 时，该次 IngestRaw 跳过入队；
- 配置层 `enrichment.source_key_blocklist` 命中的 source_key 跳过；
- CaptureBucket 的 `AppendRawBucket` 路径**不**入队（决议 2，v1 永不补 bucket-level enrich）。

**scan 兜底（辅）**

daemon 周期性 tick（默认 5 分钟一次）扫 `Raw/Inbox/`，把满足全部条件的文件入队：

- frontmatter 无 `openwhisker_enriched_at` 字段；
- 文件名不以 `_bucket.md` 结尾、frontmatter 不含 `raw/bucket` tag；
- mtime ≤ `now − 10min`（**静默期过滤**——避开用户正在编辑的文件，避免 hash guard 反复触发）；
- 对应 `enrich_jobs` 行的 `attempts < 5`（**上限熔断**——防止极端文件无限重试占 LLM budget）。

scan 兜底覆盖三类丢失：(a) 事件路径入队后但 worker 跑前 daemon crash；(b) 用户手动拷贝文件到 `Raw/Inbox/` 而非走 `IngestRaw`；(c) 用户在 Obsidian 把已 enrich 文件的 `openwhisker_enriched_at` 删了导致需要重做。

**enrich_jobs sqlite 表**

```sql
CREATE TABLE enrich_jobs (
  raw_path     TEXT PRIMARY KEY,        -- UNIQUE，scan 重复入队自动去重
  parent_job_id TEXT,                   -- 来自 IngestRaw；scan 路径为 NULL
  state        TEXT NOT NULL,           -- pending | running | done
                                        --       | skipped_concurrent_edit
                                        --       | attempts_exhausted | failed
  attempts     INTEGER NOT NULL DEFAULT 0,
  last_error   TEXT,
  enqueued_at  DATETIME NOT NULL,
  updated_at   DATETIME NOT NULL
);
```

worker 取 job 时按 `state IN ('pending', 'skipped_concurrent_edit') AND attempts < 5` 排序 `enqueued_at` 升序，单 goroutine 串行；不并发，避免 LLM provider rate limit 和同一文件被两个 worker 同时打开。

### 已知 tag 词表

enrich agent 不能自创 tag。词表完全派生自当前 vault：

- 启动时全量扫一次 `Knowledge/`、`Interview/`、`Life/`（不扫 `Raw/`、`Archive/`、`Meta/`）下所有 `.md` 的 frontmatter `tags`，收集出现过的 `topic/*` 和 `skill/*` 项作为已知词表；
- 增量：executor 每次 Apply 成功且 plan 改动了 frontmatter `tags` 字段时，更新词表（追加新出现的 `topic/` / `skill/`，不主动剔除——剔除留给手动 reindex）；
- 词表以**调用 agent 时的输入参数**形式传给 enrich agent，agent 在 `submit_result` 里返回的所有 `topic/` / `skill/` 项必须落在该集合里，否则 policy field_guard 直接 reject；
- 词表的物化与维护由 Phase 8 的 `internal/memory/` 模块承担（Phase 7 单独落地时先放在 enrich 模块内做最小实现，Phase 8 落地时切到共享实现，见 [`phase-8-memory-recall.md`](phase-8-memory-recall.md)）。

### Agent 输入契约

```json
{
  "raw_path": "Raw/Inbox/job-xxx.md",
  "raw_job_id": "job-xxx",
  "source": "matrix|cli|...",
  "source_key": "...",
  "created_at": "2026-...",
  "excerpt": "前 N 字符的 Raw Text",
  "known_topic_tags": ["topic/database", "topic/system-design", "..."],
  "known_skill_tags": ["skill/golang", "skill/python", "..."]
}
```

agent 可用 `read_vault_note(raw_path)` 拿完整内容、用 `vault_text_search` 在 Knowledge/ 子树找候选关联、用 `vault_outlinks/backlinks` 在已有图里翻邻居。

### Agent 输出契约（submit_result payload）

```json
{
  "tags": ["topic/system-design", "skill/golang"],
  "related": ["[[Knowledge/Languages/Golang/concurrency|Go 并发]]"],
  "route_suggestion": {
    "target_dir": "Interview/...",
    "confidence": 0.8,
    "reason": "..."
  } | null,
  "new_tag_candidates": [
    { "tag": "topic/work-stealing", "reason": "现有词表无对应主题" }
  ],
  "needs_review": false,
  "notes": "可选，用于 outbox debug"
}
```

字段语义：

- `tags`：1–4 个，必须全部命中 `known_topic_tags ∪ known_skill_tags`，否则 reject；
- `related`：可选；零到若干 wikilink，指向具体 Knowledge 笔记，作为辅助线索（不作为主信号）；
- `route_suggestion`：可选；只在 enrich 判断该 Raw 应归 Interview/Life/ 而非保留 Inbox 时填，confidence ≥ 0.7 才写入 frontmatter。**宽容解析**（2026-05-29）：部分 provider 偶发把该 optional 字段输出成裸字符串而非对象，`RouteSuggestion.UnmarshalJSON` 对字符串形态退化处理——存入 `reason`、`confidence` 留 0（低于 0.7 阈值故不写 frontmatter），不再让一个 optional 字段的格式偏差硬失败整条 `EnrichResult`；
- `new_tag_candidates`：可选；当 enrich 觉得"该有这个 tag 但词表里没有"时填，**不**进入 `tags` 字段；
- `needs_review`：当 `tags` 找不到合适项 或 `new_tag_candidates` 非空 或 LLM 自己拿不准时填 `true`，policy 据此向 `tags` 追加 `status/needs-review`；
- 全部字段允许为空，空对象表示"无可写出信号"，也是合法收口，policy 仍写入 `_enriched_at` / `_enrich_run_id` 留痕。

### 写回字段（frontmatter）

| 字段 | 类型 | 写入规则 |
|---|---|---|
| `tags`（vault 原生） | 字符串数组 | **仅追加**：`topic/*` 和 `skill/*` 必须命中已知词表；可追加 `status/needs-review`（当 needs_review=true）和 `raw/*`（已有约定）；禁止追加 `type/*` / `interview/*` / 词表外项；禁止删除已有项 |
| `related`（vault 原生） | wikilink 数组 | 仅追加；最多新增 3 项 |
| `openwhisker_route_suggestion` | string | confidence ≥ 0.7 才写 |
| `openwhisker_new_tag_candidates` | 数组（`{tag, reason}`） | 仅当 enrich 给出候选时写 |
| `openwhisker_enriched_at` | RFC3339 | 总是写——scan 兜底通过其缺失判断"需要 enrich" |
| `openwhisker_enrich_run_id` | string (agent_run id) | 总是写，便于回溯 trace |
| `openwhisker_enrich_attempts` | int | enrich worker 每次写回时同步当前 `enrich_jobs.attempts` 值；用于 vault 自洽（不依赖 sqlite 也能看出 enrich 走过几次） |

confidence 相关阈值（`route_suggestion.confidence ≥ 0.7`、tag 命中要求 = 严格在词表内）v1 写死代码常量，不外置 vault 配置。等 demo 跑批拿到第一手数据再决定是否外置。

Raw Text 区块及上表以外的 frontmatter 字段一概不动。policy 层用 body_hash + frontmatter 白名单双重校验。

### Policy 边界

新增 policy 子项：

- `path_guard`：enrichment plan 的 `target_path` 必须等于 enrich job 的 `parent_raw_path`，否则 reject；
- `field_guard`：rewrite_note payload 解析后做差分检查——
  - 只允许上表 7 个字段出现变化，其它 frontmatter 字段 before/after 必须完全相等；
  - `tags`：必须满足 `tags_after ⊇ tags_before`（不允许删），且 `tags_after − tags_before` 中每一项必须满足前缀规则；其中 `topic/*` 与 `skill/*` 项必须存在于已知词表；
  - `related`：必须满足 `related_after ⊇ related_before`，且新增项数 ≤ 3；
- `body_guard`：rewrite 后 Raw Text 区块的 sha256 必须等于 rewrite 前的 sha256，否则 reject。

满足三条 guard 时，risk 降为 low，auto-apply。任何一条不通过 → plan 直接 reject，写 outbox debug，不进 approval 队列（这是系统自动产物，没必要让人审 LLM 失控的产物，反而 reject 后 manual re-enrich 更干净）。

### 与 CaptureBucket 的关系

不替代。两者职责互补：

- CaptureBucket：来自同一 `source_key` 短窗口内连续输入的确定性聚合，落同一 Raw 文件；
- Enrichment：跨来源跨时间的 tag 归属定位，输出元数据。

如果 enrichment 判断当前 Raw 与某个 active CaptureBucket 主题高度一致，**只**在 outbox 提示用户"可能想 append 到 bucket X"，不自动合并。Bucket 与 Raw 的物理归属仍由 IngestRaw 同步路径决定。

### 执行通道

复用 Phase 6 已有的 agent run 队列：`JobTypeEnrichRaw` 走 `agentdispatch` 的统一 dispatch 入口，由 daemon 端单独的 enrich worker goroutine 串行消费（避免短时间内多条 Raw 同时打到 LLM provider）。trace 落 `agent_runs` 表，`trigger_kind = "enrich"`。

enrich 不复用 ask / vault-qa 的 budget，单独一组：`max_tool_calls = 8`、`wall_clock = 20s`，在 enrich dispatch 处硬编码传入 `AgentRunner`，不读 yaml 配置。enrich 是系统自动触发，调用频率比 ask 高一个量级，任务面也更窄（典型路径：`vault_text_search` 1–2 次 + `read_vault_note` 数次 + `submit_result`），独立 budget 是成本兜底。

### 失败语义

`enrich_jobs.state` 取值与处置：

| state | 触发条件 | 处置 |
|---|---|---|
| `pending` | 入队 / scan 兜底入队后初始态 | worker 拉起 |
| `running` | worker 取走后写入 | 防并发取重 |
| `done` | executor.Apply 成功 | 终态 |
| `skipped_concurrent_edit` | pre_hash != cur_hash | attempts++；不立即重试；scan 下一轮（且过 mtime 静默期）会重新捡 |
| `failed` | LLM 失败 / budget 超限 / submit_result 解析失败 / policy reject | attempts++；scan 下一轮重试；debug outbox 消息（默认不投递 Matrix） |
| `attempts_exhausted` | attempts ≥ 5 后任何失败 | 终态；scan 不再捡；debug outbox 消息（默认不投递 Matrix） |

用户可用 `openwhisker enrich <rawJobID>` 手动重跑——该子命令会把对应 `enrich_jobs` 行重置为 `pending` 并清零 `attempts`，绕过 mtime 静默期和 5 次上限。这是逃生口。

## 范围

包含：

- `internal/core/ingest.go`：`IngestRaw` 成功后 enqueue enrich job（写 `enrich_jobs` 表）；
- `internal/enrich/`（新建）：
  - `enrich.go`：EnrichRawJob 编排（vocab 装载 → pre_hash → agent run → cur_hash → buildEnrichPlan → policy → executor）；
  - `worker.go`：daemon 端 goroutine，从 `enrich_jobs` 串行取 pending；
  - `scan.go`：scan tick 入队逻辑（mtime 静默期 + bucket 过滤 + attempts 上限）；
  - `queue.go`：`enrich_jobs` 表的 DAO（Enqueue / TakePending / MarkState / Reset）；
- `internal/agent/`：新增 enrich skill 提示词模板（内置 system skill）；
- `internal/policy/`：新增 path / field / body 三 guard 的 enrich 子集校验路径，field_guard 含 `tags`/`related` 差分检查与词表查询；
- `internal/model/`：新增 `JobTypeEnrichRaw`，frontmatter 字段常量；
- `internal/tagvocab/`（临时位置，Phase 8 落地时迁入 `internal/memory/`）：已知 tag 词表的扫描、增量更新、查询；
- `cmd/openwhisker/main.go`：新增 `openwhisker enrich <rawJobID>` 子命令（手动重跑入口）；daemon 启动加 enrich worker + scan tick；
- `internal/adapters/matrix/`：识别 `/no-enrich` 一次性开关；
- `docs/phases/phase-7-inbox-enrichment.md`（本文）；
- `CHANGELOG.md`、`docs/progress.md`：在实现落地时联动更新。

不包含：

- 修改 Phase 6 `AgentRunner` / 工具集合本身；
- 修改 Phase 1 `buildRawPlan` 输出（Raw 落盘形态不变）；
- 引入向量检索 / embedding 索引；
- 自动 bucket 合并；
- 任何 vault 写工具扩展；
- 自动创建 Knowledge 笔记或自动扩 tag 词表。

## 验收

| 维度 | 验收要点 | 方式 |
|---|---|---|
| 单元测试 | `path_guard` / `field_guard`（含 tags 差分 + 词表查询 + related 仅追加）/ `body_guard` 三条 guard 各覆盖 happy path 和 reject path | `go test ./internal/policy/...` |
| 单元测试 | tag 词表扫描：在 fixture vault 上断言 `topic/*` / `skill/*` 提取正确，`Raw/` 与 `Archive/` 不参与 | `go test ./internal/tagvocab/...` |
| 集成测试 | IngestRaw → enqueue → enrich apply 全链路，断言 Raw Text hash 不变 + tags 追加预期项 + 其它 frontmatter 字段不变 | `go test ./internal/core/...` |
| Demo | 在真实 vault 跑覆盖三类（明显归属命中 / 需要 needs-review / 有 new_tag_candidate）的 Raw 各若干条，人工评估 tag 选择是否合理 | 类似 OPEN-1 的人工 demo |
| 失败注入 | LLM 返回非法 JSON / 越权字段 / 越权路径 / 改了 Raw Text / 追加词表外 `topic/` / 删了已有 tag → 全部 reject，Raw 不变 | 集成测试模拟 |
| 并发编辑 | enrich worker 跑到 cur_hash 前，故意改文件 → state = `skipped_concurrent_edit`，Raw 不变；scan 下一轮（mtime 静默期通过后）重新捡，第二次执行通过 | `go test ./internal/enrich/...` |
| 队列持久化 | enqueue 后立即重启 daemon → enrich_jobs 表中 pending 项被新进程的 worker 正确拉起；attempts ≥ 5 后转 `attempts_exhausted` 且 scan 不再捡 | `go test ./internal/enrich/...` |

## 决议（实现前对齐）

1. **enrich skill 内置 `internal/agent/`**，不走 vault `Agent/Skills/`。enrich 的 prompt 与 policy 层字段白名单、tag 词表协议是强耦合契约，放进 vault 等于让用户随手编辑 markdown 就能触发越权 reject，认知割裂大于可定制收益。
2. **AppendRawBucket 路径完全跳过 enrichment，bucket-level enrich 不规划**。bucket 在 vault 语义里就是"还没决定怎么分"的容器，给它打 tag 违背其语义；bucket 内容混杂，单次 enrich 输出不了一组语义自洽的 tag。Phase 8 `text_match` 仍可召回 bucket 内容，不是"完全消失"。未来真要给 bucket 内容打 tag，正确做法是先做 splitter agent 拆成独立 Raw 再各自 enrich——splitter 不进 Phase 7 Follow-up，等 vault 真正出现 bucket 召回痛点再启动。scan 兜底也按相同规则过滤（`filename != *_bucket.md AND NOT has tag raw/bucket`）。
3. **enrich 用独立 budget**：`max_tool_calls = 8 / wall_clock = 20s`，在 enrich dispatch 处硬编码传入 `AgentRunner`，不外置 yaml 配置。enrich 调用频率比 ask 高一个量级、任务面更窄，复用 ask budget 会让 LLM 成本失控。
4. **判断轴是 tag 归属，不是 Raw 间重复度**。Raw 是流水账，知识点反复出现是常态，把"Raw 之间是否重复"当主信号无意义；操作层的"用户重复贴同一段"由 CaptureBucket 处理。enrich 不出 `duplicate_of` 字段。
5. **不自创 tag**：所有写入 `tags` 的 `topic/` / `skill/` 项必须命中派生自当前 vault 的已知词表（严守 vault 原有"不为一次性上下文创建新标签"规则）。enrich 想建议新 tag 时走 `openwhisker_new_tag_candidates` 字段 + `status/needs-review`，留给人工/整理流程纳入词表，绝不绕过。
6. **tag 词表完全派生自 vault**（扫 `Knowledge/` / `Interview/` / `Life/` 的 frontmatter `tags`），不在 OpenWhisker 配置层维护独立列表。这条与决议 5 配套——把 vault 当作词表的唯一真值源。
7. **outbox 分级**：`route_suggestion`（有动作含义）和 `new_tag_candidates`（提示词表演化）→ 投递 Matrix；`tags` 本身的追加是静默索引更新，不 ping Matrix（每条 Raw 都会被打 tag，全推会变成噪音）。`status/needs-review` 触发时进 Matrix（明确人工动作信号）。frontmatter 写入与 Matrix 提示**统一为同一组阈值**，避免"Matrix 说要 review 但文件里没标"的认知割裂。
8. **触发模型 = M3 + P1 + Q2**：事件路径主（`IngestRaw` 末尾入队 sqlite `enrich_jobs` 表），daemon scan tick 兜底（5 分钟周期）。理由：纯事件路径无法覆盖"daemon crash 期间入队丢失"和"用户手动放文件进 Raw/Inbox/"；纯 scan 延迟高。事件 + scan 双覆盖；持久化用 sqlite 表（不是进程内 channel）因为 v1 即起步、无迁移成本，落到 sqlite 后 scan 兜底退化为"vault 不变量维护"而非"crash 恢复"，语义更干净。
9. **并发编辑策略 = hash guard + drop + scan 兜底 + mtime 静默期 + attempts 上限**：worker 写回前重读 sha256，不一致就标 `skipped_concurrent_edit`、不立即重试；scan 路径加 **mtime ≤ now − 10min** 静默期，避开用户活跃编辑窗口；`attempts ≥ 5` 转 `attempts_exhausted` 终态防极端文件无限重试。理由：enrich 写的字段全是 append-only 或 OpenWhisker 私有，"丢失"语义不存在；Obsidian 自带外部修改检测 + 用户冲突提示作为最后兜底；drop + scan 已经把可用性顶到 99%，merge 策略的额外复杂度不值。
10. **scan 周期 + 静默期 + 上限三个常量 v1 写死**：scan tick 5 分钟、mtime 静默期 10 分钟、attempts 上限 5。三者都直接落代码常量，不外置 yaml；和决议 3（budget 写死）同源——v1 没有跑批数据，过早外置 = 凭空设计配置面，等 demo 跑出第一手数据再决定是否外置。
11. **PR 边界 = 单 PR + 单 commit**：tagvocab + enrich_jobs schema + policy 升级 + enrich agent + worker + IngestRaw hook + scan + CLI + docs 全部一次入。理由：本 Phase 是单一 feature 的端到端落地，按 [[feedback_prefer_fewer_larger_commits]] 不为 reviewer 工效切片；中间 commit 都无法独立 demo，切片只产生伪整洁；端到端 demo（真实 vault 30 条 Raw）才是验收硬要求，应该 commit 即 demo。仅当真实 vault demo 暴露设计问题需要返工时，才在同一 PR 内追加 fix commit。

## Follow-up（不阻塞 Phase 7 落地）

- enrich 失败的指数退避重试（v1 用线性重试 + scan 兜底，若发现 LLM 短时故障导致 scan 反复触发，再加退避）；
- 向量检索补强 `vault_text_search` 召回；
- 自动 propose "把 Raw 合并到 bucket X"（需要新 op 类型 + approval 流）；
- `openwhisker_new_tag_candidates` 累计到一定量后，统一推一份"待词表评审"摘要给用户，辅助词表演化；
- scan 周期 / mtime 静默期 / attempts 上限三个常量在跑批数据出现后视情况外置 yaml。
