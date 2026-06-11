# OpenWhisker 部署指南

本文是 OpenWhisker **守护进程本身**的部署引导：怎么构建、配置、长期运行。Matrix 服务端（Synapse / Caddy / PostgreSQL）拓扑见 [`docs/adapters/matrix-private-im.md`](../30-design/matrix-adapter.md)。

v1 的标准部署形态是**本地硬件上的原生常驻服务**：家用桌面 Mac 走 launchd，Linux NUC 走 systemd。Docker 在 v1 里被定位为**可复现构建与验证工具**，不是生产主形态；容器形态作为可选运行方式保留（见下方「可选：容器形态」）。

本文只承担**基本引导**职责。含真实 homeserver、密钥下发、填好值的 service / compose 文件、主机拓扑的关键运作文档不进 git，放在仓库内 `deploy/local/`（已被 `.gitignore` 忽略）。

## 部署形态

OpenWhisker v1 以一个常驻进程部署在本地常开设备上，由系统服务管理器拉起：

```text
本地设备（桌面 Mac / Linux NUC）

  launchd / systemd
        │ 常驻拉起、崩溃重启
        ▼
  openwhisker daemon  ←—— 可选 Matrix client-server API ——→  [Matrix homeserver]
        │ scheduler tick / outbox delivery / 可选 Matrix poll
        ├── data/    运行状态（SQLite / daemon status / Matrix session / since-token）
        └── vault/   目标 Obsidian vault
```

主入口是 `openwhisker daemon`。它负责 scheduler tick loop、tick 后 outbox delivery，并在 Matrix 配置存在时同时运行 Matrix `/sync` poll loop。旧的 `openwhisker matrix daemon` 保留为 Matrix adapter 调试和兼容入口，但不再是长期运行的主入口。

### 为什么是原生服务而非容器

v1 的部署目标是家里常开的桌面 Mac 或 Linux NUC，不是云端 VPS/VDS。在这个前提下原生 launchd / systemd 比容器更顺：

- 与桌面 Obsidian 及 Obsidian Sync 共处同一台设备时，没有卷映射和容器 UID 与宿主文件属主的摩擦 —— daemon 直接以用户身份读写用户 home 下的 vault。
- 服务定义文件（macOS `.plist`、Linux `.service`）作为一等部署产物入库，部署可复现、可审阅。
- 不需要为一个单进程常驻服务维护一层容器运行时。

Docker 仍然有用，但职责收窄为：跑可复现构建、产出并 smoke 验证运行镜像（`make docker-build`）。容器运行形态作为可选项保留，主要面向设备本身就是容器宿主、或要与自托管 homeserver 同栈编排的场景。

## 前置条件

- 一台本地常开设备：桌面 Mac 或 Linux NUC。
- Go toolchain —— 原生构建（`make build`）需要。镜像构建与验证另需 Docker。
- 一个可达的 Matrix homeserver 和一个低权限 bot 账号（服务端部署见 `matrix-private-im.md`）。
- 一个 OpenAI-compatible LLM 端点和 API key（DeepSeek 等）。
- 目标 vault 目录（v1 默认指向一个目录；真实 vault + Obsidian Sync 生产签收见下方范围边界）。

## 构建

```sh
make build         # 本机编译到 bin/openwhisker —— 原生服务部署用这个
make docker-build  # 构建运行镜像 openwhisker:local —— 可复现构建 / 验证用
```

构建链路对 CGO 有硬依赖：SQLite 存储用的是 `github.com/mattn/go-sqlite3`，因此 `CGO_ENABLED=1`、运行镜像基于 glibc（`Dockerfile` 用 `debian:bookworm-slim`，不能换 `scratch` / distroless-static）。

注意 `make docker-build` 产出的是 Linux 容器镜像，不能直接当 macOS 原生二进制用。macOS 设备上的原生服务部署用 `make build` 本机编译。

## 配置

所有配置经环境变量注入，没有独立配置文件。完整变量清单以仓库根 [`.env.local.example`](../../.env.local.example) 为准（覆盖 LLM、Intent Router、Matrix、capture bucket 各组）。

daemon 启动时会从工作目录起向上查找并加载 `.env.local`：文件里的值注入进程环境，已显式设置的环境变量优先于 `.env.local` 中的同名值。因此原生服务部署只需：

1. `cp .env.local.example .env.local`
2. 填入真实 LLM / Matrix 凭据。`.env.local` 已被 git 忽略。
3. 把 `.env.local` 放在下文约定的 OpenWhisker home 目录，并让 service 文件的 `WorkingDirectory` 指向它。

无需在 `.plist` / `.service` 文件里内联密钥 —— 把它们留在 `.env.local` 一处。

Matrix 部署当前只配置一个私有、非 E2EE 自动化房间，并通过一个
`OPENWHISKER_MATRIX_ROOM_ID` 接入。这个房间同时承载 capture、命令、审批、
scheduler 输出和告警；不要在当前部署文档或模板中拆分 Inbox / Approval /
Alert 等多房间拓扑。

## 以原生服务运行

约定一个 **OpenWhisker home 目录**作为运行根，里面放：

```text
<openwhisker-home>/
  bin/openwhisker   由 make build 编译产出（或软链到构建产物）
  .env.local        填好的配置，git-ignored
  data/             运行状态，daemon 自动创建
  logs/             stdout / stderr 日志
```

service 文件把 `WorkingDirectory` 指向这个目录，`--db` / `--since-file` / `--session-file` 用相对 `data/` 的路径即可。

### macOS —— launchd

详细说明：[`launchd.md`](launchd.md)。

模板：[`openwhisker.daemon.plist.example`](openwhisker.daemon.plist.example)。

```sh
# 1. 拷贝模板到用户 LaunchAgents，并按真实路径填写 <...> 占位符
mkdir -p ~/Library/LaunchAgents
cp docs/deployment/openwhisker.daemon.plist.example \
   ~/Library/LaunchAgents/local.openwhisker.daemon.plist

# 2. 装为当前用户的 LaunchAgent
launchctl bootstrap gui/$(id -u) \
   ~/Library/LaunchAgents/local.openwhisker.daemon.plist
launchctl enable gui/$(id -u)/local.openwhisker.daemon
```

用 LaunchAgent（per-user）而非 LaunchDaemon，是因为 vault 在用户 home 下、桌面 Obsidian 也跑在用户会话里，daemon 以同一用户身份运行最省事。

### Linux —— systemd

详细说明：[`systemd.md`](systemd.md)。

模板：[`deploy/openwhisker.systemd.example.service`](../../deploy/openwhisker.systemd.example.service)。

```sh
# 1. 拷贝模板到 git-ignored 的 deploy/local/，按真实路径填写 <...> 占位符
cp deploy/openwhisker.systemd.example.service deploy/local/

# 2. 装为当前用户的 user service
cp deploy/local/openwhisker.systemd.example.service \
   ~/.config/systemd/user/openwhisker.service
systemctl --user daemon-reload
systemctl --user enable --now openwhisker.service

# 3. 无人值守的 NUC 需要 linger，让 user service 在没有登录会话时也常驻
loginctl enable-linger "$USER"
```

模板默认是 user service（vault 在 `$HOME` 下时最自然）。要装成 system-wide service 的变体说明见模板内注释。

### 进程与状态

- **优雅退出**：daemon 处理 `SIGINT` / `SIGTERM`；Matrix poll loop 会保存 `next_batch` token 后退出。launchd `KeepAlive` 与 systemd `Restart=on-failure` 负责崩溃后拉起。
- **持久化目录**：`data/` 里是 SQLite 库、daemon status 文件、Matrix session 缓存、`/sync` since-token。服务重装但保留该目录即可无缝续跑。
- **状态检查**：`openwhisker daemon status --status-file data/daemon-status.json` 检查本地状态文件和 PID；`openwhisker scheduler status` / `scheduler runs` 查看 scheduler runtime 和 run log。
- **vault 目录**：目标 vault 以读写方式访问（OpenWhisker 要在 `Raw/Inbox/` 下写 raw note）。

## 同步形态与真实 vault

daemon 通过 `direct_fs` 直接写 vault 文件，**自身不负责跨设备同步** —— `openwhisker daemon` 没有 `--sync` 开关。跨设备同步取决于设备拓扑，下面两种二选一，不能在同一台设备上并存：

- **桌面同步形态**：设备上同时运行桌面 Obsidian，由 Obsidian Sync 负责把 daemon 写入的文件同步到其它设备。daemon 不做额外动作。切到真实 vault 只是把 `--vault` 指向真实 vault 路径，无新增代码。
- **Headless 同步形态**：设备上没有桌面 Obsidian，跨设备同步需要 Obsidian Headless Sync（`ob` 二进制）。`ob` 不内嵌在 daemon 里 —— 它属于交互式 `plan approve` / `vault sync` CLI 路径，不在常驻服务里。

硬约束（见 [`design-philosophy.md`](../20-architecture/design-philosophy.md) §4.5）：**不要在同一台设备上让桌面 Obsidian Sync 和 Headless Sync 同时管理同一个 vault。**

## v1 范围边界

以下明确**不在 v1 部署范围**：

- **常驻服务不内嵌 `ob`（Obsidian Headless Sync CLI）**：带 sync 的 `plan approve` 属于交互式 CLI 路径，不在常驻 daemon 里。真实 vault + Obsidian Sync 的生产签收按 `CHANGELOG.md` 仍属 v1 范围外。
- 不提供 Kubernetes / Terraform / Ansible 编排。
- 不含监控告警、TLS 自动续期、备份自动化 —— 手动步骤记在 `deploy/local/runbook.md`。
- 不实现 OpenWhisker Core 的 HTTP / API service 形态。

## 可选：容器形态

当部署设备本身就是容器宿主、或要与自托管 homeserver（Synapse / Caddy / PostgreSQL）同栈编排时，可以用容器形态代替原生服务。

`deploy/openwhisker.compose.example.yaml` 是 committed 模板：

```sh
cp deploy/openwhisker.compose.example.yaml deploy/local/compose.yaml
# 在 deploy/local/compose.yaml 里填实 vault 路径与 env_file 路径
make docker-build
docker compose -f deploy/local/compose.yaml up -d   # 或 make docker-run
```

容器内建议运行同样的 `openwhisker daemon`，挂载 `data/` 持久卷与 vault 卷。与自托管 homeserver 同栈编排时，把这个服务并入 `matrix-private-im.md` 的 Synapse / Caddy / PostgreSQL 骨架。

## 关键运作文档

含真实主机名、密钥下发流程、填好值的 service / compose 文件、首启核验、token 轮换、备份的运作文档**不进 git**，放在：

```text
deploy/local/
  runbook.md                          真实环境部署与运维步骤
  compose.yaml                        由 openwhisker.compose.example.yaml 填实
  openwhisker.daemon.plist             由 docs/deployment/openwhisker.daemon.plist.example 填实（macOS）
  openwhisker.systemd.example.service  由对应 example 填实（Linux）
```

`deploy/local/` 已在 `.gitignore` 中忽略，与 `scripts/local/` 同一路数。

## 相关文档

- [Matrix Private IM 适配](../30-design/matrix-adapter.md)：homeserver / Caddy / Synapse 服务端拓扑。
- [macOS launchd 部署](launchd.md)：OpenWhisker daemon 的用户级 LaunchAgent 模板与状态检查。
- [Linux systemd 部署](systemd.md)：OpenWhisker daemon 的用户级 systemd service 模板与状态检查。
- [设计哲学](../20-architecture/design-philosophy.md)：执行器、同步形态与设备拓扑约束。
- [当前进度](../00-overview/project-status.md)：验证状态与已知遗留。
- [环境变量模板](../../.env.local.example)：完整配置项清单。
