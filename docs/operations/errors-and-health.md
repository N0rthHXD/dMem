# dMeM 错误与健康状态

## 1. 错误响应

```text
error_code
message
request_id
operation_id
retryable
retry_after
details
```

客户端依据 `error_code` 和 `retryable` 决定行为，不解析 `message`。

## 2. 错误分类

| 错误 | 可重试 | 含义 |
| --- | ---: | --- |
| invalid_request | 否 | 请求字段或语义无效 |
| duplicate_request | 否 | 幂等请求已处理，可返回原结果 |
| retryable_overload | 是 | 队列或资源暂时饱和 |
| dependency_unavailable | 是或否 | 外部模型或索引依赖不可用 |
| processing_failed | 视类别 | 异步处理失败 |
| revision_conflict | 否，需重读 | 期望版本过期 |
| storage_unavailable | 视状态 | SQLite 不可用或只读 |
| index_degraded | 可继续 | 部分派生检索不可用 |
| incompatible_version | 否 | 客户端、配置或数据版本不兼容 |
| resource_exhausted | 视状态 | 磁盘、内存或预算不足 |
| not_found | 否 | 对象不存在或已不可见 |
| unsupported | 否 | 第一阶段不支持请求的能力或内容类型 |
| idempotency_conflict | 否 | 相同幂等键对应不同请求 |

## 3. 处理状态不是错误

```text
accepted
deferred
processing
processed
indexed
```

异步未完成不能用普通失败表示。调用方通过任务接口查询最终状态。

## 4. 健康维度

```text
process_alive
ready
accepting_writes
query_available
storage_healthy
storage_writable
index_state
index_freshness
job_queue_depth
oldest_job_age
processor_state
disk_pressure
schema_compatibility
```

健康响应不得包含密钥、原始内容和不必要的本地路径。

## 5. 整体运行模式

```text
healthy       核心读写和处理正常
degraded      核心服务可用，部分索引或依赖不可用
read_only     查询和管理可用，普通写入关闭
maintenance   恢复、完整性检查或管理操作中
unavailable   无法安全提供核心服务
```

## 6. 降级规则

| 条件 | 运行模式 | 行为 |
| --- | --- | --- |
| processor 未配置 | healthy | no-op 完成提取任务，手工记忆可用 |
| FTS 不可用 | degraded | get/list 可用，search 返回 index_degraded |
| SQLite 只读 | read_only | 拒绝持久化写入 |
| schema 版本不兼容 | unavailable | 拒绝普通启动，不自动修改数据库 |
| 权威数据库损坏 | unavailable/maintenance | 不返回未经确认的记忆 |
| 磁盘临界不足 | read_only/degraded | 保留只读查询，不承诺写入型删除 |

## 7. 新鲜度

查询响应应说明：

```text
权威版本更新时间
索引构建版本
待处理任务数量
是否可能遗漏尚未索引的记忆
```

调用方可以要求最小处理状态，例如只接受已索引结果；无法满足时明确失败或返回降级状态。
