# ADR 0026：独立 Evolution proposal resource

- 状态：accepted
- 日期：2026-08-25

## 决策

针对已有 memory 的 evolution proposal 使用独立 resource 和 ID，不扩展第二阶段 candidate。Candidate 继续只表示创建新 memory；evolution action 固定为 `duplicate`、`supplement`、`replacement` 或 `conflict`。Entity mention、relation 和 aggregate 使用各自 proposal resource，但共享不可变 specification、追加式 review、幂等和 generation fencing 规则。

Evolution proposal 保存有序 target 列表，每个 target 固定包含 `memory_id` 和创建时的 `expected_revision_id`，并保存 proposed content/valid time、relation specs、entity mention specs、typed evidence、processing identity 和 specification hash。Specification 创建后不可变；同 processing version、action、有序 targets 和 specification hash 唯一。Review state 固定为 `pending`、`confirmed` 或 `rejected`。

Create/list/get/review 是 UDS HTTP 公共 API。Confirm 先校验所有 target 的 current revision、space、evidence、lifecycle 和 frozen，再在一个事务中提交全部 revision、relation、state/projection、review、idempotency、audit 和 index jobs。任一 target frozen 或 revision 改变均按 ADR 0025 返回 `revision_conflict`，proposal 保持 pending；即使 reviewer 请求 confirm，frozen target 也必须先通过独立 lifecycle API 显式 unfreeze。`duplicate` 不创建 revision，`supplement` 创建同一 memory child revision，`replacement` 必须由 reviewer 选择同一 memory 新 revision或新 memory加 `supersedes`，`conflict` 遵循 ADR 0025。自动 retract、delete、archive 或 unfreeze 返回 `unsupported`。
