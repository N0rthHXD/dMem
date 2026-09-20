# API 使用说明

dMem 通过 `internal/protocol/httpapi` 中的 Agent 中立 REST+JSON 适配器暴露其语义。
确切的路径、方法、DTO 字段、状态码、请求头与错误码由
[HTTP REST+JSON 契约](http-rest-contract.md) 定义。本文描述当前实现；项目预发布
期间，接口将按
[Agent Tool 与 dMem Provider 双路线架构](../architecture/agent-tool-and-provider-routes.md)
替换，不构成外部兼容性承诺。

适配器返回一个 `http.Handler`。`internal/server` 在 `server.listen_address` 上提供
监听（默认 `127.0.0.1:7373`），配置 TLS 后通过 ALPN 协商 HTTP/2。下面的行为是
handler 实现的语义，并由适配器与服务层测试强制。服务层不提供认证。

## 请求规则

- 每条路由都要求调用方身份。发送 `X-Caller-Subject-ID` 和/或
  `X-Caller-Client-ID`。
- 每次写操作都要求 `Idempotency-Key`。用相同 key 提交相同的规范化命令会返回
  原始结果并带 `Idempotency-Replayed: true`；用于不同命令则返回
  `409 idempotency_conflict`。
- 每个响应都携带 `X-Request-ID`。请求未提供时，适配器生成一个。
- JSON 解码严格且只允许单值：未知字段、类型不匹配、多个 JSON 值和超大请求体
  都会被拒绝。
- 客户端应基于稳定的 `error.code` 与 `error.retryable` 字段分支，绝不解析
  人类可读的 `error.message`。

## 路由

| 路由 | Gateway 模块 | 行为 |
| --- | --- | --- |
| `POST /v1/memories` | Memory | 原子地创建手动来源事件、记忆、初始不可变修订、证据、活动投影、审计数据与所需索引作业。 |
| `GET /v1/memories/{memory_id}` | Memory | 返回一条记忆的当前投影。 |
| `POST /v1/memories/{memory_id}/forget` | Memory | 逻辑删除记忆，将其移出召回，并保留权威与审计历史。 |
| `POST /v1/memories/{memory_id}/feedback` | Feedback | 向当前修订追加反馈，并可请求纠正性演化提案。 |
| `POST /v1/recall` | Retrieval | 在一个或多个 scope 中检索，受结果数量与上下文预算约束，返回选定的上下文包与执行元数据。 |
| `POST /v1/events` | Extraction | 摄取原始事件，并在同一事务中持久化入队其抽取作业。 |
| `GET /v1/events/{event_id}` | Extraction | 返回一条原始事件及其处理状态。 |
| `POST /v1/scopes` | Scope | 在类型化层级中创建一个 scope。 |
| `GET /v1/scopes/{scope_id}` | Scope | 返回一个 scope。 |

## 记忆语义

`remember` 将 `kind` 默认为 `fact`，`sensitivity` 默认为 `normal`，`source_name`
默认为调用方，`occurred_at` 默认为当前时间。这些默认值由 Memory gateway 模块
应用，而不是适配器。支持的记忆 kind 为 `fact`、`event`、`preference`、`plan`、
`procedure`、`scene`、`topic`、`timeline`。

写入返回记忆投影及其当前修订。`forget` 要求非空 reason，并保留修订、证据、
状态与审计记录。

## 召回语义

`recall` 接受自然语言或普通关键词文本，而不是 SQLite FTS5 表达式。请求表达
`intent`、可选参考时间、结果数量上限、上下文 token 预算和可选字符预算。关键词
与语义是相互独立的主召回信号；实体解析属于查询理解，实体事实与记忆关系是内部
图谱信号，而不是独立通道。

查询计划选择图谱 `off`、`features_only`、`direct` 或 `bounded_path`；生产默认值
来自配置，而不是请求。语义执行需要已配置的 embedding provider 和可用的语义
索引。自然语言查询中的实体提及会在检索前被确定性解析，无歧义的解析可以通过
直接提及或有界类型化路径增加候选。

响应报告请求与实际执行的能力、降级原因、排序/索引/上下文/反馈身份、解析出的
实体、图谱信号、cursor、截断情况与字符使用量。`explain: true` 在正常召回的
相同 scope、策略与敏感度规则下，返回每个结果的特征贡献、图谱路径、证据以及
选择或拒绝原因。结果在返回前会针对 scope、当前修订、状态与有效时间重新校验。

初始 Agent 契约有意不暴露通道选择或图谱策略调试覆盖项；它们仍然是内部诊断
能力。

## 反馈语义

反馈按用途分离，`purpose` 与 `feedback_type`、`reason_code` 是封闭组合：

| 用途 | 允许的 type | 允许的 reason |
| --- | --- | --- |
| `memory_quality_feedback` | `confirm`、`correct`、`contest`、`supplement` | `fact_incorrect`、`fact_incomplete`、`fact_outdated`、`conflicts_with_evidence`、`duplicate`、`other` |
| `retrieval_feedback` | `useful`、`irrelevant` | `answered_query`、`relevant_context`、`not_relevant`、`other` |
| `answer_feedback` | `supported`、`unsupported` | `evidence_supported`、`evidence_insufficient`、`evidence_contradicted`、`other` |

只有 `memory_quality_feedback` 的 `correct`、`supplement`、`contest` 可以请求
演化提案，且必须提供 `source_event_id` 与 `proposed_content`。提案永远只是提案，
不会直接改变权威。`retrieval_feedback` 与 `answer_feedback` 必须携带决策上下文，
该上下文保存隐私保护的决策信息（查询哈希与类型、排名、选择状态、命中的信号
类型），而不是查询文本。

## Scope 语义

公开 HTTP 术语统一使用 **scope**：`scope_id`、`scope_ids`、`parent_scope_id`、
`scope_type` 与 `/v1/scopes` 路径。内部存储可以继续使用 `space`；适配器在协议
边界重命名字段。Scope 构成类型化层级；召回可以包含祖先，但兄弟 scope 不会
隐式可见。

## 契约之外的操作

候选审核、作业控制、索引重建、配置重载、图谱维护以及更广泛的管理接口不属于
初始 Agent 协议。逻辑导出/导入与备份/恢复不是 HTTP 操作，参见
[逻辑可移植性](../storage/logical-portability.md) 与
[运维手册](../operations/runbook.md)。
