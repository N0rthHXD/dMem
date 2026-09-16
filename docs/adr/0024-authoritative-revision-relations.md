# ADR 0024：独立权威 revision relation

- 状态：superseded（被 [0035](0035-memory-retrieval-v4.md) 取代：`related_to`/`mentions` 关系类型移除，revision relation 保留 `supports`/`contradicts`/`supersedes`/`derived_from`）
- 日期：2026-08-25
- 关联：被 [0035](0035-memory-retrieval-v4.md) 取代

## 决策

Memory relation 是具有稳定 `relation_id` 的独立权威 assertion，不是普通 memory、candidate related ID 或派生索引。公开 relation type 固定为 `supports`、`contradicts`、`supersedes`、`derived_from`、`related_to` 和 `mentions`；前四种有方向，`related_to` 按 endpoint identity 排序后保存。前五种的两个 endpoint 都绑定具体 `(memory_id, revision_id)`；`mentions` 由 ADR 0023 的 `entity_mention` 单一权威对象承载，并在 relation query 中投影，不重复写 memory relation。

Relation state 固定为 `active` 或 `dismissed`，来源固定为 `processor_review`、`human_confirmation`、`conflict_resolution` 或 `import`，evidence type 固定为 `raw_event_range`、`revision_range` 或 `human_confirmation`。State history 和 evidence 仅追加；所有固定 relation type 禁止同一 revision self relation。相同 type、规范化 endpoint 和 specification hash 的重复 confirm 返回已有 assertion，不同方向或 revision 是不同 assertion。

Relation proposal create/list/get/review、relation get/list 和有界一跳 query 是 UDS HTTP 公共 API；不提供多跳路径语言。Confirm 原子校验 endpoint revision、space、evidence、frozen 和幂等状态，并写 assertion、history、review、audit 和 index job。任一 endpoint frozen 时按 ADR 0023 失败并保持 proposal pending。Relation get 从 SQLite authority 提供；索引故障只使 query/recall 明确降级。
