# macOS launchd 部署

本文档描述如何在 macOS 上把 OpenWhisker daemon 作为本地用户服务运行。

## 文件

- 示例模板：`docs/deployment/openwhisker.daemon.plist.example`
- daemon 状态文件：`data/daemon-status.json`
- stdout 日志：`logs/openwhisker-daemon.out.log`
- stderr 日志：`logs/openwhisker-daemon.err.log`

## 准备

先复制模板到用户级 `LaunchAgents` 目录，并按本机路径调整：

```sh
mkdir -p ~/Library/LaunchAgents
cp docs/deployment/openwhisker.daemon.plist.example ~/Library/LaunchAgents/local.openwhisker.daemon.plist
```

必须检查并替换模板中的占位符：

- `<repo-root>`：OpenWhisker 仓库路径。
- `<vault-root>`：目标 Obsidian vault 路径。
不要把真实 token、Matrix room id、私有网关地址或账号写进仓库内模板。运行时配置应放在本机环境、shell 启动器或未提交的本地文件中。

## 启动

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.openwhisker.daemon.plist
launchctl enable gui/$(id -u)/local.openwhisker.daemon
```

## 停止

```sh
launchctl bootout gui/$(id -u)/local.openwhisker.daemon
```

## 查看状态

```sh
bin/openwhisker daemon status --status-file data/daemon-status.json
bin/openwhisker scheduler status --db data/openwhisker.db --vault <vault-root> --vault-profile knowledge-vault
bin/openwhisker scheduler runs --db data/openwhisker.db
bin/openwhisker scheduler schedules list --db data/openwhisker.db --vault <vault-root> --vault-profile knowledge-vault
```

`daemon status` 只检查本地 status 文件和 PID 存活情况；scheduler 的真实执行记录仍以 SQLite 中的 runtime/run 记录为准。

## 查看日志

```sh
tail -f logs/openwhisker-daemon.out.log
tail -f logs/openwhisker-daemon.err.log
```

## 更新服务

如果只更新二进制或代码，重启服务：

```sh
launchctl kickstart -k gui/$(id -u)/local.openwhisker.daemon
```

如果修改了 plist，先卸载再加载：

```sh
launchctl bootout gui/$(id -u)/local.openwhisker.daemon
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/local.openwhisker.daemon.plist
```
