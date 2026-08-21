# dMeM 幂等契约

## 1. 身份分工

```text
request_id       单次 HTTP 请求追踪标识
operation_id     一个业务操作及其异步任务的关联标识
idempotency_key  调用方重试身份
event_id         原始事件领域身份
job_id           异步任务身份
```

`request_id` 和 `operation_id` 不用于重复判断。

## 2. 写接口要求

所有会改变权威状态或创建任务的第一阶段 HTTP 接口必须提供 `Idempotency-Key` 请求头。读取接口不接受幂等键。

命名空间：

```text
caller_id + HTTP method + normalized route + idempotency_key
```

同一调用方在不同写端点可以复用相同 key，不会冲突。

## 3. 请求指纹

daemon 对业务请求生成规范请求哈希：

- 不包含 `request_id`、`operation_id` 和传输相关字段。
- 包含路径参数、规范化 JSON body 和目标空间。
- JSON object key 按确定顺序规范化。
- 时间先验证并转换为统一 UTC 表达。
- 哈希算法和规范版本记录在幂等表中。

## 4. 处理规则

### 新 key

在权威业务事务中同时写入：

- 业务对象。
- 幂等记录。
- 可重放响应状态和对象标识。
- 必要的初始任务。

提交后才能返回成功。

### 相同 key、相同请求哈希

返回原始操作的 HTTP 状态、业务状态、对象标识和 job IDs。响应带 `Idempotent-Replay: true`。

不重新执行操作，不创建新事件、revision 或 job。

### 相同 key、不同请求哈希

返回：

```text
409 idempotency_conflict
```

不得返回原操作结果，也不得覆盖幂等记录。

## 5. 失败边界

- 请求在事务提交前失败：不保存成功幂等记录，调用方可重试。
- 事务已提交但响应中断：重试返回原响应摘要。
- 异步 job 后续失败：幂等重放仍返回原 accepted 操作和同一个 job，调用方查询 job 状态。
- 请求校验失败：不占用幂等键。

## 6. 原始事件身份

`event_id` 由 daemon 在首次成功 ingest 时生成。调用方可提供 `source_event_id` 作为来源元数据，但它不替代 API 幂等键。

重复 ingest 返回原 `event_id` 和原始 job IDs。

## 7. 任务幂等

任务结果幂等和 HTTP 幂等分开：

```text
job idempotency identity
  = job_type + target_type + target_id + processing_version
```

同一目标和处理版本最多有一个非终态任务。任务进入 succeeded、failed、cancelled 或 expired 后，显式 replay 可以创建新 job；HTTP 幂等重放仍返回原 job。worker 提交结果前检查 attempt fencing token 和目标当前状态。

## 8. 保留

第一阶段幂等记录不自动过期，随数据库保留。因为未实现 schema migration 和完整 purge，不能在删除 key 后继续承诺旧请求幂等。

后续引入保留清理时，必须重新定义 key 到期后的行为。

## 9. 适用端点

第一阶段需要幂等键：

- 创建空间。
- ingest 原始事件。
- replay 原始事件。
- 创建记忆。
- 修订记忆。
- 生命周期操作。
- cancel/retry job。
- FTS 重建。

GET 和 list/search 不使用幂等键。
