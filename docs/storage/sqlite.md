# dMeM SQLite 存储设计

## 1. 规范来源

当前字段、类型、约束、索引和 FTS 定义以 [`schema-v1.sql`](schema-v1.sql) 为唯一可执行规范。本文只解释主要表职责和事务边界，不重复维护伪 schema。

## 2. SQLite 基线

- schema version：1。
- 使用 `STRICT` 表。
- 时间存储为规范 UTC RFC 3339 文本。
- ID 存储为 UUID 文本。
- JSON 字段使用 `json_valid` 约束。
- 启用 foreign keys。
- 当前未发布基线不使用 ORM AutoMigrate，不实现 schema migration。

## 3. 表职责

| 表 | 类型 | 职责 |
| --- | --- | --- |
| schema_metadata | 权威 | 单一 schema 版本和初始化时间 |
| memory_spaces | 权威 | 单父节点空间树 |
| raw_contents | 权威 | 托管 text/json 或外部 reference 元数据 |
| raw_events | 权威 | 原始事件、来源、空间和处理状态 |
| memories | 权威 | 逻辑记忆、当前 revision、当前状态和 frozen |
| memory_revisions | 权威 | 不可变语义版本 |
| memory_state_events | 权威 | 追加式生命周期和冻结变化 |
| memory_evidence | 权威 | revision 到原始事件的证据 |
| memory_projections | 物化视图 | 当前可检索内容，可从权威表重建 |
| memory_fts | 派生 | 当前 projection 的 FTS 索引 |
| jobs | 权威运行状态 | 持久化队列、lease 和结果 |
| job_attempts | 权威运行历史 | generation、worker 和 attempt 结果 |
| idempotency_records | 权威 | 请求冲突检测和响应重放 |
| audit_events | 权威 | 非敏感操作审计 |
| index_metadata | 派生状态 | FTS 版本、新鲜度和重建状态 |

Phase 3 在同一 Schema v1 初始化契约中增加 entity/alias/mention、relation/evidence、conflict case/resolution、evolution proposal、feedback event/aggregate、aggregate descriptor/freshness/provenance 和 logical import operation/mapping/checkpoint 表。完整清单及约束以 `schema-v1.sql` 为准；FTS、semantic、entity/relation metadata、feedback aggregate 和 aggregate freshness 属于可重建派生状态，feedback event 和 import mapping/provenance 属于权威数据。

## 4. 权威状态

- `memory_revisions` 创建后不可变。
- 当前 revision 和状态由 `memories` 表权威表达。
- 每次状态变化追加 `memory_state_events`。
- `memory_projections` 是事务维护的物化视图，不是独立事实来源。
- FTS 只索引 searchable projection。

## 5. 事务边界

### 原始事件接收

同一事务提交：

```text
raw_contents
raw_events
idempotency_records
jobs
audit_events
```

提交后才返回 accepted/deferred。

### 创建和修订记忆

同一事务提交：

```text
memories（创建或条件更新）
memory_revisions
memory_evidence
memory_state_events（仅创建首个状态或 restore 时）
memory_projections
memory_fts
idempotency_records
audit_events
```

条件更新必须匹配 `expected_revision_id`。

普通 revise 保持 current state/frozen 不变，不追加状态事件。

### 生命周期变化

同一事务提交 current state/frozen、状态事件、projection、FTS、幂等和审计。状态变化不修改历史 revision。

### FTS 重建

rebuild job 将 index state 设为 rebuilding，从 projection 构建新内容，校验后设为 ready。失败时标记 degraded，不修改权威记忆。

## 6. Job Claim

claim 使用显式 SQL 条件更新，在短事务中设置 running、lease owner、lease until、generation，并创建 attempt。完成结果必须匹配 generation fencing token。

## 7. Schema 初始化

- 新库执行完整 `schema-v1.sql`。
- 打开旧库只校验 `schema_metadata.schema_version=1`。
- 不匹配时拒绝普通启动。
- 开发期不兼容变化重新创建数据库。

## 8. Go 映射规则

- persistence model 不作为 API 或领域对象。
- ORM 只用于普通 CRUD。
- schema 初始化、FTS、幂等、条件 revision 更新、claim 和备份使用显式 SQL。
- 枚举值同时在 Go 领域层和 SQL CHECK 中校验。
