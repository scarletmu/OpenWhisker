# 40-api

对内对外契约：LLM provider 请求/响应契约、vault note 输出 schema、捕获数据结构与消息格式。OpenWhisker v1 暂无对外 HTTP/RPC API；本区收敛"Schema / Contract"类文档。

## 本区文档

- [capture-bucket.md](capture-bucket.md)：active raw bucket、pending clarification、raw append 行为规格。
- [intent-router-model-contract.md](intent-router-model-contract.md)：Intent Router 小模型 classifier 的请求/响应契约。
- [knowledge-expander-model-contract.md](knowledge-expander-model-contract.md)：Knowledge Expander 的 LLM provider 契约。
- [knowledge-draft-schema.md](knowledge-draft-schema.md)：medium-risk Raw Organizer 落盘的 Knowledge draft 与 Raw/Processed 形态约束。
- [proposal-note-schema.md](proposal-note-schema.md)：high-risk plan 经 approve 生成的 proposal note 形态与生命周期。

完整导航见 [`docs/README.md`](../README.md)。
