# ADR 0032：知识图谱构建与检索

- 状态：accepted
- 日期：2026-09-08

## 决策

在保持 SQLite 为权威数据源、以 memory revision 为核心检索单元的前提下，引入端到端知识图谱能力：confirmed memory revision 是图谱抽取主体，raw event 只作为 provenance/evidence。

### 构建主体与生产链

`raw event -> extract_memory -> memory candidate -> confirm -> authoritative memory revision -> extract_graph job -> entity/alias/mention/entity-fact/memory-relation proposals -> deterministic validation + risk classification -> auto-confirm / pending review / reject -> authoritative graph`。

- `extract_graph` job 的稳定输入是 confirmed memory revision（`target_type=memory_revision`，`target_id=revision_id`），不绕过 confirmed revision 直接写权威图谱。
- 确认/修订/恢复产生的新 searchable revision 会在同一写事务内幂等调度 `extract_graph` job（`jobs_effective_identity` 去重）。
- 一次 graph extraction Provider 调用返回 entities、aliases、mentions、entity facts 和可选 memory relations，复用现有 lease/generation fencing、timeout、retry、policy 与 processing identity 机制，Provider 调用保持在 SQLite 写事务之外。

### Entity Resolution 属于 query understanding

实体识别不是独立顶层召回通道，而是检索前处理。查询时用确定性规则（normalized alias longest-match）从自然语言 query 中识别 mention，输出 resolved entity IDs 与 resolution confidence。构建时用 canonical-name/alias exact match + entity type 兼容 + 当前 space 优先做 resolution：唯一匹配关联已有实体，无匹配创建 proposal，多匹配/alias collision/type 冲突进入人工审核，禁止自动跨 sibling 合并。Provider 不可用时图谱检索仍可运行。

### Entity facts 与 memory relations 的区别

保留现有 revision 级 `memory_relations`，同时新增真正的 `entity_facts`：subject entity -> predicate -> object entity 或受控 literal，二者必须且只能选一个。事实带 source memory/revision、confidence、state、valid_from/valid_until、evidence、append-only state history、proposal/review 数据与去重约束。Predicate 使用受控 registry（`participates_in`、`uses_database`、`owned_by`、`located_in`、`depends_on`），不接受无限自由文本直接成为权威 schema。

### proposal-confirm 与 auto-review 边界

模型输出不得无条件直接写入权威图谱。所有自动抽取结果保留 proposal/audit 信息，但允许低风险项自动确认，同时记录 proposal ID、provider/model/processing identity、source revision、raw event evidence、confidence、auto-review rule version 与 reviewer identity（`system:auto-review-v1`）。默认策略：

- 已有实体的合法 mention：自动确认。
- 唯一 alias match：自动关联；alias collision：进入 merge proposal（pending review）。
- 新实体：默认 pending review；仅在高置信度阈值下自动确认。
- 普通低风险 entity fact：高置信度、无冲突时自动确认；与现有同 subject+predicate 冲突时 pending review。
- `related_to` 自动确认；`supports`/`derived_from` 高阈值自动确认；`contradicts`/`supersedes` 默认 pending review。
- 跨 sibling/cross-space relation 禁止自动确认。

### Graph Channel 召回

保留三个顶层通道 keyword、semantic、graph。Graph channel 内部区分信号：`direct_entity_mention`（0-hop）、`exact_predicate_fact` 与 `one_hop_fact`（1-hop）、`two_hop_path`（受限 2-hop）、`memory_relation` 与 `graph_conflict`。2-hop 仅在存在明确 predicate/path 意图时执行，最大两跳、每层 fan-out 上限、predicate 白名单、scope 在遍历前应用、active/current/valid-time 过滤、确定性排序与 cycle 保护，不允许无界 BFS。memory relation 扩展的 seed 来自 keyword、semantic 与 entity resolution 的统一 seed 集，不再只依赖重新执行 FTS。Graph candidate 回溯 supporting memory revision 与 evidence；没有可见 evidence 的 graph fact 不作为高置信度上下文返回。

### SQLite authority + rebuildable graph index

派生结构直接用有索引的 SQLite authority 表（entity alias、entity facts 的 subject/predicate/object/source_revision、memory relation 邻接）。`graph_index_metadata` 与 entity/relation metadata 一样维护 active_version、generation、state；rebuild 从 authority 完整重建并原子切换版本，失败不破坏 active version。authority 或 index 变化后 cursor 绑定 generation，旧 cursor 不得跨快照静默分页。

### 排序与 cursor version

排序仍用 versioned RRF（`rrf-v2` 保持，`ranking_version` 不变，cursor 版本不变）。graph channel 作为一个顶层通道贡献 `1/(60+graph_rank)`；direct mention 与 exact predicate 权重高、1-hop 次之、2-hop 明确衰减，并乘以 entity resolution confidence 与 relation/fact confidence，应用 temporal validity。feedback 仅作为 bounded post-ranking signal。响应保留 matched_channels、graph_signals、resolved entities、relevant facts、paths、relation direction、confidence、source memory/revision、evidence references、conflict references 与 selection reason。

## 影响

- 新表：`predicates`、`entity_facts`、`entity_fact_evidence`、`entity_fact_state_events`、`entity_fact_proposals`、`entity_fact_proposal_reviews`、`graph_index_metadata`。
- 新 job type：`extract_graph`、`rebuild_graph_index`。
- 新 API：`/v1/entity-facts`、`/v1/entity-facts/{fact_id}`、`/v1/fact-proposals`（create/list/get/confirm/reject）、`/v1/indexes/graph/rebuild`。
- Search/recall 响应新增 graph context（`graph_signals`、`graph_facts`、`graph_paths`、`resolved_entities`、`graph_index_version`）。
