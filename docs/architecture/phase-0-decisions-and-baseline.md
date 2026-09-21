# 双路线架构 Phase 0 决策与基线

## 状态与范围

状态：accepted

日期：2026-09-20

本文固化 Agent Tool Route 与 dMem Provider Route 重构开始前的产品决策、当前调用图
和测试基线。它约束后续 Phase 1 及以上的实现。

> **Phase 2-4 后状态**：九条旧 HTTP 路由及其 DTO 已被目标 application API 替换并
> 删除，`agent_tool` 单事务原子提交、`agent_tool_feedback`、修订围栏遗忘、原子
> scope ensure 与 `search_hints` 持久化均已实现（Phase 2）；无 provider 检索闭环与
> 固定检索门禁已实现（Phase 3）；`pkg/client` 薄 application client 与
> `dmem-mcp` stdio 服务器已实现（Phase 4）。Provider Route 收敛仍属后续阶段。本文
> 其余调用图与矩阵保留为重构前基线记录。

## 产品决策

### Remember 权威边界

`dmem_remember` 采用规则驱动的混合模式。Agent 只能提交结构化建议和 confidence，
不能提交 `confirmed`、`skip_policy`、`skip_review` 或等价字段。dMem 在同一个幂等
事务中根据下表确定结果：

| 输入分类 | v1 结果 | 约束 |
| --- | --- | --- |
| 全新、`normal` 敏感度、kind 为 `fact`、`event`、`preference`、`plan` 或 `procedure`，且无重复、演化或冲突信号 | 创建低风险权威 | 必须通过 scope/policy、封闭枚举、内容、时间、证据和数量边界；source 产生至少一条 `direct` evidence |
| 与当前 revision 规范内容完全重复 | 向现有 revision 附加 evidence，返回 `deduplicated` | 不创建新 memory/revision；不得降低敏感度 |
| 可能重复、补充、替换或冲突 | 创建 evolution proposal | 不自动覆盖、争议、修订或删除权威 |
| kind 为 `scene`、`topic` 或 `timeline` | 创建 aggregate proposal | 聚合继续使用有界输入 revision 和审核流程 |
| `sensitive` 内容 | 创建 proposal | v1 不由 Agent 建立敏感内容权威；后续放宽必须是显式 policy 决策 |
| entity、alias、mention、entity fact 或 memory relation 建议 | 创建相应 proposal | v1 不对 Agent 图谱建议执行自动确认；predicate registry 只负责校验，不授予 authority |
| 请求覆盖、遗忘、确认冲突、降低敏感度或指向冻结/过时 revision | 拒绝或创建相应 proposal | confidence 不参与授权；遗忘只能使用 `dmem_forget` |

一次提交中的 source、memories、proposals、evidence、audit 和幂等响应必须全部提交或
全部回滚。Tool Route 不创建 `extract_memory` 或 `extract_graph` job。确定性的 FTS、
实体/图谱投影和反馈维护可以本地执行；仅显式启用 embedding provider 时可为已提交
revision 创建 embedding 派生工作。

### v1 封闭枚举

| 类别 | v1 值 | 说明 |
| --- | --- | --- |
| memory kind | `fact`、`event`、`preference`、`plan`、`procedure`、`scene`、`topic`、`timeline` | 后三项只能走 aggregate proposal |
| entity type | `person`、`organization`、`place`、`project`、`product`、`event`、`concept`、`other` | 与当前 predicate registry 的类型约束一致；v1 不接受 `technology` |
| memory relation | `supports`、`contradicts`、`supersedes`、`derived_from` | 相似度不是权威 relation |
| feedback | `used`、`not_used`、`incorrect`、`outdated`、`duplicate` | Tool Route 的唯一公开反馈枚举；可选 reason 有界且不作为未审核排序特征 |

图谱 fact 的 predicate 使用 `internal/domain/graph_predicates.go` 中
`predicate-registry-v2` 的全部封闭名称：

```text
same_as member_of has_member works_for employs reports_to manages leads
located_at born_in owned_by owns prefers dislikes works_on created created_by
participates_in has_participant plan_targets has_status is_active occurred_at
occurred_during has_participant_count depends_on required_by derived_from
```

当前 `memory_quality_feedback`、`retrieval_feedback`、`answer_feedback` 及其 type/reason
组合属于待替换的旧 HTTP/gateway DTO，不定义目标工具契约。Phase 1 应定义五类 feedback
的协议中立语义；Phase 2 迁移存储时不得把 `not_used` 自动等同于“无关”，也不得通过
兼容映射丢失原始类别。

### Evidence 披露

Tool Route v1 不返回 evidence 正文或片段。`dmem_recall` 默认返回经过授权、受数量上限
约束的引用，最多包含 `evidence_id`、`event_id`、role、时间/序号和合法 span offset；
引用仍受 scope、policy 和 sensitivity 检查。没有安全引用时返回空数组。

当前 recall 在 `include_evidence=false` 时不返回 evidence，在 `true` 时返回上述引用，
不会读取原始正文。目标 contract 不沿用该布尔字段作为兼容要求。未来如增加片段，
必须另行决定显式请求、字符/数量上限、offset 校验和 disclosure filtering。

### MCP 与 Client

- MCP v1 只实现 stdio transport 和四个 tools：`dmem_recall`、`dmem_remember`、
  `dmem_feedback`、`dmem_forget`。Phase 4 已按此实现 `cmd/dmem-mcp` 与
  `internal/protocol/mcp`。
- MCP streamable HTTP、resources、prompts 和 sampling 不属于 v1。增加 streamable HTTP
  需要独立产品与安全决策。
- dMem 仓库内首发 Go application client（Phase 4 位于 `pkg/client`），部署在
  `dmem-mcp` 进程中供 MCP adapter 复用；它也可被原生 Go 集成选择性复用，但不是
  所有 Agent 应用的运行时依赖。
- 原生 client 位于各个 Agent 应用或对应 SDK 中，语言由 Agent 宿主决定，并基于同一
  tool contract 实现。支持 MCP 的 Agent 不需要开发原生 dMem client。
- dMem 仓库只有在出现具体消费者后才提供 TypeScript/Python 等额外官方 client。
- MCP 只能调用公共 application client，不访问 SQLite、storage、jobs 或 provider，
  不调用模型，也不通过 sampling 执行 dMem job。

## 当前调用图

共享生产组装链为：

```text
internal/daemon/daemon.go
  -> storage.Repository
  -> storage.{Memory,Recall,Feedback,Event,Scope}Capability
  -> gateway/{memory,retrieval,feedback,extraction,scope}.Service
  -> internal/protocol/httpapi.Handler.buildRoutes
  -> internal/server.Server
```

所有 gateway 写操作通过 `gateway.NewWriteIdentity` 形成调用方、稳定操作名、幂等 key
和规范化命令哈希；storage 写入 `idempotency_records` 与 `audit_events`。HTTP handler
是九条路由唯一的通用生产调用方。额外的直接消费者只有恢复流程中的 staging probe：
`internal/maintenance/maintenance.go` 直接向 assembled handler 调用 `/v1/recall`。

### 路由与模块矩阵

| 当前入口 | Handler -> gateway | storage 路径 | 主要 schema / job 影响 | 生产消费者 | 处置 |
| --- | --- | --- | --- | --- | --- |
| `POST /v1/memories` | `handleRemember` -> `memory.Service.Remember` -> `AuthorityStore.Remember` | `MemoryCapability.Remember` -> `Repository.Remember` | 写 `source_streams`、`raw_contents`、`raw_events`、`memories`、`memory_revisions`、`memory_evidence`、`memory_state_events`、投影/FTS、幂等和审计；当前可创建 `extract_graph`、`embed_memory`、`rebuild_semantic` | HTTP handler | `replace`：原子多 memory commit；禁止 Tool Route graph/memory extraction job |
| `GET /v1/memories/{memory_id}` | `handleGetMemory` -> `memory.Service.Get` -> `AuthorityStore.GetMemory` | `MemoryCapability.GetMemory` -> `Repository.GetMemory` | 读 memory/revision/evidence/conflict/aggregate 权威和投影；无 job | HTTP handler | `remove`：目标 Agent/application 面无消费者；保留内部 authority read |
| `POST /v1/memories/{memory_id}/forget` | `handleForget` -> `memory.Service.Forget` -> `AuthorityStore.ForgetMemory` | `MemoryCapability.ForgetMemory` -> `Repository.ForgetMemory` | 写 lifecycle state/event、投影/FTS、幂等和审计；可创建 semantic/aggregate 派生重建 | HTTP handler | `replace`：保留逻辑遗忘、审计和派生失效语义 |
| `POST /v1/memories/{memory_id}/feedback` | `handleFeedback` -> `feedback.Service.Append` -> `Store.AppendFeedback` | `FeedbackCapability.AppendFeedback` -> `Repository.AppendCurrentFeedback` | 写 `feedback_events`、幂等和审计；当前可写 evolution proposal；创建 `aggregate_feedback` | HTTP handler | `replace`：采用五类 Tool feedback，保留追加权威和聚合 |
| `POST /v1/recall` | `handleRecall` -> `retrieval.Service.Recall` -> capability/policy/candidate/telemetry ports | `RecallCapability` -> `Repository.executeRecall` / `recordQueryTelemetry` | 读 FTS、可选 vector、entity/graph、feedback、权威/索引元数据；写无内容 telemetry；无 job | HTTP handler；restore staging probe | `replace`：保留流水线，收敛 DTO 并修复 replay 重复 telemetry |
| `POST /v1/events` | `handleIngestEvent` -> `extraction.Service.Ingest` -> `EventStore.IngestEvent` | `EventCapability.IngestEvent` -> `Repository.IngestEvent` | 写 stream/content/event/job、幂等和审计；当前无条件创建 `extract_memory` | HTTP handler | `replace`：仅 Provider Route；disabled 时不得创建 no-op 模型 job |
| `GET /v1/events/{event_id}` | `handleGetEvent` -> `extraction.Service.GetEvent` -> `EventStore.GetEvent` | `EventCapability.GetEvent` -> `Repository.GetEvent` | 读 raw event/content；无 job | HTTP handler | `replace`：并入异步 operation/event status |
| `POST /v1/scopes` | `handleCreateScope` -> `scope.Service.Create` -> `Store.CreateScope` | `ScopeCapability.CreateScope` -> `Repository.CreateSpace` | 写 `memory_spaces`、有效 policy、幂等和审计；无 job | HTTP handler | `replace`：以稳定外部身份实现 scope ensure |
| `GET /v1/scopes/{scope_id}` | `handleGetScope` -> `scope.Service.Get` -> `Store.GetScope` | `ScopeCapability.GetScope` -> `Repository.GetSpace` | 读 `memory_spaces`；无 job | HTTP handler | `retain`：保留目标语义，迁移到统一 contract，不冻结当前路径/DTO |

`retain` 只表示领域/application 语义保留，不表示保留当前 HTTP 路径。`replace` 必须先
迁移上述消费者再删除旧实现。`remove` 只删除公开入口，不能删除 recall、feedback、
forget、review 或维护流程仍需的 authority read。

### 测试消费者与删除边界

| 层 | 当前消费者 |
| --- | --- |
| HTTP contract | `internal/protocol/httpapi/{handler,decode,errors,http2,routes}_test.go`；其中 `routes_test.go` 冻结九路由，只在 Phase 2 新 API 测试就位后删除 |
| Gateway | `internal/gateway/{memory,scope,feedback,retrieval,extraction}/*_test.go` |
| Storage adapter | `internal/storage/gateway_adapters_test.go` |
| Authority/derived | `memories_test.go`、`feedback_test.go`、`event_stream_test.go`、retrieval/graph/semantic/job 测试 |
| 生产集成 | `internal/daemon/integration_test.go` 与 `internal/server` 的真实监听测试 |
| 恢复 | `internal/maintenance/maintenance_test.go` 通过 staging recall probe 验证恢复数据库 |

Phase 0 不删除代码：每条现有路由仍有生产 handler 消费者，路由组装仍依赖全部五个
gateway service，恢复 probe 仍依赖当前 recall 路径。安全删除点是 Phase 2 完成调用方
迁移之后；届时应删除旧 DTO、冻结路由元数据/测试和失去消费者的 gateway 方法，并
再次搜索生产、测试和文档引用。

## 可重复测试基线

Phase 0 使用现有测试保存重构前行为，不新增独立测试体系。最小能力基线：

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/gateway/memory ./internal/gateway/scope ./internal/gateway/feedback ./internal/gateway/retrieval ./internal/gateway/extraction
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/protocol/httpapi
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/storage -run 'Test(MemoryCapability|ScopeCapability|Feedback|RecallCapability|IngestEvent|EventCapability)'
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/jobs
make schema-check
make quality
make offline-default
```

现有 `retrieval-corpus-v1` 门禁要求 keyword exact recall 为 `1.0`、无答案无关召回为
`0.0`、兄弟 scope 污染为 `0`。Phase 3 已实现 Agent Tool 典型查询固定集：
`internal/storage/testdata/agent-tool-recall-corpus-v1.json` 与
`agent-tool-recall-gates-v1.json` 要求 macro `Recall@10 >= 0.80`，同时 canonical
exact recall 保持 `1.0`、兄弟 scope 污染保持 `0`；该语料独立于 provider 语义语料。

完整回归基线为 `make test`；并发与静态基线为 `make test-race` 和 `make vet`。恢复与
可移植性基线分别为 `make restore-drill` 和 `make portability-drill`。实际执行结果记录
在 Phase 0 任务实施记录中。

## Phase 1 输入

Phase 1 可以直接使用本页决策定义协议中立命令和唯一 v1 JSON Schema，不得通过保留
旧 HTTP DTO 回避以下边界：mixed authority/proposal、五类 Tool feedback、reference-
only evidence、stdio-only MCP、供 `dmem-mcp` 复用的仓库内 Go application client、
宿主语言中立的原生集成，以及 `predicate-registry-v2`。
