# ADR 0019：Go 内置可重建向量索引

- 状态：accepted
- 日期：2026-08-21

## 决策

第二阶段采用纯 Go、进程内的精确 cosine 向量索引，不加载 SQLite extension、动态库或运行时模型。索引文件是派生数据，按版本写入 shadow 文件，校验后通过小型 active metadata 原子激活。

VectorIndex 端口提供 `Upsert`、`Delete`、`Search`、`Build`、`Activate`、`Drop` 和 `Status`。持久格式包含 format version、index version、provider/model、dimension 以及每条 `(memory_id, revision_id, content_hash, vector)`。结果返回前必须回查 SQLite 当前 revision、content hash、scope、state 和 validity。

精确扫描优先保证确定性和可解释性，不引入新外部依赖。发布阈值不满足时后续 ADR 可替换端口实现，但不得改变 SQLite 权威性和完整重建要求。
