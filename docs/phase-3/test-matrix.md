# dMeM 第三阶段测试矩阵

## 1. 测试约定

- `required` 是阶段完成门槛，`optional` 用于真实 provider、大规模或环境相关评估。
- 第二阶段全部回归测试继续运行。
- Processor、clock、ID、index、logical package store 和 provider 均可注入确定性 fake。
- 所有公开 HTTP request/response/error/header 使用真实 OpenAPI validator。
- 故障注入覆盖 provider 返回后、权威事务前、事务提交后、索引切换和 import checkpoint 边界。
- 固定语料包含 sibling-space、stale revision、冲突事实、entity alias、relation exact match、feedback poisoning 和 aggregate stale 场景。
- 当前 65 个 required ID 均由 `TestP3...` 或显式回归 gate 映射，`make phase-3/traceability` 检查该映射。阶段入口、门槛和最近执行状态见[质量报告](quality-report.md)。

## 2. 契约与基线

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-CONTRACT-001 | required | 第二阶段完整回归 | 自动处理、审核、检索、恢复和默认离线均通过 |
| P3-CONTRACT-002 | required | OpenAPI 与初始化 Schema | 所有新对象、枚举、错误和 header 与实现一致 |
| P3-CONTRACT-003 | required | Provider 完整输入 | context memory、sensitivity、typed evidence 和预算不被 adapter 丢失 |
| P3-CONTRACT-004 | required | 未配置 provider | entity/relation/aggregate 自动处理不联网，手工能力可用 |
| P3-CONTRACT-005 | required | Cursor 版本绑定 | corpus、ranking、feedback 或任一通道 index version 改变后旧 cursor 失效 |

## 3. Entity 与 mention

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-ENTITY-001 | required | Confirm entity proposal | entity、alias、mention、review、audit 和 index job 原子创建 |
| P3-ENTITY-002 | required | Mention revision/range 非法 | 无权威 entity assertion 或部分 review |
| P3-ENTITY-003 | required | Alias normalization collision | 产生绑定/merge proposal，不自动 merge identity |
| P3-ENTITY-004 | required | Sibling-space entity | 创建和检索均拒绝污染，返回数为 0 |
| P3-ENTITY-005 | required | Stale revision mention | Entity recall 不返回旧 revision 内容 |
| P3-ENTITY-006 | required | Entity index 删除/rebuild | 从 SQLite authority 完整重建且结果一致 |
| P3-ENTITY-007 | required | Merge 请求 | 明确 unsupported 或保持 schema-only，无隐式合并 |

## 4. Relation 与 conflict

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-REL-001 | required | Confirm relation proposal | Assertion、evidence、review、audit 和 index job 原子提交 |
| P3-REL-002 | required | Duplicate relation confirm | 幂等返回原 assertion，不重复创建 |
| P3-REL-003 | required | Endpoint revision/space 非法 | 拒绝且无部分 assertion |
| P3-REL-004 | required | One-hop query | 类型、方向、空间和 current revision 复核正确 |
| P3-REL-005 | required | Relation index 损坏 | 权威 get 可用，检索明确降级并可重建 |
| P3-CONFLICT-001 | required | Confirm conflict proposal | contradicts、case、contested state、review 和索引任务原子提交 |
| P3-CONFLICT-002 | required | Search contested | 返回双方必要结果和 conflict refs，不合并成无来源摘要 |
| P3-CONFLICT-003 | required | Concurrent resolution | 只有一个终态，resolution history 一致 |
| P3-CONFLICT-004 | required | Multi-target revision changed | 整体 revision_conflict，无部分 state/relation 更新 |
| P3-CONFLICT-005 | required | Frozen participant | 自动/审核 revise 被拒绝，可保留 proposal |
| P3-CONFLICT-006 | required | Automatic destructive action | 返回 unsupported，不 retract/delete 历史 |

## 5. Existing-memory evolution

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-EVOLVE-001 | required | Supplement confirm | 新 child revision、evidence、projection 和 jobs 原子提交 |
| P3-EVOLVE-002 | required | Replacement confirm | supersedes relation、revision/state history 符合 reviewer 选择 |
| P3-EVOLVE-003 | required | Confirm 幂等重放 | 不重复 revision、relation、review 或 job |
| P3-EVOLVE-004 | required | Lease lost before commit | 旧 generation 无法写 proposal 或权威结果 |
| P3-EVOLVE-005 | required | Provider success + transaction failure | 无 confirmed proposal 或孤儿 revision |
| P3-EVOLVE-006 | required | Context memory used without evidence | 不能自动成为 revision evidence |

## 6. Feedback 与排序

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-FEEDBACK-001 | required | useful/irrelevant 写入 | Feedback 幂等追加并固定绑定观察 revision |
| P3-FEEDBACK-002 | required | Feedback lifecycle isolation | 不改变 memory current state |
| P3-FEEDBACK-003 | required | correct/supplement | Feedback 保留，proposal 创建状态明确且不直接 revise |
| P3-FEEDBACK-004 | required | Aggregate rebuild | 从事件重建得到相同 version、watermark 和 bounded score |
| P3-FEEDBACK-005 | required | Late/stale worker | 旧 generation 不能覆盖新 aggregate |
| P3-FEEDBACK-006 | required | New revision | 旧 revision score 不自动继承 |
| P3-RANK-001 | required | Ranking v2 固定语料 | exact/entity/relation recall 不低于门槛，质量指标达到声明值 |
| P3-RANK-002 | required | Feedback poisoning | 单 caller/少量事件影响不超过固定上限 |
| P3-RANK-003 | required | Aggregate version switch | 旧 cursor invalid_request，不跨版本分页 |

## 7. Aggregation

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-AGG-001 | required | Build scene/topic proposal | 输入有界、provider 在事务外、processing identity 完整 |
| P3-AGG-002 | required | Confirm aggregate | Revision 与全部 derived_from provenance 原子创建 |
| P3-AGG-003 | required | Missing input evidence | Confirm 被拒绝，不创建不可下钻 aggregate |
| P3-AGG-004 | required | Input revision/state changed | Aggregate 标记 stale 并创建去重 rebuild job |
| P3-AGG-005 | required | Sensitivity | 输出敏感级别不低于任一输入 |
| P3-AGG-006 | required | Sibling-space input | 聚合拒绝，不跨空间泄露 |
| P3-AGG-007 | required | Autonomous profile | 不生成，返回 deferred/unsupported 契约 |

## 8. Logical export/import

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-PORT-001 | required | Export dry-run | 闭包、数量、敏感摘要和外部引用准确，无写入 |
| P3-PORT-002 | required | Deterministic export | 固定快照和参数产生相同对象内容/checksum |
| P3-PORT-003 | required | Concurrent writes during export | 包来自单一一致性快照，无断裂 revision/relation |
| P3-PORT-004 | required | Disk/checksum failure | 不发布不完整 package |
| P3-PORT-005 | required | Invalid/unknown format | 写入前拒绝，目标实例不变 |
| P3-PORT-006 | required | Import dry-run mapping | Space、ID、duplicate 和 conflict 计划稳定可审计 |
| P3-PORT-007 | required | Repeated import | Source mapping 幂等，不重复创建对象 |
| P3-PORT-008 | required | Import conflict | 创建 proposal，不覆盖本地 current revision |
| P3-PORT-009 | required | Interrupted batch | 从 checkpoint 恢复，无对象缺失 mapping/provenance |
| P3-PORT-010 | required | Export → fresh instance import | 权威对象、空间隔离和检索语义一致，索引可重建 |
| P3-PORT-011 | required | Malicious package | Symlink、路径穿越、额外成员和超限记录被拒绝 |

## 9. 恢复、质量与性能

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P3-DATA-001 | required | Backup/restore 新权威对象 | Entity/relation/conflict/feedback/provenance/import mapping 完整 |
| P3-DATA-002 | required | Restore running new jobs | Attempt 关闭、generation fenced、checkpoint/状态一致 |
| P3-DATA-003 | required | Restore 后派生数据 | 全部新增 index/aggregate 可重建且 health 明确 |
| P3-QUAL-001 | required | Sibling-space corpus | 所有召回通道和 aggregate 污染率为 0 |
| P3-QUAL-002 | required | Stale revision corpus | Entity/relation/aggregate 返回旧权威内容为 0 |
| P3-QUAL-003 | required | Conflict corpus | Conflict recall 和引用完整度达到声明门槛 |
| P3-PERF-001 | required | 目标规模多通道查询 | p95、内存和候选上限达到阶段门槛 |
| P3-PERF-002 | required | Entity/relation/feedback rebuild | 时间、内存和磁盘放大达到阶段门槛 |
| P3-PERF-003 | required | 目标规模 export/import | 吞吐、checkpoint 和临时空间达到阶段门槛 |
| P3-PERF-004 | optional | 真实生产 provider | 记录演进与聚合延迟，不进入默认离线 CI |

## 10. CI 最小矩阵

- Linux amd64，最低支持 Go 版本和当前稳定 Go 版本。
- CGO 开启，固定 SQLite/vector build profile。
- Unit、integration、race、OpenAPI validation、Schema initialization、index rebuild、backup/restore drill 和 export/import drill。
- 默认 CI 禁止外网，所有 provider 使用本地 fake/server。
- 固定质量语料和性能门槛版本化；ranking、normalization、logical format 或 index 算法变化时显式升级版本。
