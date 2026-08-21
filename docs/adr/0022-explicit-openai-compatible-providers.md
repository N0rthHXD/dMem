# ADR 0022：显式 OpenAI-compatible provider

- 状态：accepted
- 日期：2026-08-21

## 决策

首个生产 Processor 和 EmbeddingProvider 使用最小 OpenAI-compatible HTTP JSON 协议，但不依赖厂商 SDK。默认关闭；必须显式配置 provider kind、HTTPS endpoint、model、secret environment-variable reference、允许的 space/content type 和 egress policy version。

实现禁止默认云凭据链、metadata discovery、redirect、telemetry、更新检查和辅助网络请求。Authorization 值只在发送请求时解析，不进入配置响应、日志、错误、audit 或 backup。仅发送有界 target content、允许字段和有界同空间 context；reference 默认只发送 metadata，不读取 URI。

HTTP client 使用 deadline、取消、并发限制、速率限制和稳定错误分类。Provider/model、policy version、结果类别和外部 request id 可审计，但正文和原始 provider response 不可审计或记录。外部依赖失败不影响 ingest、手工 memory 和 keyword search。
