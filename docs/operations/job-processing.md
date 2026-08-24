# dMeM 异步任务处理

## 1. 任务类型

```text
extract_memory
rebuild_fts
embed_memory
rebuild_semantic
rebuild_entity_index
rebuild_relation_index
aggregate_feedback
rebuild_feedback_aggregates
rebuild_aggregate
```

任务状态持久化在 SQLite。`embed_memory` 绑定 memory revision 和 embedding provider/model identity；`rebuild_semantic` 绑定语义索引和相同 identity。Phase 3 rebuild job 分别从 entity/relation authority、feedback events 或 aggregate frozen inputs 重建派生状态，并使用相同 lease generation fencing。

## 2. 状态机

```text
pending → running
pending → cancelled
pending → expired
waiting → pending
waiting → cancelled
retrying → pending
retrying → cancelled
running → succeeded
running → retrying
running → failed
running → cancelled
failed → pending（仅显式 retry）
```

当前 job 状态机不使用 `partially_succeeded`。部分信息放在 result 中，终态仍为 succeeded 或 failed。Logical import 使用独立 `import_operations` 状态和 batch checkpoints，不伪装成单个 job。

## 3. 任务记录

```text
job_id
operation_id
job_type
target_type
target_id
idempotency_key
priority
status
attempt_count
max_attempts
next_run_at
processing_version
processor_identity
lease_owner
lease_until
lease_generation
cancel_requested_at
result_code
last_error_code
created_at
started_at
finished_at
expires_at
```

每次尝试写入 `job_attempts`，保存 attempt ID、generation、worker、开始结束时间、结果和非敏感错误摘要。

## 4. 原子领取与 Lease

worker 在短 SQLite 写事务中：

1. 选择到期且状态为 pending/retrying 的任务。
2. 条件更新为 running。
3. 写 lease owner、lease until，并递增 generation。
4. 创建 job attempt。
5. 提交后执行处理。

选择顺序固定为：

```text
priority DESC, next_run_at ASC, created_at ASC, job_id ASC
```

worker 在 lease 半周期前续租。完成提交必须匹配：

```text
job_id + lease_owner + lease_generation + status=running
```

旧 generation 的结果被拒绝，防止失效 worker 覆盖恢复后的 attempt。

## 5. 可靠提交

```text
权威变更 + 后续任务记录 = 同一 SQLite 事务
任务处理和 processor 调用 = 事务之外
```

原始事件接收事务同时创建 extract job。FTS 物化更新与记忆当前状态变更在同一事务完成。可检索 projection 的语义源发生变化时，同一事务创建 embedding job，并确保当前 embedding identity 最多有一个 waiting/runnable semantic rebuild。rebuild 只有在当前 projection 的 embedding 全部可用后才能领取。

若 projection 在 rebuild 激活后、任务完成前再次变化，rebuild 的完成事务会在关闭当前任务后检查 semantic 状态，并创建一个后继 rebuild。这样并发写入不会因 active job 唯一约束而丢失重建请求。freeze/unfreeze 等不改变 revision、space、content hash 或 searchable 的操作保留现有 embedding，不创建无效任务。

worker 的非预期 claim、周期维护和终态持久化错误写入结构化日志，字段限于 operation、job ID、attempt/generation 和错误。空队列、lease lost 及正常关闭不作为错误记录。终态事务提交失败后不盲目重试，由 lease fencing 和过期恢复处理；日志不包含原始事件、候选、provider 响应或 secret。

## 6. 幂等和重试

- 任务身份为 `job_type + target_type + target_id + processing_version`。
- 同一身份最多有一个非终态任务。存在非终态任务时，新幂等键 replay/rebuild 返回 `409 job_already_active` 和现有 job ID。
- transient failure 使用有限退避重试。
- permanent、invalid input/output 和 policy failure 进入 failed。
- 达到 max attempts 后进入 failed。
- 显式 retry 保留旧 attempt 历史并创建新 attempt。

显式 retry 在一个事务中设置 status=pending、next_run_at=now，并清空 finished_at、result_code、result_json、last_error_code、last_error_message 和 cancel_requested_at；attempt_count 不归零，历史保留在 job_attempts。

## 7. 队列和过载

任务表是持久化队列。`queue_limit` 统计 pending、waiting 和 retrying backlog。

- 低于软限制：事件和 job 同事务提交，返回 accepted。
- 达到软限制且磁盘安全：job 初始 waiting，返回 deferred。
- 达到硬资源限制：拒绝整个 ingest 事务。
- HTTP 请求不阻塞等待 worker。

不能保存事件却遗漏初始 job。

调度器每次 job 完成、取消、过期以及每秒定时检查后重新计算 backlog。当 `pending + retrying` 低于 soft limit 时，按 `priority DESC, created_at ASC, job_id ASC` 将 waiting 批量转为 pending，直到达到 soft limit。该规则在运行期和启动恢复时相同。

queue hard limit 在总非终态任务数达到 hard limit 时触发 `429 retryable_overload`；磁盘 hard limit 触发 `507 resource_exhausted`。两者都拒绝整个 ingest 事务。

## 8. 重启恢复

启动时：

1. 找出 lease 已过期的 running job。
2. 用恢复时刻关闭旧 attempt，outcome=lease_lost、finished_at=恢复时刻。
3. 可重试任务转 retrying；达到上限则 failed。
4. 递增 generation，使旧 worker 不能提交。
5. 校验目标和幂等状态。
6. 容量允许时将 waiting 转 pending。

## 9. 取消和过期

pending/waiting/retrying 可直接取消。running 先写 cancel request，worker 取消 context；只有当前 worker 或 lease 恢复器能提交 cancelled。

取消不回滚已提交权威数据。超过 expires at 的未运行任务转 expired。

## 10. No-op Processor

未配置 processor 时，extract job 被 no-op processor 成功完成，result code 为 `no_processor`。它不生成记忆，也不重试。
