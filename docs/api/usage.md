# API 使用说明

dMem 通过 `internal/protocol/httpapi` 中的 Agent Tool application 适配器暴露其语义。
确切的路径、方法、DTO 字段、状态码、请求头与错误码由
[Application HTTP 契约](http-rest-contract.md) 定义；线格式的单一来源是
`pkg/toolcontract`。本文描述当前实现；项目预发布期间，接口将按
[Agent Tool 与 dMem Provider 双路线架构](../architecture/agent-tool-and-provider-routes.md)
继续演进，不构成外部兼容性承诺。

Agent 宿主有两种接入方式：直接使用 application HTTP API，或只配置
[dmem-mcp stdio 服务器](mcp-server.md)（内部复用 `pkg/client`）。两种方式使用同一
份版本化工具契约与错误模型。

适配器返回一个 `http.Handler`。`internal/server` 在 `server.listen_address` 上提供
监听（默认 `127.0.0.1:7373`），配置 TLS 后通过 ALPN 协商 HTTP/2。服务层不提供
认证。

## 请求规则

- `GET /v1/capabilities` 是唯一的公开路由；其余路由都要求 `X-Caller-Subject-ID`
  和/或 `X-Caller-Client-ID`。
- 每次写操作都要求 `Idempotency-Key`。相同 key 提交相同的规范化命令返回原始结果
  并带 `Idempotency-Replayed: true`；用于不同命令返回 `409 idempotency_conflict`。
- `POST /v1/recall` 是读操作，不需要 `Idempotency-Key`。
- 每个响应都携带 `X-Request-ID`。请求未提供时，适配器生成一个。
- JSON 解码严格且只允许单值：未知字段、类型不匹配、多个 JSON 值和超大请求体
  都会被拒绝。
- 客户端应基于稳定的 `error.code` 与 `error.retryable` 字段分支，绝不解析
  人类可读的 `error.message`。

## 路由

| 路由 | Gateway 操作 | 行为 |
| --- | --- | --- |
| `GET /v1/capabilities` | 能力发现 | 报告 contract 版本、工具名、predicate registry 版本与 evidence 披露模式。 |
| `POST /v1/memory-commits` | `agent_tool.memory_commit` | 原子提交一个 source、多条记忆和可选知识建议，返回逐条结果。 |
| `POST /v1/recall` | `agent_tool.recall` | 有界召回相关记忆，并显式报告能力降级。 |
| `POST /v1/feedback` | `agent_tool.feedback` | 向具体修订追加封闭类别反馈。 |
| `POST /v1/forget` | `agent_tool.forget` | 按期望修订逻辑遗忘一条记忆。 |
| `POST /v1/scopes/ensure` | `scope.ensure` | 按稳定外部身份解析或创建 scope。 |
| `GET /v1/scopes/{scope_id}` | `scope.get` | 返回一个 scope。 |
| `POST /v1/events` | `extraction.ingest` | Provider Route 原始事件入口；processing 禁用时以 `stored` 保留且不创建模型 job。 |
| `GET /v1/events/{event_id}` | `extraction.event_get` | 返回事件的 `processing_status` 与已存内容，不返回 job 列表。 |
| `GET /v1/candidates` | `review.candidate_list` | 按 scope 与审核状态分页列出 Provider Route 候选。 |
| `GET /v1/candidates/{candidate_id}` | `review.candidate_get` | 返回单个候选及其修订、证据与审核记录。 |
| `POST /v1/candidates/{candidate_id}/confirm` | `review.candidate_confirm` | 确认候选；确定性整合决定创建/附加记忆或归约为拒绝。 |
| `POST /v1/candidates/{candidate_id}/reject` | `review.candidate_reject` | 拒绝候选，不写入记忆权威。 |
| `GET /v1/memories` | `memory.list` | 按 scope 与生命周期状态分页列出记忆权威。 |
| `GET /v1/memories/{memory_id}` | `memory.get` | 返回记忆详情，含当前修订、证据、冲突引用与聚合新鲜度。 |
| `GET /v1/memories/{memory_id}/revisions` | `memory.revision_list` | 返回不可变修订历史。 |
| `GET /v1/memories/{memory_id}/state-events` | `memory.state_event_list` | 返回仅追加的生命周期事件。 |
| `POST /v1/memories/{memory_id}/revisions` | `memory.revise` | 在期望修订围栏下追加一条替代修订。 |
| `POST /v1/memories/{memory_id}/state` | `memory.state_change` | 应用一次生命周期变更（confirm/contest/archive/restore/retract/freeze/unfreeze/delete）。 |
| `GET /v1/proposals` | `review.proposal_list` | 按类型与审核状态分页列出图谱、演化与聚合提案。 |
| `GET /v1/proposals/{proposal_type}/{proposal_id}` | `review.proposal_get` | 返回单个提案及其证据与审核记录。 |
| `POST /v1/proposals/{proposal_type}/{proposal_id}/confirm` | 按类型 `review.<type>_confirm` | 确认提案；存储层审核状态机物化权威。 |
| `POST /v1/proposals/{proposal_type}/{proposal_id}/reject` | 按类型 `review.<type>_reject` | 拒绝提案，不物化权威。 |
| `GET /v1/conflicts` | `conflict.list` | 按 scope 与状态分页列出显式冲突用例。 |
| `GET /v1/conflicts/{case_id}` | `conflict.get` | 返回单个冲突用例及其参与方与解决记录。 |
| `POST /v1/conflicts/{case_id}/resolve` | `conflict.resolve` | 在期望参与方围栏下解决冲突，保留双方历史。 |

前七条属于 Agent Tool Route，接下来六条属于 Provider Route，只有启用
`[processing].enabled` 时才会创建 `extract_memory` job。记忆权威管理、提案审核与
冲突管理（第 14–26 条）是本地管理面：它们不进入 MCP 工具集合，不调用模型
provider，但读取或修改的是同一份记忆权威，并遵守相同的修订围栏、幂等写身份与审计
规则。完整 DTO、输入边界与状态码见
[Application HTTP 契约](http-rest-contract.md)。

## 记忆提交语义

`dmem_remember` 对应 `POST /v1/memory-commits`。一次调用在同一个幂等事务中提交
source、memory/knowledge 建议、evidence、审计与完整响应：

- 全新、`normal` 敏感度、非聚合 kind 且无重复信号的记忆创建低风险权威；
- 与当前修订规范内容完全重复的建议向现有修订附加 evidence，返回 `deduplicated`；
- `sensitive` 内容、聚合 kind（`scene`、`topic`、`timeline`）以及全部 entity、fact、
  relation 建议创建待审核 proposal；
- 覆盖、争议、遗忘、冲突确认和降低敏感度必须经过 dMem 规则，Agent confidence 不
  参与授权。

每个 memory 使用调用内 `local_id`；实体和记忆引用必须恰好选择 local ID 或持久 ID。
Tool Route commit 不创建 `extract_memory` 或 `extract_graph` job。

## 召回语义

`recall` 接受自然语言或普通关键词文本，而不是 SQLite FTS5 表达式。请求表达
`intent`、可选实体 hints、参考时间、结果数量上限与游标。关键词与语义是相互独立
的主召回信号；实体解析属于查询理解，实体事实与记忆关系是内部图谱信号。

语义执行需要已配置的 embedding provider 和可用的语义索引。未配置时，响应在
`degraded_capabilities` 中报告 `semantic`，而不是把关键词或图谱结果伪装成向量
召回。结果在返回前会针对 scope、当前修订、状态与有效时间重新校验。evidence 始终
是有界引用，不包含来源正文。

## 反馈语义

反馈绑定具体 memory revision，`kind` 封闭为 `used`、`not_used`、`incorrect`、
`outdated`、`duplicate`。可选 `reason` 有长度上限，不进入排序模型的未审查特征。
`not_used` 不会被自动等同于“无关”。遗忘是独立的 `POST /v1/forget` 操作，不混入
反馈类别。

## Scope 语义

公开 HTTP 术语统一使用 **scope**：`scope_id`、`scope_ids`、`parent_scope_id`、
`scope_type` 与 `/v1/scopes` 路径。内部存储可以继续使用 `space`；适配器在协议
边界重命名字段。Scope 构成类型化层级；召回可以包含祖先，但兄弟 scope 不会隐式
可见。

## 契约之外的操作

作业控制、索引重建、配置重载、图谱/实体/事实/关系的浏览与维护不属于本契约。原始
事件摄取由独立的 Provider Route 入口 `POST /v1/events` 承担：启用 processing 时它
创建持久抽取 job，禁用时只把事件保留为 `stored`，不创建模型 job；事件状态通过
`GET /v1/events/{event_id}` 查询。候选审核入口 `/v1/candidates`、记忆权威管理入口
`/v1/memories`、提案审核入口 `/v1/proposals` 与冲突管理入口 `/v1/conflicts` 属于
本地管理面，也不进入 Agent 工具集合。逻辑导出/导入与备份/恢复不是 HTTP 操作，
参见 [逻辑可移植性](../storage/logical-portability.md) 与
[运维手册](../operations/runbook.md)。

## MCP 接入

`dmem-mcp` 把同一契约暴露为四个 MCP tools，并注入配置的 scope 与传输层 source
身份；它不需要 LLM 或 embedding 配置，也不运行后台 worker。最小配置、工具清单、
身份注入规则、集成模式与错误映射见 [dmem-mcp stdio 服务器](mcp-server.md)。
