# ADR 0012：允许 ORM 并限制使用边界

- 状态：accepted

## 背景

ORM 可以减少普通 CRUD 样板，但可能隐藏 SQL、事务和领域规则。

## 决策

允许 ORM 用于 repository 内的持久化模型和普通 CRUD。schema 初始化、FTS、幂等写入、条件版本更新、任务领取、备份及性能敏感查询使用显式 SQL。禁止 ORM AutoMigrate。

## 影响

- ORM model 与 API、领域对象分离。
- 应用服务显式控制事务边界。
- ORM 选型需要通过 SQLite 行为验证。
