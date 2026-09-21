# dMem Application HTTP 契约

## 状态

本文档描述当前已实现的 Agent Tool application HTTP 适配器。它是
`internal/protocol/httpapi` 当前实现的设计来源，路由元数据及其一致性测试是本文档的
可执行形式。项目尚未发布，本文档不是外部兼容性承诺；路径、DTO 和状态码可在后续
阶段按目标架构调整。

Phase 7 已从路由注释、测试名与文档中移除“冻结路由集”的表述：当前九条路由是**已
实现行为**，不是冻结契约，也不是旧九条路由的延续。唯一仍然稳定的是
`pkg/toolcontract` 的版本化 v1 schema 与各模块发布的稳定操作名。

线格式（wire DTO 与 JSON Schema）的唯一来源是 `pkg/toolcontract`。HTTP 适配器只做
严格解码、协议中立命令映射和错误转换，不拥有领域语义。`internal/server` 是生产
传输层：它在回环 TCP 地址上监听，可选终止 TLS 并通过 ALPN 协商 HTTP/2，并优雅
关闭。本契约不定义监听地址、TLS 或传输加固参数。

旧九条 `/v1` 路由、旧 purpose/type/reason feedback DTO 与 `GET /v1/memories/{id}` 已在
Phase 2 删除。Phase 5 重新引入 Provider Route 的原始事件入口
（`POST /v1/events`、`GET /v1/events/{event_id}`）；它们在 processing 未启用时只把
事件保存为 `stored`，不创建模型 job。

## 范围

包含：

- `/v1` 下当前已实现的九条 application 路由：七条 Agent Tool/scope 路由加两条
  Provider Route 原始事件路由；
- 这些路由的请求与响应 DTO（Agent Tool 路由来自 `pkg/toolcontract/schema/v1`，
  事件路由是协议本地 DTO）；
- 请求标识、调用方身份与幂等请求头；
- 严格 JSON 解码规则与请求体上限；
- 统一错误体与状态码映射；
- handler 的 HTTP/2 兼容性要求。

不包含，且有意从本契约中排除：

- h2c 与 socket 生命周期；
- 认证、网络边缘授权、限流或任何网络安全边界。当前实现**没有认证**：
  默认监听地址是回环地址 `127.0.0.1:7373`，绑定到非回环地址时
  `internal/server` 会记录一条明确的警告，运维必须自行提供网络边缘保护；
- 候选审核、作业控制、索引重建、配置重载、图谱维护或任何其他管理操作；
- 面向 Claude、OpenCode 或其他具体客户端的路由、DTO 或行为。所有路由都是
  调用方中立、客户端中立的。

## 传输

- 协议方案：由调用方决定。handler 与传输无关。
- 协议版本：HTTP/1.1 或 HTTP/2。handler 不得依赖 `r.ProtoMajor` 分支；协议协商
  由服务层负责，不提供 h2c。
- 基础路径：`/v1`。
- 媒体类型：`application/json`。
- 不使用 cookie、会话、重定向或连接相关行为。每个请求都是自描述的。

## 通用请求头

| 请求头 | 是否必需 | 规则 |
| --- | --- | --- |
| `Content-Type` | 有请求体 | 必须可解析为 `application/json`；忽略可选参数。 |
| `X-Request-ID` | 否 | 不透明令牌，1–128 个可见 ASCII 字符。缺失时自动生成。 |
| `X-Correlation-ID` | 否 | 不透明令牌，1–128 个可见 ASCII 字符。 |
| `X-Caller-Subject-ID` | 条件必需 | 不透明令牌，1–128 个可见 ASCII 字符。 |
| `X-Caller-Client-ID` | 条件必需 | 不透明令牌，1–128 个可见 ASCII 字符。 |
| `Idempotency-Key` | 写操作 | 不透明令牌，1–128 个可见 ASCII 字符。 |

调用方身份：`GET /v1/capabilities` 是公开路由，其余每条路由都要求
`X-Caller-Subject-ID` 与 `X-Caller-Client-ID` 至少一个。两者同时存在时，subject 是
主要审计身份，client 标识实际发起的客户端。

响应头：

| 请求头 | 是否总是返回 | 含义 |
| --- | --- | --- |
| `X-Request-ID` | 是 | 回显请求 ID，或返回生成的 ID。 |
| `Content-Type` | 是 | `application/json`。 |
| `Idempotency-Replayed` | 写操作 | 响应来自已提交的幂等写时返回 `true`，否则返回 `false`。 |

不透明令牌是指长度不超过 128 字节、每个字节都位于 `0x21`–`0x7E` 范围（不含空格
与控制字符）的非空字符串。

### 请求 ID 与幂等

- `X-Request-ID` 缺失时，适配器生成 UUIDv4 形状的值；存在时进行校验，非法值返回
  `422 invalid_request`。响应始终携带 `X-Request-ID`。
- 每条写路由都要求 `Idempotency-Key`。适配器只负责传递该 key；gateway 由调用方
  身份 + 操作名 + 幂等 key + 规范化命令哈希构成写身份。HTTP 方法与路由永不参与
  该身份。
- 用相同 key 提交等价命令时，返回原始结果并带 `Idempotency-Replayed: true`。
- 用相同 key 提交不同命令时，返回 `409 idempotency_conflict`。
- `POST /v1/recall` 是读操作：它不需要也不使用 `Idempotency-Key`。相同稳定请求身份
  的重复召回不会重复写入无内容遥测。

## 路由

所有路径都相对于 `/v1`。

| # | 操作 | 方法与路径 | 访问 | 调用方 | 幂等 | 请求体上限 | 成功状态 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `agent_tool.capabilities` | `GET /v1/capabilities` | 读 | 公开 | 不需要 | 无 | `200` |
| 2 | `agent_tool.memory_commit` | `POST /v1/memory-commits` | 写 | 必需 | 必需 | 1 MiB | `201` |
| 3 | `agent_tool.recall` | `POST /v1/recall` | 读 | 必需 | 不需要 | 256 KiB | `200` |
| 4 | `agent_tool.feedback` | `POST /v1/feedback` | 写 | 必需 | 必需 | 256 KiB | `201` |
| 5 | `agent_tool.forget` | `POST /v1/forget` | 写 | 必需 | 必需 | 64 KiB | `200` |
| 6 | `scope.ensure` | `POST /v1/scopes/ensure` | 写 | 必需 | 必需 | 64 KiB | `200` |
| 7 | `scope.get` | `GET /v1/scopes/{scope_id}` | 读 | 必需 | 不需要 | 无 | `200` |
| 8 | `extraction.ingest` | `POST /v1/events` | 写 | 必需 | 必需 | 1 MiB | `202` |
| 9 | `extraction.event_get` | `GET /v1/events/{event_id}` | 读 | 必需 | 不需要 | 无 | `200` |

说明：

- 写路由的成功状态由操作语义决定：`memory-commits` 与 `feedback` 返回 `201`，
  `forget` 与 `scopes/ensure` 返回 `200`，`events` 返回 `202`。
- 读路由拒绝非空请求体，返回 `400 unexpected_body`；`recall` 例外，它接受有界请求体。
- 路径标识符是 1–128 个字符的不透明非空字符串。适配器不强制 UUID 格式；未知
  标识符由 gateway 返回 `404 not_found`。
- 路由顺序稳定，即为声明顺序。
- `POST /v1/events` 是 Provider Route 入口，不属于 Agent Tool 契约。`GET /v1/events/{event_id}`
  只返回事件状态与已存内容，不返回 job 列表。

## DTO 与语义

字段、枚举、数组与字符串上限由 `pkg/toolcontract/schema/v1` 的 JSON Schema 定义。
适配器先做 schema 校验，再做严格单值解码，然后映射为协议中立 gateway 命令。Schema
是唯一定义来源，下表是便于评审的摘要（`internal/gateway/agenttool/contract.go`）：

| 字段 | 边界 |
| --- | --- |
| `query` | 1–4096 runes |
| `scope_ids` | 1–32 项，每项 ≤128 runes，无重复 |
| `entities`（recall hints） | ≤32 项，每项 ≤256 runes，无重复 |
| `limit`（recall） | 1–100，省略时默认 10 |
| `cursor` | ≤4096 runes |
| `memories` | 1–32 项，`local_id` 唯一且 ≤128 runes |
| `memories[].content` | 1–4096 runes |
| `memories[].search_hints` | ≤16 项，每项 1–256 runes |
| `entities`（commit） | ≤256 项，`aliases` ≤16 项 |
| `facts` / `relations` | 各 ≤256 项，`local_id` 唯一 |
| `confidence` / `specificity` | `[0, 1]` 且必须有限（NaN/Inf 被拒绝） |
| `source.metadata` | 深度 ≤4，每层 ≤32 键，编码 ≤16 KiB，键 ≤128 runes |
| `reason`（feedback/forget） | feedback ≤1024 runes 可选；forget 1–1024 runes 必填 |
| 全请求体 | 见上面的路由表（1 MiB / 256 KiB / 64 KiB） |

`dmem_remember` 不接受 `confirmed`、`skip_policy` 或 `skip_review`；未知字段一律
返回 `400 invalid_json`。

### `GET /v1/capabilities`

公开路由。响应 `200` 报告 `contract_version`、四个工具名、predicate registry 版本与
`references_only` evidence 披露模式。

### `POST /v1/memory-commits`

一次调用原子提交一个 `agent_tool` source、一到多条 memory 建议以及可选 entity、
fact、relation 建议：

```json
{
  "scope_id": "project-123",
  "source": {
    "type": "agent_tool",
    "name": "assistant",
    "conversation_id": "conv-123",
    "turn_id": "turn-456",
    "tool_call_id": "call-789",
    "occurred_at": "2026-09-20T10:00:00Z"
  },
  "memories": [
    {
      "local_id": "memory-1",
      "kind": "preference",
      "content": "用户为该项目选择 PostgreSQL。",
      "sensitivity": "normal",
      "confidence": 0.96,
      "search_hints": ["项目数据库选择", "PostgreSQL"]
    }
  ],
  "entities": [],
  "facts": [],
  "relations": []
}
```

响应 `201` 返回 `operation_id`、`replayed`、`source_event_id` 和每个 `local_id` 的
`status`：

```json
{
  "operation_id": "op-1",
  "replayed": false,
  "source_event_id": "event-1",
  "memories": [{"local_id": "memory-1", "status": "authority", "memory_id": "mem-1", "revision_id": "rev-1"}],
  "entities": [], "facts": [], "relations": []
}
```

`status` 封闭为 `authority`、`deduplicated`、`proposal`、`rejected`。Agent 提交的是
结构化建议：它不能设置 `confirmed`、`skip_policy` 或 `skip_review`，confidence 也
不能授权覆盖、遗忘、冲突确认或降低敏感度。敏感内容、聚合 kind 与全部 knowledge
建议进入 proposal 路径。Tool Route commit 不创建 `extract_memory` 或
`extract_graph` job。

### `POST /v1/recall`

请求表达查询意图，不暴露内部通道开关：

```json
{"query": "用户为这个项目选择了什么数据库？", "scope_ids": ["project-123"], "intent": "lookup", "entities": ["database"], "limit": 10}
```

响应 `200` 返回面向模型消费的有界上下文与显式降级：

```json
{"operation_id": "recall-1", "memories": [{"memory_id": "mem-1", "revision_id": "rev-1", "kind": "preference", "content": "...", "confidence": 0.96, "evidence": []}], "degraded_capabilities": ["semantic"], "next_cursor": null}
```

evidence 始终是有界引用（`evidence_id`、`event_id`、role、时间/序号、span offset），
不包含来源正文。未配置 dMem embedding provider 时，`semantic` 必须出现在
`degraded_capabilities` 中。

### `POST /v1/feedback`

请求绑定具体 memory revision，只接受 `used`、`not_used`、`incorrect`、`outdated`、
`duplicate` 五个封闭类别：

```json
{"scope_id": "project-123", "memory_id": "mem-1", "revision_id": "rev-1", "kind": "used", "reason": "可选", "recall_operation_id": "recall-1"}
```

响应 `201` 回显 `feedback_id`、`memory_id`、`revision_id` 与 `kind`。旧
purpose/type/reason 组合不属于本契约。

### `POST /v1/forget`

请求要求期望修订与非空 reason：

```json
{"scope_id": "project-123", "memory_id": "mem-1", "expected_revision_id": "rev-1", "reason": "用户要求遗忘"}
```

响应 `200` 返回 `memory_id`、`revision_id` 与 `state: "deleted"`。期望修订不匹配时
返回 `409 conflict`。逻辑遗忘保留修订、证据、状态事件与审计。

### `POST /v1/scopes/ensure`

按稳定外部身份原子解析或创建 scope：

```json
{"parent_scope_id": null, "scope_type": "project", "external_id": "project-123", "display_name": "Project 123"}
```

响应 `200` 返回 `scope`。重复 ensure 返回同一 scope。

### `GET /v1/scopes/{scope_id}`

响应 `200` 返回 scope 资源。

### `POST /v1/events`

Provider Route 原始事件入口。请求包含 scope、来源与内容：

```json
{
  "scope_id": "project-123",
  "source": {
    "type": "manual",
    "name": "collector",
    "event_id": "source-event-1",
    "stream_key": "collector/project-123",
    "sequence": 1,
    "occurred_at": "2026-09-20T10:00:00Z",
    "metadata": {}
  },
  "content": {"kind": "text", "media_type": "text/plain", "text": "原始事件正文"}
}
```

`content.kind` 封闭为 `text`、`json`、`reference`；`json` 使用 `content.value`，
`reference` 使用 `content.reference_uri` 与可选的 `declared_size`/`declared_hash`。

原始事件输入边界由 `internal/gateway/extraction` 强制（协议适配器只额外限制请求体
大小），因此任何入口提交的事件都是有界的：

| 边界 | 值 | 行为 |
| --- | --- | --- |
| 请求体 | 1 MiB | 超限返回 `413 request_too_large`。 |
| `content.text` | 1–65536 runes | 空或超限返回 `422 invalid_input`。 |
| `content.media_type` | 1–128 runes | 必填，超限被拒绝。 |
| `content.reference_uri` | 1–4096 runes | 超限被拒绝。 |
| `content.declared_hash` | 32–128 位十六进制 | 只校验形状，dMem 不抓取也不校验引用目标。 |
| `content.value`（JSON） | 深度 ≤ 8，节点 ≤ 4096 | 超限被拒绝。 |
| `source.metadata` | 深度 ≤ 4，每层 ≤ 32 键，编码 ≤ 16 KiB | 非有限数值与不支持类型被拒绝。 |

`source.metadata` 校验只检查形状，不读取、不回显取值，因此被拒绝的载荷不会通过
错误消息泄露。

响应 `202` 返回：

```json
{"operation_id": "op-1", "event_id": "event-1", "status": "accepted", "job_ids": ["job-1"], "accepted_at": "2026-09-20T10:00:00Z", "replayed": false}
```

`status` 取 `accepted`、`deferred` 或 `stored`。只有 processing 启用时才创建
`extract_memory` job；未启用时 `status` 为 `stored`、`job_ids` 为空，事件以明确的
`stored` 状态保留，绝不创建注定由 no-op 消费的 job。

### `GET /v1/events/{event_id}`

响应 `200` 返回事件的 `processing_status`（`stored`、`processing`、`processed`、
`failed`）、来源标识与已存内容。它不返回 job 列表或 provider 载荷。

## JSON 解码规则

每个请求体都严格解码：

1. 请求体受路由上限约束。超限返回 `413 request_too_large`。
2. `Content-Type` 必须是 `application/json`，否则返回 `415 unsupported_media_type`。
3. 请求体必须恰好包含一个顶层 JSON 对象。空请求体、非对象值（`null`、数组、
   字符串、数字、布尔值）或第二个 JSON 值都返回 `400 invalid_json`。
4. 未知对象字段返回 `400 invalid_json`。
5. 字段类型不匹配或不符合 `pkg/toolcontract` schema 返回 `400 invalid_json`。
6. 数字使用 `json.Number` 解码，整数值不会经过 `float64` 舍入。

不接受请求体的读路由不解析请求体；非空请求体返回 `400 unexpected_body`。`POST
/v1/recall` 是读语义但接受有界请求体，因为查询意图本身需要表达。

严格解码与边界由测试和 fuzz 目标共同固定：`internal/protocol/httpapi` 的
`FuzzStrictBodyDecodingIsTotal`、`FuzzRecallWireMappingPreservesContractValues` 与
`FuzzEventIngestBoundsAreEnforced` 证明任意字节输入要么完整映射为协议中立命令，
要么以有界协议错误拒绝，且错误响应不回显请求体。

## 传输加固

| 项 | 值 | 说明 |
| --- | --- | --- |
| 默认监听地址 | `127.0.0.1:7373` | 回环，见 `[server].listen_address`。 |
| `ReadHeaderTimeout` | 10s | 防止慢速请求头耗尽连接。 |
| `ReadTimeout` | 30s | 覆盖请求体读取。 |
| `WriteTimeout` | 30s | 覆盖响应写出。 |
| `IdleTimeout` | 120s | keep-alive 空闲上限。 |
| `MaxHeaderBytes` | 1 MiB | 请求头硬上限。 |
| 优雅关闭 | 15s（可配置） | `[server].shutdown_grace_period`。 |
| TLS | 可选，最低 TLS 1.2 | 证书与私钥必须同时提供，且必须是普通文件。 |
| 认证 | **未实现** | 非回环绑定时记录警告；运维必须提供网络边缘保护。 |

## 错误模型

错误响应使用 `pkg/toolcontract` 的 `error` schema：

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
| `invalid_request` | 422 | 适配器（请求头、路径标识符） |
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
- 非 gateway 错误与恢复的 panic 以 `500 internal` 和通用 message 报告，文本永不
  回显。

## HTTP/2 兼容性

- handler 不得检查 `r.ProtoMajor`、`r.Proto` 或连接状态。
- handler 不得创建监听器、`http.Server`、TLS 配置或 h2c 升级。
- 所有请求状态都存在于请求、`CallContext` 或响应中。
- 兼容性通过仅用于测试的 HTTP/2 传输（启用 `EnableHTTP2` 的 `httptest.Server`）
  以及 `internal/server` 的真实 TLS 监听测试证明；本契约不定义 h2c。
