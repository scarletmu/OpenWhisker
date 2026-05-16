# Phase 4 Matrix Test 反馈

状态：记录中。

本文记录 Phase 4 Matrix adapter 在真实 Matrix homeserver + 本地 vault 下的手动验证反馈。

## 2026-05-14 真实 Matrix 冒烟

环境：

- Matrix homeserver：`https://matrix.scarletmu.com`
- Matrix 服务端部署对象：VPS `scm-tencent`
- OpenWhisker daemon：本地 Mac
- Vault：`testdata/vault`
- Organizer：`deterministic`
- 房间：非 E2EE 房间，使用 bot 账号 `@openwhisker-bot:matrix.scarletmu.com`

已验证：

- `matrix daemon` 可以用账号密码登录模式创建本地 session cache。
- 本地 daemon 可以通过公网 Matrix 域名完成 `/sync`。
- Synapse 日志可看到 bot 账号的 `/sync` 请求。
- `/status` 已经可以回 Matrix；此前发现即时响应未回发，已修复为直接发送即时 `AdapterResponse`。
- 普通 Matrix 文本可以创建 low-risk raw capture job，并写入 `Raw/Inbox/`。
- `/organize last` 可以生成 medium-risk plan，`/diff` 可以查看 diff，`/approve` 可以 apply。
- 已验证一次 approve 落地：plan 状态为 `applied`，operation log 中 `create_note` 和 `move_note` 均为 `applied`，并产生 `Knowledge/Drafts/` 输出和 `Raw/Processed/` 处理记录。

观察到的问题：

1. Element 会把 `/jobs`、`/diff` 等输入优先解释为客户端 slash command。
   - 临时绕过方式：输入 `//jobs`、`//organize last`、`//diff <id>`，Element 会按普通消息发送成 `/jobs`。
   - 后续改进：不急于增加更多固定前缀；未来可以考虑用极小模型做 `Command Detector`，只做意图分类和参数抽取，再交给 Core 执行。

2. 对 low-risk raw capture job 执行 `/diff <job_id>` 时，会返回 `plan ... has no prepared diff`。
   - 原因：raw capture 是低风险自动 apply，不会生成 prepared diff；diff / approval 应用于 `/organize last` 生成的 medium-risk plan。
   - 已修复：Core Adapter 现在会返回 `no_diff` 人类可读提示，说明 raw capture 已低风险自动完成，需要先运行 `/organize last` 再查看整理计划 diff。

3. 启动初期出现过偶发 `TLS handshake timeout`。
   - 同期 `/_matrix/client/versions` 健康检查和 Synapse `/sync` 日志正常。
   - 初步判断为本机网络或代理链路波动；daemon 自动重试后恢复。

4. Bot 回发 diff / approval 等内容时，Element 对原始 Markdown 渲染不理想。
   - 原因：此前只发送 plain `body`，没有提供 Matrix `formatted_body`。
   - 已修复：Matrix adapter 现在会保留 plain text fallback，同时为列表、标题和代码块提供 `org.matrix.custom.html` 格式化内容。

5. `/diff` 的内容更像底层 executor preview，不适合人工审批。
   - 原因：Core Adapter 直接拼接 operation type、target path 和底层 preview，包含 frontmatter、hash 语义和过长正文片段。
   - 已修复：Matrix `/diff` 现在渲染为审批摘要，先展示 plan、摘要、操作清单、审批前检查点，再给短内容预览；底层 JSON diff 仍保留给 CLI / 调试路径。

6. 真实 vault 验证时发现 Knowledge draft tag 口径不符合 `Meta/Tagging.md`。
   - 原因：早期 Phase 4 policy 和模板使用了 `knowledge/draft`、`review/needed`，不属于当前 vault 受控前缀。
   - 已修复：当前 KnowLedge vault 的 `knowledge-vault` profile 使用 `type/knowledge`、`status/draft`、`status/needs-review`；这些 tag 不再作为所有 vault 的硬编码通用 schema。

## 进入真实 vault 前的本地状态隔离

真实 Matrix + `testdata/vault` 的完整闭环已经跑通。切到真实 vault 前，需要把本地运行库和 vault target 显式拆开：

```sh
OPENWHISKER_DEBUG_VAULT=/Users/wang/Documents/KnowLedge \
OPENWHISKER_DEBUG_DB=data/openwhisker-real.db \
scripts/local/matrix-debug.sh daemon
```

`OPENWHISKER_MATRIX_SESSION_FILE` 和 `OPENWHISKER_MATRIX_SINCE_FILE` 可以继续复用；`session` 只代表 Matrix 登录态，`since` 只代表 Matrix 增量游标。`OPENWHISKER_DEBUG_DB` 应按验证阶段拆分，避免 test vault 的 job / plan / operation log 和真实 vault 混在同一个 SQLite 文件里。

## 下一步

进入真实 vault + deterministic organizer 验证：

1. 启动前先确认真实 vault 的 `AGENTS.md` 和 `Meta/` 规则仍是当前准则。
2. 用一条低风险普通文本验证 raw capture 写入 `Raw/Inbox/`。
3. 用 `/organize last` 生成 medium-risk plan。
4. 用 `/diff <plan_id>` 检查 Knowledge draft、Raw/Processed move 和 processing note。
5. 用 `/approve <plan_id>` 验证 hash guard、Headless Sync、operation log 和 Matrix 回执。

## 2026-05-14 真实 vault + LLM 前置验证

真实 vault + deterministic organizer 已验证到 diff 阶段，并按预期 reject 了测试 plan。当前结论：

- Matrix -> Core -> Plan -> Diff -> Reject 路径正常。
- raw 仍保留在 `Raw/Inbox/`，reject 不写 Knowledge，也不移动 raw。
- deterministic organizer 只适合验证链路，不适合验收真实知识内容质量。
- Knowledge draft tag policy 已通过 `knowledge-vault` profile 对齐真实 vault 的 `Meta/Tagging.md`：`type/knowledge`、`status/draft`、`status/needs-review`。

为了避免未经确认地把真实 vault 内容发送到外部 LLM endpoint，先用合成 raw + `testdata/vault` 做了 OpenAI-compatible provider smoke。结果：

- OpenWhisker 已成功发起 Chat Completions 请求，说明本地 API key / base URL / model 配置被读取到了。
- Provider 返回 `403 Forbidden`，错误信息为“无权访问 vip 分组”。
- 失败被记录为 `organize_raw failed`，并写入 error outbox；没有生成可审批 plan，也没有写 vault。

后续修正兼容 endpoint 权限后，已重跑合成 raw + `testdata/vault` provider smoke：

- OpenAI-compatible organizer 成功返回结构化结果。
- OpenWhisker 生成 medium-risk `awaiting_approval` plan。
- LLM 输出包含中文 Knowledge draft、source trace、`raw_kind`、待核查项和 Raw/Processed processing note。
- Policy gate 通过，并在 `knowledge-vault` profile 下保留 `type/knowledge`、`status/draft`、`status/needs-review`。
- 测试 plan 已 reject，没有 apply，也没有写入测试 Knowledge draft。

当前 blocker 已解除。下一步如要进入真实 vault + OpenAI-compatible LLM，需要显式确认允许把真实 vault raw/context 发送到该 endpoint。

随后调整了真实 LLM 的默认 context 策略：

- 默认 `context-mode=minimal`：只发送 raw note、由当前 `VaultProfile` 编译出的 `VaultRawOrganizerSkill` 和当前 `VaultProfile` 摘要。
- 不再默认发送完整 vault 规则全文。
- `AGENTS.md`、`Meta/README.md`、`Meta/Tagging.md`、`Raw/AGENTS.md` 和 `Knowledge/AGENTS.md` 仅在显式 `--context-mode=vault-rules` 调试模式下附带。
- vault policy 的长期规则应先由 LLM 分析成本地候选 `VaultProfile`，经人工确认后再编译成 task-specific `VaultSkill`，并由本地 policy gate 强校验。OpenWhisker 不应把当前 vault 的目录、tag 或笔记组织方式当成所有 vault 的固定格式。
- 已用合成 raw 复测 minimal context 下的 OpenAI-compatible organizer：不附带 vault 规则全文也能生成合规 medium-risk plan；测试 plan 已 reject，未 apply。

## 2026-05-15 真实 vault + OpenAI-compatible LLM 验证

真实 vault + OpenAI-compatible LLM 已完成一次保守端到端验证，流程走到 diff 后按预期 reject。

当前结论：

- Matrix -> Core -> OpenAI-compatible Raw Organizer -> Policy Gate -> Plan -> Diff -> Reject 路径正常。
- 默认 `context-mode=minimal` 可用于真实 vault 验证，不需要默认外发完整 vault 规则全文。
- LLM 能基于真实 vault raw/context 生成可审批 medium-risk plan。
- reject 后没有 apply，不写 Knowledge draft，也不移动 raw 到 `Raw/Processed/`。
- 真实 vault + LLM 的基础规划链路已通；真实 vault + LLM + approve/apply 闭环仍未验证。

下一步：

1. 若要完成 Phase 4B 的真实写入闭环，使用一条低风险测试 raw 再跑一次真实 vault + OpenAI-compatible LLM，并在人工确认 diff 后 approve，验证 hash guard、Headless Sync、operation log、Knowledge draft 和 Raw/Processed processing note。
2. 如果暂时不希望 LLM 结果写入真实 vault，可以先进入 Phase 4C，推进 Knowledge Expander 和 high-risk proposal policy；后续再补一次 approve/apply 验证。

## 2026-05-15 Matrix diff 人类审批体验反馈

真实使用反馈：当前 Matrix `/diff` 虽然已经不是底层 JSON diff，但仍然太“机械”。它更像面向大模型或工程调试的 operation 清单，而不是给人类看的实际写入内容预览。

问题判断：

- 当前 diff 主体仍围绕 `create_note`、`move_note`、target path 和 executor preview 展开。
- 人类审批时最关心的是批准后 vault 里会出现什么内容，而不是底层会执行哪些 `VaultOperation`。
- `DiffEntry.Preview` 是工程截断预览，不适合作为 Matrix 审批页的主体。
- Matrix 回执需要继续同时支持 plain text fallback 和 `org.matrix.custom.html` formatted body，不能为了更好的人类阅读而退回纯文本。

改进方向：Matrix `/diff` 应渲染为“人类审批页”，底层 JSON diff 和工程 preview 仍保留给 CLI / debug。

目标结构：

```text
## 将写入的知识草稿

路径：Knowledge/Drafts/...
标题：...
标签：type/knowledge, status/draft, status/needs-review
来源：Raw/Inbox/...

### 正文预览

展示将创建的笔记正文主要内容。不要把 frontmatter、trace metadata 或 hash 放在第一屏。

### 待核查

- ...

## 将移动的 Raw

Raw/Inbox/...
-> Raw/Processed/...

处理记录会追加：
- 输出：Knowledge/Drafts/...
- 状态：needs review
- Plan：plan_...

## 审批动作

批准：//approve plan_...
拒绝：//reject plan_...
```

渲染要求：

- 第一屏优先展示实际产物：标题、路径、tags、来源和正文预览。
- operation 清单降级为“将创建 / 将移动”，不要作为主体。
- 默认隐藏 `before_hash`、`openwhisker_job_id`、底层 payload、trace metadata 等工程噪音。
- frontmatter 只提炼关键信息，例如 tags、source、review 状态。
- `move_note` 要解释 Raw 会移动到哪里，以及 processing note 会记录什么。
- approval / reject 命令放在底部。
- Matrix adapter 必须继续生成 `formatted_body`，保证标题、列表、代码块和转义内容在 Element 中能正常渲染；plain `body` 作为 fallback 保留。

下一步：

- 已实现第一版 Matrix/Core adapter diff renderer 改造，不改 `VaultDiff` / executor 底层数据结构。
- Renderer 现在从 `VaultOperation` payload 中提取 create / move 信息，而不是依赖 `DiffEntry.Preview` 作为主要内容来源。
- 本地 adapter 入口已预览输出效果：正文预览会跳过 `Source` / trace 段，优先展示实际草稿正文；底部保留 `//approve` / `//reject` 操作提示。
- 后续真实 Matrix 验证时重点看 Element `formatted_body` 渲染效果，以及真实 LLM 输出下标题、来源、tags、待核查项的提取是否自然。
