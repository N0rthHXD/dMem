# ADR 0016：第二阶段前向事务 migration

- 状态：accepted
- 日期：2026-08-21

## 决策

Schema migration 仅前向执行。daemon 在 ready 前持有 data directory 锁，先以只读方式检查版本；只允许从已声明的最小可迁移版本逐版本升级，未知新版本不打开写连接。

每个 migration 使用单调版本、稳定名称和固定 SHA-256 校验值，并记录到 `schema_migrations`。可事务化 DDL 在单个 `BEGIN IMMEDIATE` 事务中完成，Schema version 最后更新；失败整体回滚。长回填必须拆成兼容 Schema、可恢复后台回填、验证和启用，不在 migration 中持有无限事务。

Schema v1 SQL 和 fixture 冻结，升级测试验证权威数据、历史、idempotency 和任务数据不变。第二阶段 daemon 可读取 Schema v2 及后续本阶段版本，只迁移明确注册的旧版本。

## 结果

不支持自动降级。迁移失败保持原版本；需要不可事务化步骤时，先生成一致性备份并保持相关能力 degraded。
