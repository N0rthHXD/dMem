# dMem

dMem 是一个面向 Agent 与应用的本地单用户记忆守护进程。它以 SQLite 存储版本化、
结构化的记忆及其证据，通过带门控类型化图谱特征的关键词与语义召回检索受 scope
约束的候选，并通过协议无关的 Memory Gateway 暴露其语义。外部处理与 embedding
provider 默认关闭。

> **状态**：项目处于预发布阶段。Schema v1 没有迁移路径；schema 不兼容的数据库
> 必须由新初始化的数据目录替代。`dmemd daemon` 默认在回环地址启动 REST+JSON
> 监听服务；TLS 可选，认证与网络边缘授权仍属后续独立工作。

## 能力

- **版本化记忆权威**：稳定的逻辑 ID、不可变修订、仅追加状态事件、证据与当前
  投影；修订永不原地编辑，逻辑删除保留完整审计历史。
- **原始事件摄取与异步处理**：事件与所需处理作业在同一事务中提交；默认处理器
  为 no-op，因此没有外部 provider 时事件捕获依然可用。
- **可审核的知识构建**：实体、别名、提及、类型化实体事实与记忆关系以提案形式
  产生，经确定性校验与风险分类后自动确认或进入人工审核。
- **混合检索**：关键词（SQLite FTS5）与语义（sqlite-vec 精确余弦）作为独立的
  主召回信号，叠加门控类型化图谱特征、确定性特征排序、预算化上下文选择与
  结果多样化。
- **反馈驱动排序**：仅追加的反馈事件与可重建聚合，按调用方与修订限定在 90 天
  窗口内，永不绕过 scope、当前修订、生命周期、时间或预算检查。
- **冲突与演化**：显式冲突用例、双方保留、`contradicts` 断言与显式解决；演化
  提案覆盖重复、补充、替换与冲突。
- **聚合记忆**：经审核的 `scene`、`topic`、`timeline` 聚合，建立在显式、有界、
  去重的输入修订列表之上，并通过 `derived_from` 关系保留来源。
- **离线优先**：处理与 embedding provider 分别配置、独立限流、默认禁用；密钥
  只以环境变量引用形式存在，永不进入配置、数据库、状态或日志。
- **可移植与可恢复**：一致性在线备份与离线 staging 恢复、SQLite 完整性检查、
  派生索引重建，以及 Format v1 逻辑导出/导入。

## 架构

仓库围绕一个语义核心与轻量入站协议适配器组织：

- **Memory Gateway**（`internal/gateway`）拥有应用语义，分为 `memory`、`scope`、
  `feedback`、`retrieval`、`extraction`、`consolidation`、`policy`、`knowledge`
  八个模块。模块发布协议中立的命令、查询、结果、错误与窄能力端口，且永不导入
  `net/http`、协议包或存储实现。
- **协议适配器**（`internal/protocol/httpapi`）把线上协议翻译为 gateway 调用。
  第一个适配器是 Agent 中立的 REST+JSON `http.Handler`，覆盖冻结的九路由 `/v1`
  接口。适配器负责严格解码、请求体上限、请求头、路由与错误映射，永不拥有领域
  规则或仓储调用。
- **传输服务层**（`internal/server`）在 `server.listen_address` 上监听 TCP，应用
  固定传输加固、可选 TLS 与 ALPN 协商的 HTTP/2，并优雅关闭。它只持有
  `http.Handler`，不拥有领域规则、认证或网络边缘授权。
- **运行时适配器**（`internal/storage`、`internal/jobs`、`internal/provider`）
  在现有 SQLite、FTS5、sqlite-vec、关系图谱、作业、策略与元数据实现之上实现
  gateway 的能力端口。逻辑端口不意味着物理上分离的数据库。
- **组合根**（`internal/daemon`）组装配置、SQLite 适配器、gateway 服务、
  provider、worker 与协议 handler。

依赖方向为：

```text
Agent 客户端 -> 传输服务层 -> 协议适配器 -> gateway 模块 -> 能力端口 -> 运行时适配器
```

禁止的方向包括 `protocol -> storage`、`gateway -> net/http`、`server -> storage`、
`storage -> gateway service`，以及模块之间直接依赖具体服务。这些规则由架构测试
强制。

## 构建与运行

需要 Linux、Go 1.24 或更新版本、C 编译器，以及 SQLite FTS5 支持。语义检索静态
链接 sqlite-vec，无需外部 `.so` 或运行时扩展加载。

```sh
go mod download
CGO_ENABLED=1 go build -tags sqlite_fts5 -o dmemd ./cmd/dmemd
install -d -m 0700 "${XDG_CONFIG_HOME:-$HOME/.config}/dmem"
cp docs/operations/config.example.toml "${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml"
# 编辑示例中的绝对 data_dir 路径
./dmemd config validate
./dmemd init
./dmemd daemon
```

默认配置文件为 `${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`。使用
`--config /absolute/path/config.toml` 选择其他实例。该文件决定数据目录与所有
守护进程设置，是守护进程设置的唯一来源。随仓库提供的示例中 provider 处于禁用
状态。

`dmemd daemon` 启动 SQLite 适配器与持久 worker，应用配置快照，在
`server.listen_address`（默认 `127.0.0.1:7373`）上启动 REST+JSON 监听，然后等待
关闭。设置 `server.tls_cert_file`/`server.tls_key_file` 后启用 TLS 与 HTTP/2。
`SIGHUP` 请求一次串行化的配置重载。可用子命令为 `daemon`、`config validate`、
`init`、`backup`、`restore`。

## HTTP 契约

冻结契约见 [docs/api/http-rest-contract.md](docs/api/http-rest-contract.md)。它定义
九条 Agent 中立路由：

```text
POST /v1/memories
GET  /v1/memories/{memory_id}
POST /v1/memories/{memory_id}/forget
POST /v1/memories/{memory_id}/feedback
POST /v1/recall
POST /v1/events
GET  /v1/events/{event_id}
POST /v1/scopes
GET  /v1/scopes/{scope_id}
```

每条路由都要求调用方身份（`X-Caller-Subject-ID` 和/或 `X-Caller-Client-ID`），
每次写操作都要求 `Idempotency-Key`。请求是严格的单值 JSON，具有按路由的请求体
上限，每个响应都携带 `X-Request-ID`。`internal/protocol/httpapi` 返回
`http.Handler`；`internal/server` 在回环地址上提供监听、可选 TLS 与优雅关闭。
契约由适配器的路由、解码、错误与 HTTP/2 兼容性测试，以及服务层的真实监听测试
共同验证。服务层不提供认证。

## 文档

- [文档索引](docs/README.md)
- [架构总览](docs/architecture/overview.md)
- [领域模型与权威数据](docs/architecture/domain-model.md)
- [处理、审核与演化](docs/architecture/processing.md)
- [检索与排序](docs/architecture/retrieval.md)
- [HTTP REST+JSON 契约](docs/api/http-rest-contract.md)
- [API 使用说明](docs/api/usage.md)
- [SQLite Schema v1](docs/storage/schema.md)
- [逻辑可移植性](docs/storage/logical-portability.md)
- [配置参考](docs/operations/configuration.md)
- [运维手册](docs/operations/runbook.md)
- [测试与质量门禁](docs/development/testing.md)

## 许可证

见 [LICENSE](LICENSE)。
