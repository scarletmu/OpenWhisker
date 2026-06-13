# 30-design

子系统与模块的详细设计：各文档描述**当前态**，不带阶段编号，不维护"待实现 / 决议 / PR 边界"这类过程信息。整体架构边界见 [`20-architecture/`](../20-architecture/)；对内对外契约见 [`40-api/`](../40-api/)。

## 本区文档

- [scheduler.md](scheduler.md)：只读定时 Skill 调度（cron + 外部信息源 + outbox）。
- [agent-tooling.md](agent-tooling.md)：agent 工具调用 runtime（多轮 ReAct + 受限工具集 + budget 守卫）。
- [inbox-enrichment.md](inbox-enrichment.md)：raw 落盘后异步 enrich，在已有 tag 词表里定位归属。
- [memory-recall.md](memory-recall.md)：跨调用方共享的只读记忆召回服务。
- [im-intent-router.md](im-intent-router.md)：IM 入站消息到结构化意图的归一化中间件。
- [frontmatter-parsing.md](frontmatter-parsing.md)：统一的 frontmatter 解析包设计。
- [tool-driven-capture.md](tool-driven-capture.md)：工具驱动捕获的设计稿（走法 A，未落代码）。
- [im-quick-capture.md](im-quick-capture.md)：IM 入口重定位为零摩擦异步速记收件箱的设计稿（未落代码）。
- [matrix-adapter.md](matrix-adapter.md)：Matrix 适配器实践，含私有非 E2EE homeserver 接入与服务端拓扑。

完整导航见 [`docs/README.md`](../README.md)。
