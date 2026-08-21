# ADR 0018：进程内强类型 provider 端口

- 状态：accepted
- 日期：2026-08-21

## 决策

Processor 和 EmbeddingProvider 首先作为 daemon 进程内 Go 端口部署。Provider 只接收不可变、强类型、已最小化输入和 `context.Context`，不持有 repository、SQLite 或事务能力。

ProcessingService 分为准备、外部执行和 fenced commit。网络或模型调用发生在 SQLite 写事务之外；提交以 job id、lease owner 和 lease generation 为条件，将结果、状态、attempt、audit 和候选原子写入。deadline、取消、并发和速率限制由服务边界传播。

进程内表示部署形态，不表示实现绑定。远程 HTTP 和未来 subprocess 均通过相同端口隔离；本阶段不提供动态插件 ABI。
