# ADR 0013：本地 HTTP/JSON over Unix Domain Socket

- 状态：accepted

## 背景

第一阶段需要低接入成本、可调试且保持本地用户边界的服务协议。

## 决策

核心本地接口采用 HTTP/JSON over Unix Domain Socket，优先使用 Go 标准库，不引入 Web 或 RPC 框架。

## 影响

- Agent 适配简单，协议易于诊断。
- 强类型客户端和流式能力不是第一阶段重点。
- MCP 等协议作为适配器而非唯一核心接口。
