# dMem

dMem 是一个面向 Agent 与应用的本地单用户记忆守护进程。它以 SQLite 存储版本化、
结构化的记忆及其证据，通过带门控类型化图谱特征的关键词与语义召回检索受 scope
约束的候选，并通过协议无关的 Memory Gateway 暴露其语义。外部处理与 embedding
provider 默认关闭。

> **状态**：项目处于预发布阶段。Schema v1 没有迁移路径；schema 不兼容的数据库
> 必须由新初始化的数据目录替代。`dmemd daemon` 默认在回环地址启动 REST+JSON
> 监听服务；TLS 可选，认证与网络边缘授权仍属后续独立工作。`dmem-mcp` 提供
> stdio MCP 服务器，Agent 无需在 dMem 中配置模型即可完成记忆闭环。

## 能力

- **版本化记忆权威**：稳定的逻辑 ID、不可变修订、仅追加状态事件、证据与当前
  投影；修订永不原地编辑，逻辑删除保留完整审计历史。
- **两条模型路线**：Agent Tool Route 已可用——Agent 通过 MCP 或薄 client 直接召回
  和提交结构化记忆，无需在 dMem 配置模型；独立输入可选择 dMem Provider Route 做
  持久异步处理，通过 `POST /v1/events` 摄取原始事件，并由显式
  `[processing].enabled`/`[embedding].enabled` 控制，默认关闭。
- **MCP 与薄 client**：`dmem-mcp` 通过 stdio 暴露 `dmem_recall`、`dmem_remember`、
  `dmem_feedback`、`dmem_forget`；`pkg/client` 是供 MCP 与原生 Go 集成复用的
  application client，自动传递 caller、scope、请求与幂等身份，且不需要 LLM 或
  embedding 配置。
- **可审核的知识构建**：实体、别名、提及、类型化实体事实与记忆关系以提案形式
  产生并接受确定性校验与风险分类。Agent Tool 提交的知识建议持久化为
  `agent_tool_proposals` 审核对象，不直接发布权威；Provider Route 满足封闭谓词
  自动确认策略的事实可以自动确认（`auto-review-v1`），其余留在审核队列。
  记忆候选的确认与拒绝通过 `/v1/candidates` 暴露；可选的
  `[review.auto_confirm]` 策略（默认关闭）允许满足封闭边界的候选在抽取提交时直接
  发布，只发布非破坏性的确定性结果、`normal` 敏感度、含 `direct` 证据且通过阈值与
  每空间限流的候选，并以系统身份 `system:auto-review-v1` 留痕；图谱/聚合提案与
  冲突解决不变量由存储层强制，当前没有协议入口。
- **混合检索**：关键词（SQLite FTS5）与语义（sqlite-vec 精确余弦）作为独立的
  主召回信号，叠加门控类型化图谱特征、确定性特征排序、预算化上下文选择与
  结果多样化。
- **反馈驱动排序**：仅追加的反馈事件与可重建聚合，按调用方与修订限定在 90 天
  窗口内，永不绕过 scope、当前修订、生命周期、时间或预算检查。
- **冲突与演化**：显式冲突用例、双方保留、`contradicts` 断言与显式解决；演化
  提案覆盖重复、补充、替换与冲突。
- **聚合记忆**：经审核的 `scene`、`topic`、`timeline` 聚合，建立在显式、有界、
  去重的输入修订列表之上，并通过 `derived_from` 关系保留来源。
- **无 Provider 可用**：FTS、检索辅助文本、实体和图谱构成 Tool Route 的基础召回，
  未配置 embedding provider 时显式报告 `semantic` 降级；dMem 的处理与 embedding
  provider 是显式启用的增强，默认关闭且独立限流。
- **可移植与可恢复**：一致性在线备份与离线 staging 恢复、SQLite 完整性检查、
  派生索引重建，以及 Format v1 逻辑导出/导入。

## 架构

仓库围绕一个语义核心与轻量入站协议适配器组织：

- **Memory Gateway**（`internal/gateway`）拥有应用语义，当前分为 `agenttool`、
  `scope`、`retrieval`、`extraction`、`review`、`consolidation`（仅纯分类器）与
  `policy` 七个模块。模块发布协议中立的命令、查询、结果、错误与窄能力端口，且永不
  导入 `net/http`、协议包或存储实现。
- **协议适配器**把线上协议翻译为 gateway 调用。当前实现是
  `internal/protocol/httpapi` 与建立在同一 application contract 上的
  `internal/protocol/mcp`（stdio）。适配器负责严格解码、请求体上限、请求头、路由
  与错误映射，永不拥有领域规则或仓储调用。
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
install -d -m 0700 "$HOME/.config/dmem"
cp docs/operations/config.example.toml "$HOME/.config/dmem/config.toml"
# 编辑示例中的绝对 data_dir 路径
./dmemd config validate
./dmemd init
./dmemd daemon
```

一步安装：从 GitHub 下载与当前架构匹配的预编译发布包（无需 Go 或 C 编译器），
把 `dmemd` 安装到 `~/.local/bin/dmemd`，把默认配置安装到
`~/.config/dmem/config.toml`（已存在的配置默认保留，`FORCE=1` 覆盖），校验
SHA-256，执行 `config validate` 与 `init`，并提示如何把 `~/.local/bin` 加入
`PATH`：

```sh
curl -fsSL https://raw.githubusercontent.com/N0rthHXD/dMem/main/install.sh | sh
```

在源码检出目录中运行同一个脚本时会改为本地构建：

```sh
bash install.sh              # 本地构建安装
bash install.sh --daemon     # 安装并后台启动守护进程
```

`make install` 是底层原语，只做构建与拷贝，不做校验或初始化：

```sh
make install VERSION=0.1.1
```

发布包由 GitHub Actions（`.github/workflows/release.yml`）在推送 `v*` 标签时
构建，提供 `linux/amd64` 与 `linux/arm64` 两种归档及配套 SHA-256。

下载报 TLS 连接重置（`curl: (56)` / `GnuTLS recv error`）时，脚本会重试并回退
HTTP/1.1；仍失败可用代理或镜像覆盖下载地址，详见
[docs/operations/packaging.md](docs/operations/packaging.md)。

默认配置文件为 `$HOME/.config/dmem/config.toml`。使用
`--config /absolute/path/config.toml` 选择其他实例。该文件决定数据目录与所有
守护进程设置，是守护进程设置的唯一来源。随仓库提供的示例中 provider 处于禁用
状态。

`dmemd daemon` 启动 SQLite 适配器与持久 worker，应用配置快照，在
`server.listen_address`（默认 `127.0.0.1:7373`）上启动 REST+JSON 监听，然后等待
关闭。设置 `server.tls_cert_file`/`server.tls_key_file` 后启用 TLS 与 HTTP/2。
`SIGHUP` 请求一次串行化的配置重载。可用子命令为 `daemon`、`config validate`、
`init`、`backup`、`restore`。

`dmem-mcp` 是供 Agent 宿主启动的 stdio 子进程；它只要求 dMem endpoint 与一个
scope，不需要 TOML 文件或模型配置：

```sh
CGO_ENABLED=1 go build -tags sqlite_fts5 -o dmem-mcp ./cmd/dmem-mcp
./dmem-mcp --endpoint http://127.0.0.1:7373 --scope-type project --scope-external-id project-123
```

## 接口状态

当前可执行契约见 [docs/api/http-rest-contract.md](docs/api/http-rest-contract.md)，
MCP 接入见 [docs/api/mcp-server.md](docs/api/mcp-server.md)。
项目尚未发布，当前路由不是外部兼容性边界，将继续按
[Agent Tool 与 dMem Provider 双路线架构](docs/architecture/agent-tool-and-provider-routes.md)
演进。当前 application API 为：

```text
GET  /v1/capabilities
POST /v1/memory-commits
POST /v1/recall
POST /v1/feedback
POST /v1/forget
POST /v1/scopes/ensure
GET  /v1/scopes/{scope_id}
POST /v1/events
GET  /v1/events/{event_id}
GET  /v1/candidates
GET  /v1/candidates/{candidate_id}
POST /v1/candidates/{candidate_id}/confirm
POST /v1/candidates/{candidate_id}/reject
```

`GET /v1/capabilities` 是唯一的公开路由；其余路由都要求调用方身份
（`X-Caller-Subject-ID` 和/或 `X-Caller-Client-ID`），写操作要求
`Idempotency-Key`，`POST /v1/recall` 是读操作。请求是严格的单值 JSON，具有按
路由的请求体上限，每个响应都携带 `X-Request-ID`。Agent Tool 路由的线格式由
`pkg/toolcontract` 的版本化 schema 唯一定义；`/v1/events` 与 `/v1/candidates`
是 Provider Route 入口，processing 未启用时只把事件保存为 `stored`。
`internal/protocol/httpapi` 返回
`http.Handler`；`internal/server` 在回环地址上提供监听、可选 TLS 与优雅关闭。
服务层不提供认证。`dmem-mcp` 通过 stdio 暴露同样的四个工具，并复用
`pkg/client` 薄 application client。

## 文档

- [文档索引](docs/README.md)
- [架构总览](docs/architecture/overview.md)
- [Agent Tool 与 dMem Provider 双路线架构](docs/architecture/agent-tool-and-provider-routes.md)
- [领域模型与权威数据](docs/architecture/domain-model.md)
- [处理、审核与演化](docs/architecture/processing.md)
- [检索与排序](docs/architecture/retrieval.md)
- [HTTP REST+JSON 契约](docs/api/http-rest-contract.md)
- [API 使用说明](docs/api/usage.md)
- [dmem-mcp stdio 服务器](docs/api/mcp-server.md)
- [SQLite Schema v1](docs/storage/schema.md)
- [逻辑可移植性](docs/storage/logical-portability.md)
- [配置参考](docs/operations/configuration.md)
- [运维手册](docs/operations/runbook.md)
- [测试与质量门禁](docs/development/testing.md)
- [双路线架构开发任务](docs/development/tasks-agent-tool-and-provider-routes.md)
- [LoCoMo 中文子集评估模块](eval/README.md)

## 许可证

见 [LICENSE](LICENSE)。
