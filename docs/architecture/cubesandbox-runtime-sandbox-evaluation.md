---
title: CubeSandbox Runtime Sandbox Evaluation
type: architecture-evaluation
status: evaluation
created: 2026-05-13
updated: 2026-05-13
language: zh-CN
project:
  - OpenWhisker
  - Obsidian Knowledge Vault
  - LLM Wiki Agent
topic:
  - sandbox-runtime
  - CubeSandbox
  - agent-tool-execution
principle:
  - plan-before-write
  - executor-not-free-shell
  - human-in-control
  - local-first
---

# CubeSandbox 运行沙箱评估

## 0. 文档定位

本文记录 CubeSandbox 作为 OpenWhisker 运行沙箱候选方案的评估结论。

本次目标只是留下架构判断，不代表已经决定进入开发实施，也不改变 Phase 1 / Phase 2 的已完成范围。

评估基于截至 2026-05-13 的公开文档和仓库说明。CubeSandbox 仍处在快速演进阶段，后续真正接入前需要重新核对版本、API、部署要求和安全默认值。

## 1. 结论

CubeSandbox 适合作为 OpenWhisker 的 **Agent 工具运行时沙箱**。

它不应该替代 `VaultExecutor`，也不应该获得真实 Obsidian vault 的直接写入权。

推荐边界是：

```text
Wiki Agent Host
  -> CubeSandbox 临时工具执行
  -> 结构化结果 / artifacts
  -> VaultPlan
  -> Policy Check / Diff / Risk Classification
  -> Human Approval or Low-Risk Auto-Allow
  -> VaultExecutor
  -> Local Vault Files
```

换句话说，CubeSandbox 可以承接 LLM workflow 中需要临时执行工具的部分，例如网页处理、代码运行、格式转换、解析、批量文本分析和可丢弃实验环境。

但它的输出只能成为 agent reasoning 或 `VaultPlan` 的输入，不能绕过 OpenWhisker 的写入链路。

## 2. 为什么适合 Agent 工具运行时

CubeSandbox 的定位与 OpenWhisker 的安全边界有较高契合度：

- 它基于 KVM MicroVM，为每个 sandbox 提供独立 Guest OS kernel，适合隔离 LLM 触发的工具执行。
- 它兼容 E2B SDK，可以作为后续 agent tool runtime 的可替换后端，减少专有 sandbox API 绑定。
- 它使用 template 模型，可以把 Python、Node、browser automation、文档解析等工具预装到固定运行环境中。
- 它通过 `envd` 暴露命令执行和文件读写接口，适合上传临时输入、执行受控命令、取回结构化输出。
- 它的网络隔离能力由 CubeVS / eBPF 承担，方向上适合为 agent 工具执行设置更细粒度的 egress policy。

这些能力能补上 OpenWhisker 当前设计中的一个空位：

```text
LLM 不拿宿主 shell，但某些 workflow 仍需要临时工具执行环境。
```

CubeSandbox 可以作为这个临时执行环境，而不是作为 vault 写入执行器。

## 3. 推荐集成边界

推荐新增的长期抽象是 `SandboxRuntime`，位置在 `Wiki Agent Host` 侧，而不是 `VaultExecutor` 侧。

`SandboxRuntime` 的职责：

- 创建短生命周期 sandbox。
- 上传 raw input、上下文片段和临时文件。
- 执行固定工具命令或受限脚本。
- 读取 stdout、stderr、`result.json` 和 artifact 清单。
- 执行超时、输出大小、文件数量和清理策略。
- 把结果交还给 planner，由 planner 生成或修订 `VaultPlan`。

`SandboxRuntime` 不负责：

- 写 vault 文件。
- 执行 `VaultOperation`。
- 判断 plan 是否可以自动通过。
- 读取 OpenWhisker SQLite 数据库。
- 持有用户长期 secrets。
- 调用 Obsidian CLI 或 Headless Sync。

这个边界保持 OpenWhisker 的第一架构约束：

```text
LLM reasoning -> structured plan -> policy and approval -> deterministic execution
```

## 4. 不推荐的边界

### 4.1 不建议把整个 OpenWhisker 主服务放入 CubeSandbox

OpenWhisker Core 需要管理 durable job state、approval state、operation log、outbox 和 executor 生命周期。

这些职责需要稳定、可观察、可恢复的本地服务边界。把整个主服务放入短生命周期 sandbox，会让持久化、升级、故障恢复和本地资源访问变复杂。

CubeSandbox 更适合承载可丢弃的工具执行任务，而不是承载系统事实来源。

### 4.2 不建议把真实 vault 挂载给 CubeSandbox

把真实 Obsidian vault 挂载进 sandbox 会削弱当前最重要的安全边界：

- path guard 可能被额外文件系统语义绕开或复杂化；
- LLM 触发的命令可能直接修改 vault；
- diff、approval、`before_hash` 和 operation log 可能被旁路；
- Obsidian Sync conflict 的责任边界会变得不清晰。

如需让 agent 使用 vault context，应由宿主侧选择必要的只读片段传入 sandbox，而不是挂载整个 vault。

### 4.3 不建议让 CubeSandbox 执行 approved VaultOperation

即使某个 `VaultPlan` 已经通过 approval，真正写入仍应由 `VaultExecutor` 执行。

原因是 `VaultExecutor` 不只是“写文件”，它同时承担：

- path guard；
- allowed operation enforcement；
- vault lock；
- preflight check；
- `before_hash` guard；
- conflict state；
- operation log；
- sync-aware execution。

这些是 OpenWhisker 的核心写入控制面，不应下放给通用工具 sandbox。

## 5. 安全影响

引入 CubeSandbox 后，OpenWhisker 的安全模型应增加一层：

```text
Untrusted tool execution boundary
```

但这层边界不能替代现有的 policy 和 executor。

最低安全要求：

- Cube API 只能暴露在内网或本机可信网络。
- 必须启用认证；CubeSandbox 文档说明未配置认证回调时默认允许请求。
- Sandbox 不接收宿主 vault path、`.obsidian`、`.git`、OpenWhisker SQLite、SSH key、API token 或个人配置目录。
- Sandbox 输出必须按不可信输入处理。
- Sandbox 结果进入 durable knowledge 前，仍需要 source traceability、risk classification、diff 和 approval。
- 对网络出口设置默认拒绝或最小允许策略，避免网页处理 workflow 变成任意外连能力。
- 对每次执行设置 timeout、CPU、memory、disk、stdout/stderr 和 artifact size 限制。
- 记录 sandbox request metadata，例如 `job_id`、source refs、template id、timeout 和结果摘要。

## 6. 部署判断

优先评估部署环境为 Linux 服务器 / NAS。

这个选择比 Mac 本机开发更匹配 CubeSandbox 的默认假设：

- x86_64 Linux；
- KVM 可用；
- Docker、QEMU 和镜像拉取能力；
- 常驻 Cube API / CubeMaster / Cubelet；
- 更容易做内网访问控制和资源配额。

Mac 本机更适合临时阅读和概念验证，不适合作为第一目标运行环境，因为 CubeSandbox 需要额外 Linux/KVM 或 VM 层。

云 VM 可以作为备选，但需要单独确认 KVM、nested virtualization 或 PVM 部署路径。

## 7. 未来 POC 验证项

后续如果决定进入开发，应先做最小 POC，而不是直接接入主链路：

1. 部署 CubeSandbox 到 Linux 服务器 / NAS。
2. 创建最小 template，基于 `cubesandbox-base`，保留 `envd`。
3. OpenWhisker 通过 E2B-compatible API 创建 sandbox。
4. 上传一份 raw input 和固定工具脚本。
5. 执行命令，生成 `result.json`。
6. 读取结果并销毁 sandbox。
7. 确认结果只能进入 planner，不能触发 vault write。
8. 验证 timeout、输出超限、无效 JSON、template missing、API unavailable 时的 job failure 行为。
9. 验证未配置认证时 OpenWhisker 拒绝启用 CubeSandbox adapter。

POC 验收标准：

- 不需要真实 Obsidian vault。
- 不修改任何 vault 文件。
- 不新增 `VaultOperation` 类型。
- 不改变 `VaultExecutor` 的职责。
- 所有 sandbox 产物都可追踪到具体 `WikiJob`。

## 8. 当前判断

CubeSandbox 是一个值得保留的候选运行时方向。

它最适合解决的问题是：

```text
如何让 LLM workflow 安全地运行临时工具，而不把宿主 shell 暴露给 LLM。
```

它不应该解决的问题是：

```text
如何写入 Obsidian vault。
```

OpenWhisker 的 vault 写入问题已经由 `VaultPlan / PolicyChecker / VaultExecutor` 架构处理。CubeSandbox 的正确位置，是在 plan 生成前或 plan 修订过程中的临时工具执行层。

## 9. 参考资料

- [CubeSandbox GitHub Repository](https://github.com/TencentCloud/CubeSandbox)
- [CubeSandbox Documentation](https://docs.cubesandbox.ai/)
- [CubeSandbox Quick Start](https://docs.cubesandbox.ai/guide/quickstart.html)
- [CubeSandbox Architecture Overview](https://docs.cubesandbox.ai/architecture/overview)
- [CubeSandbox Authentication](https://docs.cubesandbox.ai/guide/authentication.html)
- [CubeSandbox Bring Your Own Image](https://docs.cubesandbox.ai/guide/tutorials/bring-your-own-image.html)
