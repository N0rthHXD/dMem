# 检索与排序

检索把一次自然语言查询转化为一个有界、可复现的上下文包。它由
`internal/gateway/retrieval` 编排，`internal/retrieval` 提供纯版本化契约。

## 流水线

```text
PreparePrimary -> ExpandGraph -> RankingInput -> Rank
  -> cursorStart -> Materialize -> Select -> Render
```

1. **PreparePrimary**：构建查询计划、编译关键词查询、执行主召回通道。
2. **ExpandGraph**：在策略允许时解析查询实体并扩展有界图谱路径。
3. **RankingInput**：汇总候选特征，包括时间、质量、反馈与图谱信号。
4. **Rank**：确定性特征排序器计算最终分数。
5. **cursorStart**：校验并定位游标。
6. **Materialize**：重新校验结果的 scope、当前修订、生命周期、有效时间与预算。
7. **Select**：去重、多样化与预算化上下文选择。
8. **Render**：生成上下文包与执行元数据。

## 查询分析

查询计划由 `BuildQueryPlan` 构建，输出问题类型、图谱策略与所需能力。查询意图
（`intent`）取值为 `auto`、`lookup`、`temporal`、`entity`、`relational`、
`multi_hop`、`procedural`、`exploratory`、`verification`。请求只表达意图与
预算，生产默认值来自配置。

实体解析属于查询理解，而不是独立召回通道。自然语言查询中的实体提及在检索前被
确定性解析：按最长活跃别名匹配，结合词边界与逆频率先验。无歧义的解析可以通过
直接提及或有界类型化路径增加候选。

## 主召回通道

关键词与语义是相互独立的主召回信号：

- **关键词**：SQLite FTS5。查询文本被编译为 FTS 表达式（`keyword-compiler-v2`），
  而不是直接透传用户输入。
- **语义**：embedding 以紧凑 `float32` BLOB 存储在权威表 `memory_embeddings`
  中，通过静态链接的 sqlite-vec 做精确余弦检索。语义执行需要已配置的 embedding
  provider 和可用的语义索引。

权威、scope、策略、身份与有效性过滤在 top-k 之前执行。`semantic_index_version`
是活跃语义语料的单调 generation，而不是不可变向量快照：投影资格与兼容向量
成员关系的变化会在事务内推进它。语义候选在进入排序前经过校准：使用绝对分数
下限（默认 0.35）与相对 top-score 间距（默认 0.08），取代固定分数地板
（`semantic-calibration-v1`）。

## 图谱通道与门控

图谱使用查询计划选择的四种策略之一：

| 策略 | 行为 |
| --- | --- |
| `off` | 不进行图谱扩展。 |
| `features_only` | 只用图谱特征为已有候选加分，不新增候选。 |
| `direct` | 允许直接实体提及增加候选。 |
| `bounded_path` | 允许有界类型化路径增加候选。 |

图谱只使用就绪且兼容的快照与已校准的主召回种子。种子来自校准语义 top 10 与
关键词 top 10 的去重并集，最多 15 条修订。扩展最多两跳，并带有 hub 惩罚的
有界扇出。图谱条目总数上限为 256。

仅由图谱产生的候选使用单独的准入公式，而不是直接套用主召回分数。实体事实与
记忆关系是内部图谱信号（`graph-scoring-v3`），不是独立通道。显式能力覆盖只
限于内部诊断与调试，Agent HTTP 契约不暴露这些覆盖项。

## 特征排序

排序器计算：

```text
score = 加权主召回分数 + 有界图谱提升 + 反馈信号 - 惩罚项
```

默认权重（`ranking-weights-v1`）为：语义 1.0、关键词 0.35、精确匹配 0.15、
时间 0.10、质量 0.10、信号一致性 0.05。权重是起点而非固定真理，可在配置中调整。

排序保留原始分数与校准分数。受保护的精确主召回匹配不会被图谱提升挤出；不可能
或不兼容的时间区间会被过滤。反馈信号来自可重建聚合：保留 90 天窗口内每个调用方
与修订的最新反馈，将使用分数钳制到 `[-1, 1]`（算法版本 `v2`，最小支持度 3，
半衰期 45 天），并且不能绕过 scope、当前修订、生命周期、时间或预算检查。反馈
属于被引用的修订，不会被子修订继承。

## 上下文选择

`SelectContext`（`context-selection-v2`）在硬预算内做预算化覆盖选择：

- 使用并查集对重复候选聚类并选择代表项；
- 使用 MMR 效用平衡相关性与多样性；
- 强制结果数、token 与字符预算（默认 20 条结果、4000 token、16000 字符，
  token 格式版本 `context-tokens-v1`）。

过时聚合默认被排除在召回之外；显式调试包含会报告过时原因与溯源，而不是带警告
返回。请求可以通过 `skip_diversification` 关闭多样化。

## 游标与分页

游标版本为 `cursorVersion = 1`。游标绑定查询哈希与七个身份：schema、查询计划、
成员关系（语料 generation）、排序、图谱、上下文与反馈；此外还绑定结果上限、
字符上限、参考时间与上一次分数。

generation 变化会使旧游标失效，而不是在已变化的成员关系上继续。分页约定为
`limit`（1–200，默认 50）与不透明 `cursor`，列表响应形状为
`{"items": [...], "next_cursor": ...}`。

## 降级与结果校验

能力门控决定哪些通道实际执行。当语义 provider 未配置、语义索引不可用、图谱索引
generation 不存在或策略不允许时，响应会报告请求与实际执行的能力及降级原因，
而不是静默声称完成了完整检索。

在结果返回之前，dMem 会重新检查其 scope、当前修订、生命周期状态、有效时间以及
结果/token/字符预算。被拒绝的上下文候选以有界 id/reason 记录返回。

## 遥测与 explain

每次召回都会追加一条有界、无内容的决策遥测记录（`query_telemetry`、
`context_decisions`）：查询计划类型与身份、请求与实际执行的能力、各来源候选
数量、门控拒绝、分数摘要、图谱种子/边/路径/提升计数、重复聚类、上下文 token
与字符预算、排序/上下文/反馈身份、降级原因与各阶段延迟。遥测永不记录查询文本、
记忆正文、证据片段或 provider 载荷。

`explain` 模式返回每个结果的特征贡献、图谱路径、证据以及选择或拒绝原因，并
强制与正常召回相同的 scope、策略与敏感度规则：它不能暴露被拒绝的 scope、敏感
内容或隐藏证据。

## 算法身份

| 阶段 | 身份 |
| --- | --- |
| Schema | `schema-v1`（`schema_version = 1`） |
| 处理 | processor `builtin-noop-v1`，provider 契约 `processing-structured-v1` |
| 整合 | `consolidation-v1`（哈希开启，余弦 0.90/0.94，32 候选） |
| Embedding 文档/查询 | `embedding-document-v2` / `embedding-query-v1`，基于 `canonical-document-v1` |
| 语义校准 | `semantic-calibration-v1`（floor 0.35，margin 0.08） |
| 关键词编译器 | `keyword-compiler-v2` |
| 谓词注册表 | `predicate-registry-v2` |
| 图谱抽取/评分 | `graph-typed-v1` / `graph-scoring-v3`；`auto-review-v1`、`entity-graph-statistics-v2` |
| 查询计划/时间 | `query-plan-v2` / `temporal-features-v1` |
| 排序特征/权重 | `ranking-features-v1` / `ranking-weights-v1` |
| 上下文选择 | `context-selection-v2` / `context-tokens-v1` |
| 反馈 | `v2`（90 天窗口，最小支持度 3，半衰期 45 天） |
| 质量/重复 | `memory-quality-v1` / `duplicate-cluster-v1` |

## 容量边界

整合最多接受 32 个候选。结构化抽取每个事件限制为 32 个候选、4096 个内容字符、
100 个证据片段与 64 个实体引用。图谱条目限制为 256，种子限制为校准语义 top 10
加关键词 top 10 去重后最多 15 条修订、最多两跳。上下文限制为 4000 token、
16000 字符与 20 条结果。反馈先验上限为全局 0.001、查询条件化 0.0005。

## 已知限制

- 精确余弦检索仍是唯一的语义实现。ANN 扩展点与精确重排已被测试，但由于实测
  规模不需要，ANN 尚未实现。
- 学习式重排器仅用于诊断；生产使用确定性排序器及其回退。
- 校准提升未在固定合成开发集上达标，因此权重保持确定性默认值而非拟合值。
- provider 默认禁用。关键词召回、直接记忆、查询规划与降级图谱工作可在无密钥、
  无网络时离线运行；结构化抽取与图谱权威需要配置 provider。
- 在图谱索引 generation 存在之前，图谱健康状态报告为不可用，排序退化为
  `features_only` 或 `off`，而不返回图谱候选。
