# Linux systemd 部署

本文档描述如何在 Linux 上把 OpenWhisker daemon 作为用户级 `systemd` 服务运行。优先使用 user service，因为目标 vault 通常在用户 home 下，daemon 以同一用户身份访问 vault、`.env.local`、`data/` 和 Matrix session 文件最直接。

## 文件

- 示例模板：`deploy/openwhisker.systemd.example.service`
- daemon 状态文件：`data/daemon-status.json`
- 运行日志：`journalctl --user -u openwhisker.service`
- 本机填实文件：`deploy/local/openwhisker.systemd.example.service`

## 准备

先准备 OpenWhisker home 目录，并确认里面有：

```text
<openwhisker-home>/
  bin/openwhisker
  .env.local
  data/
  logs/
```

然后复制模板到 git-ignored 的本地目录并填写占位符：

```sh
mkdir -p deploy/local ~/.config/systemd/user
cp deploy/openwhisker.systemd.example.service deploy/local/
```

必须检查并替换模板中的占位符：

- `<openwhisker-home>`：OpenWhisker 运行根目录。
- `<target-vault-path>`：目标 Obsidian vault 路径。

不要把真实 token、Matrix room id、私有网关地址或账号写进仓库内模板。运行时配置应放在 `<openwhisker-home>/.env.local`，该文件已被 git 忽略。

## 安装

```sh
cp deploy/local/openwhisker.systemd.example.service \
  ~/.config/systemd/user/openwhisker.service
systemctl --user daemon-reload
systemctl --user enable --now openwhisker.service
```

无人值守的 Linux NUC 需要开启 linger，让 user service 在没有登录会话时也能常驻：

```sh
loginctl enable-linger "$USER"
```

## 停止

```sh
systemctl --user stop openwhisker.service
systemctl --user disable openwhisker.service
```

## 查看状态

```sh
systemctl --user status openwhisker.service
bin/openwhisker daemon status --status-file data/daemon-status.json
bin/openwhisker scheduler status --db data/openwhisker.db --vault <target-vault-path> --vault-profile knowledge-vault
bin/openwhisker scheduler runs --db data/openwhisker.db
bin/openwhisker scheduler schedules list --db data/openwhisker.db --vault <target-vault-path> --vault-profile knowledge-vault
```

`daemon status` 只检查本地 status 文件和 PID 存活情况；scheduler 的真实执行记录仍以 SQLite 中的 runtime/run 记录为准。

## 查看日志

```sh
journalctl --user -u openwhisker.service -f
journalctl --user -u openwhisker.service --since today
```

如果需要文件日志，可以在本机 `deploy/local/` 里的 service 副本中增加 `StandardOutput=append:<openwhisker-home>/logs/openwhisker-daemon.out.log` 和 `StandardError=append:<openwhisker-home>/logs/openwhisker-daemon.err.log`。不要把真实路径写回 committed 模板。

## 更新服务

如果只更新二进制或代码，重启服务：

```sh
systemctl --user restart openwhisker.service
```

如果修改了 service 文件，重新加载 unit 后再重启：

```sh
systemctl --user daemon-reload
systemctl --user restart openwhisker.service
```

## System-wide 变体

默认模板是 user service。只有在 vault 和运行状态明确归属某个服务用户、且不依赖桌面用户会话时，才考虑 system-wide service：

1. 把填实后的 service 安装到 `/etc/systemd/system/openwhisker.service`。
2. 在 `[Service]` 下增加 `User=<service-user>`。
3. 把 `[Install]` 的 `WantedBy` 改成 `multi-user.target`。
4. 使用 `systemctl daemon-reload` 和 `systemctl enable --now openwhisker.service` 管理。

system-wide 形态仍应让 daemon 以低权限用户运行，不要用 root 直接访问 vault。
