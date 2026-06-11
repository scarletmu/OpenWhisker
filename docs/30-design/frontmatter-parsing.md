# Frontmatter 解析统一设计

状态：已实现（签字范围，2026-05-29）。`internal/markdown` 已落地，6 个读取型解析点已收敛；3 个字节保真改写器与 `internal/enrich` 的 4 个只读 helper 见文末「实现记录」。

本文针对一个跨包重复问题：解析笔记 YAML frontmatter 的逻辑在仓库里被重写了多次，且各实现规则并不一致。目标是收敛到一个共享的 `internal/markdown` 包，同时保留各调用方原有的"策略"语义（严格校验、安全检查、字段级 diff）。

## 为什么要做

解析 frontmatter 的代码目前散落在 8 处，而且**没有两份的边界规则是一致的**。这不是普通复制粘贴，而是**有分歧的复制粘贴**：

- 在一处对解析做的加固/修复，不会自动惠及其他处。
- 这些解析器在**对 policy（安全相关）和 scheduler（输入校验相关）真正有影响的边界情况上互相打架**——一个 `\r\n` 文件、一个带 BOM 的文件、一个 `---` 收尾不规范的文件，在不同模块里会得到不同结果。

## 现状盘点

| # | 解析器 | 返回 | 起始标记 | 结束标记 | 异常处理 | 列表 |
|---|--------|------|----------|----------|----------|------|
| 1 | `core/adapter.go splitFrontmatter` | `map[string]string`+body | `TrimSpace(首行)=="---"` | 整行 `---` | 容忍（部分 map） | ✗ 仅标量 |
| 2 | `memory/frontmatter.go ExtractFrontmatterTags` | `[]string`（tags） | 字面量 `"---\n"`+去 BOM | 子串 `"\n---"` | 容忍（nil） | ✓ 流式+块式 |
| 3 | `vault/linkindex splitFrontmatter` | fm 字符串+body | 字面量 `"---\n"` | 整行 `---` | 容忍（空） | ✓（单独扫 `related`） |
| 4 | `policy/checker.go markdownFrontmatter` | fm+body+ok | 字面量 `"---\n"`+去 BOM，**不归一化 CRLF** | 子串 `"\n---"` | 容忍（ok=false） | ✓ 经 `frontmatterHasListValue` |
| 5 | `scheduler/registry.go splitFrontmatter`+`parseSimpleFrontmatter` | fm+body+**error** | 字面量 `"---\n"` | 整行 `---` | **严格——报错** | ✓ 丰富（列表/map/skill_config） |
| 6 | `core/plans.go` 内联（约 973 行） | 区域字符串 | 字面量 `"---\n"` | 子串 `"\n---"` | 容忍 | 不适用（区域扫描） |
| 7 | `policy/enrich.go splitFrontmatterFields` | 按 key 的**块** map | — | — | 容忍 | ✓（用于逐字节 diff） |
| 8 | `enrich/*.go` 临时 `const opener="---\n"` | — | 字面量 | 不定 | — | — |

危险的分歧点：

- **结束标记**：子串 `"\n---"` 会匹配 `\n----`／`\n--- x`，而整行匹配不会 → body 切分结果不同。
- **CRLF 归一化**：#4（policy）不做，所以 `\r\n` 文件在 policy 里与别处行为不同。
- **去 BOM**：只有 #2/#4 做。
- **严格度**：只有 #5 报错，其余一律容忍。

## 方案：`internal/markdown`（分层，而非一个巨型函数）

### 第 1 层 — 唯一的切分器（边界的唯一真相来源）

```go
// SplitFrontmatter 归一化 CRLF + 去 BOM，要求开头是 "---\n"，
// 以第一个独占一行的 "---" 闭合。返回内部块（不含围栏）、正文，以及是否找到。
func SplitFrontmatter(content string) (block, body string, found bool)
```

**统一边界规则（决策 #2 待签字项）**：采用"最严格-安全"的并集——

1. 先 `\r\n` → `\n` 归一化；
2. 去掉前导 BOM（`﻿`）；
3. 开头必须是 `"---\n"`；
4. 以**第一个独占一行**（trim 后等于 `---`）的行作为闭合，而不是子串 `"\n---"`。

这样迁移是在**收紧**而非放松——把现有最宽松的解析器向最加固的行为靠拢。

### 第 2 层 — 块之上的类型化访问器（覆盖所有读取模式）

```go
type Doc struct { /* 解析一次 */ }
func Parse(block string) Doc
func (Doc) Scalar(key string) string          // #1,#4 标量读取
func (Doc) Has(key string) bool               // #4
func (Doc) List(key string) []string          // #2,#3 —— 流式 [a,b] 和块式 "- a"
func (Doc) HasListValue(key, val string) bool // #4
```

### 第 3 层 — 各调用方保留"策略"，只丢掉"解析"

- **Scheduler**：把严格性保留成一层薄包装——`SplitFrontmatter` → `!found` 时返回它自己的 "frontmatter required" 错误 → 再做它自己的缩进／`skill_config` 校验。Scheduler 语义不变，解析共享。
- **`policy/enrich.go splitFrontmatterFields`**（按 key 的逐字节块 diff）**不在本次范围内**：它的目标是字段级 diff 保真度，不是读 key，强行并入会损伤其语义。

## 解决了什么

- 加固 frontmatter 处理只剩一个地方；
- 新阶段不会再长出第 9 个解析器；
- 结束标记／CRLF／BOM 的分歧收敛到一条规则；
- 净减约 −150 行，把现在的 8 处变成一个真正的收口点。

## 影响范围与风险

涉及 `core`、`memory`、`vault/linkindex`、`policy/checker`、`scheduler`、`enrich`。

**关键风险**：迁移到统一边界规则会改变那些"宽松"调用方的行为（依赖 `"\n---"` 子串闭合、或依赖 policy 不归一化 CRLF 的）。因此迁移**一次只换一个调用方，每个都用它自己的测试把关**，逐点核对行为差异，而不是 8 个一次性全换。Scheduler 的严格路径和 policy 的安全检查放到最后、最谨慎地迁。

## 推进顺序

1. 落地 `internal/markdown` + 单元测试（专门覆盖上表里互相打架的边界情况，把统一规则钉死）。
2. 先迁**容忍型读取方**：linkindex、memory tags、core 内联、adapter。
3. 最后迁 policy + scheduler，带上明确的 before/after 断言。

## 决策依据：vault 实测 + Obsidian Properties 规范

统一边界规则会"收紧"行为，因此先对照真实数据和权威规范评估收紧幅度。

**Vault 全量实测（196 篇 `.md`，2026-05-29）**

- `BOM = 0`，`CRLF = 0`：全库统一 LF、无 BOM。
- `tags` 100% 块式（`tags:\n  - x`），无流式 `[a, b]`。
- **0 篇** frontmatter 依赖"子串 `\n---` 能匹配、整行 `---` 匹配不到"的非标准收尾——每一篇都是独占一行 `---` 闭合。

**权威形状**

- `Meta/Templates/{Raw-Intake,Knowledge-Note,Interview-Note,Project-or-Travel}.md` 与真实 Knowledge/Raw 笔记：顶部 `---` 开、独占一行 `---` 闭、块式 `tags`/`source`/`related`/`aliases`。
- Obsidian Properties 本身要求 frontmatter 必须位于文件最顶部、首尾各一行独占的 `---`；不满足者不是合法 Properties。

**结论**：统一边界规则对当前 vault 100% 的笔记是 no-op，且正是 Obsidian 对合法 Properties 的要求。现有的宽松行为（子串闭合、policy 不归一化 CRLF、选择性去 BOM）是各解析器无意漂移出来的差异，无任何真实笔记依赖。**决定：全量收紧到统一规则。** scheduler 的"frontmatter 必填、禁止意外缩进"报错属其自身校验层，与边界规则无关，作为薄包装保留。

## 决策记录

- **决策 #2（已定，2026-05-29）**：采用上述统一边界规则，全量收紧。依据见上。进入实现按"推进顺序"执行。

## 实现记录（2026-05-29）

`internal/markdown` 已落地：`SplitFrontmatter` 为唯一切分器，`Doc` 提供 `Has` / `Scalar` / `List`（流式 + 块式）/ `HasListValue`（按 key）四个访问器，单元测试钉死了上表里互相打架的边界（四横线行、`--- x` 收尾、CRLF、BOM、流式 vs 块式）。

已收敛到统一规则的 **6 个读取型解析点**（对应现状盘点表 #1–#6）：

- `memory/frontmatter.go ExtractFrontmatterTags` → `SplitFrontmatter` + `Doc.List("tags")`。
- `vault/linkindex splitFrontmatter` + `extractFrontmatterRelated` → 委托统一切分 + `Doc.List("related")`，删除随之失效的 `splitInlineList`。
- `core/plans.go parseExpanderSourceTracePaths` → 边界改用 `SplitFrontmatter`，保留 `openwhisker` 嵌套块的专用游走。
- `core/adapter.go summarizeMarkdownDocument` → 用 `SplitFrontmatter` + `Doc.List("tags")`，删除本地 `splitFrontmatter`（scalar map 版）与 `extractFrontmatterTags`。
- `policy/checker.go validateKnowledgeDraftPayload` → 解析一次为 `Doc`，用 `Has` / `Scalar` / `HasListValue("tags", …)`；`markdownFrontmatter` 降为委托薄包装（`policy/enrich.go` 的 body-guard 共用，自动受益），删除 `frontmatterHasKey` / `frontmatterValue` / `frontmatterHasListValue`。
- `scheduler/registry.go splitFrontmatter` → 委托统一切分，严格层保留为薄包装：缺块时仍按"缺开头 / 缺收尾"返回两条作者诊断；`parseSimpleFrontmatter` 的缩进 / `skill_config` 校验不变。

**两处有意的行为收紧**（vault 实测对现有笔记均为 no-op）：

- `core/adapter.go` 的写入预览 Tags：旧 scalar-map 路径对块式 `tags:` 取到空串、一直返回空；现走 `Doc.List` 能正确提取块式标签（顺带修好的潜在缺陷）。
- `policy` 的 `RequiredDraftTags` 校验：旧检查不分 key（任意 `- tag` 行即通过），现按 `tags` 作用域，杜绝把必需标签塞进 `related` 蒙混过关。

**明确排除（字节保真改写器，与读取边界不同类）**：`policy/enrich.go splitFrontmatterFields`（#7，逐字段 diff）、`enrich/render.go findFrontmatterBounds`（prefix+updated+suffix 重拼，须保尾随换行）、`core/ingest.go rewriteRawBucketUpdated`（就地插 `updated:` 行）。这三者依赖精确字节偏移做改写，统一切分会丢偏移、损语义，按与 #7 相同的理由保留。

**后续项**：`internal/enrich` 的 4 个**只读** helper（`extractFrontmatterKV` / `bodyExcerpt` / `hasFrontmatterKey` / `hasFrontmatterListTag`）仍使用旧的子串 `\n---` 边界，未在本次签字的"推进顺序"内。它们是只读路径、低风险，可作为独立小步收敛（边界换 `SplitFrontmatter`，按键探测换 `Doc`），不与上面的字节保真改写器混淆。
