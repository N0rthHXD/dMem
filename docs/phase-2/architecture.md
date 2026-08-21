# dMeM 第二阶段目标架构

## 1. 组件边界

```text
UDS HTTP API
  ├─ ingest / memory / job / search（现有）
  ├─ candidate review（新增）
  └─ backup / restore operation status（新增或 CLI 暴露）

Application services
  ├─ ProcessingService
  ├─ CandidateService
  ├─ RetrievalService
  └─ DataMaintenanceService

Ports
  ├─ Processor
  ├─ EmbeddingProvider
  ├─ VectorIndex
  └─ BackupStore

SQLite repositories
  ├─ raw events / jobs / memories（现有权威数据）
  ├─ candidates / reviews / migrations（新增权威数据）
  └─ embedding metadata / index metadata（新增派生数据元数据）

Derived indexes
  ├─ SQLite FTS（现有）
  └─ semantic vector index（新增，可重建）
```

API handler 不直接编排 provider。外部调用、候选校验、权威事务和索引更新由 application service 分开承担，以保持 provider、SQLite 和 HTTP 生命周期解耦。

## 2. 自动处理数据流

### 2.1 准备

worker claim `extract_memory` job 后，ProcessingService 在短只读操作中加载：

- job target 对应的 raw event；
- processor identity 和 job 固定的 processing version；
- 空间与数据类型策略；
- 有上限的局部上下文；
- deadline 和当前 lease generation。

准备阶段不把 repository 或数据库句柄交给 provider。

### 2.2 外部执行

provider 在 SQLite 事务外执行。输入是不可变 DTO，输出是强类型 outcome。取消、deadline 或 lease 丢失时停止接收结果；无法停止的外部调用也不得在返回后绕过 fencing 提交。

### 2.3 提交

结果提交事务必须同时完成：

- 再次确认 job、lease owner、generation 和 running 状态；
- 校验 target event 和 processing version；
- 写入去重后的 candidates 和 evidence；
- 更新 raw event processing status；
- 写入 audit；
- 完成 job attempt 和 job result。

事务失败时以上变更全部不可见。provider 成功但 fencing 失败时结果被丢弃，由当前 generation 决定最终状态。

## 3. 候选与权威记忆

候选是 processor 建议，不是当前记忆投影。无论最终采用独立表还是 memory candidate 状态，都必须满足：

- 候选有稳定 ID、不可变提取内容和独立审核状态。
- 候选绑定 raw event evidence、processor identity 和 processing version。
- 同一 target event、processing version 和候选规范哈希不能重复创建。
- reject 不删除候选；confirm/reject 追加审核事件。
- confirm 创建权威 memory 时引用候选 ID，并在同一事务完成 memory、revision、evidence、state、projection、FTS、idempotency 和 audit。
- 候选不进入 FTS 或 semantic 默认检索。

推荐使用独立 candidate 对象，避免把“待审核的模型建议”和“已存在的逻辑 memory 生命周期”混为一体。最终 Schema 选择由 ADR 固化。

## 4. Embedding 与索引

Embedding 输入来自当前可检索 `memory_projection`，而不是 raw event 或候选。每条派生记录至少关联：

```text
memory_id
revision_id
content_hash
embedding_provider
embedding_model
dimension
index_version
created_at
```

revision、状态或检索内容变化时：

- 权威事务更新 projection 和 FTS。
- 同一事务记录 embedding 失效或创建 durable embedding job。
- 旧向量可以短期存在，但查询复核必须排除非 current revision。
- rebuild 从 SQLite 当前可检索 projection 生成新 index version，验证完成后原子切换 active version。

向量值和索引文件是派生数据，不进入最小权威备份要求。

## 5. 混合检索

RetrievalService 接收一个固定 reference time 和版本化查询配置：

1. 解析空间祖先及结构化条件。
2. 独立执行 FTS 与 semantic recall。
3. 以 `(memory_id, revision_id)` 取并集并保留通道分数。
4. 使用版本化融合规则生成确定性排序键。
5. 回到 SQLite 检查 current revision、状态、有效时间和作用域。
6. 按 `limit` 和 `max_chars` 装配。

cursor 必须包含查询哈希、融合版本、索引版本、reference time 和最后排序键。请求配置或 active semantic index version 改变时，旧 cursor 返回 `invalid_request`，不产生跨版本分页漂移。

## 6. 健康与降级

健康响应分别表达：

```text
storage_state
schema_compatibility
queue_state
processor_state
processor_identity
semantic_index_state
semantic_index_version
fts_index_state
backup_state
overall_status
```

状态不得仅由配置推断。至少区分：

- `not_configured`：未启用，属于预期离线行为。
- `ready`：最近探测或实际请求成功，依赖可用。
- `degraded`：部分能力不可用，可执行明确回退。
- `unavailable`：请求的能力无法提供。
- `rebuilding`：新索引正在构建，旧 active version 可继续服务。

Processor 不可用不影响 ingest、手工 memory 和 keyword search。Semantic index 不可用不影响 keyword search；hybrid 回退必须在响应中标记。

## 7. 配置与隐私

所有 provider 默认关闭。配置至少包含：

- provider kind、endpoint 和 model identity；
- secret reference，而不是明文 secret 输出；
- 允许外发的 space 和 content type；
- 输入字符上限和上下文条数上限；
- request timeout、并发、重试和速率限制；
- processing/embedding version；
- 遥测关闭选项。

日志和错误不得包含完整 raw event、candidate、embedding 输入、Authorization header、secret 值或 provider 原始响应。审计记录调用方、provider/model identity、目标 event、外发策略版本、结果类别和外部 request ID。

## 8. 事务和并发不变量

- provider 和 embedding 调用不持有写事务。
- job 完成和其权威结果使用同一个 fencing 条件和提交事务。
- candidate confirm 使用 Idempotency-Key，重复请求返回原 memory。
- 同一 candidate 只能从 pending 转为 confirmed 或 rejected；并发审核只有一个成功。
- confirm 失败不得留下无 memory 的 confirmed candidate，或无 confirmed candidate 的 processor-created memory。
- 索引切换只在新版本构建和验证完成后发生。
- backup 与 migration 使用 data directory 单实例锁及维护状态，不能与 restore 并发写入。
