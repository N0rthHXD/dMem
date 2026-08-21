# ADR 0015：第一阶段不实现 SQLite Schema Migration

- 状态：accepted

## 背景

第一阶段处于初始开发期，尚无需要原地升级的已发布持久化数据库。此时引入迁移执行器、回滚语义和升级兼容测试会增加开发范围。

## 决策

第一阶段使用版本化初始化 SQL 创建新数据库，并保存单一 schema 版本。daemon 打开已有数据库时要求版本完全匹配；不兼容时拒绝普通启动，不运行 ORM AutoMigrate 或自动 DDL。第一阶段未提供导入导出，开发期数据库视为可丢弃。

## 影响

- 第一阶段不需要 migration 库或迁移执行框架。
- 不支持旧 SQLite 数据库原地升级。
- 初始化 SQL、persistence model 和 schema 版本必须同步维护。
- 正式发布需要持久化兼容后，再通过独立 ADR 引入 schema migration。
