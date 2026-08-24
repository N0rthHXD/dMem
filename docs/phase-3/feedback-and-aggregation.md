# dMeM 第三阶段反馈、排序与聚合

## 1. Feedback 边界

Feedback 是调用方对固定 revision 或检索结果的追加式观察。它与以下对象不同：

- Lifecycle command：改变 memory 当前状态。
- Candidate review：决定 proposal 终态。
- Audit event：记录操作本身。
- Feedback aggregate：从 feedback event 重建的派生统计。

## 2. Feedback 契约

第三阶段支持：

```text
useful
irrelevant
correct
supplement
contest
```

每条反馈至少包含 memory/revision、caller、类型、时间和 operation ID；对搜索结果的反馈还可包含 search operation、query hash、result rank、ranking version 和 matched channels。Reason code 使用固定枚举，用户 note 受长度限制且不进入普通日志。

`correct`、`supplement` 和 `contest` 可以创建演进 proposal，但两者使用独立 ID 和事务结果；proposal 创建失败不能丢失已可靠接受的 feedback event，响应必须区分两步状态。

## 3. 幂等与隐私

- 写入使用 `Idempotency-Key`；同 caller、route、key 和 request hash 返回原 feedback。
- 同 key 不同 payload 返回 idempotency conflict。
- Feedback 不能引用不存在或调用方作用域外的 revision。
- 默认 API 不返回其他 caller 的自由文本 note；统计按最小必要维度公开。
- 日志不记录 query text、memory content 或 note 全文。

## 4. Feedback aggregate

Aggregate 至少按 revision 和算法版本保存：

```text
revision_id
positive_count
negative_count
bounded_usage_score
window_start, window_end
algorithm_version
source_watermark
state, updated_at
```

原始 feedback event 是权威数据，aggregate 可以删除后重建。算法 v1 必须固定：输入类型、去重规则、窗口、caller 上限、分数范围和 tie-break。单 caller 重复反馈不能无界放大影响。

Feedback 到达时记录 durable aggregation job 或推进可恢复 watermark。晚到旧 generation 不能覆盖较新 aggregate version。

## 5. 排序集成

- Keyword、semantic、entity 和 relation 先独立召回并完成基础融合。
- `usage_score` 只作为有上限的后置排序信号，不能绕过 scope、state、valid time、current revision 或 hard budget。
- Exact identifier、entity exact alias 和显式 relation 命中不得因负反馈完全失去召回资格。
- 旧 revision feedback 不继承到新 revision；如需继承只能通过未来显式算法版本决定。
- Search response 返回 ranking 和 feedback aggregate version。
- Cursor 绑定 aggregate version；版本改变后旧 cursor 失效，不跨反馈快照分页。

上线 ranking v2 前必须在固定语料上比较 keyword exact recall、semantic quality、entity/relation recall、无答案率、空间污染和被反馈操纵的最坏情况。

## 6. 场景与主题聚合

第三阶段只实现显式范围、有界输入的 `scene` 和 `topic` 聚合。`profile` 只保留 Schema 边界，不默认生成。

Aggregate proposal 至少包含：

```text
aggregate_kind
space_id
input_revision_ids
input_fingerprint
content and valid time
sensitivity
provider/model/processing version
evidence and derived_from specs
```

输入必须是调用方显式空间内可访问的固定 revision。Provider 在事务外执行，输出通过强类型校验后成为 pending proposal。

## 7. Aggregate confirm

Confirm 前重新检查：

- 所有输入 revision 仍存在，且是否 current 与 proposal 策略一致。
- 输入未跨越不允许的 sibling spaces。
- 输出 sensitivity 不低于输入最大敏感级别。
- 每个事实片段有输入 provenance，且至少能沿输入 evidence 下钻到 raw event。
- Proposal processing identity、input fingerprint 和 reviewer 选择一致。

Confirm 创建普通不可变 aggregate revision，并创建 `derived_from` relations。Aggregate 不能覆盖、删除或静默替代输入原子记忆。

## 8. Freshness 与 rebuild

当输入 memory 的 current revision、state、space 或 sensitivity 改变时：

- Aggregate freshness 标记 stale。
- 默认检索可以继续返回已确认 aggregate，但必须标明 stale；阶段实现也可选择默认排除，选择必须在 OpenAPI 固定。
- 创建去重后的 `rebuild_aggregate` job。
- 新生成内容再次进入审核，不自动替换已确认 revision。

Rebuild 删除缓存后必须可从权威 input relations 重建任务。旧 aggregate revision 和 provenance 永久保留到显式生命周期操作。

## 9. 非目标

- 不根据访问日志生成隐式反馈。
- 不根据 feedback 自动 archive、retract 或 delete。
- 不在线训练模型或自动调整 provider prompt。
- 不做跨空间全局画像或敏感属性推断。
- 不让 aggregate 成为其输入事实的唯一保存位置。
- 不在第三阶段实现自动摘要优先的复杂上下文装配策略。
