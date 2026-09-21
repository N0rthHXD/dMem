# dMem 文档

dMem 是一个以 SQLite 为后端的本地 Linux 守护进程。当前产品面包括直接记忆操作、
原始事件处理与审核、受 scope 约束的关键词与语义主召回（叠加门控类型化图谱特征
与预算化上下文选择）、实体、实体事实与关系、显式冲突演化、反馈驱动排序、经
审核的 scene/topic/timeline 聚合、决策遥测与 explain 输出、备份与恢复，以及
逻辑导入导出。

语义位于协议无关的 Memory Gateway 模块中。当前入站协议适配器是 Agent Tool
application REST+JSON `http.Handler`（Phase 2）与 `dmem-mcp` stdio 服务器
（Phase 4）；两者使用同一份 `pkg/toolcontract` 版本化 schema 与原子 memory commit，
MCP 服务器复用 `pkg/client` 薄 application client。`internal/server` 是生产传输层：
它在回环地址上监听，可选 TLS 与 ALPN 协商的 HTTP/2，并优雅关闭。认证与网络边缘
授权仍属后续独立工作。Provider Route 已在 Phase 5 收敛为显式
`[processing].enabled`/`[embedding].enabled` 加 `POST /v1/events` 入口。

Schema v1 没有迁移路径。当可执行 schema 变化时，应初始化新的数据目录，而不是
复用不兼容的数据库。

## 架构

- [架构总览](architecture/overview.md)：进程模型、模块边界、能力端口、依赖规则、
  配置与策略、作业与恢复。
- [Agent Tool 与 dMem Provider 双路线架构](architecture/agent-tool-and-provider-routes.md)：
  Agent 工具、MCP server、无 provider 检索、dMem provider 处理及 API 重构边界。
- [Phase 0 决策与基线](architecture/phase-0-decisions-and-baseline.md)：双路线重构的
  authority/proposal、枚举、MCP/client 决策，重构前九路由调用图与测试基线。
- [领域模型与权威数据](architecture/domain-model.md)：scope、记忆与修订、结构化
  字段、证据、生命周期、实体/关系/事实、冲突、演化与聚合。
- [处理、审核与演化](architecture/processing.md)：事件摄取、处理 provider、候选
  与整合、审核流程、出网策略、作业与重载。
- [检索与排序](architecture/retrieval.md)：查询计划、主召回通道、图谱门控、
  特征排序、上下文预算、游标、遥测与算法身份。

## 接口

- [Application HTTP 契约](api/http-rest-contract.md)：Phase 2 实现的路由、DTO、
  请求头、解码规则与错误模型；线格式来自 `pkg/toolcontract`。
- [API 使用说明](api/usage.md)：gateway 语义如何映射到契约。
- [dmem-mcp stdio 服务器](api/mcp-server.md)：最小 Agent 配置、四个工具、scope/
  source 注入、集成模式、错误与取消语义。

## 存储

- [SQLite Schema v1 说明](storage/schema.md)：表分组、权威与派生的划分。
- [SQLite Schema v1](storage/schema-v1.sql)：新数据库初始化的可执行定义。
- [逻辑可移植性](storage/logical-portability.md)：Format v1 导出/导入库及其一致性
  边界。

## 运维

- [配置参考](operations/configuration.md)：严格 TOML 配置的全部 section、键、
  默认值与校验规则。
- [配置示例](operations/config.example.toml)：可直接复制的完整示例。
- [运维手册](operations/runbook.md)：初始化、备份、恢复、重建与故障处理。

## 开发

- [测试与质量门禁](development/testing.md)：构建配置与可执行检查。
- [双路线架构开发任务](development/tasks-agent-tool-and-provider-routes.md)：按阶段实施
  Tool/MCP、application API、无 provider 检索、Provider Route 和旧流程清理。

## 契约优先级

当散文描述与可执行契约冲突时，以可执行契约为准：

- [HTTP REST+JSON 契约](api/http-rest-contract.md) 描述当前
  `internal/protocol/httpapi` 实现。项目预发布期间，它将按双路线架构替换，不构成
  外部兼容性承诺。
- [SQLite Schema v1](storage/schema-v1.sql) 定义新数据库初始化，并由
  `internal/storage` 内嵌并校验一致。
