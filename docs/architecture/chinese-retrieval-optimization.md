# 中文记忆检索效果分析与优化方案

> **状态**：方案文档，基于当前代码与测试基线整理。本文不改变当前线上算法；实施时必须按算法身份、索引 generation 和发布验收门禁逐步上线。

## 1. 结论摘要

当前中文检索的**字面召回能力较强，语义理解能力不足**：

- `memory_fts` 使用 SQLite FTS5 `trigram`，canonical content 与 Agent Tool search hints 分列索引；中文 1–2 字短词另走字面子串召回。
- 查询编译器会生成 CJK trigram，并用一组中文虚词切分内容片段；当候选覆盖所有片段时启用精确层，避免只命中一个三字窗口的误召回。
- 现有固定语料的中文、短词和中英混排用例均通过，但这些用例大多要求查询词字面出现在内容或 hint 中，未覆盖中文同义改写、繁简转换、词序变化和复杂问句。
- 查询计划目前只把中文识别为 `cjk` 语言；问题类型和关系谓词的确定性规则主要是英文规则。因此中文的“什么时候/谁负责/如何操作/是否完成”等问句通常会落入 `lookup`，不能稳定触发时间、关系、程序性或验证策略。
- 没有 embedding provider 时，系统不会凭关键词推断同义关系；有 provider 时也没有中文专用模型、中文校准集或语言分层阈值的默认配置。

因此，优化顺序应是：**先补齐中文评测与查询理解，再增加独立的中文词项索引和受控扩展，最后校准多语 embedding 与排序**。不建议直接把分词结果写入现有 canonical content 或 hints 字段。

## 2. 当前链路与中文影响

| 环节 | 当前实现 | 中文已解决的问题 | 仍然存在的上限 |
| --- | --- | --- | --- |
| 查询规范化 | `normalizeQuery` 压缩空白、统一全角/半角；`detectLanguage` 检测 Han/Hiragana/Katakana | 中英文混排和全角输入可进入同一链路 | 语言只有 `cjk`/`en` 两类，不能区分中文、日文、混合比例或繁简变体 |
| 关键词编译 | `CompileKeyword` 生成 CJK trigram；1–2 rune 进入 `ShortTerms` | 短词不会因 trigram 最小长度而丢失；中英混排不会关闭中文 fallback | trigram 不理解词边界；长查询最多受 `maximumKeywordTerms=32` 限制，且没有同义词、词序和改写处理 |
| 中文精确层 | `CompileQuerySegments` 用封闭类虚词切出内容片段；`preciseKeywordSet` 要求覆盖所有片段 | 可抑制“数据库连接池”与“负责人”只命中一侧造成的跨词误召回；无精确候选时保留宽召回 | 虚词表不是分词器；无虚词边界的查询仍是宽 trigram；分词歧义、词序变化和新词无法处理 |
| FTS 索引 | `memory_fts(content,hints)`，`tokenize='trigram'`；content BM25 权重 1.0，hints 0.25 | canonical 与 hint 来源隔离，hint-only 不会压过 canonical 命中 | 1–2 字 fallback 使用 `LIKE '%term%'`，大语料下可能扫描投影；FTS 不能直接表达中文词项边界 |
| 语义召回 | 可选 sqlite-vec 精确余弦；无 provider 时显式降级到 keyword/graph | provider 故障不会让基础召回失败；向量空间身份和重建机制完整 | 没有中文 embedding 基线、语言特定校准或 query/document 中文改写；无 provider 时无法召回纯同义改写 |
| 查询意图 | `classifyQuestion` 和 `predicateIntent` 主要匹配英文短语 | 英文 temporal/entity/procedural/relational 策略可确定触发 | 中文问句大多默认为 `lookup`，中文关系/时间谓词不会稳定驱动图谱或选择策略 |
| 实体/图谱 | 活跃 alias 最长匹配；CJK 不要求拉丁词边界；图谱策略有界且 fail-closed | 中文实体名/别名可直接解析，scope 隔离和图谱安全门控保留 | 中文关系词没有本地谓词映射；默认 `features_only` 不新增候选，中文实体召回依赖索引已就绪 |
| 排序/选择 | 确定性特征排序、MMR、多样性和 token/字符预算 | 结果可复现，scope、revision、生命周期和预算在 top-k 前后均校验 | 中文相关性特征仍主要来自字面 BM25；中文 token 统计与长文本片段价值没有专门校准 |

## 3. 可复现基线

在 Linux/CGO 环境按项目要求启用 `sqlite_fts5` 后执行：

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/storage ./internal/retrieval \
  -run 'Test(AgentToolRecall|KeywordRecall|KeywordSubstring|KeywordPrecision|CompileQuerySegments|CompileKeyword)' \
  -count=1 -v
```

当前结果：

- `TestAgentToolRecallQualityFixedCorpus`：18 个查询，`macro Recall@10 = 1.000`。
- `TestAgentToolRecallEveryQueryHitsTopK`：18/18 个查询逐查询 `Recall@10 = 1.0`。
- 固定语料中 7 个中文或中英混排查询（数据存储、存储、支付负责人、灰度 deploy、产品评审会议、数据库连接池上限、手册）全部命中。
- 中文短词、混合查询并行 fallback、虚词片段精确层、精确层无候选时的宽召回均通过。
- Agent Tool 兄弟 scope 污染为 0；当前语料的 canonical exact recall 也为 1.0。

这组指标只能证明当前**字面和权限边界**正确，不能证明中文语义检索质量。当前语料的局限包括：每个中文查询通常只有一个相关记忆、缺少 graded relevance、缺少同义/改写/词序/繁简对照、缺少无答案中文查询，也没有单独记录中文 p95 延迟。

## 4. 优化目标与验收门禁

新增 `chinese-retrieval-corpus-v1`，每条查询至少包含语言、意图、相关 revision、相关性等级、是否允许返回 hint-only，以及 scope。建议分层：

1. **字面层**：1/2/3+ 字、标点/空格、数字日期、姓名和中英混排。
2. **边界层**：虚词切分、歧义切分、新词、专有名词、同一词在不同词边界中的误召回。
3. **语义层**：同义词、近义改写、词序变化、口语/书面语、问句与陈述句互换。
4. **规范化层**：简体/繁体、全角/半角、大小写、数字与中文数字。
5. **意图层**：时间、关系、多跳、程序、验证、实体和探索性中文问句。
6. **安全层**：无答案查询、兄弟 scope、过时 revision、敏感和 contested 记忆。

发布门禁建议如下（分语言、意图和 query difficulty 报告，不能只看宏平均）：

| 指标 | 目标 |
| --- | --- |
| 中文 canonical exact Recall@5 | 1.00，保持现有门禁 |
| 中文 Recall@10（总体） | ≥ 0.95；每个意图层 ≥ 0.90；任何单条关键查询不得为 0 |
| 中文 nDCG@10 / MRR@10 | 以标注集建立基线，优化后至少提升 10%，不得牺牲 exact tier |
| 中文无答案误召回 | ≤ 现有 0.0 门禁；若引入扩展，必须单独报告 expansion-only 误召回 |
| scope 污染、过时 revision、敏感泄露 | 均为 0 |
| 关键词 p95 | 500 条记忆不超过现有 100 ms 门禁；10k/100k 规模分别压测并记录增长曲线 |
| 索引重建 | 记录 wall time、峰值内存、磁盘放大；不得突破现有重建门禁 |

## 5. 分阶段优化方案

### P0：建立中文可观测基线（优先级 P0）

**目标**：在不改变生产召回的情况下，知道问题发生在哪个通道。

- 新增中文分层语料和人工标注规范；相关性至少使用 `3=直接回答、2=部分回答、1=主题相关、0=无关`。
- 评测同时运行 keyword-only、semantic-only、hybrid、graph-off 四种模式，输出 Recall@k、MRR、nDCG、首个相关结果 rank、无答案误召回、scope 污染和 p50/p95 延迟。
- 在 content-free telemetry 中增加语言桶、意图桶、召回路径计数、扩展版本和索引版本；不记录 query、正文或 hint 文本。
- 将当前 18 条 Agent Tool 语料保留为回归集，另加中文困难集，避免用更宽松的平均值掩盖单条中文失败。

**验收**：基线报告可按 `language × intent × difficulty × channel` 重现；现有全部门禁保持不变。

### P1：补齐中文查询理解（优先级 P0）

修改 `internal/retrieval/query_plan.go`，但所有行为变化都必须升级版本（至少 `query-plan-v3`）：

- 将语言检测拆为 `zh`、`ja`、`cjk-mixed` 和 `en`（保留旧 `cjk` 的兼容映射，避免游标/校准身份静默复用）。
- 增加有界中文意图词表：时间（何时、什么时候、哪天、多久、上周）、关系（谁负责、属于、依赖、和…关系）、程序（如何、怎么、步骤、流程）、验证（是否、是不是、有没有）、实体/探索（谁是、介绍、有哪些）。
- 增加中文 predicate map，并将“负责/维护/属于/依赖/位于/参与/拥有/决定”等映射到已有封闭 predicate registry；不直接把任意用户词变成图谱谓词。
- 支持中文日期、相对时间和中文数字的解析；解析失败时仍保留原始字面查询，不因意图识别失败而清空召回。
- 规则顺序采用“显式调用 intent > provider enrichment（若获授权）> 本地中文规则 > lookup”并记录命中的规则版本。

**验收**：中文时间/关系/程序/验证查询的意图分类准确率达到标注集 ≥ 95%；错误分类不得导致 scope 或权限放宽。

### P2：增加独立中文词项召回（优先级 P0/P1）

不要把分词结果写入 canonical content 或现有 hints。建议建立独立派生投影（例如 `memory_fts_zh` 或 `memory_terms`），包含 `memory_id/revision_id/field/term`，同时保留现有 trigram 作为回退通道。

- 采用可审计的**领域词典 + 最大匹配/反向最大匹配 + 字符 bigram fallback**。词典来源可以是已确认实体 alias、memory kind/attribute 名称、人工维护领域词表和经过审核的 query expansion；不得让未审核 provider 输出直接改变权威事实。
- canonical content、search hints、实体 alias 分开生成词项，explain 只报告 `content/hints/zh_terms` 字段名。
- 候选生成分成 exact phrase、中文词项、trigram/短词三路；用受限 RRF 或 query-local 标准化融合，再进入现有确定性 ranker。词项扩展不能获得 exact-match 分数。
- 将当前 `LIKE '%term%'` 短词路径替换为有索引的 term lookup；保留短词覆盖数排序和 revision/scope/时间过滤。
- 对繁简、全角和常见数字变体生成受限规范化键；原文仍保留，规范化命中必须在 explain 中可追溯。

**Schema 约束**：仓库当前 Schema v1 没有迁移路径。新增派生表/列应定义 Schema v2，并通过逻辑 export/import 建立新数据目录；如果暂时不能升级 schema，应先实现内存/离线评测原型，不把临时投影写入生产表。索引重建必须递增 `index_version`、使旧游标失效，并支持失败后回退到现有 trigram。

**验收**：中文边界集 Recall@10 ≥ 0.95；词序变化和同义扩展集相比 P1 基线显著提升；无答案误召回、exact recall、scope 污染保持门禁；500 条记忆关键词 p95 ≤ 100 ms。

### P3：中文语义与混合排序（优先级 P1）

- 选择并固定一个中文/多语 embedding provider，分别评估中文、英文和混合 query/document；不要仅凭英文 deterministic provider 推断中文效果。
- 将中文规范化、query/document 格式和模型身份纳入现有 `compatibility_identity`；模型、维度或格式变化必须新建向量空间并重建，不能混用旧向量。
- 用中文标注集校准 `absolute_floor`、`top_margin`，必要时按 `language=zh`、memory kind、question type 建立分层阈值；默认不降低绝对地板来换取召回。
- 维持 semantic 不可用时的明确降级；provider 超时、维度错误或旧 generation 只关闭 semantic，不改变 keyword/graph 安全边界。
- 若增加 cross-encoder/reranker，只允许对受限候选窗口运行，并保留确定性 ranker 作为超时/异常回退；模型分数不得绕过 scope、lifecycle、sensitivity 和 revision 校验。

**验收**：中文语义集 Recall@10、nDCG 和 MRR 相对 P2 提升；hybrid 不得降低 canonical exact recall 或提高无答案误召回；semantic p95 遵守现有 250 ms、hybrid p95 遵守现有 300 ms 门禁。

### P4：实体、反馈与线上发布（优先级 P1/P2）

- 为中文 alias 增加规范化、别名冲突和同名消歧测试；图谱新增候选仍必须由显式 `direct/bounded_path` 策略允许，默认 `features_only` 不变。
- 反馈聚合按语言、意图、召回通道和扩展版本分桶，设置最小支持度；禁止单一中文查询或单一调用方快速改变全局排序。
- 先以 shadow 模式记录 P2/P3 的候选差异和延迟，再按 scope/调用方做灰度；每次改变绑定 query-plan、keyword compiler、index、embedding、ranking 和 context identity。
- 保留一键回退：关闭中文词项/扩展或 semantic，回到当前 `keyword-compiler-v3` + trigram + literal fallback 路径。

## 6. 风险与控制

| 风险 | 表现 | 控制措施 |
| --- | --- | --- |
| 分词错误 | 新词被切碎或错误合并 | 词典版本化、字符 bigram 回退、保留 trigram 并行召回、困难集逐条门禁 |
| 扩展过宽 | 无答案查询和主题噪声上升 | 扩展数量上限、来源分级、扩展不参与 exact 分数、独立 expansion-only 指标 |
| 短词扫描变慢 | `LIKE` 在大语料 p95 退化 | 使用 term/FTS 索引，压测 500/10k/100k 规模，保留查询预算 |
| 繁简归一化误合并 | 专名、代码或引用串被错误折叠 | 仅对普通中文词生成变体；代码、日期、quoted phrase 和 identifier 保持原文 exact |
| 模型漂移 | 中文 embedding 换模型后分数不可比 | compatibility identity、全量重建、旧 generation 隔离 |
| Schema v1 不可迁移 | 线上无法原地添加中文索引 | Schema v2 + logical export/import；未完成迁移前只做 shadow/离线评测 |
| 隐私泄露 | 遥测或 explain 写入 query/hint 正文 | 只记录 hash、桶、版本和计数；字段名 explain，不返回投影文本 |

## 7. 推荐实施顺序

1. **先做 P0 + P1**：补中文困难集、中文意图/谓词规则和可观测性；这部分无需 schema 改造，风险最低。
2. **再做 P2**：以独立中文词项投影解决边界、短词和受控扩展；先离线原型，确认 schema v2 与重建成本后再灰度。
3. **最后做 P3/P4**：选择中文/多语 embedding，校准 hybrid 排序，并接入反馈和 shadow 发布。

在 P2 完成前，不应把“当前中文固定语料 Recall@10=1.0”表述为中文语义能力达标；它只代表现有字面路径、短词 fallback、scope 隔离和当前精确层的回归基线。

