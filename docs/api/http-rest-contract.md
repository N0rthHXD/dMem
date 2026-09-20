# dMem HTTP REST+JSON 契约

## 状态

本文档描述当前 Agent 中立 REST+JSON 协议适配器的可执行契约。它是
`internal/protocol/httpapi` 当前实现的设计来源；此前的 Unix socket
`docs/api/openapi.yaml` 已删除。`internal/protocol/httpapi` 中的路由元数据及其
一致性测试是本文档的可执行形式。

项目尚未发布，本文档不是外部兼容性承诺。目标 API 将按
[Agent Tool 与 dMem Provider 双路线架构](../architecture/agent-tool-and-provider-routes.md)
重构，当前路由和对应底层流程可以替换或移除。在替换完成前，修改当前实现仍必须
同步更新本文档、路由元数据和契约测试，避免代码与文档失配。

适配器返回一个 `http.Handler`。`internal/server` 是生产传输层：它在回环 TCP 地址
上监听，可选终止 TLS 并通过 ALPN 协商 HTTP/2，并优雅关闭。本契约不定义监听地址、
TLS 或传输加固参数；它们属于配置与运维文档。

## 范围

包含：

- `/v1` 下的九条 Agent 中立路由；
- 这些路由的请求与响应 DTO；
- 请求标识、调用方身份与幂等请求头；
- 严格 JSON 解码规则与请求体上限；
- 统一错误体与状态码映射；
- 为后续列表路由预留的分页约定；
- handler 的 HTTP/2 兼容性要求。

不包含，且有意从本契约中排除：

- h2c 与 socket 生命周期；
- 认证、网络边缘授权、限流或任何网络安全边界；
- 候选审核、作业控制、索引重建、配置重载、图谱维护或任何其他管理操作；
- 面向 Claude、OpenCode 或其他具体客户端的路由、DTO 或行为。所有路由都是
  调用方中立、客户端中立的。

## 传输

- 协议方案：由调用方决定。handler 与传输无关。生产服务层默认明文监听并只提供
  HTTP/1.1；配置 TLS 后提供 HTTP/1.1 与 HTTP/2。
- 协议版本：HTTP/1.1 或 HTTP/2。handler 不得依赖 `r.ProtoMajor` 分支；协议协商
  由服务层负责，不提供 h2c。
- 基础路径：`/v1`。
- 媒体类型：`application/json`。
- 不使用 cookie、会话、重定向或连接相关行为。每个请求都是自描述的。

## 通用请求头

请求头：

| 请求头 | 是否必需 | 规则 |
| --- | --- | --- |
| `Content-Type` | 写操作 | 必须可解析为 `application/json`；忽略可选参数。 |
| `X-Request-ID` | 否 | 不透明令牌，1–128 个可见 ASCII 字符。缺失时自动生成。 |
| `X-Correlation-ID` | 否 | 不透明令牌，1–128 个可见 ASCII 字符。 |
| `X-Caller-Subject-ID` | 条件必需 | 不透明令牌，1–128 个可见 ASCII 字符。 |
| `X-Caller-Client-ID` | 条件必需 | 不透明令牌，1–128 个可见 ASCII 字符。 |
| `Idempotency-Key` | 写操作 | 不透明令牌，1–128 个可见 ASCII 字符。 |

调用方身份：本契约中的每条路由都是非公开路由。`X-Caller-Subject-ID` 与
`X-Caller-Client-ID` 至少需要一个。两者同时存在时，subject 是主要审计身份，
client 标识实际发起的客户端。两个请求头的名称与语义都不针对特定 Agent。

响应头：

| 请求头 | 是否总是返回 | 含义 |
| --- | --- | --- |
| `X-Request-ID` | 是 | 回显请求 ID，或返回生成的 ID。 |
| `Content-Type` | 是 | `application/json`。 |
| `Idempotency-Replayed` | 写操作 | 响应来自已提交的幂等写时返回 `true`，否则返回 `false`。 |

不透明令牌是指长度不超过 128 字节、每个字节都位于 `0x21`–`0x7E` 范围
（不含空格与控制字符）的非空字符串。

### 请求 ID

- `X-Request-ID` 缺失时，适配器生成 UUIDv4 形状的值。
- 存在时进行校验；非法值返回 `422 invalid_request`。
- 响应始终携带 `X-Request-ID`，错误响应也不例外。
- 请求 ID 会进入 gateway 的 `CallContext.RequestID`。它仅用于审计关联，永不参与
  幂等身份，也永不改变规范化命令哈希。

### 幂等

- 每条写路由都要求 `Idempotency-Key`。
- 适配器只负责传递该 key。gateway 由调用方身份 + 操作名 + 幂等 key + 规范化命令
  哈希构成写身份。HTTP 方法与路由永不参与该身份。
- 用相同 key 提交等价命令时，返回原始结果并带 `Idempotency-Replayed: true`。
- 用相同 key 提交不同命令时，返回 `409 idempotency_conflict`。
- 读路由忽略存在的 `Idempotency-Key`。

## 路由

所有路径都相对于 `/v1`。

| # | 操作 | 方法与路径 | 访问 | 调用方 | 幂等 | 请求体上限 | 成功状态 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `memory.remember` | `POST /v1/memories` | 写 | 必需 | 必需 | 1 MiB | `201` |
| 2 | `memory.get` | `GET /v1/memories/{memory_id}` | 读 | 必需 | 不需要 | 无 | `200` |
| 3 | `memory.forget` | `POST /v1/memories/{memory_id}/forget` | 写 | 必需 | 必需 | 64 KiB | `200` |
| 4 | `feedback.append` | `POST /v1/memories/{memory_id}/feedback` | 写 | 必需 | 必需 | 256 KiB | `201` |
| 5 | `retrieval.recall` | `POST /v1/recall` | 写 | 必需 | 必需 | 256 KiB | `200` |
| 6 | `extraction.ingest` | `POST /v1/events` | 写 | 必需 | 必需 | 4 MiB | `202` |
| 7 | `extraction.event.get` | `GET /v1/events/{event_id}` | 读 | 必需 | 不需要 | 无 | `200` |
| 8 | `scope.create` | `POST /v1/scopes` | 写 | 必需 | 必需 | 64 KiB | `201` |
| 9 | `scope.get` | `GET /v1/scopes/{scope_id}` | 读 | 必需 | 不需要 | 无 | `200` |

说明：

- `POST /v1/recall` 被归类为写操作，因为它会记录有界的召回遥测，因此要求幂等
  key。它不修改记忆权威数据。
- 读路由拒绝非空请求体，返回 `400 unexpected_body`。
- 路径标识符是 1–128 个字符的不透明非空字符串。适配器不强制 UUID 格式；未知
  标识符由 gateway 返回 `404 not_found`。
- 元数据表中的路由顺序稳定，即为声明顺序。

## 命名

公开 HTTP 接口统一使用 **scope** 术语：`scope_id`、`scope_ids`、
`parent_scope_id`、`scope_type` 以及 `/v1/scopes` 路径。内部存储可以继续使用
`space`。本契约的任何响应都不输出 `space_id` 字段。

资源体中的嵌套协议中立值（记忆修订、上下文包、反馈事件、内容）使用 gateway
领域 JSON 形状。适配器拥有外层信封以及为公开契约重命名的字段。

## DTO

### `POST /v1/memories`

请求：

```json
{
  "scope_id": "string, required",
  "content": "string, required",
  "kind": "string, optional",
  "sensitivity": "string, optional",
  "valid_from": "RFC3339 string, optional",
  "valid_until": "RFC3339 string, optional",
  "source_name": "string, optional",
  "source_event_id": "string, optional",
  "source_stream_key": "string, optional",
  "sequence": "non-negative integer, optional",
  "occurred_at": "RFC3339 string, optional",
  "metadata": "JSON object, optional",
  "reason": "string, optional"
}
```

响应 `201`：

```json
{
  "operation_id": "string",
  "replayed": false,
  "memory": { "memory_id": "string", "scope_id": "string", "kind": "fact", "current_state": "active", "frozen": false, "sensitivity": "normal", "current_revision": {}, "created_at": "string", "updated_at": "string" },
  "event_id": "string"
}
```

语义默认值（`kind`、`sensitivity`、`source_name`、`occurred_at`、`metadata`、
`reason`）由 gateway 应用，而不是适配器。字段省略时不会进入命令，由 gateway
应用默认值。

### `GET /v1/memories/{memory_id}`

响应 `200`：

```json
{ "memory": { "memory_id": "string", "scope_id": "string", "current_revision": {} } }
```

### `POST /v1/memories/{memory_id}/forget`

请求：

```json
{ "reason": "string, required, 1-1024 characters" }
```

响应 `200`：

```json
{ "operation_id": "string", "replayed": false, "memory": { "memory_id": "string", "scope_id": "string" } }
```

### `POST /v1/memories/{memory_id}/feedback`

请求：

```json
{
  "scope_id": "string, required",
  "purpose": "memory_quality_feedback | retrieval_feedback | answer_feedback",
  "feedback_type": "string, required",
  "reason_code": "string, required",
  "note": "string, optional",
  "decision": "decision context object, optional",
  "create_proposal": false,
  "source_event_id": "string, optional",
  "proposed_content": "string, optional"
}
```

响应 `201`：

```json
{ "operation_id": "string", "replayed": false, "feedback": {}, "proposal": { "status": "string" } }
```

用途、类型、原因、决策上下文与提案规则由 Feedback gateway 模块强制，而不是
适配器。

### `POST /v1/recall`

请求：

```json
{
  "query": "string, required",
  "scope_ids": ["string, required, at least one"],
  "include_ancestors": "boolean, optional",
  "memory_types": ["string, optional"],
  "states": ["string, optional"],
  "reference_time": "RFC3339 string, optional",
  "intent": "string, optional",
  "max_results": "integer, optional",
  "max_chars": "integer, optional",
  "max_context_tokens": "integer, optional",
  "include_evidence": "boolean, optional",
  "explain": "boolean, optional",
  "exclude_stale_aggregates": "boolean, optional",
  "cursor": "string, optional",
  "skip_diversification": "boolean, optional"
}
```

初始 Agent 契约不暴露能力或图谱策略调试覆盖项。响应 `200`：

```json
{ "context": { "items": [], "tokens_used": 0, "chars_used": 0, "truncated": false, "next_cursor": null }, "execution": {} }
```

### `POST /v1/events`

请求：

```json
{
  "scope_id": "string, required",
  "source_type": "string, required",
  "source_name": "string, required",
  "source_event_id": "string, optional",
  "source_stream_key": "string, optional",
  "sequence": "non-negative integer, optional",
  "occurred_at": "RFC3339 string, optional",
  "content": { "kind": "text | json | reference", "media_type": "string", "text": "string", "value": "any JSON", "reference_uri": "string", "declared_size": 0, "declared_hash": "string" },
  "metadata": "JSON object, optional"
}
```

内容 kind、必需字段与互斥字段规则由 Extraction gateway 模块强制。响应 `202`：

```json
{ "operation_id": "string", "replayed": false, "event_id": "string", "status": "string", "job_ids": [], "accepted_at": "string" }
```

### `GET /v1/events/{event_id}`

响应 `200`：

```json
{ "event": { "event_id": "string", "scope_id": "string", "source_type": "string", "content": {}, "processing_status": "string" } }
```

### `POST /v1/scopes`

请求：

```json
{
  "parent_scope_id": "string or null, optional",
  "scope_type": "user | agent | application | project | task | session",
  "external_id": "string, required, 1-128 characters",
  "display_name": "string, required, 1-256 characters"
}
```

响应 `201`：

```json
{ "operation_id": "string", "replayed": false, "scope": { "scope_id": "string", "parent_scope_id": null, "scope_type": "string", "external_id": "string", "display_name": "string", "created_at": "string" } }
```

### `GET /v1/scopes/{scope_id}`

响应 `200`：

```json
{ "scope": { "scope_id": "string", "parent_scope_id": null, "scope_type": "string", "external_id": "string", "display_name": "string", "created_at": "string" } }
```

## JSON 解码规则

每个写请求体都严格解码：

1. 请求体受路由上限约束。超限返回 `413 request_too_large`。
2. `Content-Type` 必须是 `application/json`，否则返回 `415 unsupported_media_type`。
3. 请求体必须恰好包含一个顶层 JSON 对象。空请求体、非对象值（`null`、数组、
   字符串、数字、布尔值）或第二个 JSON 值都返回 `400 invalid_json`。
4. 未知对象字段返回 `400 invalid_json`。
5. 字段类型不匹配返回 `400 invalid_json`。
6. 数字使用 `json.Number` 解码，整数值不会经过 `float64` 舍入。类型化的整数字段
   仍然拒绝小数或指数形式的值。
7. 承载任意用户数据的 JSON 对象值（`metadata`、`content.value`）保留
   `json.Number` 语义。

读路由不解析请求体。读路由上的非空请求体返回 `400 unexpected_body`。

## 错误模型

每个错误响应使用同一种请求体形状：

```json
{
  "error": {
    "code": "string, stable",
    "message": "string, human-readable, not stable",
    "request_id": "string",
    "retryable": false,
    "retry_after_ms": 1000,
    "details": { "key": "string" }
  }
}
```

`retry_after_ms` 与 `details` 缺失时省略。客户端必须基于 `code` 和 `retryable`
分支，绝不解析 `message`。

状态码映射：

| 错误码 | 状态码 | 来源 |
| --- | --- | --- |
| `invalid_json` | 400 | 适配器 |
| `unexpected_body` | 400 | 适配器 |
| `not_found` | 404 | 适配器（未知路由）或 gateway |
| `method_not_allowed` | 405 | 适配器 |
| `request_too_large` | 413 | 适配器 |
| `unsupported_media_type` | 415 | 适配器 |
| `invalid_request` | 422 | 适配器（请求头、路径标识符、分页） |
| `invalid_input` | 422 | gateway |
| `permission_denied` | 403 | gateway |
| `conflict` | 409 | gateway |
| `idempotency_conflict` | 409 | gateway |
| `resource_exhausted` | 可重试时 429，否则 507 | gateway |
| `capability_unavailable` | 503 | gateway |
| `internal` | 500 | gateway 或适配器 |
| `canceled` | 499 | 适配器（客户端取消；非标准，尽力而为） |
| `deadline_exceeded` | 504 | 适配器 |

安全错误：

- gateway 的 `*gateway.Error` 只贡献其声明为安全的 message、可重试性、重试提示和
  字符串 details。
- 被包装的内部原因永不序列化。
- 非 gateway 错误以 `500 internal` 和通用 message 报告，其文本永不回显。
- 恢复的 panic 以 `500 internal` 和通用 message 报告，恢复值永不回显。

## 分页

当前契约中没有路由返回分页。以下约定由当前实现使用；目标 API 可以在重构时替换：

- 查询参数：`limit`（整数，`1`–`200`，默认 `50`）与 `cursor`（不透明字符串，
  最多 512 个字符）。
- 非法值返回 `422 invalid_request`。
- 列表响应使用 `{"items": [...], "next_cursor": "string"|null}`。
- cursor 是不透明的，由所属 gateway 模块生成；客户端不得构造或解析。
- `internal/protocol/httpapi` 导出 `ParsePageRequest` 实现该约定。

## HTTP/2 兼容性

- handler 不得检查 `r.ProtoMajor`、`r.Proto` 或连接状态。
- handler 不得创建监听器、`http.Server`、TLS 配置或 h2c 升级。
- 所有请求状态都存在于请求、`CallContext` 或响应中。
- 兼容性通过仅用于测试的 HTTP/2 传输（启用 `EnableHTTP2` 的 `httptest.Server`）
  以及 `internal/server` 的真实 TLS 监听测试证明；本契约不定义 h2c。
