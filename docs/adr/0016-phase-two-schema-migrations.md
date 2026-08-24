# ADR 0016：第二阶段前向事务 migration（拒绝）

- 状态：rejected
- 日期：2026-08-21

## 原提案

Schema migration 仅前向执行。daemon 在 ready 前持有 data directory 锁，先以只读方式检查版本；只允许从已声明的最小可迁移版本逐版本升级，未知新版本不打开写连接。

每个 migration 使用单调版本、稳定名称和固定 SHA-256 校验值，并记录到 `schema_migrations`。可事务化 DDL 在单个 `BEGIN IMMEDIATE` 事务中完成，Schema version 最后更新；失败整体回滚。长回填必须拆成兼容 Schema、可恢复后台回填、验证和启用，不在 migration 中持有无限事务。

Schema v1 SQL 和 fixture 冻结，升级测试验证权威数据、历史、idempotency 和任务数据不变。第二阶段 daemon 可读取 Schema v2 及后续本阶段版本，只迁移明确注册的旧版本。

## 结果

该提案不实施。第二阶段和后续阶段在项目负责人明确声明项目开发完成并发布前，均属于初始开发阶段。Schema 变化直接更新完整初始化 SQL，开发数据库重新初始化，不提供 migration、旧 Schema 兼容或版本适配。该规则以 ADR 0015 的更新决策为准。
