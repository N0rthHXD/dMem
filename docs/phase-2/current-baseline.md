# dMeM 第二阶段当前实现基线

## 1. 已完成能力

当前实现已经覆盖第一阶段五个垂直切片：

| 领域 | 当前实现 |
| --- | --- |
| 运行 | Linux 用户级 Go daemon、UDS HTTP、单实例锁、优雅关闭 |
| 存储 | SQLite Schema v1、固定 CGO/FTS5 build profile、精确版本检查、启动完整性检查 |
| 接入 | 空间树、文本/JSON/外部引用事件、幂等写入、事件查询和重放 |
| 任务 | durable job、claim、lease、heartbeat、generation fencing、attempt、重试、取消和重启恢复 |
| 记忆 | 手工创建、不可变 revision、evidence、状态历史、乐观并发和逻辑删除 |
| 检索 | 当前 projection、SQLite FTS、祖先作用域、结构化过滤、稳定 cursor 和字符预算 |
| 索引 | FTS 同事务维护、全量 rebuild job、degraded 状态 |

因此第二阶段不重写 daemon、repository 或任务队列，而是在现有事务和 fencing 约束上增加自动处理、候选对象和可重建语义索引。

## 2. 直接复用的基础

- `raw_events` 是自动处理的稳定输入和事实依据。
- `jobs`、`job_attempts` 和 worker service 承载 provider 调用与索引构建。
- `memory_revisions`、`memory_evidence` 和 `memory_state_events` 承载候选确认后的权威结果。
- `memory_projections` 是关键词与语义索引共同的当前源投影。
- `index_metadata` 和 FTS rebuild 模式可扩展为版本化语义索引构建。
- `Idempotency-Key`、`X-Caller-ID` 和 audit 事务模式继续适用于新增写接口。

## 3. 第二阶段必须补齐的边界

### 3.1 Processor

当前生产入口固定使用 no-op processor。现有 `Processor` 只接收 `domain.Job` 并返回弱类型 `Outcome.Value`，尚不能表达：

- processor identity 和可配置 processing version；
- 目标原始事件和受限上下文；
- 强类型候选、evidence 和 confidence；
- provider usage、外部请求标识和稳定错误详情；
- 输出校验后与 job 完成的原子提交；
- raw event processing status 的实际转换。

### 3.2 候选审核

Schema v1 包含 `candidate` 状态，但当前没有生产路径创建候选，也没有候选 list/get/reject API。第二阶段必须明确候选是独立审核对象还是 memory 的生命周期状态，不能继续依赖不可达的预留枚举。

### 3.3 语义检索

当前仅支持 FTS；`semantic` 请求稳定返回 unsupported。尚无 embedding provider、向量元数据、向量索引、混合排序、语义 rebuild 或相关健康状态。

### 3.4 数据演进

当前只接受精确 Schema v1，不提供 migration、正式备份和 restore。第二阶段新增候选与向量元数据时，必须先建立从现有数据库升级和失败恢复的机制。

### 3.5 契约与运行治理

第二阶段开始前应将以下第一阶段差异纳入切片 A：

- 使用真正的 OpenAPI request/response validator，而不是字符串或局部 golden 检查。
- 明确 FTS metadata degraded 与 FTS 实际不可查询的不同响应。
- 让 processor health、processing status 和 index freshness 来自实际状态而不是固定值。
- 明确配置支持范围；不把未实现的配置文件或环境变量描述为已支持。
- 修正第一阶段完成标准中的测试矩阵路径。

这些工作不扩展产品能力，但属于第二阶段可靠上线的前置门槛。

## 4. 保持不变的不变量

- 原始事件先提交，外部处理后执行。
- provider 不直接访问 repository 或 SQLite。
- provider 调用不持有 SQLite 长事务。
- 只有当前 lease owner 和 generation 可以提交处理结果。
- revision 和 state event 保持追加、不可变。
- 模型不能物理删除、静默覆盖或自主撤回权威记忆。
- 作用域过滤先于相似度排序。
- 派生索引必须能从 SQLite 权威数据完整重建。
- 默认不联网；未显式配置 provider 时行为保持为 `no_processor`。
