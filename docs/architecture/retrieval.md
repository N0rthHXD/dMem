# 检索与排序

> **状态**：本文描述当前检索实现。Phase 3 已把 Agent Tool search hints 纳入 FTS
> 文档并在无 provider 时形成完整基础召回，semantic 不可用时明确降级为
> keyword/graph。目标架构要求 Agent Tool Route 在未配置 dMem embedding provider
> 时，以 FTS、search hints、实体和图谱形成完整基础召回，并显式报告 semantic
> 降级。参见
> [Agent Tool 与 dMem Provider 双路线架构](agent-tool-and-provider-routes.md)。

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

- **关键词**：SQLite FTS5。查询文本被编译为 FTS 表达式（`keyword-compiler-v4`），
  而不是直接透传用户输入。`memory_fts` 有两个受索引字段：`content` 保存
  canonical retrieval document，`hints` 保存受 revision 约束的 Agent Tool
  search hints 投影（`canonical-search-hints-v1`）。字段来源保持区分：hint 永不
  进入 canonical document、embedding 输入或 exact-match 判定，explain 只报告
  `content` / `hints` 字段名而不返回 hint 文本。
  编译出的 FTS 表达式与**字面子串 fallback 并联执行**：trigram tokenizer 无法
  表示的 1–2 rune 词（CJK unigram/bigram 与短拉丁词）由 `display_content` /
  hint 的字面匹配召回，两条通路在同一快照上运行、按 revision 去重合并，FTS 的
  BM25 顺序优先。字面通路没有 BM25，因此按编译词的命中覆盖数排序，再按
  `updated_at` 与 memory id 决胜。二者不能互斥：一旦以「FTS 表达式为空」作为
  fallback 的唯一开关，含拉丁词的中英混合查询会丢掉其中的中文词。
  当编译结果含 Required 项（acronym、identifier、引号短语）且该表达式返回 0 行
  时，同一快照、同一 limit 下最多追加一次**放宽查询**（`RelaxedMatch`）：Required
  降级为 Optional，把部分匹配候选交给排序而不是返回空结果。放宽命中不获得
   exact feature 与 exact-primary 保护，并通过内容无关的 `keyword_relaxed`
  telemetry 计数；放宽表达式与原表达式相同（无 Required 项或无可降级项）时不再
  发起第二次查询。
- **中文精确层**：trigram 不尊重词边界，OR-of-trigrams 会召回仅与查询某个片段
  共享 3 字窗口的记忆（「数据库连接池的负责人」也会召回「数据库连接池：暂停
  两周」）。编译器用封闭类中文虚词把查询切成**内容片段**
  （`CompileQuerySegments`，无词典、无模型）。当查询确实被切分（≥2 个片段，且至少
  一个 rune 落在片段之外）时，只有覆盖全部片段的候选进入结果集
  （`preciseKeywordSet`）；若没有候选覆盖全部片段，则保留完整宽召回，**召回不会
  塌陷为 0**。无法切分的查询（无双片段边界，如「上海产品评审会议」）行为不变。
- **语义**：embedding 以紧凑 `float32` BLOB 存储在权威表 `memory_embeddings`
  中，通过静态链接的 sqlite-vec 做精确余弦检索。语义执行需要已配置的 embedding
  provider 和可用的语义索引。

### 向量空间身份

每个向量都属于一个显式的向量空间身份 `compatibility_identity`，它由
provider/model 标记、配置的 `dimensions`、canonical document 格式、query 格式与
canonical document 结构版本共同哈希而成。身份不是“同一个模型”的近似：它决定哪些
向量可以一起比较。存储、索引元数据、召回查询和持久 embedding 作业身份都绑定该值，
因此 provider、model、维度或任一格式版本变化都会选定一个新空间，旧向量既不参与
查询也不进入新的活跃索引，绑定旧身份的 `embed_memory`/`rebuild_semantic` 作业会被
取代并在新身份下重新调度。维度变化与 model 变化同等对待，因为它同样改变向量空间。
没有 embedding provider 时不产生向量空间身份，语义通道按下面的降级规则报告。

关键词排序使用列加权 BM25（content 权重 1.0，hints 权重 0.25），因此 hint
召回不会仅凭关键词特征压过 canonical content 匹配；确定性排序进一步保证任何
canonical content 匹配排在 hint-only 匹配之前。hint-only 候选在上下文选择中
获得有界 relevance（0.5），高于选择效用下限、低于完整 canonical 覆盖，因此它
既可被返回，也不会替代 exact canonical 匹配。

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

`SelectContext`（`context-selection-v3`）在硬预算内做预算化覆盖选择：

- 使用并查集对重复候选聚类并选择代表项。重复证据只能由**共享的来源片段**
  （event + offset）判定；只有 event、没有 offset 的批量提交记忆是不同记忆，
  不会塌缩成一个聚类。
- 使用 MMR 效用平衡相关性与多样性。效用差在
  `selectionTieEpsilon`（0.001，低于最小多样性权重）以内时按确定性聚类序
  （exact 保护优先，然后按 rank）决胜，避免内容长度（cost 项）单独把排名更
  好的候选挤出。
- 无校准 semantic 通道执行时（无 provider 部署），keyword 通道命中获得
  `0.5 + 0.15 × 归一化关键词分` 的选择相关性下限：至少进入选择，且名次带内
  排序不会触发 MMR 的 score-discontinuity 停止。semantic 执行时该下限关闭，
  校准分数保持权威，hybrid 排序不回退。
- 强制结果数、token 与字符预算（默认 20 条结果、4000 token、16000 字符，
  token 格式版本 `context-tokens-v1`）。

过时聚合默认被排除在召回之外；显式调试包含会报告过时原因与溯源，而不是带警告
返回。`skip_diversification` 关闭多样化只是**内部存储层**的召回参数，用于诊断与
测试；Agent Tool v1 契约不暴露任何内部通道或流程开关。

## 游标与分页

游标版本为 `cursorVersion = 1`。游标绑定查询哈希与七个身份：schema、查询计划、
成员关系（语料 generation）、排序、图谱、上下文与反馈；此外还绑定结果上限、
字符上限、参考时间与上一次分数。

generation 变化会使旧游标失效，而不是在已变化的成员关系上继续。

分页约定按调用面区分：

| 调用面 | `limit` | 默认 | 响应形状 |
| --- | --- | --- | --- |
| `dmem_recall`（Agent Tool v1） | 1–100 | 10 | `{operation_id, memories, degraded_capabilities, next_cursor}` |
| 内部 `gateway.PageRequest`（管理/列表查询） | 1–200 | 50 | `{"items": [...], "next_cursor": ...}` |

Agent 工具面不暴露内部管理列表，因此不返回 `items` 包裹形状。

## 降级与结果校验

能力门控决定哪些通道实际执行。当语义 provider 未配置、语义索引不可用、embedding
维度或模型变化使已发布索引属于另一个向量空间、图谱索引 generation 不存在或策略不
允许时，响应会报告请求与实际执行的能力及降级原因，而不是静默声称完成了完整检索。
没有执行 semantic 时，结果不会返回 semantic score，也不会把 keyword/graph 结果标记
为 semantic、hybrid 或 vector 召回；Agent Tool Route 通过 `degraded_capabilities`
报告 `semantic` 降级。embedding provider 故障或旧的向量空间都只降级语义通道，召回
整体继续成功。

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
| 处理 | 启用时 processor identity `openai-compatible/<model>`，provider 契约 `processing-structured-v1`；禁用时为 `local-deterministic-v1` |
| 整合 | `consolidation-v1`（哈希开启，余弦 0.90/0.94，32 候选） |
| Embedding 文档/查询 | `embedding-document-v2` / `embedding-query-v1`，基于 `canonical-document-v1` |
| 向量空间 | `compatibility_identity` = provider/model 标记 + `dimensions` + 文档/查询格式 + canonical document 版本，哈希为 `sha256:` |
| Embedding 作业身份 | `<provider>/<model>#<compatibility_identity>` |
| 语义校准 | `semantic-calibration-v1`（floor 0.35，margin 0.08） |
| 关键词编译器 | `keyword-compiler-v4` |
| Search hints 投影 | `canonical-search-hints-v1`（`memory_fts.hints`） |
| 谓词注册表 | `predicate-registry-v2` |
| 图谱抽取/评分 | `graph-typed-v1` / `graph-scoring-v3`；`auto-review-v1`、`entity-graph-statistics-v2` |
| 查询计划/时间 | `query-plan-v2` / `temporal-features-v1` |
| 排序特征/权重 | `ranking-features-v1` / `ranking-weights-v1` |
| 排序顺序 | `hint-canonical-v1`（canonical content 匹配先于 hint-only） |
| 上下文选择 | `context-selection-v3` / `context-tokens-v1` |
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
- provider 默认禁用。关键词召回、canonical content 与 search hints、直接记忆、
  查询规划与降级图谱工作可在无密钥、无网络时离线运行；结构化抽取与图谱权威需要
  配置 provider 或经审核的图谱权威。Agent Tool Route 的空 provider 配置是完整
  可用产品路径，而非仅存储降级模式：固定 Agent Tool 语料（同义表达、实体别名、
  时间、偏好、决策和程序性记忆）在无 embedding 时通过
  `internal/storage/testdata/agent-tool-recall-corpus-v1.json` 门禁，要求 macro
  Recall@10 ≥ 0.80、canonical exact recall = 1.0、兄弟 scope 污染 = 0。语料含中文
  文档与查询（纯 CJK、1–2 rune 短词、中英混排），并由
  `TestAgentToolRecallEveryQueryHitsTopK` 补充逐查询召回必须为 1.0 的硬断言，
  防止某个中文查询被 macro 平均值吸收。
- 中文关键词召回以字面匹配为基础：精确层消除了跨词边界的误召回，但 trigram
  tokenizer 仍不能桥接同义改写，无词典切分也无法处理词序变化（「评审会议产品」
  vs「产品评审会议」）；provider 未配置时也没有 hint 投影可依赖。进一步提升需要
  引入真正的分词器并做索引级改造（全量重建 + `index_version` 递增），目前不作为
  默认路径。
- 实体名称、别名与 typed graph expansion 不依赖 embedding，但需要已构建的
  entity/relation/graph 索引；默认 `features_only` 图谱策略只加分不新增候选，
  新增候选需要显式放宽图谱策略。
- 在图谱索引 generation 存在之前，图谱健康状态报告为不可用，排序退化为
  `features_only` 或 `off`，而不返回图谱候选。
