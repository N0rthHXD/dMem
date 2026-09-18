# ADR 0035：结构化记忆与 v4 检索契约

- 状态：accepted
- 日期：2026-09-14
- 关联：取代 [0020](0020-versioned-rrf-ranking.md)、[0024](0024-authoritative-revision-relations.md) 的 `related_to` 权威关系、[0027](0027-feedback-and-ranking-v2.md) 的 ranking v2、[0033](0033-three-channel-retrieval-fusion.md) 的 `rrf-v3` 与显式 `channels` 抽象、[0028](0028-aggregate-memories-and-freshness.md) 的 stale aggregate 默认包含；把 [0032](0032-knowledge-graph-construction-and-retrieval.md) 的图模型收敛为 typed predicate registry；[0015](0015-no-schema-migration-in-phase-one.md)、[0005](0005-derived-indexes-are-rebuildable.md) 继续有效。

## 背景

dMem 尚未发布。此前实现把抽取、图构建、召回、重排和上下文选择当作相互独立的阶段：抽取产出的近重复记忆大量增殖，权威图把语义相似度当作 `related_to` 关系并自动确认，召回用等权 `rrf-v3` 把 keyword、semantic、graph 三路 rank 融合，graph seed 来自候选并可二次传播，最终结果按固定 top-k 直接拼接。LoCoMo 诊断显示 graph 对头排的伤害大于收益，且上下文缺少时间、speaker、证据与去重。

本 ADR 一次性定义替换契约，后续阶段（Phase 2+）在此之上实现算法，不再反复重塑基础模型。所有旧数据库、API、cursor、ranking、配置、export 与 checkpoint 兼容性被丢弃，不写 migration，不保留 deprecated 字段或 legacy 分支。数据库不兼容时初始化全新数据目录。

## 决策

### 1. 结构化记忆与不可变修订

记忆分为显式检索角色 `fact`、`event`、`preference`、`plan`、`procedure`、`scene`、`topic`、`timeline`；`scene`/`topic`/`timeline` 是带完整 provenance 的普通记忆，不是第二内容权威。

每个 `memory_revision` 在展示内容之外显式保存规范化检索字段：

```text
kind
display_content
canonical_content
subject_entity_ids[]
object_entity_ids[]
event_type
attributes{}
event_time_start / event_time_end / time_precision / time_source
valid_from / valid_until
source_speaker
language
sensitivity
specificity
confidence
```

`event_time_*` 描述事件发生时间，`valid_*` 描述事实/偏好有效区间，二者不得混同。provider 只提出 proposal，由确定性校验归一化类型、时间区间、实体引用与边界后才可发布。

### 2. 有序事件流与证据角色

原始事件进入 `source_stream`，在同一事务内分配稳定 `sequence`。处理上下文按 stream、sequence、timestamp、稳定 ID 排序，禁止用 UUID 顺序替代会话顺序。

修订引用多个证据 span，顺序显式稳定（event timestamp、event sequence、start offset、evidence ID），并保存证据角色：

- `direct`：来源直接陈述。
- `derived`：由多个来源推导。
- `temporal_anchor`：为相对时间提供锚点。
- `context`：有信息但不是充分支持。

可召回修订至少需要 `direct` 或合法的 `derived` 证据集合；仅 `context` 不能建立权威。

### 3. 合并先于增殖

创建记忆前，在目标空间内做有界 consolidation：canonical subject/kind 相等、规范化实体与属性重叠、valid-time 重叠、canonical-content hash 相等、高语义相似度、共享证据事件或来源邻近。分类为 `attach_evidence`、`refine`、`supersede`、`contest`、`create`、`reject`。确定性精确规则先行，provider 只能对未决候选提出 proposal。`cosine` 单独永不触发 supersede、contest 或删除。

### 4. 质量与重复投影

维护可重建的 memory quality 与 duplicate cluster 投影。质量分不能使不合格记忆变得合格，只影响候选优先级与上下文选择。重复簇由同一逻辑记忆、共享主证据、等价 canonical tuple、兼容时间的高相似度、aggregate-to-source provenance 定义，代表者选择确定性。

### 5. QueryPlan 与 intent/budget API

检索前由确定性规则生成带版本的 `QueryPlan`：normalized_query、language、exact_terms、resolved_entities、entity_ambiguities、time_expression、reference_time、question_type、target_memory_kinds、target_predicates、requested_scope、graph_policy、candidate_budget、context_budget。provider query analysis 可选且只能在 egress policy 与严格边界内增强。

`POST /v1/recall` 以 `intent` 与 `budget` 为主抽象：`reference_time`、`intent`、`max_results`、`max_context_tokens`、`include_evidence`、`explain`。内部通道/特性 override 仅作为明确命名的 diagnostic/debug 契约存在，生产默认来自 query planning。响应默认返回 selected context package 与 execution metadata；原始候选与拒绝原因仅在调试时显式请求。

### 6. 独立 primary recall 与校准

keyword 与 semantic 是独立 primary 通道。keyword 使用单一 field-aware compiler，保留实体、标识符、日期、数字、引号短语、acronym、短专有名词，返回 raw BM25、matched fields、exact flags、term coverage 与一个确定性 tie-break。semantic 从 canonical content + 紧凑结构化字段构建带 kind prefix 与版本化格式的 embedding；保留 raw similarity 并按 embedding identity、language、kind、query type 校准，用绝对 floor + top-score margin 取代固定 `0.2` 阈值。temporal 作为 primary 特征，在 top-k 前过滤不可能区间。

### 7. Gated typed graph

权威图只包含 typed、有方向、有证据的知识：`entity --predicate--> entity/typed literal`、事件 participant/time/place、revision `supports/derived_from/contradicts/supersedes`。删除 `related_to` 及其自动确认；语义相似度是派生检索特征，绝不作为知识关系存储或遍历。

predicate registry 定义 subject/object types、literal type、direction、inverse、cardinality、transitivity、temporal policy、allowed path positions、auto-confirm policy；provider 不能定义自由 predicate。

query plan 选择 graph policy：`off`、`features_only`（默认）、`direct`、`bounded_path`。graph seed 仅限校准 semantic top 10 与 keyword top 10，去重后至多 15 个 revision；弱候选、相似度/graph/relation 扩展候选不得成为 seed。graph-only 候选必须具备 query plan 的 direct/bounded_path、无歧义实体解析、达到 policy 阈值、直接证据、且不是仅由 revision relation 产生。graph score 保持数值（predicate intent × entity confidence × fact confidence × evidence strength × temporal compatibility × hub penalty），经 feature ranker 保留置信度并封顶。

### 8. 确定性特征重排

以版本化 weighted feature ranker 取代 `rrf-v3`。初始公式：

```text
primary =
    1.00 * semantic_calibrated
  + 0.35 * keyword_calibrated
  + 0.15 * exact_match
  + 0.10 * temporal_match
  + 0.10 * memory_quality
  + 0.05 * primary_agreement

graph_boost = graph_gate * min(0.10 * primary, 0.08 * graph_calibrated)
penalty     = 0.15 * duplicate_risk + 0.10 * aggregate_overbreadth + 0.10 * stale_or_contested
final       = primary + graph_boost + bounded_feedback - penalty

graph_only =
    graph_candidate_gate
  * (0.45 * graph_calibrated + 0.15 * exact_match + 0.10 * temporal_match + 0.10 * memory_quality)
```

缺失特征保持缺失，不赋误导性的零。graph-only 使用独立 admission floor 与 protected-rank 规则，且不再叠加 `graph_boost`。权重在固定开发集上校准，经质量门禁后才提升为默认。

### 9. 预算化上下文选择

ranking 与 selection 分离。selection 在 token、character、result-count、source、sensitivity 预算下，用 provenance-aware duplicate clustering 与 MMR 风格 utility 选择：

```text
utility = relevance + evidence_coverage_gain + entity/session_coverage_gain
        - redundancy - context_cost
```

问题类型调整 coverage 项；达到 score discontinuity 或预算边界即停止，不强制填满 top-k。返回结构化 context item（rank、memory/revision ID、kind、display content、event/valid time、source speaker、evidence refs、matched features/channels、graph explanation、confidence/lifecycle、estimated tokens），纯文本渲染只在字段存在时呈现日期与 speaker。

### 10. 反馈、身份与可观测性

反馈分为 `memory_quality_feedback`、`retrieval_feedback`、`answer_feedback`，append-only。retrieval feedback 保存 query hash、query type、允许的 entity IDs、ranking/feature/context identity、candidate rank、selected/rejected state、matched signals、caller、timestamp，不保存明文查询。全局反馈是有界先验，query-conditioned 调整需满足 minimum support、time decay、caller dedup 与 poisoning 限制，且永不改变权威或绕过 review。在线与维护重建共享同一版本化聚合函数。

下列算法各自独立版本化身份，并由 cursor/checkpoint 绑定所有影响 membership、rank 或 rendered context 的身份：processing、candidate consolidation、canonical memory projection、embedding document、embedding query、semantic calibration、keyword compiler、entity normalization、predicate registry、graph extraction、graph scoring、query planning、ranking feature schema、ranking weights、context selection、feedback aggregation、evaluation metric schema。identity 使用规范化非密文输入生成 `sha256:` 前缀值。

每个 recall 操作记录有界、无内容 telemetry：query-plan version/type、requested/executed capabilities、candidate counts、rejection gates、score summaries、graph seed/edge/path/promotion counts、duplicate clusters、context budgets、index/ranking identities、degradation reasons、phase latency。explain mode 返回每结果特征贡献、graph path、evidence、selection/rejection reason，并执行与普通 recall 相同的 policy 与 sensitivity 规则。

### 11. 版本身份存储

schema 增加通用版本身份存储与 active-generation metadata，覆盖 membership、ranking、graph、context、evaluation identity。cursor 与 checkpoint 只接受新版本；任何绑定身份变化使旧 cursor 失效，旧 cursor 不被解析或降级。

## 后果

- 同一权威证据只计一次；graph 不能独立提升弱相似度或低置信关系。
- 记忆更少更密、证据完整、时间/实体显式，检索与上下文选择成为两个独立版本化阶段。
- schema、API、ranking、cursor、配置、export 与 checkpoint 契约一次性切换，旧数据目录不可复用。
- 实现必须删除 `related_to`、旧 graph predicate、`rrf-v3`、显式 `channels` 默认抽象、first-evidence-only 映射与 stale aggregate 默认包含，不保留任何兼容分支。
- 后续阶段按 tasks.md 在替换契约之上实现算法与评估门禁。
