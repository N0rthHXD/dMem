# dMeM 第三阶段文档

## 1. 阶段目标

第三阶段已将第二阶段的“可审核自动提取与语义混合检索”扩展为“可追溯的记忆演进、实体关系辅助检索、显式反馈闭环和逻辑数据便携”。实现和 required test 映射已完成；当前验收命令及最近验证状态见[质量报告](quality-report.md)。

当前实现满足：

- 自动处理可以提出针对已有记忆的补充、替代和冲突建议，但不能绕过人工审核、乐观并发和 frozen 规则修改权威记忆。
- 实体 mention 和记忆关系成为有证据、可审计的权威对象；实体与关系索引仍是可重建派生数据。
- 冲突可以按具体 revision 建模、查询、审核和解决，检索结果不会把相互矛盾的事实合并成无来源结论。
- 调用方可以追加 `useful`、`irrelevant` 等显式反馈，排序只使用版本化、有限影响且可重建的反馈聚合。
- 场景或主题聚合可以从明确的原子 revision 生成、审核、下钻和失效，不成为唯一事实来源。
- 当前逻辑格式可以按显式空间范围导出、校验和幂等导入；导入冲突进入正常演进流程，不覆盖本地当前 revision。

第三阶段完成不改变项目的未发布状态。Schema 变化仍直接更新完整初始化 SQL、OpenAPI 和实现，开发数据库重新初始化；逻辑记忆迁移不等于 SQLite Schema migration。

## 2. 设计依据

- [当前实现基线](current-baseline.md)中的第二阶段实际代码能力。
- [总体架构](../architecture/overview.md)中的“阶段三：记忆演进与反馈”。
- [领域模型](../architecture/domain-model.md)、[记忆处理](../architecture/memory-processing.md)和[检索设计](../architecture/retrieval.md)。
- [备份、恢复与迁移](../storage/backup-and-migration.md)中的逻辑便携方向。
- 现有 ADR 关于原始事实、不可变 revision、可重建索引、人工审核、版本化排序和非破坏性更新的约束。

总体架构中的阶段三条目仍是方向性摘要。本目录描述已实现的实体、关系、冲突、反馈、聚合和逻辑迁移边界；公开 HTTP 和 SQLite 细节分别以当前 OpenAPI 和 Schema 为准。

## 3. 文档权威顺序

第三阶段发生描述冲突时，按以下顺序判断：

1. [第三阶段能力矩阵](scope.md)。
2. 当前 OpenAPI 和完整初始化 Schema。
3. 本目录专题设计和[测试矩阵](test-matrix.md)。
4. 现有领域主题文档和总体架构。
5. ADR。

本目录是第三阶段已实现设计与验收记录。只有 OpenAPI 中声明的操作属于公开 HTTP 契约；逻辑便携当前仅有 `internal/portability` Go library 和测试/演练入口，不存在 `dmemd export`、`dmemd import` 或对应 HTTP 路径。

## 4. 可执行入口

- 完整阶段检查：`make phase-3/check`。
- HTTP 契约检查：`make openapi`；运行时入口为 UDS 上的 `/v1` OpenAPI 路径。
- Schema 文档、嵌入 SQL 和空库初始化：`make schema-check`；保留空库使用 `make schema-init DATA_DIR=...`。
- 备份恢复演练：`make restore-drill`；实际运维命令仅为 `dmemd backup` 和 `dmemd restore`。
- 逻辑便携演练：`make export-import-drill`；产品集成入口为 `portability.NewExporter` 和 `portability.NewImporter`，当前无公开 CLI/API。
- Phase 2/3 固定质量和性能门槛：`make retrieval-gates`。
- 默认离线验证：`make offline-default`。

## 5. 文档索引

- [当前实现基线](current-baseline.md)：说明第三阶段完成后的实际能力、边界和已落实先修。
- [能力矩阵](scope.md)：定义 required、schema-only、unsupported 和 deferred 能力。
- [实现架构](architecture.md)：定义组件、权威边界、数据流和共同不变量。
- [实体、关系与冲突演进](evolution.md)：定义权威 assertion、proposal 和人工解决流程。
- [反馈、排序与聚合](feedback-and-aggregation.md)：定义反馈事件、排序信号和场景聚合。
- [逻辑导入导出](logical-portability.md)：定义便携格式、闭包、映射和导入事务。
- [测试矩阵](test-matrix.md)：定义 required 验收场景。
- [交付计划](delivery-plan.md)：定义决策门、垂直切片和退出条件。
- [质量报告](quality-report.md)：记录门槛、执行证据和一次可复现阶段检查结果。
- [运行故障手册](../operations/failure-runbook.md)：给出恢复、重建、导入 checkpoint 和安全事件处置顺序。

## 6. 已落实的阶段决策

以下决策已在对应 ADR 中形成并由实现/测试落实：

| 决策 | 最迟时间 | 必须回答的问题 |
| --- | --- | --- |
| Entity identity 与 mention 模型 | 切片 B 前 | entity 是否版本化；alias、mention、merge proposal 和空间边界如何表达 |
| Relation 权威模型 | 切片 C 前 | relation 是否独立对象；端点绑定 memory 还是 revision；状态和证据如何追加 |
| Conflict case 与 contested 分工 | 切片 D 前 | relation、case、proposal 和 memory state 如何原子协作 |
| Evolution proposal 模型 | 切片 D 前 | 扩展 candidate 还是建立独立对象；多目标 expected revision 如何表达 |
| Feedback 与排序版本 | 切片 E 前 | 反馈身份、聚合窗口、影响上限、cursor 和评测门槛 |
| Aggregate 权威边界 | 切片 F 前 | 聚合内容、输入 provenance、stale 和审核语义 |
| Logical format v1 | 切片 G 前 | 闭包规则、ID 映射、敏感数据、批次恢复和格式版本 |

## 7. 范围控制

第三阶段不建设通用知识图谱，不做自动实体合并、自动冲突裁决、自治长期画像、在线训练、跨主机同步、多用户 ACL 或 SQLite 旧库升级。若这些能力被请求，按 [scope.md](scope.md) 明确 deferred 或 unsupported，不能以隐式简化行为代替。
