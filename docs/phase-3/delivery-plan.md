# dMeM 第三阶段交付计划

> 状态：实现完成。各切片和 required test 映射已交付；本文保留交付顺序和退出条件作为追溯记录，最近 `make phase-3/check` 状态见[质量报告](quality-report.md)。

## 1. 交付策略

按可独立验证的垂直切片交付。优先完成权威关系、冲突和已有记忆演进闭环，再引入反馈排序、聚合和逻辑迁移。每个切片完成后 daemon 保持可运行，未启用 provider 默认关闭，新索引失败可明确降级。

## 2. 切片 A：契约、先修与当前 Schema

交付：

- 冻结 [能力矩阵](scope.md)、测试 ID 和文档权威顺序。
- 完成 README 中列出的 entity、relation、conflict、proposal、feedback、aggregate 和 logical format ADR。
- 修正 provider context/evidence/sensitivity 契约。
- 引入权威 corpus generation，并将 cursor 绑定新增版本。
- 更新完整初始化 Schema 和 OpenAPI 草案，不实现 migration runner。
- 扩展 backup manifest 与 restore job 状态验证。

退出条件：

- 第二阶段全部测试通过。
- 新建数据库一次初始化并通过完整不变量检查。
- 默认配置不联网。
- 所有第三阶段对象的权威/派生身份有唯一解释。

## 3. 切片 B：Entity 与 mention

交付：

- Entity、alias、mention 权威 Schema 和 append-only 来源。
- Entity proposal confirm/reject、list/get API。
- 固定 normalization version 和 merge proposal 边界。
- Entity index、独立 recall、rebuild、health 和降级。

退出条件：

- Mention 均绑定有效 revision/event range。
- Alias collision 不自动 merge。
- Stale revision 和 sibling-space 返回为 0。
- 删除索引后可完整重建。

## 4. 切片 C：Relation 与一跳查询

交付：

- 独立 relation、evidence、state history Schema。
- Relation proposal review 和幂等 API。
- 固定 relation type、方向和 endpoint revision 契约。
- Relation index、一跳 query/recall、health 和 rebuild。

退出条件：

- Candidate related IDs 不能绕过 review 形成 relation。
- 并发 confirm 只产生一个 assertion。
- Endpoint 作用域和 current revision 复核通过。

## 5. 切片 D：冲突与已有记忆演进

交付：

- Evolution proposal 的 supplement/replacement/conflict action。
- Conflict case、participant 和 resolution history。
- 多目标 expected revision 与 frozen 检查。
- Confirm 原子创建 revision/relation/state/projection/index jobs。
- Search/get conflict refs 和显式 resolution API。

退出条件：

- 并发审核/解决只有一个终态。
- 任一 target 改变时没有部分提交。
- Frozen memory 不被自动或审核动作绕过。
- 自动破坏性 action 明确拒绝。

## 6. 切片 E：Feedback 与版本化排序

交付：

- Feedback event write/list API 和幂等契约。
- Useful/irrelevant 与 correct/supplement proposal 联动。
- Durable feedback aggregate、watermark、rebuild 和 health。
- Ranking v2、影响上限、解释字段和 cursor version。
- 固定质量与 poisoning 语料。

退出条件：

- Feedback 不改变 lifecycle，旧 revision 分数不继承。
- Aggregate 删除后可重建。
- Ranking 质量、精确召回和操纵上限达到门槛。

## 7. 切片 F：场景与主题聚合

交付：

- Aggregate proposal、input fingerprint 和 `derived_from` provenance。
- `build_aggregate`/rebuild durable job 和 fencing。
- Scene/topic confirm、drill-down、freshness 和 health。
- Sensitivity 继承与 stale 策略。
- Profile schema-only 边界。

退出条件：

- 无 provenance 的 aggregate 不能确认。
- 输入变化可检测并排入重建。
- 跨 sibling space 和 sensitivity 降级均为 0。
- 不发生自治 profile 生成。

## 8. 切片 G：Logical Export v1

交付：

- Selection/closure planner 和 dry-run。
- Versioned deterministic JSONL package、manifest 和 checksums。
- SQLite read snapshot、一致性验证和原子发布。
- 敏感对象与 external reference 报告。

退出条件：

- 并发写入期间导出仍为单一一致快照。
- 相同输入的对象内容/checksum 确定。
- 失败不发布半成品，不导出 secret 或派生索引。

## 9. 切片 H：Logical Import v1

交付：

- Strict package validation 和 dry-run mapping。
- Durable import operation、batch、checkpoint 和 generation fencing。
- Source/local ID mapping、imported provenance 和幂等重放。
- Duplicate/conflict 到 evolution proposal 的桥接。
- 完成后的派生数据 rebuild。

退出条件：

- 未知格式、恶意包在写入前拒绝。
- 中断后可恢复，已提交对象都有 mapping 和 provenance。
- 重复导入不重复创建，冲突不覆盖本地 current revision。

## 10. 切片 I：综合恢复与阶段完成

交付：

- Backup/restore 覆盖新增权威表、job 和 import checkpoint。
- Entity/relation/feedback/aggregate 全量 rebuild drill。
- Export → fresh instance import 迁移演练。
- 运维故障手册、最终 OpenAPI/Schema 和质量报告。

退出条件：

- Restore staging 通过新增引用、空间、fencing 和索引检查。
- [能力矩阵](scope.md)完成标准和[测试矩阵](test-matrix.md)全部 required 项通过。
- 默认离线、非破坏性自动化和 sibling-space 零污染不变量保持成立。

## 11. 依赖关系

```text
切片 A
  → 切片 B
  → 切片 C
  → 切片 D

切片 A
  → 切片 E

切片 C + 切片 D
  → 切片 F

切片 B + 切片 C + 切片 D + 切片 F
  → 切片 G
  → 切片 H

切片 D + 切片 E + 切片 F + 切片 H
  → 切片 I
```

Entity 与 Feedback 可在切片 A 后并行；Relation 契约稳定前不实现 conflict case；Export 格式必须在全部 required 权威对象稳定后冻结；Import 必须晚于 Export validation。

## 12. 风险与控制

| 风险 | 控制 |
| --- | --- |
| 把 related-memory suggestion 当作权威关系 | 独立 relation 对象、显式 review、endpoint revision 和 evidence |
| Entity 误合并污染大量关系 | 第三阶段只保存 merge proposal，不自动执行 |
| Conflict 只用 contested flag 表达 | 独立 relation、case 和 resolution history |
| 多目标演进部分提交 | 全目标 expected revision 检查和单事务提交 |
| Feedback poisoning | Caller 上限、时间窗口、bounded score、固定评测和版本化算法 |
| Aggregate 隐藏冲突或泄露敏感信息 | 全量 provenance、conflict refs、sensitivity 上取高值、人工审核 |
| 新通道导致空间污染或 stale 命中 | Recall 前置 scope、返回前 authority recheck、零污染测试 |
| Cursor 因写入/反馈变化漂移 | Corpus、ranking、aggregate 和各 index version 全绑定 |
| Import 绕过领域状态机 | Strict validation、source mapping、proposal bridge、禁止直接 SQL |
| 大包长事务和不可恢复 | Durable operation、有界批次、checkpoint 和 generation fencing |
| 逻辑迁移与 Schema migration 混淆 | 独立 format version，明确拒绝 SQLite 文件和旧 Schema |
| 范围扩张为图平台或自治画像 | 多跳、自动 merge、自动裁决和自治 profile 明确 deferred |
