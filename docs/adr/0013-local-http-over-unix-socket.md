# ADR 0013：本地 HTTP/JSON over Unix Domain Socket

- 状态：superseded（被 Memory Gateway 与协议适配器架构取代）
- 关联：当前架构见 [memory-gateway-refactor](../architecture/memory-gateway-refactor.md) 与 [HTTP REST+JSON 契约](../api/http-rest-contract.md)
- 历史说明：本 ADR 记录的 UDS 传输与 OpenAPI 契约已在本仓库中删除；当前运行时组装 `internal/protocol/httpapi` 的 `http.Handler`，但不创建任何 listener

## 背景

第一阶段需要低接入成本、可调试且保持本地用户边界的服务协议。

## 决策

核心本地接口采用 HTTP/JSON over Unix Domain Socket，优先使用 Go 标准库，不引入 Web 或 RPC 框架。

## 影响

- Agent 适配简单，协议易于诊断。
- 强类型客户端和流式能力不是第一阶段重点。
- MCP 等协议作为适配器而非唯一核心接口。

## 取代说明

UDS 传输、socket 生命周期、旧 UDS 客户端与 OpenAPI 契约已删除。协议无关的 Memory Gateway 成为语义核心，首个入站适配器是无传输绑定的 REST+JSON handler；生产 listener、TLS、认证与网络安全边界属于后续独立设计。
