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
