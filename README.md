# OpenWhisker 🦭

OpenWhisker 是一个自托管的 Obsidian 知识库助手：它常驻在你自己常开的设备上（家用 Mac / Linux NUC），数据和进程都在你手里，但你从手机或任何地方通过 IM 就能用它。IM 入口走可插拔的 adapter，目前实现了 Matrix。

你在 IM 里随手扔一段话，它帮你记进 Obsidian 并自动补上标签；你说一句"整理一下"，它把零碎输入整理成一篇 Knowledge 草稿，并先把改动摆给你看，你点头它才写；你也可以直接问它问题，它读你自己的库来回答；还能挂几个只读的定时任务，帮你盯 RSS、定期提醒。

它的核心立场是：**让 agent 帮你动笔，但不许它越过你写字**。

## 你能用它做什么

### 1. 随手说一句，它帮你存进知识库（并自动补标签）

在 Matrix 里直接发：

```text
记录一下：今天和 X 聊到 OAuth2 的 PKCE，关键点是 code_verifier
```

OpenWhisker 会在你 vault 的 `Raw/Inbox/` 下创建一条带 frontmatter 的 raw note，记下时间、来源和原文。下一句继续：

```text
补充：还有 device flow 的对比
```

它会把这一句追加到刚才那条 raw note，而不是新开一个文件。等你说"结束记录"或者超时，这一组就关闭。

存好之后，后台有一个 agent 会自己读这条 raw note，给它补上 `topic/*`、`skill/*` 之类的标签——但**只追加你 vault 里已经存在的 tag**（命中已知词表才写，避免乱造新标签），而且**绝不动你的正文**：正文 body 有 sha256 钉死，标签是追加，原文一字不改。拿不准归属时它会改打 `status/needs-review` 让你自己定。这一步也是自动的。

这一整步都是**自动写入**——因为风险低（只在 `Raw/Inbox/` 下新增 / 追加 / 补标签，不会动你既有的 Knowledge 笔记）。临时不想自动打标签，发一句 `/no-enrich` 就跳过这一次。

### 2. 说"整理一下"，它先给你看 diff

```text
整理刚才
```

OpenWhisker 会读这一组 raw 输入，调 LLM 生成一份 Knowledge 草稿，然后**不写入**，而是回你一份人类可读的预览：将创建哪个文件、文件里写什么、原 raw note 会被移到哪。

你回 `写进去` 才真的执行；回 `先不写` 就丢弃。也可以用 `/diff <plan_id>` 查看更早的待审批计划。

想往一篇**已有**的 Knowledge 笔记里补内容，用 `expand`：它会读那篇笔记，生成一段追加（中风险，照样先给你看 diff），大改结构则降级成一份 proposal note 让你先审，不会就地动刀。

### 3. 问它问题，它读你的库来回答

不只是往里写，也能往外查。在 Matrix 里 `@` 一个 skill，或在 CLI 里 `ask`，它就以**只读**身份读你的 vault 来回答：

```text
@notes 我之前关于 OAuth2 都记了些什么？
```

回答之前它会先做一次**记忆召回**——按标签、正文、以及笔记之间的 `[[wikilink]]` 关系，把相关的旧笔记捞出来作为上下文。整个问答路径只读，不会写你的库。

### 4. 定时只读任务：盯 RSS、定期提醒

你可以在 vault 里放几个只读的定时 Skill（5 字段 cron + 时区），让它定期跑：拉 RSS / Atom（含 RSSHub route）出摘要、做周期性提醒等。结果发到 IM，**不会写 vault**。如果某条定时输出建议你"记一笔"，它会以 `suggested_raw_captures` 给出，你回一句 `/scheduler accept` 才会转入上面第 1 步的捕获流。

### 5. 听不懂的时候，它问你

如果你的输入模糊（比如"那个事儿再补一句"，但当前有多个 active 话题），OpenWhisker 不会乱猜，而是给你编号选项：

```text
你是想——
1) 接着上一组（OAuth2 笔记）
2) 新开一组
3) 都不用
```

你回 `1` 或者 `选 1` 或者 `第一个` 都行。

### 6. 真实 vault 不会被偷偷写

- 它只写你用 `--vault <path>` 显式指定的那个 vault，且写入被严格限制在 vault root 内——绝对路径、`..` 穿越、symlink 逃逸、`.obsidian/` / `.git/` 等隐藏目录一律拒绝。（没配 `--vault` 时只会动仓库自带的 `testdata/vault` 示例，碰不到你的真实库。）
- 用 CLI `plan approve` 在真实 vault 上 apply 时，默认（`--sync=auto`）会在前后各做一次 Obsidian Headless Sync，避免和手机端并发冲突；常驻 `daemon` 不自己做同步，跨设备同步交给同机的桌面 Obsidian Sync（详见[部署指南](docs/deployment/README.md)）。
- 每个被改写的文件都带 `before_hash` 校验，并发或手动改动会被拒绝而不是覆盖。

### 7. 不想用 IM 也可以纯 CLI

捕获与审批流：

```sh
go run ./cmd/openwhisker ingest raw --text "需要整理的一段原始输入"
go run ./cmd/openwhisker organize last
go run ./cmd/openwhisker plan diff <plan_id>
go run ./cmd/openwhisker plan approve <plan_id>
go run ./cmd/openwhisker plan reject <plan_id> --reason "暂不整理"
```

其余能力同样有对应命令：

```sh
go run ./cmd/openwhisker enrich --path Raw/Inbox/xxx.md       # 手动给某条 raw note 补标签
go run ./cmd/openwhisker expand Knowledge/Systems/Observability.md  # 扩写已有 Knowledge 笔记
go run ./cmd/openwhisker ask --skill <skill_id> "你的问题"     # 让 agent 只读你的库来回答
go run ./cmd/openwhisker scheduler tick                       # 手动跑一遍到点的定时 Skill
go run ./cmd/openwhisker scheduler accept <run_id>            # 把定时输出里的建议转成 raw 捕获
go run ./cmd/openwhisker agent runs list                      # 查看 agent 运行记录（含工具轨迹）
go run ./cmd/openwhisker memory reindex                       # 重建标签 / 链接召回索引
```

完整命令面见 `go run ./cmd/openwhisker`（无参数会打印 usage）。

## 快速开始

```sh
cp .env.local.example .env.local
```

按需填 LLM 和 Matrix 配置（`.env.local` 已被 git ignore，CLI 启动自动加载）：

```sh
# 用来生成 Knowledge 草稿的 LLM —— 只要是 OpenAI-compatible 端点都行：
#   云端 DeepSeek (https://api.deepseek.com)、OpenAI (https://api.openai.com/v1)，
#   或本地 Ollama (http://localhost:11434/v1)。BASE_URL 指哪它就用哪。
# 接本地 Ollama 时连模型也留在本机，整套就是完全自包含的（API key 随便填）。
OPENWHISKER_LLM_API_KEY=
OPENWHISKER_LLM_BASE_URL=
OPENWHISKER_LLM_MODEL=

# IM 自然语言入口要不要启用小模型 fallback
OPENWHISKER_INTENT_ROUTER=hybrid      # off / rules / hybrid
OPENWHISKER_INTENT_API_KEY=
OPENWHISKER_INTENT_BASE_URL=
OPENWHISKER_INTENT_MODEL=

# Matrix 入口
OPENWHISKER_MATRIX_HOMESERVER=
OPENWHISKER_MATRIX_USER_ID=
OPENWHISKER_MATRIX_PASSWORD=
OPENWHISKER_MATRIX_ROOM_ID=
OPENWHISKER_MATRIX_SCHEDULER_USER_ID=
OPENWHISKER_MATRIX_SCHEDULER_PASSWORD=
```

跑起来：

```sh
# 单纯本地 CLI 玩一下，不需要 IM
go run ./cmd/openwhisker ingest raw --text "raw input"

# 长期运行：scheduler 定时 tick + 自动 enrich + 可选 Matrix 监听，一个进程全包
go run ./cmd/openwhisker daemon

# 只想要 Matrix 监听
go run ./cmd/openwhisker matrix daemon
```

默认数据库 `data/openwhisker.db`，默认 test vault `testdata/vault`。

跑测试：

```sh
go test ./...
```

## 部署

把 OpenWhisker 作为长期服务跑起来（本地硬件上的原生 launchd / systemd 服务），见 [部署指南](docs/deployment/README.md)。

## 它为什么不会乱来

- **LLM 只生成计划，不动文件**：LLM 输出的是结构化 `VaultPlan`，由本机受控的 executor 执行，并经过 policy check（必须有 source refs、target paths、合规的 frontmatter / tags 等）。
- **没有 shell、没有 Obsidian CLI**：LLM 拿不到任意命令执行权限。OpenWhisker 自己调用 `ob` 时也只允许 `ob sync-status` 和 `ob sync` 两条白名单命令。
- **路径硬约束**：严格阻止绝对路径、`..` 穿越、symlink 逃逸、写出 vault root、动 hidden file。
- **写入前 hash 校验**：任何 rewrite / move 操作都带 `before_hash`，并发或手动改动会让 plan 失败而不是覆盖。
- **自动打标签受约束**：enrich 唯一允许的写操作是给本次对应的 inbox 文件 `rewrite_note`，frontmatter 只能追加已知词表内的 `topic/*` / `skill/*`（外加 `status/needs-review`、`raw/*`），不能删既有项，正文 body 由 sha256 钉死，原文永不被改；失败或超时一律静默丢弃，不进审批队列。
- **定时任务只读**：scheduled Skill 只能读 vault 和拉取允许列表内的外部信息源，registry 阶段就拒绝 vault 写入、自动审批、任意 shell / HTTP / 文件写；想把它的建议落库，必须你显式 `accept`。
- **问答只读**：`ask` / `@skill` 路径只读你的库，agent 工具是闭集的只读工具 + 记忆召回，`.obsidian/` `.git/` `.trash/` 等永远禁入。
- **隐私 audit 不泄露内容**：IM intent 分类的 audit 日志只记录类型、置信度、是否被采纳，不写入消息正文、room id、sender id 或 source key。

## 想了解更深

- [Project Guide](AGENTS.md)：面向 LLM agent 的项目规则、参考源关系和工作边界。
- [文档索引](docs/README.md)：当前文档结构和推荐阅读顺序。
- [部署指南](docs/deployment/README.md)：构建、配置、以原生服务长期运行。
- [当前进度与交接说明](docs/progress.md)：当前状态、验证状态和下一步优先级。
- [项目决策](docs/project-decisions.md)：已收敛的长期架构决策和阶段默认值。
- [设计哲学](docs/architecture/design-philosophy.md)：VaultPlan / VaultExecutor 架构理念。
- [架构概览](docs/architecture/overview.md)：当前架构边界和后续方向。
- [IM Intent Router 架构规格](docs/architecture/im-intent-router.md)
- [Capture Bucket 规格](docs/architecture/capture-bucket.md)
- [Intent Router 小模型 Contract](docs/architecture/intent-router-model-contract.md)
- [只读定时 Skill 调度](docs/phases/phase-5-read-only-skill-scheduler.md)：scheduler、cron、外部信息源适配器。
- [Agent 工具调用](docs/phases/phase-6-scheduler-skill-creator.md)：`ask` / `@skill` 多轮工具循环与 budget 守卫。
- [收件箱自动打标签](docs/phases/phase-7-inbox-enrichment.md)：enrich 编排与三道 policy guard。
- [记忆召回](docs/phases/phase-8-memory-recall.md)：tag / text / link 三 pass 召回。
- [Matrix Adapter 实践](docs/adapters/matrix-private-im.md)
- [Changelog](CHANGELOG.md)

## 一句话架构

```text
你说的话 (IM / CLI)
  → 听懂意图（规则优先，听不懂才问小模型，仍听不懂就反问你）
  → 生成计划（LLM 写 VaultPlan，不写文件）
  → 给你看 diff（低风险跳过，中风险等你点头）
  → 受控执行（路径 / hash / lock / sync 全程把关）
  → 写进 Obsidian vault
```

上面是主**写**路径。raw 存入后的自动 enrich 也走同一条 `VaultPlan → Policy → Executor`，只是被收窄到"只能给这条 inbox 文件追加标签"。另有两条**只读**侧路——`ask` / `@skill` 问答和 scheduled 定时 Skill——只读你的库、不写一个字。
