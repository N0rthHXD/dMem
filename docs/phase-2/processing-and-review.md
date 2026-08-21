# dMeM 第二阶段处理器与候选审核

## 1. Processor 输入

Processor 接收不可变、受预算限制的输入：

```text
processor_identity
processing_version
job_id
attempt
event { event_id, space_id, source_type, source_name, occurred_at, content }
context_events[]
context_memories[]
policy { allowed_memory_types, max_candidates, max_content_chars }
deadline
```

外部 reference 默认只发送引用元数据，不读取或上传目标内容。Context 仅来自当前空间及文档允许的祖先范围，不自动加入兄弟、后代、agent 或 application 空间。

## 2. Processor 输出

Outcome 保持稳定枚举：

```text
no_processor
no_memory
candidates_extracted
transient_failure
permanent_failure
invalid_output
policy_rejected
```

`candidates_extracted` 返回一个有界候选数组。每个候选至少包含：

```text
content
memory_type
space_id
valid_from
valid_until
confidence
evidence_refs[]
suggestion_kind
related_memory_ids[]
```

`suggestion_kind` 可为 `new`、`duplicate`、`supplement`、`replacement` 或 `conflict`。它只表达建议，不执行修订或生命周期变化。

Provider 结果还可以包含 usage、provider request ID 和可公开错误摘要。原始 provider 响应不作为 API 错误直接返回。

## 3. 输出校验

业务层在提交前验证：

- outcome 与字段组合一致；
- 候选数量、内容长度和总输出大小不超过策略；
- memory type 属于当前 Schema 支持枚举；
- space 等于事件空间或明确允许的目标空间；
- evidence 只引用输入中允许的 raw event，offset 合法且不越界；
- valid interval 满足 `[valid_from, valid_until)`；
- confidence 是有限数且位于 `[0,1]`；
- related memory 存在且在可见作用域内；
- provider 未请求不允许的状态变化或删除操作。

结构不合法返回 `invalid_output`，不能转换为 `no_memory`。策略拒绝返回 `policy_rejected`，并保留不含敏感内容的原因码。

## 4. 错误和重试

| Outcome | Job 行为 | Event 状态 |
| --- | --- | --- |
| `no_processor` | succeeded，不重试 | `stored` |
| `no_memory` | succeeded，不重试 | `processed` |
| `candidates_extracted` | 候选提交后 succeeded | `processed` |
| `transient_failure` | 按现有退避和 attempt 规则重试 | `processing`，终态耗尽后 `failed` |
| `permanent_failure` | failed | `failed` |
| `invalid_output` | failed，不自动重试 | `failed` |
| `policy_rejected` | succeeded，不重试并保留原因码 | `stored` |

只有 `transient_failure` 自动重试。网络超时、限流和暂时服务不可用可以映射为 transient；认证失败、模型不存在和稳定的请求拒绝通常为 permanent。

## 5. Processing version 与幂等

- 每个 extract job 在创建时固定 processing version，不从运行时动态读取最新值。
- 同一 event 和 processing version 最多有一个活跃 extract job。
- candidate 规范哈希至少覆盖 event、processing version、规范化内容、类型、空间、有效时间和 evidence。
- job 重试、lease 恢复或 provider 重复响应不能创建重复 candidate。
- 按新 processing version replay 创建新 job 和新候选集，不修改旧候选或旧审核记录。

## 6. 候选状态机

```text
pending ──confirm──> confirmed
   └─────reject────> rejected
```

- `pending` 候选不参与默认 memory list/search。
- `confirmed` 保存创建的 `memory_id` 和 `revision_id`。
- `rejected` 保存原因码和可选人工备注。
- confirmed/rejected 为终态；纠正审核通过新的显式业务操作完成，不改写审核历史。
- processing replay 产生新候选，不重置旧候选。

## 7. 候选 API 契约草案

第二阶段 OpenAPI 至少增加：

```text
GET  /v1/candidates
GET  /v1/candidates/{candidate_id}
POST /v1/candidates/{candidate_id}/confirm
POST /v1/candidates/{candidate_id}/reject
```

list 支持 `space_id`、review state、memory type、processor identity、processing version 和 created time，使用稳定 cursor。get 返回 evidence reference、provider/model identity、confidence、suggestion kind 和审核历史，但不返回 provider 凭据或原始响应。

confirm/reject 必须携带 `X-Caller-ID` 和 `Idempotency-Key`。confirm 可接受必要的人工修正字段，但实际写入内容及其规范哈希必须进入幂等请求；不能在重放时重新应用当前默认值。

## 8. 确认事务

confirm 同一事务完成：

1. 检查 candidate 为 pending。
2. 校验 candidate evidence 和作用域仍有效。
3. 创建 active memory 和首个不可变 revision。
4. 复制或明确转换 evidence。
5. 创建首个 state event、projection 和 FTS 行。
6. 标记 candidate confirmed 并绑定 memory/revision。
7. 创建 embedding job 或持久化 embedding stale 标记。
8. 保存幂等响应和 audit。

并发 confirm/reject 使用 candidate 当前状态作为条件。失败或竞争不能留下部分提交。

## 9. 远程数据外发

远程 provider 仅在全部条件满足时调用：

- provider 显式启用；
- event space 和 content type 在 allowlist；
- sensitivity 和来源策略允许；
- 输入经过字符、上下文和字段最小化；
- endpoint、model 和 secret reference 配置有效；
- 审计能够记录策略版本和目标 provider。

默认拒绝优先于 provider 可用性。策略拒绝不通过重试绕过。日志、metrics label 和错误摘要不得包含原始内容。

## 10. 冻结和非破坏性规则

第二阶段候选可以引用 frozen memory 并给出 duplicate/conflict 建议，但 confirm 只创建新 memory，不修订 frozen memory。自动 revise、supersede、archive、retract 和 delete 均不在本阶段范围。人工调用现有 revise/lifecycle API 时继续执行 expected revision 和现有状态机。
