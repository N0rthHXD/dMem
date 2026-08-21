# dMeM API 设计

## 1. API 原则

- API 面向本地 Agent、应用和采集器。
- 第一阶段默认仅提供本地端点。
- 请求必须携带协议版本或支持版本协商。
- 写入操作支持幂等键。
- 权威写入、记忆处理和索引完成是三个不同状态。
- 错误类别稳定，客户端不应依赖错误文本。
- 第一阶段 Agent 全部可信，但请求仍记录 `caller_id` 和作用域。
- HTTP 方法、路径、状态码和 JSON schema 以 [OpenAPI](openapi.yaml) 为权威定义。

## 2. 通用请求字段

```text
X-Caller-ID
Idempotency-Key（写接口）
```

daemon 为每个请求生成 `request_id` 并在错误响应和审计中返回或记录；第一阶段调用方不传入 request ID。

调用方必须提供 `X-Caller-ID`。daemon 对缺少的 request ID 和 operation ID 负责补充。调用方身份不在第一阶段承担 Agent 级安全授权。

## 3. 写入原始事件

```text
ingest(event) -> acceptance
```

事件字段：

```text
source_type
source_name
occurred_at
content_type
content
metadata
space
```

响应：

```text
event_id
operation_id
status
job_ids
accepted_at
```

状态：

```text
accepted       原始事件已保存
duplicate      已存在，返回原事件标识
deferred       已保存，等待后台处理
rejected       未保存
```

## 4. 查询记忆

```text
search(query, scope, filters, time_range, budget) -> results
```

请求：

```text
query
spaces
project_id
task_id
session_id
memory_types
time_range
retrieval_modes
limit
max_chars
include_evidence
include_explanation
```

响应：

```text
results[]
  memory_id
  revision_id
  content
  memory_type
  space
  status
  valid_time
  relevance
  evidence_refs
  conflict_refs
  selection_reason
  index_state
degraded
freshness
```

查询流程：

```text
作用域过滤
  → 关键词候选和结构化过滤
  → 统一排序
  → 当前版本确认
  → 生命周期过滤
  → 时间和冲突处理
  → 预算装配
```

## 5. 获取单条记忆

```text
get(memory_id, revision_id?) -> memory
```

未指定版本时返回当前投影；指定版本时返回历史版本。历史版本查询必须明确标注其非当前状态。

## 6. 修订记忆

```text
revise(memory_id, expected_revision_id, change) -> revision
```

请求：

```text
expected_revision_id
content_patch
status_change
valid_time_change
metadata_patch
reason
evidence_refs
```

规则：

- 必须携带期望版本。
- 版本不匹配返回 `revision_conflict`。
- 修订创建新版本，不原地改历史。
- 证据和审计事件与新版本在同一事务中提交。

## 7. 反馈操作

```text
feedback(memory_id, revision_id, type, reason) -> feedback_event
```

支持：

```text
confirm
correct
supplement
contest
retract
freeze
archive
useful
irrelevant
```

第一阶段实现 confirm、contest、freeze、archive 和 retract，它们追加状态事件。correct 和 supplement 使用 revise。useful 和 irrelevant 延期。

## 8. 生命周期操作

```text
archive(memory_id)
restore(memory_id, revision_id?)
freeze(memory_id)
retract(memory_id, reason)
delete(scope, mode)
```

`delete` 必须返回删除范围和完成状态。逻辑删除完成不代表备份和所有派生数据已经物理清除。

## 9. 任务接口

```text
get_job(job_id) -> job
cancel_job(job_id) -> result
retry_job(job_id) -> job
```

任务状态：

```text
pending
running
waiting
retrying
succeeded
failed
cancelled
expired
```

## 10. 管理接口

```text
rebuild_index(index_kind)
health()
metrics()
```

第一阶段不实现 export、import 和 backup HTTP API。

管理接口必须区分查询、普通写入和破坏性操作，并写入审计记录。

## 11. 错误类别

```text
invalid_request
invalid_json
duplicate_request
retryable_overload
dependency_unavailable
processing_failed
revision_conflict
storage_unavailable
index_degraded
incompatible_version
resource_exhausted
not_found
unsupported
idempotency_conflict
```

HTTP 状态映射以 OpenAPI 为准。所有成功写响应都应携带 `Idempotent-Replay`，首次执行为 false，幂等重放为 true。

错误响应包含：

```text
error_code
message
request_id
operation_id
retryable
retry_after
details
```

`accepted_async` 是成功处理状态，不是错误码。

## 12. 兼容性

- API 版本与数据库 schema 版本独立。
- 第一阶段请求 JSON 出现未知字段时返回 `invalid_request`。
- 响应新增字段不应破坏旧客户端。
- 排序、分页、重试和异步状态是行为契约的一部分。
- 处理任务绑定提交时的处理版本，升级后必须有明确的继续、迁移或失败策略。
