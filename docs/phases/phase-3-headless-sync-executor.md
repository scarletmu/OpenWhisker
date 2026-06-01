# Phase 3 Sync-Aware Approval Execution

状态：Phase 3A / 3B 最小实现已落地。

这是第三阶段值得先构建的最小实现边界。

当前实现状态：Phase 2 的 `organize_raw` CLI 审批链路已经落地，Phase 3 已补上受控 Headless Sync client、手动 sync status / one-shot sync 命令，以及 `plan approve` apply 前后的 sync-aware 编排。

目标不是进入 LLM Raw Organizer，也不是接入桌面 Obsidian CLI，而是让现有 plan-before-write pipeline 具备 sync-aware execution：apply 前先拉取远端最新状态，继续用 `before_hash` 判断冲突；apply 后再同步输出，并把 sync 结果反馈给用户。

## 本次推进结论

Phase 3 的第一版不新增一个可以独立写 vault 的 executor。更准确的落点是：

```text
core.PlanService approval orchestration
  -> SyncClient pre-sync
  -> existing direct_fs_executor Apply
  -> SyncClient post-sync
```

`HeadlessSyncClient` 只负责受控调用 Headless Sync 命令；vault mutation 仍然只由 `internal/executor.DirectFS` 执行。这样可以复用 Phase 2 已完成的 lock、path guard、`before_hash` guard 和 operation log，不把 sync 误建模成事务系统。

第一版实现目标拆成两个小切片：

1. **Phase 3A：手动 sync 命令与受控 client**。先落 `SyncClient`、`NoopSyncClient`、`HeadlessSyncClient`、`vault sync-status`、`vault sync` 和 allowlist 测试。
2. **Phase 3B：approval apply sync-aware**。再把 `plan approve` 的默认行为改为真实 vault 同步优先，并补 `--sync=off`、conflict / warning regression。

## 目标

构建一个 Headless Sync 执行骨架，使 OpenWhisker 可以：

1. 对真实 vault 默认通过 Headless Sync client 检查 vault sync 状态；
2. 在 approved plan apply 前执行 one-shot sync；
3. 在 pre-sync 后重新执行现有 hash guard；
4. hash guard 通过后继续由 `direct_fs_executor` 执行 vault 写入；
5. apply 成功后执行 one-shot sync out；
6. 将 sync 成功、失败或 warning 写入用户可见结果；
7. 保持 Phase 1 / Phase 2 的 SQLite 和 test vault 默认值不依赖真实 Obsidian Sync。

## 初始支持的 workflow

```text
plan approve
  -> resolve approved VaultPlan
  -> sync before apply through Headless `ob` CLI backend
  -> direct_fs_executor Apply
  -> acquire vault lock inside DirectFS
  -> re-read current files / before_hash guard
  -> filesystem writes
  -> operation log
  -> sync after apply through Headless `ob` CLI backend
  -> outbox result with sync status or warning
```

手动 sync workflow：

```text
vault sync-status
  -> HeadlessSyncClient runs sync-status
  -> user-visible status result

vault sync
  -> HeadlessSyncClient runs one-shot sync
  -> user-visible sync result
```

## 范围内

- [x] `SyncClient` 最小接口，用于 status 和 one-shot sync。
- [x] `NoopSyncClient`，保证现有 test vault、Phase 1 / Phase 2 tests 和显式 `--sync=off` 不依赖真实 Obsidian Sync。
- [x] `HeadlessSyncClient`，只封装允许的 `ob` 命令。
- [x] apply 前的 pre-sync hook。
- [x] pre-sync 后继续执行现有 `before_hash` guard。
- [x] apply 后的 post-sync hook。
- [x] post-sync 失败时返回 warning，不把已经成功的 vault write 误标为未执行。
- [x] `vault sync-status` CLI entrypoint。
- [x] `vault sync` CLI entrypoint。
- [x] `plan approve` 默认对真实 vault sync-aware；`--sync=off` 可显式关闭。
- [x] sync command allowlist、pre-sync failure、post-sync warning 和 Phase 2 conflict regression 测试。

## 建议实现落点

### 数据模型

在 `internal/model/types.go` 增加最小 sync 结果类型：

```go
const (
	SyncModeAuto = "auto"
	SyncModeOff  = "off"
	SyncModeOn   = "on"

	SyncBackendHeadless = "headless"

	SyncPhaseStatus = "status"
	SyncPhaseManual = "manual"
	SyncPhaseBefore = "before_apply"
	SyncPhaseAfter  = "after_apply"
)

type SyncResult struct {
	Mode     string `json:"mode"`
	Backend  string `json:"backend,omitempty"`
	Phase    string `json:"phase"`
	OK       bool   `json:"ok"`
	Command  string `json:"command,omitempty"`
	Output   string `json:"output,omitempty"`
	Warning  string `json:"warning,omitempty"`
	Error    string `json:"error,omitempty"`
}
```

`VaultApplyResult` 可以增加：

```go
SyncBefore *SyncResult `json:"sync_before,omitempty"`
SyncAfter  *SyncResult `json:"sync_after,omitempty"`
```

`PlanActionResult` 可以增加 `SyncBefore` / `SyncAfter` 字段，便于 CLI 直接展示。Phase 3 不新增 `sync_events` 表；sync 结果先进入 job `result_json` 和 outbox body。

### Sync client

新增 `internal/executor/sync_client.go`：

```go
type SyncClient interface {
	Status(ctx context.Context, vaultRoot string) (model.SyncResult, error)
	Sync(ctx context.Context, vaultRoot string, phase string) (model.SyncResult, error)
}
```

实现：

- `NoopSyncClient`：用于 `NewPlanService` 默认构造、test vault、测试和显式 `--sync=off`，返回 `mode=off`、`ok=true`，不执行外部命令。
- `HeadlessSyncClient`：只允许：
  - `ob sync-status --path <vault_root>`
  - `ob sync --path <vault_root>`
- `CommandRunner`：测试注入点，用 fake runner 验证参数数组；生产实现用 `exec.CommandContext`，不经过 shell。

`HeadlessSyncClient` 不应该接收任意子命令字符串。调用方只能选择 `Status` 或 `Sync`。

### Core orchestration

`internal/core/plans.go` 保持 `NewPlanService(store, vaultRoot)` 的测试友好默认值，继续使用 `NoopSyncClient`。CLI 使用 options 构造入口，并按 `--sync=auto` 解析真实运行时行为。

新增 options 构造入口：

```go
type PlanServiceOptions struct {
	SyncMode   string
	SyncBackend string
	SyncClient executor.SyncClient
}

func NewPlanServiceWithOptions(store *storage.Store, vaultRoot string, opts PlanServiceOptions) PlanService
```

`Approve` 的 Phase 3B 顺序固定为：

```text
resolve awaiting plan
mark approved
policy CheckApproved
mark job/plan applying
pre-sync if effective sync is on
direct_fs_executor.Apply(ctx, plan)
post-sync if effective sync is on
mark plan applied
mark job done with result JSON
write result outbox
```

注意：由于 `DirectFS.Apply` 内部已经包含 `AcquireLocks -> preflightApply -> write -> ReleaseLocks`，Phase 3 第一版可接受的顺序是“pre-sync 后由 Apply 重新获取 lock 并执行 hash guard”。如果后续要严格做到文档里的 `acquire lock -> pre-sync -> hash guard -> apply`，需要把 lock 编排从 `DirectFS.Apply` 抽到 core 或新增 executor transaction API；这不是 Phase 3A 的前置条件。

错误语义：

- pre-sync 失败：不调用 `DirectFS.Apply`，plan/job 标记 failed，写 error outbox。
- pre-sync 成功但 `DirectFS.Apply` hash guard 失败：沿用 Phase 2 conflict 语义。
- post-sync 失败：plan 保持 applied，job 保持 done，`VaultApplyResult.SyncAfter.Warning` 和 outbox body 带 warning。

### CLI wiring

`cmd/openwhisker/main.go` 增加：

```text
openwhisker vault sync-status [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]
openwhisker vault sync [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]
openwhisker plan approve [--sync=auto|off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault] <plan_id|job_id>
```

`plan approve` 默认 `--sync=auto`：

- 当目标 vault 是默认 `testdata/vault` 时，effective sync 为 `off`，因此现有本地测试和 demo 不触发外部命令。
- 当用户显式传入真实 vault 路径时，effective sync 为 `on`，默认执行 pre-sync 和 post-sync。
- 用户可以通过 `--sync=off` 明确关闭同步。
- 用户可以通过 `--sync=on` 强制启用同步，即使目标路径看起来像测试 vault。

Phase 3 只暴露用户行为层的 `--sync=auto|off|on`。`headless` 是内部 backend 名称，第一版默认 backend 是 Headless Sync 的 `ob` CLI，不要求用户在 approve 命令里理解或选择 `headless`。

`--ob-bin` 解析优先级：

```text
CLI --ob-bin
  -> OPENWHISKER_OB_BIN
  -> ob
```

Phase 3 不引入持久配置文件。

## 实施顺序

1. 新增 `SyncResult`、`SyncMode*`、`SyncPhase*`，并把 `VaultApplyResult` 扩展为可携带 sync 结果。
2. 新增 `SyncClient`、`NoopSyncClient`、`HeadlessSyncClient` 和 fake runner 测试，先验证命令 allowlist。
3. 新增 `vault sync-status` 和 `vault sync` CLI，手动 sync 命令默认 `--sync=on`；`--sync=off` 返回明确的 disabled/noop status。
4. 给 `PlanService` 增加 options 构造入口；现有 `NewPlanService` 保持 Phase 2 默认。
5. 在 `Approve` 中接入 pre-sync；失败时确认不创建 Knowledge draft、不移动 Raw note。
6. 在 `Approve` 中接入 post-sync；失败时确认 plan applied、job done、outbox/result 带 warning。
7. 跑通 Phase 1 / Phase 2 regression，确认 `plan approve` 面向默认 test vault 时不调用真实 sync client，`plan approve --sync=off` 不调用 sync client。

## 已确认的 Phase 3 决策

- Phase 3 优先 Headless Sync，不优先桌面 Obsidian CLI。
- Sync 对真实 vault 是默认行为；用户行为层使用 `--sync=auto|off|on`，内部默认 backend 是 Headless Sync。
- `ob` 只作为受控 sync client，不作为 LLM 可自由调用的 shell。
- Phase 3 只使用 one-shot sync，不使用 continuous sync。
- `sync-setup`、login、config 和 unlink 等账号或配置命令不由 OpenWhisker 自动执行。
- `direct_fs_executor` 仍是 vault mutation 执行器；Headless Sync 只负责 apply 前后同步。
- Sync 不是事务系统；冲突判断仍由 `before_hash`、vault lock 和 path guard 完成。
- 默认 vault target 仍是本地 test vault；真实 Obsidian vault 必须由用户显式传入。显式真实 vault 默认同步，除非用户传 `--sync=off`。

## 范围外

- LLM-backed Raw Organizer。
- Knowledge Expander。
- 正式 `Knowledge/` note patch。
- frontmatter update。
- `/replan`。
- desktop Obsidian CLI executor。
- `/open job_id` 或打开 Obsidian note。
- Headless Sync setup、login、logout、config、unlink。
- `ob sync --continuous`。
- Obsidian plugin 工作。
- IM platform adapter 实现。
- 高风险 vault restructuring 自动执行。
- 写入旧 FlashBang 仓库。

## 最小 policy

允许的 Headless Sync 命令：

```text
ob sync-status --path <vault_root>
ob sync --path <vault_root>
```

必须满足：

- 只有 effective sync 为 `on` 时才调用默认的 `HeadlessSyncClient`；
- `ob` binary path 来自 CLI 参数或环境变量，默认命令名为 `ob`；
- `vault_root` 必须是当前 executor 已配置的 vault root；
- sync command 必须通过固定参数数组执行，不能拼接 shell 字符串；
- Headless Sync 输出只作为 status / warning / error 摘要保存，不进入 LLM prompt；
- pre-sync 失败时不得执行任何 vault 写入；
- pre-sync 成功但 hash guard 失败时，plan 进入 conflict；
- post-sync 失败时，vault 写入保持 applied，结果中必须包含 sync warning。

继续阻止：

- arbitrary shell；
- 任意 `ob` 子命令；
- `ob sync-setup`；
- `ob login` / `ob logout`；
- `ob sync-config`；
- `ob sync-unlink`；
- `ob sync --continuous`；
- 写出配置的 vault root；
- 修改 `.obsidian`、`.git` 或其他 hidden path；
- 把 Headless Sync 当作事务或冲突解决机制。

## CLI 验收

默认 test vault 不触发 sync：

```sh
go run ./cmd/openwhisker plan approve <plan_id|job_id>
```

真实 vault 默认触发 sync：

```sh
go run ./cmd/openwhisker plan approve --vault /path/to/vault <plan_id|job_id>
```

显式关闭 sync：

```sh
go run ./cmd/openwhisker plan approve --sync=off --vault /path/to/vault <plan_id|job_id>
```

显式启用 sync：

```sh
go run ./cmd/openwhisker plan approve --sync=on --vault /path/to/vault <plan_id|job_id>
```

手动检查和触发 sync：

```sh
go run ./cmd/openwhisker vault sync-status --sync=on --vault /path/to/vault
go run ./cmd/openwhisker vault sync --sync=on --vault /path/to/vault
```

可选 binary path：

```sh
go run ./cmd/openwhisker vault sync-status --sync=on --ob-bin /custom/path/ob --vault /path/to/vault
```

## 测试场景

- fake `ob` runner 能验证 `sync-status` 只调用 `ob sync-status --path <vault_root>`。
- fake `ob` runner 能验证 one-shot sync 只调用 `ob sync --path <vault_root>`。
- `plan approve` 面向默认 test vault 时保持 Phase 2 行为，不调用真实 sync client。
- `plan approve --vault /real/vault` 默认 sync-aware。
- `plan approve --sync=off` 保持 Phase 2 行为，不调用 sync client。
- effective sync 为 `on` 时，顺序是 pre-sync、`DirectFS.Apply` 内部 lock、hash guard、apply、post-sync。
- pre-sync 失败时，Knowledge draft 不创建，Raw note 不移动。
- pre-sync 后目标文件变化时，apply 进入 conflict，不留下部分写入。
- post-sync 失败时，plan 保持 applied，outbox/result 包含 sync warning。
- Phase 1 raw ingest、Phase 2 organize/diff/approve/reject/conflict regression 继续通过。

## Review 问题

- `SyncResult` 第一版不建独立 `sync_events` 表，先放入 outbox / job result JSON；等出现 sync history 查询需求再建表。
- `plan approve` 第一版不支持 `--sync=before-only`，故障恢复先通过手动 `vault sync` 处理。
- Headless Sync binary path 优先级为 CLI flag、`OPENWHISKER_OB_BIN`、默认 `ob`。
- post-sync warning 不新增 `done_with_warning` 状态；保持 `job status = done`、`plan status = applied`，只在 result/outbox 中标记 warning。
- desktop Obsidian CLI executor 不作为 Phase 4 前置；先完成 Headless Sync 和 IM approval/outbox 后再评估是否接入。
