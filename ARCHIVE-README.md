# OpenWhisker 文档归档分支

本分支（`docs-archive`）是 OpenWhisker **开发过程文档的归档快照**，与 `main` 没有共享历史（orphan 分支），不参与主线开发。

## 为什么单独成支

`main` 上的 `docs/` 收敛为面向当前的精选参考集（架构概览、子系统设计、Schema / Contract、部署）。逐阶段的开发日志属于"过程产物"——它们记录了设计是怎么一步步演进到现在这样的，对追溯有价值，但放在公开主线会让 `docs/` 持续臃肿、且和当前态描述重复。因此把它们整体搬到这个独立快照分支。

## 这里有什么

- `docs/phases/` — Phase 1–8 的逐阶段文档（设计意图、范围边界、决议、验收、Review 问题），以及 `phase-4-matrix-test-feedback.md` 测试反馈、`phase-6-scheduler-skill-creator/` 的 demo 摘要与 JSON Schema。
- `docs/architecture/openwhisker-v1-review.md` — Phase 1–4C 的收官设计回顾。

各 Phase 当前态的设计已蒸馏进 `main` 的子系统文档：

| 归档文档 | `main` 上的当前态 |
| --- | --- |
| phase-5 read-only skill scheduler | `docs/architecture/scheduler.md` |
| phase-6 agent-driven vault knowledge response | `docs/architecture/agent-tooling.md` |
| phase-7 inbox enrichment | `docs/architecture/inbox-enrichment.md` |
| phase-8 memory recall | `docs/architecture/memory-recall.md` |
| phase-1~4 + v1-review | `docs/architecture/overview.md` + `docs/project-decisions.md` |

## 注意

这是**历史快照**，不随主线更新。文档内的状态标记、日期、commit hash 反映的是归档时点的情况，可能与当前实现不一致。要了解当前能力以 `main` 分支的 `docs/` 为准。
