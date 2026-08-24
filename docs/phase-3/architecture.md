# dMeM 第三阶段实现架构

## 1. 组件边界

```text
UDS HTTP API / offline library boundary
  ├─ memory / candidate / search / feedback
  ├─ entity / relation / conflict / aggregate
  └─ logical export / import（Go library，无公开 HTTP/CLI）

Application services
  ├─ ProcessingService / EvolutionService
  ├─ EntityService / RelationService / ConflictService
  ├─ FeedbackService / AggregationService
  ├─ RetrievalService
  └─ PortabilityService

Ports
  ├─ Processor / EmbeddingProvider
  ├─ EntityRelationIndexer
  ├─ VectorIndex
  └─ LogicalPackageStore

SQLite authority
  ├─ raw events / memories / revisions / evidence / candidates
  ├─ entities / mentions / relations / conflict cases
  ├─ feedback events / aggregate provenance
  └─ jobs / reviews / idempotency / audit / import mappings

Derived data
  ├─ FTS / semantic vector index
  ├─ entity and relation indexes
  ├─ feedback aggregates
  └─ aggregate generation cache and freshness metadata
```

API handler 不直接编排 provider、关系写入或逻辑包内部 SQL。Application service 分离外部执行、强类型校验、权威事务和派生任务，使第三阶段继续遵守短事务与 generation fencing。

## 2. 权威对象与派生对象

### 2.1 权威对象

- Entity identity、确认后的 alias 和 entity mention。
- Memory relation assertion、relation 状态和来源证据。
- Conflict case、参与 revision、resolution event 和操作结果引用。
- Feedback event。
- 审核后的 aggregate memory revision 及其 `derived_from` provenance。
- Logical import source、object mapping、批次状态和 imported provenance。

### 2.2 派生对象

- Entity 名称/alias 检索索引。
- Relation 邻接和检索加速索引。
- Feedback 统计、窗口分数和 usage score。
- Aggregate 输入 fingerprint、生成缓存和 freshness projection。
- 所有 keyword、semantic 和新增召回通道索引。

派生对象损坏不能改变权威 relation、feedback 或 aggregate 历史。重建输入只来自 SQLite 当前权威对象。

## 3. Entity 和 mention

Entity 表达稳定身份，mention 表达某个具体来源对 entity 的提及。推荐边界：

```text
entity
  entity_id, space_id, entity_type, canonical_name, state, sensitivity

entity_alias
  entity_id, alias, normalization_version, source, state

entity_mention
  mention_id, entity_id,
  memory_id + revision_id | event_id,
  range, mention_type, evidence, created_by, processing_version
```

Mention 必须绑定固定 revision 或 event range；只保存 `memory_id` 不足以证明历史提及。Entity index 命中后回查 mention 来源仍在查询空间内，并确认关联 memory revision 是否为当前可检索版本。

第三阶段不自动 merge entity。Merge proposal 可以持久化，但稳定 identity 的合并、relation 迁移和 alias 重写不在本阶段执行。

## 4. Relation 和 conflict

Relation 是独立权威 assertion，不用 `memory_type=relation` 模拟：

```text
relation
  relation_id, relation_type, direction,
  source_memory_id, source_revision_id,
  target_memory_id, target_revision_id,
  state, confidence, created_by, processing_version

relation_evidence
  relation_id, event_id | revision_id, range, evidence_type

relation_state_event
  relation_id, from_state, to_state, reason, actor, created_at
```

第三阶段固定支持 `supports`、`contradicts`、`supersedes`、`derived_from`、`related_to` 和 `mentions`。Relation endpoint 绑定具体 revision，从而保留“当时哪两版内容之间存在何种 assertion”。

Conflict 使用三个分离对象：

- `conflict` proposal：未审核模型建议。
- `contradicts` relation：确认后的语义 assertion。
- `conflict_case`：需要处理的一组 relation/revision 及其 open/resolved/dismissed 状态。

`memory.current_state=contested` 只影响当前检索投影，不保存完整冲突身份。确认 conflict proposal 的事务应原子创建 relation/case、必要 state event、索引任务、review、idempotency 和 audit。

## 5. Evolution proposal

普通第二阶段 candidate confirm 继续表示“创建新 memory”。第三阶段针对已有记忆的 proposal 必须显式包含：

```text
action: supplement | replacement | conflict | duplicate
target_memory_id
expected_revision_id
proposed_content and valid time
relation specs
entity mention specs
evidence
processing identity and specification hash
```

审核分为准备、可选外部执行和权威提交。提交时重新检查：

- Proposal 仍 pending。
- Target current revision 与 `expected_revision_id` 一致。
- Target 未 frozen，且处于允许动作的生命周期状态。
- Evidence、related revision 和 entity 均存在且在允许作用域内。
- 当前 review、idempotency key 和 job generation 仍有效。

任何条件失败都不能留下新 revision、relation、state event 或 confirmed proposal 的部分结果。

## 6. Feedback 与排序

Feedback event 至少保存：

```text
feedback_id, operation_id, caller_id
memory_id, revision_id
feedback_type, reason_code, note
search_operation_id, query_hash, ranking_version
created_at
```

Feedback 固定绑定观察时 revision。`useful` 和 `irrelevant` 不修改 memory state；`correct` 和 `supplement` 可以触发 proposal，但反馈事件本身仍独立保留。

Feedback aggregate 使用固定算法、窗口和版本，从事件重建。RetrievalService 只读取有限影响的 `usage_score`，且排序顺序始终晚于 scope、current revision、lifecycle 和 valid-time 过滤。Ranking 或 aggregate version 改变时旧 cursor 返回 `invalid_request`。

## 7. Aggregate memory

场景或主题聚合使用以下边界：

- Provider 输入是有上限的固定 revision 集合。
- 自动结果先成为 aggregate proposal。
- 人工 confirm 后创建普通不可变 memory revision，并以 `derived_from` relation 绑定所有直接输入 revision。
- Aggregate sensitivity 不低于任一输入；禁止跨未显式允许的 sibling space 聚合。
- 输入 current revision 或生命周期变化时，freshness projection 标记 stale 并创建 fenced rebuild job。
- 已确认历史 aggregate revision 和 provenance 不因缓存重建而删除。

长期画像只保留 `aggregate_kind=profile` 等稳定边界，不提供自治生成和持续更新。

## 8. 多通道检索

```text
structured filters
  → keyword recall
  → semantic recall
  → entity recall
  → relation recall
  → candidate union by memory_id/revision_id
  → versioned fusion + bounded feedback signal
  → SQLite authority and conflict check
  → budget assembly
```

每个通道独立召回。Entity/relation 命中不能只给 semantic top-k 加分。Cursor 至少绑定 query hash、reference time、corpus generation、ranking version、feedback aggregate version、各 active index version 和最后排序键。

返回解释至少扩展：

```text
matched_channels
matched_entity_ids
matched_relation_ids
conflict_refs
ranking_version
feedback_aggregate_version
degraded_channels
```

## 9. Logical portability

逻辑包是领域格式，不是数据库备份：

```text
selection + closure planning
  → deterministic manifest and objects
  → checksums and atomic package publish
  → validate and dry-run target mapping
  → durable import batches with source mappings
  → conflicts become proposals
  → rebuild derived indexes
```

导入默认生成本地 ID，并持久化 `(source_identity, source_object_id) → local_id`。重复导入同一对象返回已有映射。Import 不直接覆盖 current revision，也不导入 provider secret、任务 lease、派生索引或 feedback aggregate。

## 10. 共同事务和并发不变量

- 外部 processor 和 aggregate provider 调用不持有写事务。
- Job 完成及其权威结果使用同一 generation fence 和事务。
- Proposal review 采用 Idempotency-Key，重复请求返回原结果。
- 多目标演进先检查全部 expected revision，再在同一事务写入全部结果。
- Relation、conflict case、memory state 和索引任务不得出现部分提交。
- Feedback event 与幂等响应原子写入；聚合可以异步重建。
- Import object 与 source mapping 同事务提交；批次 checkpoint 只在该批全部成功后推进。
- 所有新增 append-only 表通过 repository 和 Schema trigger 防止普通修改或删除历史。

## 11. 健康与恢复

Health 增加：

```text
entity_index_state/version
relation_index_state/version
feedback_aggregate_state/version
aggregate_queue_state
import_operation_state
```

状态来自实际 metadata、探测和任务结果。新增索引不可用不影响 raw ingest、手工 memory 和可用检索通道；所有降级在响应中可见。

Backup 包含全部新增权威表和 import mapping。Restore staging 验证 relation 端点、mention 来源、conflict case、feedback revision、aggregate provenance、import mapping、job fencing和空间隔离，并从权威数据重建 FTS、entity/relation metadata、feedback aggregate 和 aggregate freshness；semantic index 被置为 absent/stale 后由恢复任务重新 embedding 和构建。
