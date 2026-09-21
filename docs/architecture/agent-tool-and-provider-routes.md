# Agent Tool 与 dMem Provider 双路线架构

## 状态与目标

本文定义 dMem 的目标模型执行架构。项目尚未发布，现有 HTTP 路由、DTO、gateway
模块和底层处理流程都不是兼容性边界；实现可以直接重构、替换或移除。实施进度见
[双路线架构开发任务](../development/tasks-agent-tool-and-provider-routes.md)。

dMem 只保留两条模型能力路线：

1. **Agent Tool Route**：默认 Agent 集成。Agent 在正常推理中通过工具召回和提交
   结构化记忆，dMem 不调用 LLM；MCP server 是该路线的标准适配器。
2. **dMem Provider Route**：独立处理路线。dMem 的持久 worker 调用显式配置的 LLM
   和 embedding provider，处理原始事件、图谱、聚合和向量化。

目标是让普通 Agent 只开发一个薄 client，或直接配置 MCP server，即可使用记忆
服务，而不需要在 dMem 中重复配置 Agent 已经拥有的 LLM 和 embedding。需要脱离
Agent 生命周期处理原始事件的部署，仍可选择 dMem Provider Route。

以下形态不属于目标架构：

- dMem 调用 Agent 暴露的模型 endpoint；
- Agent 领取 dMem 内部 job、维护 lease/heartbeat 后回传模型结果；
- dMem 通过 MCP 反向调用 Agent 的 LLM 或 embedding；
- Agent client 直接操作候选表、图谱表、向量表或内部 job；
- 为两条路线维护两套记忆权威、校验规则或召回语义。

## 总体结构

```text
                          +-----------------------+
                          |      Agent Runtime    |
                          |  LLM + tool calling   |
                          +-----------+-----------+
                                      |
                        native client | MCP tools
                                      v
+-------------------+       +-----------------------+
| External sources |       | Agent Tool Adapter    |
| collectors/files |       | REST API / MCP server |
+---------+---------+       +-----------+-----------+
          |                             |
          | raw events                  | typed memory commands
          v                             v
+---------+-----------------------------+-------------------------+
|                         Memory Gateway                          |
|  scope  recall  remember  feedback  forget  event ingestion   |
|  policy  validation  conflict/evolution  authority commit      |
+--------------------------+-------------------------------------+
                           |
                           v
+----------------------------------------------------------------+
| SQLite authority and derived indexes                           |
| revisions/evidence/graph/FTS/optional vectors/jobs/audit       |
+--------------------------+-------------------------------------+
                           ^
                           |
                  validated proposals/results
                           |
+--------------------------+-------------------------------------+
| dMem Provider Route                                            |
| durable jobs -> processing provider / embedding provider       |
+----------------------------------------------------------------+
```

两条路线在进入 dMem 后共享同一套：

- scope、调用方和幂等语义；
- 输入边界和领域校验；
- 记忆、证据、修订与生命周期模型；
- 实体、事实、关系及冲突处理；
- policy、audit 和敏感度规则；
- FTS、可选向量、图谱、排序及上下文预算；
- 写入后的索引维护和派生状态重建。

路线差异仅在于模型推理发生在哪里，以及入口提交的是原始材料还是结构化建议。

## 路线选择

路线按一次写入来源选择，不做隐式 fallback：

| 输入 | 路线 | 行为 |
| --- | --- | --- |
| Agent 正常对话中的记忆 | Agent Tool | Agent 调用 `dmem_remember` 提交结构化建议 |
| Agent 回答前的上下文召回 | Agent Tool | Agent 调用 `dmem_recall` |
| collector、文件或无 Agent 在线的原始事件 | dMem Provider | dMem 摄取事件并创建处理 job |
| 需要 dMem 自动图谱抽取或聚合 | dMem Provider | dMem worker 调 processing provider |
| Tool 路线提交后的确定性索引维护 | 共享本地运行时 | 不调用模型 |

同一实例可以同时启用两条路线。例如 Agent 直接提交对话记忆，同时 collector 事件由
dMem Provider Route 异步处理。单个输入只能有一个模型提取所有者，禁止先由 Agent
结构化写入后又默认创建 `extract_memory` job，避免重复提取和冲突结果。

## Agent Tool Route

### 设计原则

Agent Tool Route 利用 Agent 当前正在进行的 LLM 推理完成“是否值得记忆”和“如何
结构化”的判断。dMem 暴露记忆领域工具，不暴露模型任务或存储实现。

原生集成时，薄 client 位于各个 Agent 应用或其 SDK 中。它的最小职责是：

1. 向 Agent 注册工具定义；
2. 将工具调用映射到 dMem API；
3. 传递稳定的 caller、scope 和幂等身份；
4. 把结果原样返回给 Agent tool loop。

client 不负责：

- 记忆 revision、冲突或 proposal 状态机；
- job 调度、lease、重试或 generation fence；
- SQLite、FTS 或图谱维护；
- 重新调用一个专用于 dMem 的 LLM；
- 在所有 Agent 都不具备该能力时模拟 embedding API。

### 工具集合

第一版稳定工具面为：

| 工具 | 用途 | 是否修改权威 |
| --- | --- | --- |
| `dmem_recall` | 召回与当前任务相关的记忆 | 否；允许写无内容遥测 |
| `dmem_remember` | 原子提交记忆、证据和可选知识建议 | 是 |
| `dmem_feedback` | 报告记忆有用、无用、错误、过时或重复 | 追加反馈 |
| `dmem_forget` | 请求逻辑遗忘一条记忆 | 是 |

scope 管理属于 client/bootstrap API，不要求 LLM 将其作为日常工具调用。管理、审核、
备份、索引重建和 provider 配置也不进入 Agent 工具集合。

### `dmem_recall`

工具输入表达查询意图，不暴露内部通道开关：

```json
{
  "query": "用户为这个项目选择了什么数据库？",
  "scope_ids": ["project-123"],
  "intent": "lookup",
  "entities": ["database"],
  "reference_time": "2026-09-20T10:00:00Z",
  "limit": 10
}
```

返回面向模型消费的有界上下文：

```json
{
  "memories": [
    {
      "memory_id": "mem-123",
      "revision_id": "rev-456",
      "kind": "preference",
      "content": "用户为该项目选择 PostgreSQL。",
      "valid_from": "2026-09-18T09:00:00Z",
      "confidence": 0.96,
      "evidence": []
    }
  ],
  "degraded_capabilities": ["semantic"],
  "next_cursor": null
}
```

dMem 仍负责 scope、生命周期、有效时间、敏感度、预算、去重和结果重新校验。Agent
负责结合当前问题对少量候选做最终语义判断，不把召回内容当作不可质疑的事实。

### `dmem_remember`

一次调用原子提交来源、一个或多个记忆以及可选知识建议：

```json
{
  "scope_id": "project-123",
  "source": {
    "type": "agent_tool",
    "name": "assistant",
    "conversation_id": "conv-123",
    "turn_id": "turn-456",
    "occurred_at": "2026-09-20T10:00:00Z"
  },
  "memories": [
    {
      "local_id": "memory-1",
      "kind": "preference",
      "content": "用户为该项目选择 PostgreSQL。",
      "sensitivity": "normal",
      "confidence": 0.96,
      "search_hints": [
        "项目数据库选择",
        "PostgreSQL",
        "用户偏好的数据库"
      ]
    }
  ],
  "entities": [
    {
      "local_id": "entity-1",
      "type": "product",
      "name": "PostgreSQL",
      "aliases": ["postgres"]
    }
  ],
  "facts": [
    {
      "subject": "user",
      "predicate": "prefers",
      "object": "entity-1",
      "evidence_memory": "memory-1"
    }
  ],
  "relations": []
}
```

具体字段以实施阶段形成的版本化 JSON Schema 为准。Phase 1 形成了
[Agent Tool Contract v1](agent-tool-contract-v1.md)；Phase 2 已把该 schema 映射到
application HTTP API 与单事务原子提交；Phase 4 已实现薄 Go application client 与
`dmem-mcp` stdio 服务器（见 [dmem-mcp stdio 服务器](../api/mcp-server.md)）。
Phase 5 已用显式 `[processing].enabled`/`[embedding].enabled` 收敛 Provider Route，
并删除 no-op 消费模型 job 的流程。核心语义是：

- `source` 与 memory/knowledge 建议在一个幂等事务中提交；
- `local_id` 只在本次命令中解析引用，不成为长期外部 ID；
- `search_hints` 是 Agent 生成的检索辅助文本，不是独立事实；
- Agent 提交的是结构化建议，不可自行设置 `confirmed=true`；
- dMem 根据来源、风险、冲突、敏感度和确定性规则决定直接创建低风险权威、附加证据
  或创建待审核 proposal；
- 覆盖、争议、删除和高风险图谱变更不能仅由模型置信度自动确认；
- 同一调用不会再创建 `extract_memory` 或 `extract_graph` 模型 job，但会创建必要的
  FTS、图谱统计、反馈聚合或 provider embedding 派生工作。

### `dmem_feedback` 与 `dmem_forget`

Tool Route 反馈采用封闭类别：

```text
used  not_used  incorrect  outdated  duplicate
```

自由文本 reason 可选、有长度上限且不进入排序模型的未审查特征。`dmem_forget` 是
独立工具，执行逻辑遗忘并保留审计，不把 `forget` 混入反馈类别。

Phase 2 已用该工具契约替换旧 HTTP feedback DTO：Tool feedback 以
`agent_tool_feedback` purpose 持久化并原样保留五类 kind，不再把 `not_used` 映射为
`irrelevant`。最终 authority/proposal、枚举、evidence、MCP 与 client 决策见
[Phase 0 决策与基线](phase-0-decisions-and-baseline.md)。

### Agent 行为模式

client 提供两种提示策略，但不改变 dMem API：

- `opportunistic`：默认。Agent 自主决定何时 recall、remember 和 feedback。
- `checkpoint`：宿主在 turn 结束前要求 Agent 明确选择 `remember` 或 `no_memory`，
  适用于支持 required tool choice 或生命周期 hook 的框架。

checkpoint 是 Agent client 行为，不要求 dMem 保存对话 session，也不新增一个长期
`dmem_checkpoint` 领域资源。

### Client 与 MCP

Phase 4 已实现本节描述的 Go application client 与 stdio MCP server；原生 Agent
adapter 仍位于各 Agent 应用或 SDK 中。协议中立的薄 client contract 为：

```text
recall(input)   -> RecallResult
remember(input) -> RememberResult
feedback(input) -> FeedbackResult
forget(input)   -> ForgetResult
```

具体 Agent adapter 位于 Agent 应用或对应 SDK 中，只负责把同一份 JSON Schema 转为
OpenAI、Anthropic、LangChain 等框架的工具定义。其实现语言由 Agent 宿主决定，
不要求使用 Go。支持 MCP 的 Agent 可以不实现原生 dMem client，只配置 `dmem-mcp`；
MCP server 在自己的进程内复用 dMem 仓库提供的 Go application client，是薄协议
适配器：

```text
Agent -> MCP tool -> dmem-mcp -> dMem application API -> Memory Gateway
```

MCP server 不调用模型、不访问 SQLite、不拥有记忆规则，也不通过 sampling 反向要求
Agent 执行 dMem 内部任务。第一版使用 MCP `tools`；resources、prompts 和 sampling
不是基础依赖。

仓库边界（Phase 4 已按此实现）：

```text
cmd/dmem-mcp/                 MCP server 进程
pkg/client/                   dmem-mcp 复用的 Go application client
pkg/toolcontract/             工具 DTO、JSON Schema 与版本
internal/protocol/mcp/        MCP stdio adapter
internal/protocol/httpapi/    application HTTP adapter
internal/gateway/...          领域语义
```

`pkg/client` 不部署进每个 Agent，也不限定外部 Agent client 的语言。原生 Agent 集成
应在各自应用或 SDK 中基于同一 tool contract 实现薄 adapter；后续只有出现具体消费
者时，才在 dMem 仓库提供额外语言的官方 client。

具体包名可在实现阶段调整，但工具契约必须只有一个来源，禁止 MCP DTO、HTTP DTO 和
各语言 SDK 分别演进。

## 无 dMem Embedding 的检索

Agent Tool Route 的基础能力不能依赖 Agent 暴露 embedding。tool calling 和 MCP 都
不能保证宿主提供可编程 embedding 原语，因此默认召回使用：

```text
FTS keyword
+ canonical memory content
+ search hints
+ entity names and aliases
+ typed graph expansion
+ temporal, recency, quality and feedback features
+ deterministic ranking and context selection
```

Agent 当前 LLM 对返回的有界候选完成最终语义筛选。`dmem_recall` 必须报告实际执行
能力；未配置 dMem embedding provider 时，`semantic` 显式降级，不将关键词/图谱
结果伪装为向量召回。

`search_hints` 属于可重建的检索辅助投影或受 revision 约束的结构化属性。它们参与
FTS，但不能在返回结果中冒充用户陈述，也不能绕过 scope、敏感度或生命周期过滤。

### 可选 Agent 向量扩展

基础版本不要求该扩展。若某个 Agent SDK 确实暴露稳定 embedding API，未来可在薄
client 中提供可选 adapter，并让 recall/remember 接受版本化向量信封。启用前必须
解决：

- document 与 query 使用同一兼容模型空间；
- model identity、dimension、document/query format 进入 compatibility identity；
- 向量有限、非零且长度严格匹配；
- 模型变化会使已有向量失效并触发重建；
- dMem 仍生成规范化 document/query input，client 不复制格式算法。

在这些条件未完成前，不接受“任意 Agent 提交一个同维向量”。维度相同不表示向量
空间兼容。

## dMem Provider Route

dMem Provider Route 保留现有持久异步处理模型，但收敛为显式选择的独立路线。Phase 5
已实现本节的能力开关、事件入口和 provider 失败分类；原始事件通过
`POST /v1/events` 摄取，禁用时只以 `stored` 状态保留：

```text
raw event
  -> durable processing job
  -> bounded and policy-filtered input
  -> dMem processing provider
  -> validated candidate/graph/aggregate proposal
  -> review or deterministic confirmation

memory revision
  -> durable embedding job
  -> canonical document
  -> dMem embedding provider
  -> validated vector
  -> semantic index
```

该路线适用于：

- collector 或文件等非 Agent 来源；
- Agent 不在线时仍需持续处理的输入；
- 需要统一模型、稳定批处理和可控重试的部署；
- dMem 作为独立记忆服务而不是某个 Agent 的附属工具运行。

provider 调用继续遵守：

- 默认关闭和显式配置；
- 默认拒绝的出网策略；
- 输入裁剪与 disclosure filtering；
- 请求超时、并发、RPM 和响应大小限制；
- SQLite 写事务外调用；
- lease、重试、配置/policy generation fence；
- 严格输出 contract 和 authority/proposal 分离；
- 密钥只通过环境变量引用延迟解析。

配置应明确表达能力是否启用，而不是依靠 no-op processor 模糊状态：

```toml
[processing]
enabled = false

[embedding]
enabled = false
```

启用后才要求对应 provider 配置。Agent Tool Route 不要求增加 `executor = "agent"`，
因为它不是 dMem 后台 job executor；它是独立的结构化应用入口。

## API 重构方向

### 目标 application API

HTTP 是 client 和 MCP server 共用的 dMem application transport。目标资源面至少包含：

| 操作 | 目标语义 |
| --- | --- |
| recall | 有界召回，支持显式能力降级 |
| memory commit | 原子提交 source、memories 和知识建议 |
| feedback | 追加封闭类别反馈 |
| forget | 逻辑遗忘 |
| scope ensure/get | client bootstrap 和稳定 scope 映射 |
| raw event ingest | 仅供启用 Provider Route 的原始输入 |
| operation/event status | 仅在异步 provider 处理需要时查询 |

实现阶段可以重新设计路径和 DTO，不以当前九条 `/v1` 路由为兼容目标。应先确定
gateway 命令和版本化 tool schema，再决定 REST 资源形状；MCP 与 HTTP 不得各自发明
领域语义。

### 可移除或收敛的现有流程

实施时允许删除无法服务目标架构的接口和底层流程，包括但不限于：

- 把当前九路由视为冻结外部契约的文档和测试；
- 只为旧 DTO 存在的转换层、兼容字段和路由元数据；
- provider 禁用时由 `builtin-noop` 领取并完成模型 job 的流程；
- Agent Tool 写入后再次创建 memory/graph 模型提取 job 的流程；
- 如果新 memory commit 已覆盖其语义，旧的重复直接写入入口；
- 对目标产品不可达的同步 aggregate/provider 入口；
- 依赖已移除 API 且没有内部消费者的 gateway/storage capability。

删除必须基于调用图和测试确认。记忆 revision、证据、policy、审核、幂等、审计、
索引维护及恢复能力不能因为入口重构而被绕过。

## 一致性与幂等

`dmem_remember` 是一次原子、可重放的写命令。幂等身份至少绑定：

```text
caller identity
+ stable operation name
+ scope
+ client idempotency key
+ canonical command hash
```

相同 key 和相同命令返回首次结果；相同 key 和不同命令返回冲突。Agent tool loop、
MCP transport 或 HTTP 响应丢失都不能创建重复记忆。

来源身份优先使用宿主已有的 conversation/turn/tool-call ID。client 无稳定 ID 时生成
并持久到本次调用重试范围内，不把随机 key 每次重试都重新生成。

Provider Route 继续使用 durable job 的 at-least-once 执行和 fenced commit。两条路线
可以产生相同候选内容，最终仍通过 specification hash、证据和 consolidation 规则
去重，而不是依赖模型自行避免重复。

## 安全边界

MCP server 和 client 不是可信模型输出的豁免通道。所有工具请求仍需：

- 严格 JSON 和未知字段拒绝；
- 数组、字符串、图谱条目和请求体硬上限；
- caller、scope 和 capability 授权；
- sensitivity 与 disclosure policy；
- 封闭 memory kind、entity type、predicate 和 feedback 枚举；
- evidence、本地引用、时间区间和数值范围校验；
- 日志不记录记忆正文、查询文本、向量或工具完整载荷；
- 非回环部署使用独立认证和网络边缘保护。

Agent 提供的 confidence 仅是一个输入特征，不能授权覆盖、遗忘、确认冲突或降低
敏感度。

## 可观测性

两条路线必须使用统一 provenance，至少区分：

```text
origin = agent_tool | dmem_provider | manual | import
client/tool contract version
provider/model identity（仅 Provider Route）
caller and scope
request/correlation/idempotency identity
source conversation/turn/event identity
```

指标和审计应能回答：

- recall 使用了哪些能力、为何降级；
- Agent Tool 提交产生了 authority、proposal、deduplicated 还是 rejected 结果；
- Provider job 的等待、重试、阻塞和失败原因；
- FTS、图谱和语义索引的新鲜度；
- tool contract 和 provider contract 的版本分布。

不得在指标、普通日志或无内容遥测中记录正文。

## 关键决策摘要

1. 只有 Agent Tool Route 和 dMem Provider Route，不建设 Agent Job Executor Route。
2. Tool Route 提交记忆领域命令，不暴露 dMem 模型 job。
3. MCP server 是薄适配器，不使用 sampling 反向执行 dMem 任务。
4. 无 dMem provider 时仍提供 FTS、search hints、实体和图谱召回。
5. Agent embedding 是未来可选扩展，不是 Tool Route 接入要求。
6. Provider Route 保留 durable jobs、出网策略和 fenced commit。
7. 两条路线共享同一权威、校验、policy、索引和召回流水线。
8. 当前 HTTP 契约没有兼容性约束，可按目标工具语义重构和删除。
