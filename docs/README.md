# dMeM 文档

## 开发状态

dMeM 当前处于未发布开发阶段。第一阶段、第二阶段和第三阶段的计划范围均已实现，当前第三阶段验收入口为 `make phase-3/check`；这表示当前开发基线完成，不表示已发布版本，也不承担历史数据迁移、旧接口兼容或旧实现适配义务。不兼容变更直接更新当前 Schema、OpenAPI 和实现，开发数据库重新初始化。

只有项目负责人明确声明项目开发完成并发布后，才重新评估数据迁移、向后兼容和升级策略。阶段编号本身不触发该义务。

## 规范权威顺序

发生描述冲突时，按以下顺序判断：

1. 当前阶段能力矩阵：决定该阶段是否实现某项能力。第一阶段见 [第一阶段能力矩阵](phase-1/scope.md)，第二阶段见 [第二阶段能力矩阵](phase-2/scope.md)，第三阶段见 [第三阶段能力矩阵](phase-3/scope.md)。
2. 当前 [OpenAPI](api/openapi.yaml) 和 [Schema v1](storage/schema-v1.sql)：定义已实现的 HTTP 和 SQLite 可执行契约。逻辑导入导出不在 OpenAPI 中，其当前可执行边界见[逻辑导入导出](phase-3/logical-portability.md)。
3. 领域主题文档：定义对象语义、状态、作用域、幂等和运行规则。
4. [总体架构](architecture/overview.md)：描述完整系统方向和后续阶段能力。
5. [ADR](adr/README.md)：记录决策背景和原因，不覆盖更新后的可执行规范。

## 第一阶段入口

- [能力矩阵](phase-1/scope.md)
- [测试矩阵](phase-1/test-matrix.md)
- [处理器契约](phase-1/processing-providers.md)
- [OpenAPI](api/openapi.yaml)
- [Schema v1](storage/schema-v1.sql)

## 第二阶段入口

- [阶段说明与文档索引](phase-2/README.md)
- [当前实现基线](phase-2/current-baseline.md)
- [能力矩阵](phase-2/scope.md)
- [目标架构](phase-2/architecture.md)
- [处理器与候选审核](phase-2/processing-and-review.md)
- [语义与混合检索](phase-2/semantic-retrieval.md)
- [备份与恢复](phase-2/backup-and-restore.md)
- [测试矩阵](phase-2/test-matrix.md)
- [交付计划](phase-2/delivery-plan.md)

## 第三阶段入口

- [阶段说明与文档索引](phase-3/README.md)
- [当前实现基线](phase-3/current-baseline.md)
- [能力矩阵](phase-3/scope.md)
- [目标架构](phase-3/architecture.md)
- [实体、关系与冲突演进](phase-3/evolution.md)
- [反馈、排序与聚合](phase-3/feedback-and-aggregation.md)
- [逻辑导入导出](phase-3/logical-portability.md)
- [测试矩阵](phase-3/test-matrix.md)
- [交付计划](phase-3/delivery-plan.md)
- [质量报告](phase-3/quality-report.md)

## 架构

- [总体架构](architecture/overview.md)
- [领域模型](architecture/domain-model.md)
- [记忆处理](architecture/memory-processing.md)
- [记忆生命周期](architecture/memory-lifecycle.md)
- [检索与上下文装配](architecture/retrieval.md)
- [空间与作用域](architecture/scope-model.md)
- [原始内容与附件](architecture/attachment-storage.md)

## API

- [API 设计](api/design.md)
- [OpenAPI](api/openapi.yaml)

## 存储

- [SQLite 存储设计](storage/sqlite.md)
- [Schema v1](storage/schema-v1.sql)
- [幂等契约](storage/idempotency.md)
- [备份、恢复与迁移](storage/backup-and-migration.md)

## 运行治理

- [Daemon 运行设计](operations/daemon.md)
- [配置设计](operations/configuration.md)
- [异步任务处理](operations/job-processing.md)
- [错误与健康状态](operations/errors-and-health.md)
- [隐私、保留与删除](operations/privacy-and-retention.md)
- [维护与恢复](operations/maintenance.md)
- [运行故障手册](operations/failure-runbook.md)

## 开发

- [需求说明](development/requirements.md)
- [依赖准入策略](development/dependency-policy.md)
- [测试与质量评估](development/testing-and-evaluation.md)

## 架构决策

- [ADR 目录](adr/README.md)
