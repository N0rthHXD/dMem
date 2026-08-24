# ADR 0015：未发布开发阶段不实现 SQLite Schema Migration

- 状态：accepted

## 背景

项目处于初始开发期，尚无需要原地升级的已发布持久化数据库。阶段编号不代表发布。此时引入迁移执行器、回滚语义和升级兼容测试会增加开发范围。

## 决策

项目明确完成并发布前，所有开发阶段都使用版本化初始化 SQL 创建新数据库，并保存单一 schema 版本。daemon 打开已有数据库时要求版本完全匹配；不匹配时拒绝普通启动，不运行 ORM AutoMigrate 或自动 DDL。开发期数据库视为可丢弃，Schema 变化后重新初始化。

## 影响

- 未发布开发阶段不需要 migration 库或迁移执行框架。
- 不支持旧 SQLite 数据库原地升级。
- 初始化 SQL、persistence model 和 schema 版本必须同步维护。
- 只有项目负责人明确声明项目开发完成并发布后，才通过独立 ADR 重新评估 schema migration、历史数据兼容和版本适配。
