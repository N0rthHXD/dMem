# ADR 0017：独立候选记忆与人工审核

- 状态：accepted
- 日期：2026-08-21

## 决策

自动处理结果持久化为独立 `candidate` 对象，不复用 `memories.current_state = candidate`。旧枚举为 Schema v1 兼容保留，但第二阶段不创建该状态的 memory。

Candidate 内容、类型、空间、有效期、证据、processor/provider identity、processing version 和规范化 hash 不可变；review history 仅追加。`pending` 只能原子转换为 `confirmed` 或 `rejected`。确认创建一个全新的 active memory 和首个 revision，并在同一事务中写 evidence、projection、FTS、embedding stale/job、idempotency 和 audit。拒绝不创建 memory。

同一 event、processing version 和 candidate specification hash 唯一。related memory 仅为审核建议，不建立权威关系，也不允许自动修订或改变已有 memory 生命周期。
