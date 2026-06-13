---
title: OpenWhisker Design Philosophy
type: architecture
status: active
created: 2026-05-12
updated: 2026-05-13
language: zh-CN
project:
  - OpenWhisker
  - Obsidian Knowledge Vault
  - LLM Wiki Agent
historical_source:
  - FlashBang
supersedes_partial:
  - plugin-sync-as-primary-write-path
principle:
  - wiki-first
  - human-in-control
  - plan-before-write
  - executor-not-free-shell
  - source-traceability
  - sync-aware-write
  - local-first
---

# OpenWhisker 设计哲学：VaultPlan / VaultExecutor

## 0. 文档定位

本文是 OpenWhisker 的设计哲学文档，用来说明这个项目为什么采用 wiki-first、plan-before-write、executor-not-free-shell 的架构方向。

它来自 FlashBang 阶段的架构反思，但现在作为 OpenWhisker 的核心设计依据：LLM 负责生成可审计计划，受控 executor 负责真正写入 vault。

本文重点讨论一个新的执行层判断：

```text
原设计：FlashBang Core → SyncAction → Obsidian Plugin → Vault
新设计：FlashBang Core → WikiJob → VaultPlan → Human Approval → VaultExecutor → Vault → Obsidian Sync
```

这并不是推翻前一版 Wiki-first 架构，而是对“谁来真正执行 Vault 写入”的重新定位。

前一版架构把 Obsidian Plugin 作为主要写入执行者。这个模式在 FlashBang 还是“捕获中继 + profile-driven sync action”时非常合理，因为它能保证：

- Go 服务不直接修改 Vault。
- Obsidian 插件在本地上下文中执行写入。
- 写入行为可控、可预览、可 ack。
- Profile 与 SyncAction 使目标路径和动作保持确定性。

但当系统升级为 LLM Wiki Agent 之后，写入需求会变得更复杂：

- 一个 workflow 可能包含 create、append、patch、move、frontmatter update、raw processed record、sync、notify 等连续动作。
- 用户希望通过 IM 手动触发和审批，而不是等待插件轮询 pending action。
- Obsidian 已有 CLI 和官方 Sync / Headless Sync 能力，可以承接本地或服务器环境中的执行与同步。
- Agent 的目标不是“自动接管知识库”，而是“在用户确认后，把复杂细节一次性执行干净”。

因此，本文建议将插件 sync 从主路径降级为可选执行器、预览器和 fallback；主路径升级为 `VaultPlan / VaultExecutor`。

---

## 1. 核心结论

最终推荐的执行模型是：

```text
LLM 不直接写 Vault。
LLM 不直接执行 shell。
LLM 不直接调用任意 Obsidian CLI 命令。

LLM 只生成 VaultPlan。
VaultPlan 经过 policy check、diff、risk classification 和人工确认。
VaultExecutor 执行被批准的、受控的 VaultOperation。
```

一句话版本：

```text
FlashBang 不再只是“把 action 交给插件写入”的系统，
而是“把 agent workflow 编排成可审计 VaultPlan，并在用户批准后由受控执行器写入 Vault”的系统。
```

这保持了三件事的平衡：

1. **手动控制**：关键知识变更仍然由用户通过 IM 或 Obsidian UI 审批。
2. **执行灵活**：workflow 可以一次性完成组织、写入、移动、同步、通知。
3. **安全边界**：LLM 只提出计划，不拥有任意文件系统、shell 或 CLI 权限。

---

## 2. 背景：为什么可以弱化插件 sync 主路径

### 2.1 用户目标变化

系统最初的目标是：

```text
低摩擦捕获 → 写入 raw → 插件同步到 Obsidian
```

现在的目标已经升级为：

```text
低摩擦捕获 → LLM Wiki Agent 组织知识 → 生成可审计计划 → 用户确认 → 自动执行 workflow → Sync 到所有设备
```

这意味着执行层不能只表达简单的 `create_note` 或 `append_section`，而要能表达完整的知识库维护计划。

### 2.2 Obsidian CLI 与 Sync 能力变得足够关键

Obsidian CLI 可以从命令行控制正在运行的 Obsidian，用于 scripting、automation 和 external tools。它支持 daily note、append、search、read、create、tags、diff、sync status/history 等命令。

Obsidian Headless / Headless Sync 则适合没有桌面 Obsidian 的服务器、CI、agent 或自动化场景，可以从命令行同步 Vault。

因此执行层可以根据部署环境选择不同 executor：

- 桌面环境：Obsidian CLI + 桌面 Obsidian Sync。
- 服务器环境：direct filesystem write + Obsidian Headless Sync。
- 保守环境：Obsidian Plugin executor。
- 混合环境：direct filesystem write + CLI open/diff + Sync。

### 2.3 插件轮询模式不再是唯一高可信方式

插件执行原本的优势是安全和可控，但有了 `VaultPlan + Approval + Executor + Hash Guard + Operation Log` 后，可控性可以在服务端工作流中实现。

插件仍然有价值，但角色改变：

```text
从：唯一主要写入执行者
变成：可选预览器、Obsidian 内 UI、fallback executor、命令桥
```

---

## 3. 新总架构

```text
                      IM / HTTP / Scheduler
                               │
                               ▼
                         FlashBang Core
                  capture / command / jobs / outbox
                               │
                               ▼
                        Wiki Agent Host
             raw organizer / knowledge expander / query / lint
                               │
                               ▼
                           VaultPlan
                  proposed operations + risk + diff
                               │
                ┌──────────────┴──────────────┐
                ▼                             ▼
          Manual Approval                Auto Allow
       /approve job_xxxx             low-risk operations
                │                             │
                └──────────────┬──────────────┘
                               ▼
                         VaultExecutor
          direct_fs / obsidian_cli / headless_sync / plugin
                               │
                               ▼
                         Local Vault Files
                               │
                               ▼
                       Obsidian Sync Layer
                               │
                               ▼
                        Other Devices
```

新的主链路是：

```text
Capture / Command
  ↓
WikiJob
  ↓
Agent generates VaultPlan
  ↓
Policy check
  ↓
Prepare diff
  ↓
Manual approval if needed
  ↓
Vault lock
  ↓
Sync latest / check latest state
  ↓
Hash guard
  ↓
Apply operations
  ↓
Sync out
  ↓
Operation log
  ↓
IM / UI notification
```

---

## 4. 组件职责

## 4.1 FlashBang Core

FlashBang Core 仍然是系统中枢，但职责从“生成插件 sync action”扩展为“编排 agent workflow 和 VaultPlan 生命周期”。

主要职责：

- 接收 HTTP / IM / Scheduler 输入。
- 保存 capture、command、event。
- 做本地轻量 classification。
- 调用 LLM digest 或 Wiki Agent。
- 创建和调度 `wiki_jobs`。
- 管理 `vault_plans` 生命周期。
- 管理 approval、reject、retry、cancel。
- 管理 outbox，把结果推送回 IM。
- 记录 operation log 和错误。

FlashBang Core 不应该：

- 让 LLM 直接拿到 shell。
- 让 IM adapter 直接写 Vault。
- 无审批地 patch 长期知识笔记。
- 把某个 Vault 的目录规则硬编码到核心代码。

---

## 4.2 Wiki Agent Host

Wiki Agent Host 是 LLM Wiki 的思考层，负责从原始资料、已有 Wiki、AGENTS.md 和用户指令中生成计划。

它的输出不是“直接写入”，而是 `VaultPlan`。

子 Agent：

| Agent | 职责 | 输出 |
| --- | --- | --- |
| Raw Organizer | 处理 Raw/Inbox、Raw/Sources、Raw/Tasks | organize raw 的 VaultPlan |
| Knowledge Expander | 创建、扩展、拆分、维护 Knowledge notes | create / patch / append / split proposal |
| Wiki Reader | Wiki-first 问答 | answer + cited paths + optional update proposal |
| Maintenance Agent | 检查 broken links、frontmatter、tags、needs-review | lint report / maintenance plan |
| Scheduler Agent | 执行 daily / weekly workflow | report / plan / reminder |

Wiki Agent Host 必须遵守：

- 创建、拆分、移动 notes 前读取最近适用的 `AGENTS.md`。
- Raw 到 durable note 必须保持 source traceability。
- source content、inference、needs-review 必须区分。
- 长期笔记修改必须生成 diff 和 reason。
- 时间敏感或不确定内容必须标记 `needs-review`。

---

## 4.3 VaultExecutor

VaultExecutor 是唯一有权真正修改 Vault 的执行层。

它接收已经通过 policy check 的 `VaultPlan`，并执行其中的 `VaultOperation`。

VaultExecutor 的职责：

- 检查目标路径是否合法。
- 检查允许写入目录。
- 准备 diff。
- 获取 Vault lock。
- 执行前同步最新状态。
- 检查 `before_hash`。
- 应用 create / append / patch / move / update frontmatter 等操作。
- 执行后同步。
- 写入 operation log。
- 返回 apply result。

VaultExecutor 不负责：

- 解释用户意图。
- 决定知识应该去哪个目录。
- 调用 LLM。
- 自由扩展操作权限。

---

## 4.4 Obsidian Plugin

Obsidian Plugin 不再是唯一主写入路径。

新的角色：

1. **Preview UI**
   在 Obsidian 内展示 agent proposal、diff、source refs、risk、approval 状态。

2. **Fallback Executor**
   当 CLI / direct_fs / Headless 不可用时，仍可执行旧式 pending action。

3. **Command Bridge**
   某些 Obsidian 内部插件能力或 UI command 不适合 CLI 执行时，可以由插件暴露给服务端。

4. **Local Context Provider**
   提供 active file、current workspace、selected text 等上下文，但不应绕过审批直接写长期笔记。

---

## 4.5 Obsidian Sync / Headless Sync

Obsidian Sync 负责跨设备同步，不负责知识组织逻辑。

推荐部署策略：

```text
如果 Agent 服务运行在主力桌面机：
  使用 Obsidian CLI + 桌面 Obsidian Sync。

如果 Agent 服务运行在服务器 / NAS / VPS：
  使用 direct_fs_executor + Obsidian Headless Sync。

不要在同一台设备上让桌面 Obsidian Sync 和 Headless Sync 同时管理同一个 Vault。
```

Sync 不是事务系统。VaultExecutor 仍然需要自己实现 hash guard、lock 和 conflict job。

---

## 5. 核心数据结构

## 5.1 WikiJob

`WikiJob` 表示一次 agent workflow。

```go
type WikiJob struct {
    ID              string
    Type            string // ingest_raw / organize_raw / expand_note / answer_query / lint_vault / daily_review
    Status          string // pending / running / proposed / approved / applying / done / failed / conflicted / rejected
    Source          string // im / http / scheduler / system
    SourceCaptureID string
    InputJSON       string
    ResultJSON      string
    Error           string
    CreatedAt       time.Time
    UpdatedAt       time.Time
    RunAt           *time.Time
    Attempts        int
}
```

常见 job type：

```text
ingest_raw
organize_raw
expand_knowledge
answer_query
lint_vault
daily_raw_review
weekly_maintenance
reindex_vault
sync_vault
```

---

## 5.2 VaultPlan

`VaultPlan` 是 agent workflow 的可审计执行计划。

```go
type VaultPlan struct {
    ID               string
    JobID            string
    Purpose          string
    RiskLevel        string // low / medium / high / critical
    RequiresApproval bool
    Summary          string
    SourceRefs       []string
    TargetPaths      []string
    Operations       []VaultOperation
    Diff             *VaultDiff
    Status           string // proposed / approved / applied / rejected / conflicted / failed
    CreatedAt        time.Time
    ApprovedAt       *time.Time
    AppliedAt        *time.Time
}
```

设计原则：

- 一个 `VaultPlan` 可以包含多个 `VaultOperation`。
- 每个 operation 必须有 reason。
- 所有可能覆盖已有内容的 operation 必须有 `before_hash`。
- plan 必须能在 IM 中用简短摘要展示，也能在 Obsidian 或 Web UI 中展开查看完整 diff。

---

## 5.3 VaultOperation

```go
type VaultOperation struct {
    ID          string
    Type        string
    TargetPath  string
    BeforeHash  string
    PayloadJSON string
    Reason      string
    RiskLevel   string
}
```

推荐 operation type：

```text
create_note
append_note
patch_note
move_note
rename_note
update_frontmatter
add_wikilinks
create_processed_raw_record
move_raw_to_processed
write_agent_report
open_note
run_sync
```

初期不建议支持：

```text
delete_note
arbitrary_shell
arbitrary_file_write
modify_hidden_config
bulk_rename_without_preview
```

删除类动作应先用 `archive_note` 或 `move_note` 替代。

---

## 5.4 VaultExecutor interface

```go
type VaultExecutor interface {
    Name() string
    Prepare(ctx context.Context, plan VaultPlan) (VaultDiff, error)
    Apply(ctx context.Context, plan VaultPlan) (VaultApplyResult, error)
}
```

`Prepare` 阶段：

- 校验路径。
- 校验 operation 类型。
- 读取 before content。
- 生成 diff。
- 判断风险。
- 返回给 approval UI。

`Apply` 阶段：

- 获取 lock。
- sync latest。
- 读取 current hash。
- 检查 hash 是否匹配。
- 应用操作。
- sync out。
- 写 operation log。
- 返回结果。

---

## 5.5 Operation Log

```sql
CREATE TABLE vault_operations (
  id TEXT PRIMARY KEY,
  plan_id TEXT NOT NULL,
  job_id TEXT NOT NULL,
  op_type TEXT NOT NULL,
  target_path TEXT NOT NULL,
  before_hash TEXT,
  after_hash TEXT,
  payload_json TEXT,
  result_json TEXT,
  reason TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  applied_at TEXT
);
```

Operation log 不保存完整 chain-of-thought，只保存可审计摘要：

```text
为什么改？
基于哪些 source？
改了哪些文件？
哪些内容仍然不确定？
是否需要后续 review？
```

---

## 5.6 Vault Lock

```sql
CREATE TABLE vault_locks (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL,        -- whole_vault / path / directory
  target TEXT NOT NULL,
  holder_job_id TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
```

锁的用途不是替代 Sync，而是在本地 agent workflow 中避免两个 job 同时 patch 同一个 note。

推荐策略：

- Raw create 可以按目录锁。
- patch_note 必须按 path 锁。
- bulk move / split hub 必须 whole_vault 或目录锁。
- 锁必须有过期时间。

---

## 6. Executor 类型

## 6.1 direct_fs_executor

直接用 Go 修改 Markdown 文件。

适合：

- 创建 raw。
- 创建 processed raw record。
- 创建 draft note。
- append daily note。
- 应用确定性 patch。
- move note。

优点：

- 最可控。
- 不依赖 Obsidian desktop 是否运行。
- 容易做 hash guard 和 diff。
- 容易写 operation log。

风险：

- 不会自动触发某些 Obsidian 插件逻辑。
- 需要自己实现 frontmatter、wikilink、path、conflict 处理。

推荐用途：主力写入执行器。

---

## 6.2 obsidian_cli_executor

通过 Obsidian CLI 控制正在运行的 Obsidian desktop app。

适合：

- open note。
- search vault。
- read active file。
- append daily note。
- diff / sync status / sync history。
- 执行 Obsidian command 或插件 command。

优点：

- 与 Obsidian desktop 上下文一致。
- 能利用 Obsidian 自带命令和插件命令。
- 适合交互和预览。

限制：

- 需要 Obsidian app 运行。
- 不适合作为 LLM 的自由 shell。
- 应作为受控 tool 使用。

推荐用途：辅助执行器、打开结果、读取状态、触发 Obsidian 语义命令。

---

## 6.3 headless_sync_executor

通过 Obsidian Headless Sync 执行同步。

适合：

- agent 服务运行在服务器、NAS、CI 或无桌面环境。
- sync latest before apply。
- apply 后 sync out。
- continuous sync 或 one-shot sync。

优点：

- 不需要桌面 Obsidian。
- 适合自动化和 agent 工作流。
- 保留 Obsidian Sync 的隐私和加密能力。

限制：

- 当前是 open beta，应保留 fallback 和备份策略。
- 不应与同设备桌面 Obsidian Sync 同时管理同一个 Vault。

推荐用途：服务器部署下的同步执行器。

---

## 6.4 plugin_executor

旧式 Obsidian Plugin sync action 执行器。

适合：

- 需要在 Obsidian 内人工预览。
- 需要使用插件 UI。
- CLI / Headless 不可用。
- 用户希望完全保留旧式 pending action 模式。

优点：

- 安全边界清晰。
- Obsidian 内上下文完整。
- 适合 UI preview。

限制：

- 轮询 pending action 效率较低。
- 复杂 workflow 表达能力弱。
- 对 IM 手动控制不够直接。

推荐用途：fallback 和可选 UI。

---

## 6.5 hybrid_executor

推荐的默认执行器。

组合：

```text
direct_fs_executor:
  create / append / patch / move / frontmatter

obsidian_cli_executor:
  open / search / diff / sync status / desktop context

headless_sync_executor 或 desktop Sync:
  sync latest / sync out

plugin_executor:
  preview / fallback
```

推荐默认模式：

```yaml
vault:
  executor: hybrid
  root: /path/to/vault

executors:
  direct_fs:
    enabled: true
  obsidian_cli:
    enabled: true
  headless_sync:
    enabled: false
  plugin:
    enabled: true
```

---

## 7. 风险分级与审批策略

## 7.1 Low Risk：可自动执行

适合：

```text
create Raw/Inbox note
create Raw/Sources note
create Raw/Tasks note
append Daily note
write agent report
write lint report
create draft proposal
```

特点：

- 不覆盖已有长期知识。
- 不移动已有知识结构。
- 不删除内容。
- 可通过 source traceability 回溯。

默认策略：

```text
auto-allow
```

---

## 7.2 Medium Risk：需要 IM 审批

适合：

```text
organize raw into Knowledge draft
append to existing Knowledge note
update frontmatter
add backlinks
move Raw item to Raw/Processed
create Interview answer with Knowledge links
```

默认策略：

```text
requires /approve job_id
```

审批消息必须包含：

- 本次计划摘要。
- 涉及文件。
- operation 数量。
- 风险等级。
- 是否修改已有文件。
- `/diff job_id` 查看详情。
- `/approve job_id` 执行。
- `/reject job_id` 放弃。

---

## 7.3 High Risk：默认只生成 proposal

适合：

```text
patch evergreen note
rename note
split hub
merge notes
bulk move
modify AGENTS.md
archive note
large-scale retagging
```

默认策略：

```text
write proposal note only
```

proposal 路径：

```text
Raw/Agent-Proposals/YYYY-MM-DD-<slug>.md
```

用户可以在 Obsidian 里阅读 proposal，再通过 IM 或 UI 批准部分操作。

---

## 7.4 Critical Risk：默认禁止

```text
delete note
wipe directory
arbitrary shell
modify .obsidian config without approval
export secrets
write outside vault root
network post with vault content
```

默认策略：

```text
disabled
```

---

## 8. 写入流程详解

## 8.1 Raw capture：低风险自动执行

```text
User IM:
  记一下：RAG 不应该只做向量检索，要结合 Wiki structure。

Flow:
  IM Adapter
    → FlashBang capture
    → local classification
    → WikiJob ingest_raw
    → VaultPlan create Raw/Inbox note
    → auto-allow
    → direct_fs create note
    → sync out
    → IM outbox reply
```

返回示例：

```text
已保存到：Raw/Inbox/2026-05-12-rag-wiki-structure.md
状态：raw inbox
可用命令：/organize last
```

---

## 8.2 Organize raw：中风险审批执行

```text
User IM:
  /organize last

Flow:
  command router
    → WikiJob organize_raw
    → Raw Organizer reads raw + nearest AGENTS.md
    → search existing wiki notes
    → generate VaultPlan
    → Prepare diff
    → IM approval prompt
    → user /approve
    → VaultExecutor apply
    → move raw to Raw/Processed
    → sync out
    → operation log
    → IM result
```

审批摘要示例：

```text
我准备执行 4 个操作：
1. 创建 Knowledge/LLM/RAG 与 LLM Wiki.md
2. 给 Knowledge/Obsidian/个人知识库.md 添加一个链接
3. 创建 Raw/Processed/2026-05-12-rag-wiki-structure.md
4. 在 processed raw 中记录 source、output links、needs-review

风险：medium
回复 /diff job_123 查看差异，/approve job_123 执行，/reject job_123 放弃。
```

---

## 8.3 Wiki query：默认只读，必要时生成 proposal

```text
User IM:
  /ask 我现在的知识库架构应该是 RAG-first 还是 Wiki-first？

Flow:
  command router
    → WikiJob answer_query
    → Wiki Reader
    → search hub/index notes
    → graph expansion
    → answer with cited vault paths
    → optional update proposal if user asks
```

默认不写 Vault。

只有在这些情况下才生成写入计划：

- 用户明确说“整理成笔记”。
- 用户说“把这个结论写入 Knowledge”。
- 回答暴露了明显缺口，且用户批准补充。
- 用户纠正了答案，需要把修正转成 note update。

---

## 8.4 Daily / Weekly scheduler：主动但受控

```text
Scheduler:
  daily_raw_review at 08:00

Flow:
  scheduled event
    → WikiJob daily_raw_review
    → scan yesterday raw
    → generate report
    → low-risk write Daily or Meta/Reports
    → IM notify
```

周维护：

```text
weekly_maintenance:
  - broken wikilinks
  - missing frontmatter
  - uncontrolled tags
  - stale needs-review notes
  - Raw/Processed missing output links
  - Interview notes missing Knowledge backlinks
```

默认只写 report，不直接重构。

---

## 8.5 Manual sync

```text
User IM:
  /sync

Flow:
  WikiJob sync_vault
    → VaultExecutor sync latest/out
    → sync status
    → IM reply
```

如果是桌面机：

```text
obsidian_cli_executor → sync:status / sync commands
```

如果是服务器：

```text
headless_sync_executor → ob sync / ob sync-status
```

---

## 9. Sync 与冲突控制

Obsidian Sync 能同步多设备，但不应被当成事务系统。

必须设计本地冲突控制：

```text
1. Apply 前 sync latest。
2. 读取目标文件 current hash。
3. 与 VaultOperation.before_hash 比较。
4. 如果不一致，停止 apply。
5. 标记 job 为 conflicted。
6. 通知用户重新生成 plan 或手动处理。
7. Apply 后 sync out。
```

冲突状态：

```text
job status = conflicted
plan status = conflicted
operation status = skipped_conflict
```

IM 提示示例：

```text
job_123 未执行：目标文件已变化。
文件：Knowledge/Redis/Redis.md
计划生成时 hash：abc123
当前 hash：def456
建议：回复 /replan job_123 重新生成计划，或 /open Knowledge/Redis/Redis.md 手动查看。
```

---

## 10. Policy Check

`VaultPlan` 在进入 approval 前必须经过 policy check。

检查项：

```text
路径安全：
  - 禁止绝对路径
  - 禁止 ../
  - 禁止写出 vault root
  - 禁止覆盖隐藏配置，除非显式允许

目录权限：
  - Raw 可自动 create
  - Knowledge patch 需要审批
  - AGENTS.md 修改高风险
  - Archive move 需要审批

操作合法性：
  - operation type 必须在 allowlist
  - payload 必须符合 schema
  - patch 必须能应用到 before_hash

Traceability：
  - derived note 必须包含 source ref
  - processed raw 必须包含 output links
  - uncertain claims 必须进入 needs-review

Metadata：
  - long-lived notes 必须有 type/status/tags
  - tags 必须符合 controlled prefixes
  - 不确定 tag 必须标记 status/needs-review
```

---

## 11. IM 控制命令

推荐命令：

```text
/raw <text>
  强制作为 raw input 保存。

/ask <question>
  Wiki-first 只读问答。

/organize last
  整理最近一条 raw。

/diff <job_id>
  查看某个 VaultPlan 的 diff。

/approve <job_id>
  批准执行。

/reject <job_id>
  拒绝计划。

/replan <job_id>
  基于当前 vault 状态重新生成计划。

/open <path|job_id>
  用 Obsidian CLI / URI 打开相关 note。

/sync
  手动触发同步或查看同步状态。

/status
  查看当前 job、sync、vault lock 状态。

/jobs
  查看最近 jobs。

/lint
  运行 vault health check。

/review raw
  查看未处理 raw。
```

普通文本默认行为：

```text
普通文本 → capture_purpose = raw_input → Raw/Inbox
命令消息 → capture_purpose = control_command → 不写 Raw，除非命令本身要求
系统事件 → capture_purpose = system_event
```

---

## 12. 示例 VaultPlan

```json
{
  "id": "plan_20260512_001",
  "job_id": "job_20260512_001",
  "purpose": "Organize last raw note into LLM Wiki architecture knowledge",
  "risk_level": "medium",
  "requires_approval": true,
  "summary": "Create one Knowledge note, append one backlink, and move the raw item to Raw/Processed.",
  "source_refs": [
    "Raw/Inbox/2026-05-12-rag-wiki-structure.md"
  ],
  "target_paths": [
    "Knowledge/LLM/LLM Wiki.md",
    "Knowledge/Obsidian/个人知识库架构.md",
    "Raw/Processed/2026-05-12-rag-wiki-structure.md"
  ],
  "operations": [
    {
      "id": "op_001",
      "type": "create_note",
      "target_path": "Knowledge/LLM/LLM Wiki.md",
      "before_hash": "",
      "risk_level": "medium",
      "reason": "Raw material contains a reusable architecture distinction between RAG-first and Wiki-first knowledge systems.",
      "payload_json": "{...}"
    },
    {
      "id": "op_002",
      "type": "append_note",
      "target_path": "Knowledge/Obsidian/个人知识库架构.md",
      "before_hash": "sha256:abc123",
      "risk_level": "medium",
      "reason": "Existing architecture note should link to the new LLM Wiki note.",
      "payload_json": "{...}"
    },
    {
      "id": "op_003",
      "type": "move_raw_to_processed",
      "target_path": "Raw/Processed/2026-05-12-rag-wiki-structure.md",
      "before_hash": "sha256:def456",
      "risk_level": "medium",
      "reason": "Raw item has been processed and linked to generated outputs.",
      "payload_json": "{...}"
    }
  ]
}
```

---

## 13. 数据库表建议

## 13.1 wiki_jobs

```sql
CREATE TABLE wiki_jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  status TEXT NOT NULL,
  source TEXT NOT NULL,
  source_capture_id TEXT,
  input_json TEXT,
  result_json TEXT,
  error TEXT,
  run_at TEXT,
  attempts INTEGER DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

## 13.2 vault_plans

```sql
CREATE TABLE vault_plans (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  status TEXT NOT NULL,
  risk_level TEXT NOT NULL,
  requires_approval INTEGER NOT NULL,
  summary TEXT,
  source_refs_json TEXT,
  target_paths_json TEXT,
  operations_json TEXT NOT NULL,
  diff_json TEXT,
  created_at TEXT NOT NULL,
  approved_at TEXT,
  applied_at TEXT
);
```

## 13.3 vault_operations

```sql
CREATE TABLE vault_operations (
  id TEXT PRIMARY KEY,
  plan_id TEXT NOT NULL,
  job_id TEXT NOT NULL,
  op_type TEXT NOT NULL,
  target_path TEXT NOT NULL,
  before_hash TEXT,
  after_hash TEXT,
  payload_json TEXT,
  result_json TEXT,
  reason TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  applied_at TEXT
);
```

## 13.4 outbox_messages

```sql
CREATE TABLE outbox_messages (
  id TEXT PRIMARY KEY,
  target_type TEXT NOT NULL,
  target_ref TEXT NOT NULL,
  message_type TEXT NOT NULL,
  body TEXT NOT NULL,
  related_capture_id TEXT,
  related_job_id TEXT,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  sent_at TEXT
);
```

## 13.5 scheduled_jobs

```sql
CREATE TABLE scheduled_jobs (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  cron_expr TEXT NOT NULL,
  timezone TEXT NOT NULL,
  job_type TEXT NOT NULL,
  input_json TEXT,
  enabled INTEGER NOT NULL,
  last_run_at TEXT,
  next_run_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

---

## 14. 配置建议

```yaml
vault:
  root: "/path/to/ObsidianVault"
  executor: "hybrid"
  language: "zh-CN"
  timezone: "Asia/Tokyo"

policy:
  auto_allow:
    - create_raw_note
    - append_daily_note
    - write_agent_report
    - write_lint_report
  require_approval:
    - patch_note
    - append_existing_knowledge
    - move_raw_to_processed
    - update_frontmatter
    - add_wikilinks
  proposal_only:
    - rename_note
    - split_hub
    - merge_notes
    - modify_agents_md
    - bulk_retag
  disabled:
    - delete_note
    - arbitrary_shell
    - arbitrary_file_write

executors:
  direct_fs:
    enabled: true
    allowed_roots:
      - Raw
      - Knowledge
      - Interview
      - Life
      - Meta
      - Skills
    blocked_paths:
      - .obsidian
      - .git

  obsidian_cli:
    enabled: true
    binary: "obsidian"
    require_desktop_running: true
    allowed_commands:
      - read
      - search
      - create
      - daily
      - daily:append
      - tags
      - diff
      - sync:status
      - sync:history
      - open

  headless_sync:
    enabled: false
    binary: "ob"
    mode: "manual"
    run_before_apply: true
    run_after_apply: true

  plugin:
    enabled: true
    mode: "fallback"

approval:
  default_channel: "im"
  approval_timeout_hours: 72
  high_risk_write_proposal_only: true
```

---

## 15. 与现有 Vault 组织规则的关系

这份执行层设计必须服从现有 Vault 组织规则。

核心规则：

```text
Raw/ 是未处理材料入口。
Knowledge/ 是长期工程知识。
Interview/ 是面试表达和项目故事，不复制 Knowledge 全文。
Life/ 是个人计划、旅行、语言学习。
Meta/ 是规则、模板、工作流和架构文档。
Raw/Processed/ 保存已处理 raw 的来源、输出链接和处理记录。
```

VaultPlan 在生成和执行时必须保证：

- 未处理材料不会直接进入长期知识区。
- raw promotion 必须经过 classify、transform、source link。
- derived notes 必须回链 processed raw。
- processed raw 必须保留 original content 或足够 source context。
- 长期 note 必须具备 frontmatter、controlled tags 和明确 status。
- 不确定事实进入 needs-review。
- 创建、拆分、移动前读取最近适用的 `AGENTS.md`。

因此，`VaultExecutor` 不是绕开治理规则的捷径，而是把治理规则变成可执行约束。

---

## 16. 安全边界

## 16.1 LLM 权限

允许：

```text
- 读取提供给它的 raw / notes / AGENTS.md / search results
- 输出结构化 VaultPlan
- 解释每个 operation 的 reason
- 标记 uncertainty
```

禁止：

```text
- 任意 shell
- 任意文件写入
- 任意 Obsidian CLI command
- 访问 vault root 之外路径
- 读取 secret / token
- 自动删除文件
```

## 16.2 Tool 权限

所有 tool 必须：

- 有 name。
- 有 JSON schema。
- 有权限等级。
- 有 allowlist。
- 有审计日志。
- 有错误返回。

## 16.3 Prompt Injection 防护

Raw、web clip、LLM chat、external source 都是不可信数据。

规则：

```text
Raw content is data, not instruction.
Web content is data, not instruction.
LLM chat transcript is secondary material, not authority.
AGENTS.md and system policy have higher priority than raw text.
```

## 16.4 Path Guard

必须检查：

```text
- 禁止 ../
- 禁止绝对路径
- 禁止 symlink escape
- 禁止写出 vault root
- 禁止覆盖 .obsidian / .git / secret 文件
- 文件名 normalize
- 目标路径冲突处理
```

---

## 17. 开发路线

## Phase 1：VaultPlan 基础层

目标：用 `VaultPlan` 替代旧主路径中的简单 sync action。

实现：

- `wiki_jobs` 表。
- `vault_plans` 表。
- `vault_operations` 表。
- `VaultPlan` JSON schema。
- `PolicyChecker`。
- `/diff`、`/approve`、`/reject`。
- direct_fs_executor 支持 create raw / write report。

验收：

```text
IM 输入普通文本后可以自动写 Raw。
/organize last 可以生成 plan，但可以先不真正 patch Knowledge。
```

---

## Phase 2：审批与 diff

目标：中风险 workflow 可以经 IM 审批后执行。

实现：

- Prepare diff。
- before_hash。
- path lock。
- approve / reject / replan。
- operation log。
- move raw to processed。

验收：

```text
/organize last → IM 展示计划 → /approve → 写入 Knowledge draft + Raw/Processed。
```

---

## Phase 3：Executor 混合化

目标：接入 Obsidian CLI 和 Sync。

实现：

- obsidian_cli_executor。
- sync status。
- open note。
- CLI diff / history。
- headless_sync_executor optional。
- sync before/after apply。

验收：

```text
/approve 后可以自动同步并返回 sync status。
/open job_id 可以打开相关 Obsidian note。
```

---

## Phase 4：Wiki Agent 完整 workflow

目标：Raw Organizer 和 Knowledge Expander 真正形成 LLM Wiki 维护能力。

实现：

- 读取最近 AGENTS.md。
- Wiki search + graph expansion。
- source/inference/needs-review 区分。
- frontmatter 和 controlled tags 校验。
- hub / child note proposal。

验收：

```text
raw capture 与 capture bucket 在输入期完成 topic 分组，organize last 为最近一条 raw 或一个 capture bucket 生成 VaultPlan。
高风险 split / merge 只生成 proposal note。
```

---

## Phase 5：Maintenance 与 Scheduler

目标：系统主动维护 Vault 健康。

实现：

- daily raw review。
- weekly maintenance。
- broken wikilink check。
- frontmatter check。
- controlled tag check。
- stale needs-review report。
- Interview → Knowledge backlink check。

验收：

```text
每天/每周生成 report，并通过 IM 推送摘要。
```

---

## 18. 设计红线

这套系统不应该变成：

```text
- 泛用 agent 平台
- 任意 shell 自动化工具
- 自动改写整个 Vault 的黑盒系统
- 没有 source traceability 的总结机器人
- 把 Obsidian Sync 当事务系统的写入器
- 把插件 sync 和 direct executor 混用到不可审计
```

必须坚持：

```text
Plan before write.
Human approval before risky write.
VaultExecutor, not free shell.
Source traceability before durable knowledge.
Wiki-first, retrieval-assisted.
Sync-aware, hash-guarded writes.
Plugin optional, not mandatory.
```

---

## 19. 第一版最小验收标准

第一版成功标准：

```text
1. 普通 IM 消息能自动保存到 Raw/Inbox。
2. /organize last 能生成 VaultPlan。
3. /diff job_id 能看到将要写哪些文件。
4. /approve job_id 后能执行低/中风险写入。
5. 写入前检查 before_hash。
6. 写入后生成 operation log。
7. 处理后的 raw 有 processed record 和 output links。
8. Knowledge note 有 frontmatter、tags、source link。
9. /ask 默认只读，不污染 Raw。
10. /sync 能触发或检查同步状态。
```

---

## 20. 最终推荐模式

最终推荐的默认模式是：

```text
低风险：自动执行。
中风险：IM 审批后执行。
高风险：只生成 proposal note。
极高风险：默认禁用。
```

默认执行器：

```text
hybrid_executor = direct_fs + obsidian_cli + sync integration + plugin fallback
```

最终架构：

```text
FlashBang Core
  负责 capture、command、job、outbox、plan lifecycle。

Wiki Agent Host
  负责理解 raw、wiki、AGENTS.md，并生成 VaultPlan。

VaultExecutor
  负责在 policy、approval、hash guard 和 lock 保护下执行操作。

Obsidian Sync / Headless Sync
  负责跨设备同步。

Obsidian Plugin
  负责可选 preview、fallback 和 Obsidian 内 UI。
```

最终原则：

```text
让 FlashBang 成为个人 LLM Wiki workflow host，
让 Agent 生成计划，
让用户保留控制，
让 VaultExecutor 执行确定性操作，
让 Obsidian Sync 负责同步，
让 Obsidian Vault 继续作为长期知识事实源。
```

---

## 21. 参考资料

- Obsidian CLI Help: https://obsidian.md/help/cli
- Obsidian Sync Help: https://obsidian.md/help/sync
- Obsidian Headless Help: https://obsidian.md/help/headless
- Obsidian Headless Sync Help: https://obsidian.md/help/sync/headless
- Obsidian Sync Troubleshooting: https://obsidian.md/help/sync/troubleshoot
- Current Knowledge Base Data Organization Process: local vault architecture document, 2026-05-12
