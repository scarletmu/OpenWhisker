# OpenWhisker

OpenWhisker 是一个住在你本机上的 Obsidian 知识库助手。

你在微信式的 IM 里随手扔一段话，它帮你记进 Obsidian；你说一句"整理一下"，它把零碎输入整理成一篇 Knowledge 草稿，并先把改动摆给你看，你点头它才写。

它的核心立场是：**让 agent 帮你动笔，但不许它越过你写字**。

## 你能用它做什么

### 1. 随手说一句，它帮你存进知识库

在 Matrix 里直接发：

```text
记录一下：今天和 X 聊到 OAuth2 的 PKCE，关键点是 code_verifier
```

OpenWhisker 会在你 vault 的 `Raw/Inbox/` 下创建一条带 frontmatter 的 raw note，记下时间、来源和原文。下一句继续：

```text
补充：还有 device flow 的对比
```

它会把这一句追加到刚才那条 raw note，而不是新开一个文件。等你说"结束记录"或者超时，这一组就关闭。

这一步是**自动写入**——因为风险低（只在 `Raw/Inbox/` 下新增 / 追加，不会动你既有的 Knowledge 笔记）。

### 2. 说"整理一下"，它先给你看 diff

```text
整理刚才
```

OpenWhisker 会读这一组 raw 输入，调 LLM 生成一份 Knowledge 草稿，然后**不写入**，而是回你一份人类可读的预览：将创建哪个文件、文件里写什么、原 raw note 会被移到哪。

你回 `写进去` 才真的执行；回 `先不写` 就丢弃。也可以用 `/diff <plan_id>` 查看更早的待审批计划。

### 3. 听不懂的时候，它问你

如果你的输入模糊（比如"那个事儿再补一句"，但当前有多个 active 话题），OpenWhisker 不会乱猜，而是给你编号选项：

```text
你是想——
1) 接着上一组（OAuth2 笔记）
2) 新开一组
3) 都不用
```

你回 `1` 或者 `选 1` 或者 `第一个` 都行。

### 4. 真实 vault 不会被偷偷写

- 默认只写本机 test vault (`testdata/vault`)，不会碰你真实的 Obsidian vault。
- 真实 vault 必须显式 `--vault <path>` 才会被写入，并且默认会在 apply 前后调用 Obsidian Headless Sync 做一次 one-shot 同步，避免和手机端冲突。
- 每个被改写的文件都带 `before_hash` 校验，并发改动会被拒绝而不是覆盖。

### 5. 不想用 IM 也可以纯 CLI

```sh
go run ./cmd/openwhisker ingest raw --text "需要整理的一段原始输入"
go run ./cmd/openwhisker organize last
go run ./cmd/openwhisker plan diff <plan_id>
go run ./cmd/openwhisker plan approve <plan_id>
go run ./cmd/openwhisker plan reject <plan_id> --reason "暂不整理"
```

## 快速开始

```sh
cp .env.local.example .env.local
```

按需填 LLM 和 Matrix 配置（`.env.local` 已被 git ignore，CLI 启动自动加载）：

```sh
# 用来生成 Knowledge 草稿的 LLM（OpenAI-compatible，DeepSeek 等都可以）
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
```

跑起来：

```sh
# 单纯本地 CLI 玩一下，不需要 IM
go run ./cmd/openwhisker ingest raw --text "raw input"

# 长期 Matrix 监听
go run ./cmd/openwhisker matrix daemon
```

默认数据库 `data/openwhisker.db`，默认 test vault `testdata/vault`。

跑测试：

```sh
go test ./...
```

## 它为什么不会乱来

- **LLM 只生成计划，不动文件**：LLM 输出的是结构化 `VaultPlan`，由本机受控的 executor 执行，并经过 policy check（必须有 source refs、target paths、合规的 frontmatter / tags 等）。
- **没有 shell、没有 Obsidian CLI**：LLM 拿不到任意命令执行权限。OpenWhisker 自己调用 `ob` 时也只允许 `ob sync-status` 和 `ob sync` 两条白名单命令。
- **路径硬约束**：严格阻止绝对路径、`..` 穿越、symlink 逃逸、写出 vault root、动 hidden file。
- **写入前 hash 校验**：任何 rewrite / move 操作都带 `before_hash`，并发或手动改动会让 plan 失败而不是覆盖。
- **隐私 audit 不泄露内容**：IM intent 分类的 audit 日志只记录类型、置信度、是否被采纳，不写入消息正文、room id、sender id 或 source key。

## 想了解更深

- [Project Guide](AGENTS.md)：面向 LLM agent 的项目规则、参考源关系和工作边界。
- [文档索引](docs/README.md)：当前文档结构和推荐阅读顺序。
- [当前进度与交接说明](docs/progress.md)：当前状态、验证状态和下一步优先级。
- [项目决策](docs/project-decisions.md)：已收敛的长期架构决策和阶段默认值。
- [设计哲学](docs/architecture/design-philosophy.md)：VaultPlan / VaultExecutor 架构理念。
- [架构概览](docs/architecture/overview.md)：当前架构边界和后续方向。
- [IM Intent Router 架构规格](docs/architecture/im-intent-router.md)
- [Capture Bucket 规格](docs/architecture/capture-bucket.md)
- [Intent Router 小模型 Contract](docs/architecture/intent-router-model-contract.md)
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
