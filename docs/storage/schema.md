# SQLite Schema v1

dMem 使用一个 SQLite 数据库作为领域权威。Schema v1 由
[schema-v1.sql](schema-v1.sql) 定义，并由 `internal/storage` 内嵌；两者必须逐字节
一致，`TestDocumentedSchemaMatchesEmbeddedSchema` 强制这一点。`SchemaVersion`
为 `1`。

共 92 张表：91 张 `CREATE TABLE` 与 1 张虚拟表 `memory_fts`，全部为 `STRICT`
表。`schema_metadata` 保存 `schema_version`。

Schema v1 没有迁移路径。当可执行 schema 变化时，应初始化新的数据目录；不兼容
的数据库会被拒绝。`ValidateExactSchema` 会把完整的 `sqlite_schema` 目录与新建的
内存数据库比较，任何差异都会导致拒绝。

## 权威表

权威记录由触发器保护为仅追加或不可变：更新与删除会被 `RAISE(ABORT, ...)`
拒绝。恢复流程不会重建它们。

### 摄取与记忆

| 表 | 用途 |
| --- | --- |
| `memory_spaces` | Scope 层级与外部身份。 |
| `source_streams` | 来源流的稳定标识与顺序。 |
| `raw_contents` | 原始内容（文本 / JSON / 引用）。 |
| `raw_events` | 原始事件及其来源元数据。 |
| `memories` | 记忆的逻辑身份、当前修订、生命周期状态与冻结标记。 |
| `memory_revisions` | 不可变修订及其规范化检索字段。 |
| `revision_subject_entities` | 修订与主体实体的关联。 |
| `revision_object_entities` | 修订与客体实体的关联。 |
| `memory_state_events` | 仅追加生命周期状态事件。 |
| `memory_evidence` | 修订证据片段及其角色、偏移。 |
| `revision_attributes` | 修订的类型化属性键值。 |

### 候选与整合

| 表 | 用途 |
| --- | --- |
| `candidates` | 独立审核对象的候选。 |
| `candidate_evidence` | 候选证据片段。 |
| `candidate_reviews` | 候选审核记录（仅追加）。 |
| `consolidation_decisions` | 确定性或 provider 整合决策。 |

### 实体与关系

| 表 | 用途 |
| --- | --- |
| `entities` | 带 scope 的实体权威。 |
| `entity_events` | 实体状态事件（仅追加）。 |
| `entity_aliases` | 类型化别名。 |
| `entity_alias_events` | 别名事件（仅追加）。 |
| `entity_mentions` | 实体到修订或事件的不可变绑定。 |
| `entity_proposals` / `entity_proposal_reviews` | 实体提案及其审核。 |
| `memory_relations` | 修订级关系（`supports`、`contradicts`、`supersedes`、`derived_from`）。 |
| `relation_evidence` | 关系证据。 |
| `relation_state_events` | 关系状态事件（仅追加）。 |
| `relation_proposals` / `relation_proposal_reviews` | 关系提案及其审核。 |

### 演化与冲突

| 表 | 用途 |
| --- | --- |
| `evolution_proposals` | 演化提案（重复、补充、替换、冲突）。 |
| `evolution_proposal_targets` | 提案的目标修订。 |
| `evolution_proposal_evidence` | 提案证据。 |
| `evolution_proposal_reviews` | 提案审核（仅追加）。 |
| `evolution_result_refs` | 确认后产生的结果引用。 |
| `conflict_cases` | 冲突用例及其终态。 |
| `conflict_case_participants` | 冲突参与方修订（仅追加）。 |
| `conflict_case_relations` | 冲突用例与 `contradicts` 关系的关联。 |
| `conflict_resolution_events` | 解决事件（仅追加）。 |
| `conflict_resolution_result_refs` | 解决结果引用。 |

### 反馈与聚合

| 表 | 用途 |
| --- | --- |
| `feedback_events` | 仅追加反馈事件。 |
| `aggregate_descriptors` | 聚合记忆的类型化描述符。 |
| `aggregate_proposals` | 聚合提案。 |
| `aggregate_proposal_inputs` | 聚合输入修订。 |
| `aggregate_proposal_reviews` | 聚合提案审核（仅追加）。 |

### 类型化图谱

| 表 | 用途 |
| --- | --- |
| `predicates` / `predicate_definitions` | 封闭谓词注册表（`predicate-registry-v2`）。 |
| `entity_facts` | 主体-谓词-客体/字面量事实。 |
| `entity_fact_evidence` | 事实证据。 |
| `entity_fact_state_events` | 事实状态事件（仅追加）。 |
| `entity_fact_proposals` / `entity_fact_proposal_reviews` | 事实提案及其审核。 |
| `event_participants` | 事件参与实体。 |
| `event_temporal_bindings` | 事件时间绑定。 |

### 可移植性

| 表 | 用途 |
| --- | --- |
| `export_operations` | 导出操作及其不可变请求。 |
| `import_sources` | 导入来源包。 |
| `import_operations` | 导入操作及其不可变计划。 |
| `import_space_mappings` | 源到本地空间映射。 |
| `import_object_mappings` | 源到本地对象映射。 |
| `imported_provenance` | 导入溯源（仅追加）。 |
| `import_checkpoints` | 分批检查点（仅追加）。 |

### 配置、策略与运维

| 表 | 用途 |
| --- | --- |
| `configuration_revisions` | 每次重载尝试的配置修订。 |
| `configuration_activations` | 激活记录与 generation。 |
| `provider_policies` | 版本化 provider 出网策略。 |
| `policy_assignments` | 策略分配（按空间、空间类型或外部标识）。 |
| `capability_states` | 能力可用性状态。 |
| `idempotency_records` | 写身份到已提交结果的映射。 |
| `audit_events` | 操作级审计轨迹。 |

## 派生表

派生数据可重建。恢复流程会删除或标记这些表为重建中，并重新入队相应作业。

| 分组 | 表 |
| --- | --- |
| 检索投影 | `memory_projections`、`memory_embeddings`、`memory_fts`（虚拟 FTS5 表）、`corpus_metadata`、`projection_metadata` |
| 质量与去重 | `memory_quality`、`duplicate_clusters`、`duplicate_cluster_members` |
| 索引元数据 | `index_metadata`、`semantic_index_metadata`、`entity_index_metadata`、`relation_index_metadata`、`graph_index_metadata`、`entity_graph_statistics` |
| 反馈聚合 | `feedback_aggregates`、`feedback_query_aggregates`、`feedback_aggregate_metadata` |
| 策略与资格投影 | `effective_space_policies`、`embedding_eligibility`、`effective_configuration_state` |
| 聚合新鲜度 | `aggregate_freshness` |
| 作业与调和 | `jobs`、`job_attempts`、`reconciliation_runs` |
| 遥测 | `query_telemetry`、`context_decisions` |
| 引导 | `schema_metadata` |

`memory_embeddings` 存储紧凑 `float32` BLOB，并通过 sqlite-vec 精确余弦检索。
`memory_fts` 是 FTS5 虚拟表。语义向量与 FTS 行都可以从权威修订重建。
