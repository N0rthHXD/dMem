# ADR 0022：显式 OpenAI-compatible provider

- 状态：accepted
- 日期：2026-08-21

## 决策

首个生产 Processor 和 EmbeddingProvider 使用最小 OpenAI-compatible HTTP JSON 协议，但不依赖厂商 SDK。两类能力独立配置、独立启用并使用独立 client；各自必须显式配置 provider kind、HTTPS endpoint、model、secret environment-variable reference、允许的 space/content type/source type/sensitivity、telemetry disabled 和 egress policy version。processing 和 embedding 可以使用不同 endpoint、model、凭据、并发和速率预算，任一能力关闭不阻止另一能力运行。

实现禁止默认云凭据链、metadata discovery、redirect、telemetry、更新检查和辅助网络请求。Authorization 值只在发送请求时解析，不进入配置响应、日志、错误、audit 或 backup。策略在解析 Authorization 和发起网络请求前检查。`max_content_chars` 是 target 与 context 的 text/JSON 字符总预算；`max_context_items` 是独立的 context 数量上限。仅发送允许字段和有界同空间 context；reference 默认只发送 metadata，不读取 URI。

HTTP client 使用 deadline、取消、并发限制、速率限制和稳定错误分类。Provider/model、policy version、结果类别和外部 request id 可审计，但正文和原始 provider response 不可审计或记录。外部依赖失败不影响 ingest、手工 memory 和 keyword search。CLI 使用 `--processing-provider-*` 和 `--embedding-provider-*` 两组参数；未发布阶段直接移除旧 `--provider-*` 参数，不定义兼容优先级。
