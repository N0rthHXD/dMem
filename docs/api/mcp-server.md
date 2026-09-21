# dmem-mcp stdio 服务器

## 状态

Phase 4 已实现。本文描述当前可执行的 `dmem-mcp` 进程：它是 Agent Tool Route 的
MCP stdio 适配器，复用仓库内的 Go application client（`pkg/client`）与唯一版本化
工具契约（`pkg/toolcontract`）。MCP streamable HTTP、resources、prompts 和
sampling 不属于 v1，尚未实现。

## 定位

```text
Agent -> MCP tool -> dmem-mcp -> dMem application HTTP API -> Memory Gateway
```

`dmem-mcp` 是薄协议适配器：

- 不调用模型，不使用 MCP sampling 反向要求 Agent 执行 dMem 内部任务；
- 不访问 SQLite、storage、jobs 或 provider；
- 不拥有记忆规则、审核、幂等或索引维护；
- 不运行后台 worker，也不需要 LLM 或 embedding 配置。

它只做四件事：把 MCP JSON-RPC 消息翻译为 application client 调用、注入宿主身份
（caller、scope、source、请求与幂等身份）、把结构化结果回传，以及把 dMem 结构化
错误原样暴露给 Agent。

## 最小 Agent 配置

支持 MCP tools 的 Agent 只需要配置 `dmem-mcp` 与一个 scope：

```json
{
  "mcpServers": {
    "dmem": {
      "command": "/absolute/path/dmem-mcp",
      "args": [
        "--endpoint", "http://127.0.0.1:7373",
        "--scope-type", "project",
        "--scope-external-id", "project-123"
      ]
    }
  }
}
```

配置文件格式由各 Agent 宿主决定；上面的字段是通用形状，不绑定任何商业模型。
`dmemd daemon` 必须先在该 endpoint 上运行。

## 配置

所有设置都可以用命令行选项或环境变量提供；命令行优先。`--scope-type` 与
`--scope-external-id` 是必填项。

| 选项 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `--endpoint` | `DMEM_ENDPOINT` | `http://127.0.0.1:7373` | dMem application base URL，必须为 `http`/`https`，不能带 query 或 fragment。 |
| `--caller-subject-id` | `DMEM_CALLER_SUBJECT_ID` | 空 | 主要审计身份。 |
| `--caller-client-id` | `DMEM_CALLER_CLIENT_ID` | `dmem-mcp` | 实际发起的客户端身份。 |
| `--scope-type` | `DMEM_SCOPE_TYPE` | 必填 | scope 类型，例如 `project`、`user`、`session`。 |
| `--scope-external-id` | `DMEM_SCOPE_EXTERNAL_ID` | 必填 | 稳定外部标识；首次需要时按该标识 ensure scope 并缓存。 |
| `--scope-display-name` | `DMEM_SCOPE_DISPLAY_NAME` | 等于 external id | scope 显示名。 |
| `--scope-parent-id` | `DMEM_SCOPE_PARENT_ID` | 空 | 可选父 scope 的 application ID。 |
| `--source-name` | `DMEM_SOURCE_NAME` | `assistant` | 记忆提交记录的 provenance source 名称。 |
| `--integration-mode` | `DMEM_INTEGRATION_MODE` | `opportunistic` | Agent 记忆策略：`opportunistic` 或 `checkpoint`。 |
| `--request-timeout` | `DMEM_REQUEST_TIMEOUT` | `30s` | 单次 tool call 上限（含 client 重试），必须大于零且不超过 1 小时。 |
| `--log-level` | `DMEM_LOG_LEVEL` | `info` | stderr 日志级别：`debug`、`info`、`warn`、`error`。 |
| `--version` | — | — | 打印版本并退出。 |

退出码：用法与配置错误为 `2`，运行期错误为 `1`，成功为 `0`。日志只写 stderr；
stdout 只承载 MCP 消息。

## 工具

`tools/list` 返回四个工具，其 input/output schema 直接派生自
`pkg/toolcontract/schema/v1`：

| 工具 | 语义 | 注解 |
| --- | --- | --- |
| `dmem_recall` | 有界召回，报告 `degraded_capabilities` | 只读、幂等 |
| `dmem_remember` | 原子提交 source、记忆与可选知识建议 | 非只读 |
| `dmem_feedback` | 追加 `used`/`not_used`/`incorrect`/`outdated`/`duplicate` 反馈 | 非只读 |
| `dmem_forget` | 按期望修订逻辑遗忘 | 非只读、可破坏 |

MCP 广播的 schema 与 canonical contract schema 的差异只有两项，且是机械放宽：

- `scope_id` / `scope_ids` 变为可选，因为服务器会注入配置的默认 scope；
- `dmem_remember` 的 `source` 属性被移除，因为来源身份由传输层生成。

其余字段、枚举与上限与 canonical schema 完全一致；服务器在注入身份后仍用
canonical schema 做严格解码。

## Scope 与来源身份

- scope 在首次需要时通过 `scope/ensure` 解析为 application `scope_id` 并缓存；
  失败不会缓存，下一次调用会重试。
- 调用省略 scope 时注入配置的 scope。调用显式提供 scope 时，必须与配置的 scope
  完全一致，否则返回 `permission_denied`；MCP server 只服务一个 scope。
- `dmem_remember` 的 `source` 由服务器生成：`type = agent_tool`，`name` 来自
  `--source-name`，`conversation_id` 是本次 MCP 会话的稳定标识，`turn_id` 每次调用
  重新生成，`tool_call_id` 为 JSON-RPC 请求 ID，`occurred_at` 为当前 UTC 时间。
  Agent 不能提供或伪造 `source`；提供时会返回 `invalid_json`。

## 集成模式

`initialize` 响应的 `instructions` 字段携带版本化记忆策略提示
（`toolcontract.MemoryPolicyPrompt`）：

- `opportunistic`（默认）：Agent 在正常 tool loop 中自行决定何时 recall、remember
  和反馈。
- `checkpoint`：宿主要求 Agent 在每轮结束前明确选择 `remember` 或不做记忆调用。

checkpoint 是 Agent client 行为，不改变 dMem API，也不新增领域资源。原生 client
可以直接嵌入同一提示文本。

## 错误与取消

- 未知工具名：JSON-RPC 错误 `-32602`。
- 参数不符合契约、scope 不匹配、dMem 领域错误（`invalid_input`、`conflict`、
  `permission_denied`、`capability_unavailable` 等）：`tools/call` 结果带
  `isError: true`，`content[0].text` 是 `pkg/toolcontract` 的 error JSON，包含稳定
  `code`、`retryable`、`retry_after_ms` 与字符串 `details`。Agent 应基于 `code` 与
  `retryable` 分支，绝不解析 `message`。
- 成功结果同时提供 `structuredContent`（JSON 对象）与序列化后的 `text`，供不支持
  结构化结果的客户端回退。
- `notifications/cancelled` 会取消对应请求的 application 调用，并且不返回该请求的
  响应。
- 每个 tool call 受 `--request-timeout` 约束，取消与超时都会传播到 HTTP 请求。
- 单条入站消息受 2 MiB 硬上限约束；超限返回 JSON-RPC `-32700` 并继续服务。

## 隐私

`dmem-mcp` 与 `pkg/client` 的日志只包含方法、路径、状态、请求 ID 与耗时；工具参数、
记忆正文、查询文本与向量永不写入日志。诊断信息只写 stderr，且不得进入 stdout。
