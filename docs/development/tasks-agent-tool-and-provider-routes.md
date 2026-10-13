# 双路线架构开发任务

## 目标

按 [Agent Tool 与 dMem Provider 双路线架构](../architecture/agent-tool-and-provider-routes.md)
把 dMem 收敛为：

1. 默认的 Agent Tool Route，包括薄 client 和 MCP server；
2. 可选的 dMem Provider Route，包括 dMem 自有 LLM/embedding provider 和持久 job。

项目尚未发布，本任务不保留重构前九条 HTTP 路由或旧 DTO 的兼容性。每个阶段都应
删除已经失去消费者的接口、实现和测试，避免在新架构旁长期保留旧路线。Phase 7 完成
后，仓库中已不存在重构前的九路由 handler、旧 DTO 或标记为“冻结契约”的测试。

## 全局完成标准

- Agent 不配置 dMem LLM/embedding，也能通过 MCP 或薄 client 完成 remember、recall、
  feedback 和 forget。
- Tool Route 不创建 memory/graph 模型提取 job，也不要求后台 Agent worker。
- 未配置 embedding provider 时，FTS、search hints、实体和图谱检索可用，并明确报告
  semantic 降级。
- 启用 Provider Route 时，原始事件处理、图谱提取和向量化继续使用 durable job、
  policy、重试和 generation fence。
- 两条路线写入同一记忆权威并使用相同校验、审核、幂等、审计和检索语义。
- MCP、HTTP 和 client 的工具 schema 来自单一版本化契约。
- 移除旧 API 后不存在不可达 gateway/storage 流程或仅为兼容保留的代码。
- 文档、配置示例、架构测试和集成测试与最终实现一致。

## 阶段依赖

```text
Phase 0 决策与基线
  -> Phase 1 领域命令与工具契约
      -> Phase 2 application API 与旧接口替换
          -> Phase 3 无 provider 的检索闭环
              -> Phase 4 薄 client 与 MCP server
      -> Phase 5 Provider Route 收敛
          -> Phase 6 可选向量增强
              -> Phase 7 清理、加固与发布门禁
```

Phase 3、4 与 Phase 5 在 Phase 2 后可以并行，但共享契约或 schema 的修改必须先合并。

## Phase 0：决策、调用图与测试基线

### 任务

- [x] 记录当前九条 HTTP 路由到 gateway、storage、job 和 schema 的完整调用图。
- [x] 标记每个现有接口为 `retain`、`replace` 或 `remove`，并记录唯一消费者。
- [x] 确认第一版 `dmem_remember` 是创建低风险权威、创建 proposal，还是按规则混合；
      固化规则表，不允许 Agent 自行选择 confirmed 状态。
- [x] 确认第一版支持的 memory kind、entity type、predicate、relation 和 feedback 枚举。
- [x] 确认 Tool Route 第一版是否返回 evidence 正文；默认建议只返回有界引用和允许
      披露的片段。
- [x] 确认 MCP transport：第一版实现 stdio；是否同时提供 streamable HTTP 单独决策。
- [x] 确认仓库内 client 首发语言为 Go，供 MCP 内部复用；原生 client 位于各 Agent
      应用或 SDK 中且语言由宿主决定，额外官方 client 随具体需求提供。
- [x] 保存重构前测试基线，覆盖 memory、scope、feedback、recall、event 和
      provider job。

### 交付物

- 一份位于本任务文档中的最终决策记录，或独立 ADR；
- 路由/模块保留矩阵；
- 可重复运行的基线测试命令和结果。

### 完成条件

- 所有待删除入口都有调用图证据；
- Tool Route 的 authority/proposal 边界无未决语义；
- 后续阶段不需要通过保留旧 DTO 来规避产品决策。

### Phase 0 实施记录

- 状态：completed
- 完成日期：2026-09-20
- 主要变更：新增 `docs/architecture/phase-0-decisions-and-baseline.md`；同步架构、领域
  模型与文档索引中的目标枚举和状态说明。
- 验证命令：`CGO_ENABLED=1 go test -tags sqlite_fts5` 分别运行 gateway 五个模块、
  `internal/protocol/httpapi`、storage 能力基线与 `internal/jobs`；`make schema-check`、
  `make quality`、`make offline-default`、`make restore-drill`、
  `make portability-drill`、`make vet`、`make test`、`make test-race`、`make build` 和
  `git diff --check`，全部通过。
- 删除内容：无。调用图确认九条路由当前均有生产 handler 消费者，restore staging
  probe 还直接依赖 `/v1/recall`；Phase 0 提前删除会破坏当前运行时与基线。
- 遗留问题：目标命令、JSON Schema、application API、MCP/client 和运行时迁移属于
  Phase 1 及后续阶段；当前 purpose/type/reason feedback 与九路由仍是已实现行为。
- 下一阶段前置条件：Phase 1 必须以 Phase 0 决策为唯一产品输入，先定义协议中立命令
  与版本化 schema，不引用旧 HTTP DTO、storage DTO、job 或 provider 类型。

## Phase 1：领域命令与版本化工具契约

### 任务

- [x] 定义协议中立的 `Recall`、`MemoryCommit`、`Feedback`、`Forget` gateway 命令与
      结果，不引用 HTTP 或 MCP 类型。
- [x] 为 `dmem_recall`、`dmem_remember`、`dmem_feedback`、`dmem_forget` 定义唯一的
      版本化 JSON Schema。
- [x] 定义 tool contract version、能力发现和错误模型。
- [x] 定义 `source`、conversation/turn/tool-call identity 和 evidence 绑定规则。
- [x] 定义 `search_hints` 的规范化、数量/长度上限、持久化位置和 FTS 语义。
- [x] 定义 memory/entity/fact/relation 的调用内 `local_id` 引用规则。
- [x] 定义 Agent confidence 的范围和用途，确保它不能授权破坏性变更。
- [x] 定义每种输入的严格边界：请求体、数组、字符串、图谱条目、metadata 和时间。
- [x] 定义幂等规范化，保证 JSON 字段顺序和等价空值不会改变 command hash。
- [x] 增加 schema golden tests、未知字段测试、边界测试和 canonical hash 测试。

### 设计约束

- tool schema 是 HTTP DTO、MCP tool 和 SDK 类型的唯一来源；
- gateway contract 不导入 protocol、storage、jobs 或 provider；
- 不在 contract 中暴露 job type、lease、SQLite ID 或 provider prompt；
- `dmem_remember` 不接受 `confirmed`、`skip_policy` 或 `skip_review`。

### 完成条件

- 四个工具拥有稳定的 v1 schema 和示例；
- schema 可以独立验证请求和响应；
- gateway 单元测试覆盖成功、去重、冲突、拒绝和幂等 replay。

### Phase 1 实施记录

- 状态：completed
- 完成日期：2026-09-20
- 主要变更：新增 `internal/gateway/agenttool` 协议中立命令、结果、语义校验与规范化
  hash；新增 `pkg/toolcontract` 及嵌入式 `schema/v1`；新增
  `docs/architecture/agent-tool-contract-v1.md`；更新 gateway 架构包清单。
- 验证命令：`gofmt -w internal/gateway/agenttool pkg/toolcontract`；
  `go test ./internal/gateway/... ./pkg/toolcontract`；`CGO_ENABLED=1 go test -tags
  sqlite_fts5 ./...`；`make test`；`CGO_ENABLED=1 go test -race -tags sqlite_fts5
  ./internal/gateway/... ./pkg/toolcontract`；`make schema-check`；`make vet`；
  `CGO_ENABLED=1 go vet -tags sqlite_fts5 ./...`；`git diff --check`，全部通过。
- 删除内容：无。旧 HTTP、storage、job 与 provider 流程仍有 Phase 0 记录的消费者，
  删除边界属于 Phase 2。
- 遗留问题：application 原子提交、`search_hints` 持久化与 FTS 投影尚未实现；
  HTTP/MCP/client 尚未接线；这些不属于 Phase 1。
- 下一阶段前置条件：Phase 2 必须将唯一 v1 schema 映射到新的 application API，完成
  source、memory、proposal、evidence、audit 和幂等响应的同事务提交，并在迁移消费者
  后删除旧 HTTP DTO/路由。

## Phase 2：Application API 与原子 Memory Commit

### 任务

- [x] 设计新的 application HTTP API，路径由资源语义决定，不受当前九路由约束。
- [x] 实现原子 memory commit：source、memories、knowledge proposals、evidence、audit、
      idempotency response 在同一事务提交。
- [x] 复用或重构现有 memory、knowledge、consolidation 能力，保持 revision 和 proposal
      不变量。
- [x] 确保 Tool Route commit 不创建 `extract_memory`/`extract_graph` job。
- [x] 写入后只调度必要的确定性派生工作和已启用 Provider Route 的 embedding 工作。
- [x] 实现 recall、feedback、forget 和 scope bootstrap 的目标 API。
- [x] 明确 recall 的幂等策略；若继续写遥测，修复当前重复请求重复写遥测的问题。
- [x] 生成或手写严格 DTO 映射，保持 protocol 不能直接访问 storage/jobs。
- [x] 用新 API 测试替换旧 HTTP 契约测试。
- [x] 删除被替换的路由、DTO、转换器和仅为旧路由存在的 gateway 方法。

### 数据与 schema 工作

- [x] 评估现有 revision attributes 是否足以存放 search hints；不足时更新 schema。
- [x] 为 tool origin、contract version、conversation/turn identity 增加必要 provenance。
- [x] 确保 source/event 与 memory commit 的关系可审计但不会重复触发 provider extraction。
- [x] 更新 schema 一致性、触发器和逻辑导入导出；项目未发布时直接更新 Schema v1。

### 完成条件

- 单次 remember 可原子产生多条 memory 和关联知识建议；
- HTTP 重试不会产生重复 authority、proposal、evidence 或 audit；
- Tool Route 集成测试证明没有模型 job 被创建；
- 旧路由已经移除，或有明确仍被目标架构使用的保留理由。

### Phase 2 实施记录

- 状态：completed
- 完成日期：2026-09-20
- 主要变更：
  - `internal/gateway/agenttool`：新增 `ServiceImpl`、consumer-owned `Store`/`RecallPort`/
    `Authorizer` 端口与 gateway 持久化 DTO；校验、canonical write identity、authority
    write 授权、稳定 operation ID 与降级能力映射。
  - `internal/storage/gateway_agenttool.go`：单事务 `commitAgentToolMemory`、原生
    `appendAgentToolFeedback`、修订围栏 `forgetAgentToolMemory`；复用 revision/evidence/
    projection 助手与 `classifyCandidateForConsolidation` 确定性整合规则。
  - `internal/protocol/httpapi`：以 `pkg/toolcontract` v1 schema 为唯一来源，替换为
    `/v1/capabilities`、`/v1/memory-commits`、`/v1/recall`、`/v1/feedback`、
    `/v1/forget`、`/v1/scopes/ensure`、`/v1/scopes/{scope_id}`；recall 为读操作并返回
    `operation_id`。
  - `internal/gateway/scope`：原子 `Ensure`/`EnsureScope` 端口与校验。
  - Schema v1：新增权威 `agent_tool_sources`、`agent_tool_search_hints`、
    `agent_tool_proposals`（含审核终态触发器），扩展 `feedback_events` 的
    `agent_tool_feedback` 与五类 kind，新增
    `query_telemetry(operation_id, query_hash)` 唯一索引；两份 SQL 与内嵌 schema 同步。
  - `internal/portability`、`internal/storage/imports.go`、`internal/maintenance`：
    导出/导入 Agent Tool provenance、search hints 与 proposal，并纳入恢复权威清单。
  - `internal/daemon`：组装 Agent Tool 服务、recall 适配器与 policy 适配器，删除旧
    memory/feedback/extraction HTTP 依赖。
  - 文档：重写 `docs/api/http-rest-contract.md`、`docs/api/usage.md`、README 与相关
    架构/存储文档以描述当前实现。
- 验证命令：
  - `gofmt -l internal pkg cmd`（无输出）；
  - `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`；
  - `make build`、`make test`、`make test-race`、`make vet`；
  - `make schema-check`、`make quality`、`make offline-default`、`make config-check`、
    `make restore-drill`、`make portability-drill`；
  - `git diff --check`；全部通过。
- 删除内容：旧九路由 handler/DTO/转换器与测试（`/v1/memories`、`/v1/events`、
  `/v1/scopes` create、旧 recall/feedback DTO、`routes_test.go`、`fakes_test.go`、
  `decode_test.go`）；daemon 对 memory/feedback/extraction HTTP 服务的组装；旧
  purpose/type/reason feedback 的 HTTP 入口。注：`errors_test.go` 并未被删除，它
  当前仍在仓库中并覆盖新适配器的错误映射与不泄露断言，因此不属于本阶段删除项。
- 遗留问题：无消费者的 `gateway/memory`、`gateway/feedback` 模块与其存储适配器仍保留，
  由 Phase 7 全仓调用图清理（**Phase 7 已删除**：`gateway/memory`、`gateway/feedback`、
  `gateway/knowledge`、consolidation 入口服务及对应存储适配器）；Tool knowledge proposal
  以统一 `agent_tool_proposals` envelope 持久化（审核物化到 entity/fact/relation 权威
  仍属后续审核阶段）；scope ensure 为 resolve-then-create，依赖 `memory_spaces` 唯一
  约束处理并发；MCP、薄 client、Provider Route 收敛与 search hints 进入 FTS 已在
  Phase 3-5 完成。
- 下一阶段前置条件：Phase 3 必须在同一版本化契约下把 canonical content 与 search hints
  纳入 FTS 文档、建立无 provider 固定检索质量门禁，并在 response capability metadata 中
  稳定报告 semantic 降级；不得改变 Phase 2 已稳定的 application API 与原子提交语义。

## Phase 3：无 Provider 的检索闭环

### 任务

- [x] 将 canonical memory content 和 search hints 纳入 FTS 文档，保留字段来源区分。
- [x] 验证 entity names、aliases 和 typed graph expansion 在无 embedding 时可参与召回。
- [x] 调整 capability selection，使 semantic 不可用时明确降级到 keyword/graph。
- [x] 禁止在无 semantic 执行时返回 semantic score 或宣称 hybrid/vector recall。
- [x] 使用 Agent Tool 典型查询建立固定测试集，覆盖同义表达、实体别名、时间问题、
      偏好、决策和程序性记忆。
- [x] 调整确定性排序，使 search hints 召回不会压过 canonical exact match。
- [x] 验证 scope、敏感度、生命周期、current revision、时间和预算过滤在所有通道前
      或返回前生效。
- [x] 验证 explain 和无内容 telemetry 不泄露 search hints、query 或正文。

### 质量门禁

- [x] 无 processing/embedding provider 的全新实例通过 remember -> recall 集成测试。
- [x] 固定测试集达到在 Phase 0 确定的最低 Recall@K；指标未达标时先改进 FTS 文档和
      query compiler，不把 embedding 重新变成硬依赖。
- [x] 语义降级原因稳定、可测试并出现在 response capability metadata 中。

### 完成条件

- Agent Tool Route 在空 provider 配置下是完整可用的产品路径，而不只是存储降级模式。

### Phase 3 实施记录

- 状态：completed
- 完成日期：2026-09-21
- 主要变更：
  - Schema v1：`memory_fts` 新增受索引 `hints` 字段；两份 SQL 与内嵌 schema 同步。
  - `internal/storage/canonical.go`：新增 `canonicalSearchHintDocument`、
    `revisionSearchHints` 与导出的 `RevisionSearchHintDocument`
    （`canonical-search-hints-v1`）；hint 投影独立于 canonical document 与
    embedding 输入。
  - `internal/storage/keyword_query.go`：关键词召回改为字段感知；列加权 BM25
    （content 1.0 / hints 0.25），记录 `contentMatched`/`hintMatched`/`hintOnly`，
    substring fallback 也覆盖 hints。
  - `internal/storage/{memories,indexes}.go`、`internal/maintenance/maintenance.go`：
    projection 写入、FTS 重建与恢复重建均填充 `content` 与 `hints`。
  - `internal/gateway/retrieval`：`Candidate` 携带字段来源与 `HintOnly`；确定性排序
    保证任何 canonical content 匹配先于 hint-only 匹配。
  - `internal/storage/{search,ranking,context_selection}.go`：`KeywordHintMatched`
    进入 selection relevance（有界 0.5，exact canonical 仍受保护）；explain 只报告
    `content`/`hints` 字段名；`rankingIdentity` 增加 `hint-canonical-v1` 顺序身份。
  - 测试：新增 `internal/storage/agent_tool_recall_test.go`、
    `testdata/agent-tool-recall-corpus-v1.json`、
    `testdata/agent-tool-recall-gates-v1.json`，以及 daemon
    `TestProviderDisabledAgentToolHintRecallClosesLoop`。
  - `Makefile`：`quality` 纳入 Agent Tool 检索门禁，`offline-default` 纳入 hint
    recall 闭环。
  - 文档：更新 `docs/architecture/retrieval.md`、`agent-tool-contract-v1.md`、
    `docs/storage/schema.md`、`docs/development/testing.md`、
    `docs/architecture/phase-0-decisions-and-baseline.md`。
- 验证命令：
  - `gofmt -l internal pkg cmd`（无输出）；
  - `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`；
  - `make build`、`make test`、`make test-race`、`make vet`；
  - `make schema-check`、`make quality`、`make offline-default`、
    `make config-check`、`make restore-drill`、`make portability-drill`；
  - `git diff --check`；全部通过。
- 删除内容：无。Phase 3 只新增字段与投影，未移除仍被目标架构使用的入口。
- 遗留问题：默认 `features_only` 图谱策略只加分不新增候选，typed graph 新增候选
  仍需显式放宽策略；MCP、薄 client 与 Provider Route 收敛属于 Phase 4-5。
- 下一阶段前置条件：Phase 4 必须在不改变 Phase 3 稳定检索语义的前提下，用同一份
  `pkg/toolcontract` v1 schema 实现 Go application client 与 stdio MCP server，
  不复制领域规则、不访问 storage/jobs/provider。

## Phase 4：薄 Client 与 MCP Server

### Client 任务

- [x] 在 dMem 仓库实现供 `dmem-mcp` 复用的 Go application client，覆盖 recall、
      remember、feedback、forget 和 scope bootstrap；原生 client 位于 Agent 应用或
      对应 SDK 中，不要求使用 Go。
- [x] 自动传递 caller、request/correlation 和 idempotency identity。
- [x] 对网络重试复用同一个 idempotency key，禁止每次 retry 重新生成。
- [x] 提供 tool definitions 和标准 memory policy prompt；支持 `opportunistic` 与
      `checkpoint` 集成指导。
- [x] 保留 dMem 结构化错误和 retryability，不把所有错误压成字符串。
- [x] 限制日志，默认不输出工具参数、记忆正文或召回内容。

### MCP 任务

- [x] 新增 `dmem-mcp` 可执行程序。
- [x] 第一版实现 MCP stdio transport 和四个 tools。
- [x] MCP handler 只依赖公共 application client，不导入 storage/jobs/provider。
- [x] tool input/output schema 直接来自 Phase 1 的契约来源。
- [x] 将 caller/client identity、scope 默认值和 dMem endpoint 作为 MCP server 配置。
- [x] 正确传播取消、超时和结构化错误。
- [x] 增加 MCP 协议测试以及 MCP handler 与 application client 的集成测试。
- [x] 文档提供最小 Agent 配置和工具使用示例，但不绑定特定商业模型。

### 完成条件

- 一个支持 MCP tools 的 Agent 只配置 `dmem-mcp` 和 scope，即可完成完整记忆闭环；
- MCP server 不需要 LLM API key，不调用 MCP sampling，不维护后台 job worker；
- 原生 client 与 MCP 对相同输入产生相同 application 命令和错误语义。

### Phase 4 实施记录

- 状态：completed
- 完成日期：2026-09-21
- 主要变更：
  - `pkg/toolcontract/tools.go`：新增协议中立 `ToolDefinition`、`V1ToolDefinitions`
    （从嵌入式 v1 schema 机械派生，支持 `DefaultScope`/`HostSource` 放宽）、
    `IntegrationMode` 与 `MemoryPolicyPrompt`。
  - `pkg/client`：新增薄 Go application client（capabilities、recall、remember、
    feedback、forget、scope ensure/get），自动传递 caller/请求/关联身份，写操作生成
    并在重试间复用同一幂等 key 与请求 ID，按 `retryable`/`retry_after_ms` 退避，
    保留 `*client.Error`（code/retryable/retry_after/details/request_id），校验请求
    与响应契约，限制响应大小，日志只含方法与状态。
  - `internal/protocol/mcp`：新增 stdio JSON-RPC 2.0 适配器（newline framing、
    initialize 版本协商、tools/list、tools/call、ping、cancelled 通知、消息大小
    上限、并发分发与取消抑制响应）；`ConfiguredScope` 按外部身份 ensure 并缓存
    scope；remember 注入传输层 source，scope 不匹配返回 `permission_denied`，
    所有 dMem 错误以 `isError` + 结构化 error JSON 回传。
  - `cmd/dmem-mcp`：新增可执行程序与 flag/env 配置（endpoint、caller、scope、
    source name、integration mode、request timeout、log level、version）。
  - `internal/storage/gateway_agenttool.go`：修复 Phase 2 遗留缺陷——commit、
    feedback、forget 三个 capability 记录未回填 `OperationID`，导致 wire
    `operation_id` 为空并违反 v1 output schema；client 的响应校验暴露了该问题。
  - 测试：`pkg/toolcontract/tools_test.go`、`pkg/client/client_test.go`、
    `pkg/client/arch_test.go`、`internal/protocol/mcp/{arch,server,tools,integration}_test.go`、
    `cmd/dmem-mcp/main_test.go`；`internal/daemon/integration_test.go` 新增
    `TestMCPToolRouteClosesMemoryLoopWithoutProvider`（真实 SQLite + HTTP +
    `pkg/client` + `dmem-mcp` 的 remember/recall/feedback/forget 闭环）。
  - `internal/protocol/httpapi/arch_test.go`：协议树白名单加入 `../mcp`。
  - `Makefile`：新增 `mcp-check` 并纳入 `check`。
  - 文档：新增 `docs/api/mcp-server.md`；更新 `agent-tool-contract-v1.md`、
    `agent-tool-and-provider-routes.md`、`overview.md`、`phase-0-decisions-and-baseline.md`、
    `docs/api/usage.md`、`docs/operations/runbook.md`、`docs/development/testing.md`、
    `docs/README.md` 与 `README.md`。
- 验证命令：
  - `gofmt -l internal pkg cmd`（无输出）；
  - `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`；
  - `CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./pkg/... ./internal/protocol/... ./cmd/dmem-mcp/ ./internal/daemon/`；
  - `make build`、`make test`、`make test-race`、`make vet`；
  - `make schema-check`、`make quality`、`make offline-default`、`make config-check`、
    `make restore-drill`、`make portability-drill`、`make mcp-check`；
  - `git diff --check`；全部通过。
- 删除内容：无。Phase 4 只新增适配器与 client，并修复一个领域输出缺陷；旧路由与
  DTO 已在 Phase 2 删除，Provider Route 的 no-op 流程属于 Phase 5。
- 遗留问题：MCP streamable HTTP、resources、prompts 与 sampling 明确不属于 v1；
  `dmem-mcp` 每个进程只服务一个配置 scope；非 Go 官方 client 仅在出现具体消费者后
  提供；`internal/protocol/mcp` 仅依赖 `pkg/client`，其协议测试使用进程内管道而非
  真实 Agent 宿主。
- 下一阶段前置条件：Phase 5 必须在不改变已稳定的 application API、工具契约与
  Phase 4 client/MCP 语义的前提下，用显式 `processing.enabled`/`embedding.enabled`
  表达 Provider Route 状态，删除 `builtin-noop` 消费模型 job 的流程，并保证 Tool
  Route commit 与 Provider Route event 不对同一 source 双重提取。

## Phase 5：dMem Provider Route 收敛

### 配置任务

- [x] 用显式 `processing.enabled` 和 `embedding.enabled` 表达 Provider Route 能力状态。
- [x] disabled 状态不要求 endpoint、model、secret 或 provider policy。
- [x] enabled 状态严格要求 provider 配置和默认拒绝出网策略。
- [x] 移除把 `builtin-noop` 当作正常模型执行结果的模糊流程。
- [x] 保证配置 reload、identity 和 reconciliation 对 enabled/disabled 变化行为明确。

### 处理任务

- [x] raw event ingest 只在 processing enabled 时创建模型提取 job，或以明确的 stored
      状态保留；不得创建注定由 no-op 消费的 job。
- [x] 保留 memory extraction 的 candidate/review 边界和严格输出验证。
- [x] 保留 graph extraction 的 proposal、predicate registry 和 revision fence。
- [x] 审查 aggregate provider 的可达性；无目标产品消费者则删除同步/异步流程，
      有消费者则统一 provider attempt、policy 和错误分类。
- [x] 统一 embedding 和 aggregate 的 transient/permanent/invalid-output 分类，避免
      永久错误被无差别重试。
- [x] 统一 provider attempt provenance 和外部调用计数。
- [x] 验证 Tool Route commit 与 Provider Route event 不会对同一 source 双重提取。

### 完成条件

- Provider Route 显式启用时通过 event -> candidate -> review 和 revision -> embedding
  集成测试；
- Provider Route 禁用时没有模型 job 被 no-op worker 消费；
- 两条路线写出的 memory 可由同一个 recall 流水线检索。

### Phase 5 实施记录

- 状态：completed
- 完成日期：2026-09-21
- 主要变更：
  - `internal/config`：`[processing].enabled` 与 `[embedding].enabled` 成为唯一能力
    开关，删除 `[processing].version`、`DefaultProcessingVersion` 与 provider 级
    `enabled`；禁用时不校验 provider 配置，启用时严格要求连接、密钥、限制与
    default-deny 策略；`ProcessingVersion` 改为派生（启用时 provider identity，
    禁用时 `domain.LocalProcessingVersion`）；capability policy identity 对禁用
    provider 返回空；`manager` 用同一 identity 判定能力变化并产生
    `enable`/`disable`/`runtime_limits` 调和类。
  - `internal/provider`：新增 `ProcessorIdentity(cfg)`，供配置与 provenance 复用。
  - `internal/storage/repository.go`：`processingVersion` 改为原子值并提供
    `SetProcessingVersion`；新增 `SetProcessingEnabled`；`IngestEvent` 仅在
    processing 启用时创建 `extract_memory` job，禁用时返回 `stored` 与空
    `job_ids`；删除无消费者的 `ReplayEvent`。
  - `internal/storage/provider_errors.go`：新增 `ClassifyProviderError` 与
    `ErrProviderTransient`/`ErrProviderPermanent`/`ErrProviderInvalidOutput`；
    `embedMemory` 与 aggregate `RebuildClaimed` 统一分类 provider 失败。
  - `internal/storage/configuration.go`：processing `disable` 调和取代
    `pending`/`waiting`/`retrying`/`blocked_policy` 模型 job、把原始事件保留为
    `stored`，并给运行中的 job 写入取消请求。
  - `internal/storage/aggregates.go`：新增 `AggregateScope` 供 worker 做 processing
    policy 判定。
  - `internal/jobs`：删除 `NoopProcessor`；`SetProcessing` 在未配置真实 provider 时
    保持 nil processor 与 `not_configured`；无处理器的模型 job 以
    `processing_disabled` 永久失败而非记录成功；`ProcessingNoProcessor` 与
    `GraphExtractionNoProcessor` 不再提交为成功；`rebuild_aggregate` 经过 processing
    policy 与 `MarkProviderAttempt`；index 失败按 permanent/invalid-output/transient
    分流。
  - `internal/gateway/extraction` + `internal/protocol/httpapi`：接入
    `POST /v1/events` 与 `GET /v1/events/{event_id}`，`Dependencies` 增加
    consumer-owned `ExtractionService`，新增协议本地事件 DTO 与严格解码。
  - `internal/daemon`：组装 extraction 服务；`applySnapshot` 同步 processing 版本、
    enabled 状态与 graph 门控；删除 aggregate 同步入口的版本来源。
  - `internal/aggregate`：删除无消费者的同步 `Build`，保留并统一异步
    `RebuildClaimed`。
  - 测试：config 能力开关与派生版本、storage ingest 门控与 disable 调和、embedding
    失败分类、jobs permanent 分类与 aggregate policy/attempt、httpapi 事件路由、
    daemon `TestProviderRouteEventToCandidateReviewEmbeddingAndRecall` 与
    `TestProviderDisabledEventIngestStoresWithoutModelJob`、storage
    `TestToolCommitAndProviderEventDoNotDoubleExtractOneSource`。
  - 文档：更新 `docs/architecture/processing.md`、`overview.md`、
    `agent-tool-and-provider-routes.md`、`retrieval.md`、`docs/api/http-rest-contract.md`、
    `docs/api/usage.md`、`docs/operations/configuration.md`、`config.example.toml`、
    `docs/development/testing.md`。
- 验证命令：
  - `gofmt -l internal pkg cmd`（无输出）；
  - `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`；
  - `CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./internal/config/... ./internal/storage/... ./internal/jobs/... ./internal/aggregate/... ./internal/protocol/... ./internal/daemon/...`；
  - `make build`、`make test`、`make test-race`、`make vet`；
  - `make schema-check`、`make quality`、`make offline-default`、`make config-check`、
    `make restore-drill`、`make portability-drill`、`make mcp-check`；
  - `git diff --check`；全部通过。
- 删除内容：`builtin-noop` 处理版本与 `NoopProcessor`；provider 级 `enabled` 配置键；
  `[processing].version`；无消费者的 `Repository.ReplayEvent` 及其测试；aggregate
  无消费者的同步 `Build` 及其测试；no-op 完成模型 job 的提交路径。
- 遗留问题：aggregate 生成目前只有导入的 descriptor 能触发异步 rebuild，尚无
  HTTP/MCP 触发入口；禁用期间摄取的事件保持 `stored`，重新启用不会追溯处理；
  MCP 仍只暴露四个 Agent 工具，不暴露事件入口；`builtin-noop` 在历史 Phase 0
  基线文档中作为当时行为保留。
- 下一阶段前置条件：Phase 6 必须在 Provider Route 语义稳定的前提下，保留
  canonical document、query format 与 compatibility identity，并让 recall 在
  embedding provider 不可用时按 Phase 3 降级，不得改变 Phase 5 的能力开关与
  失败分类。

## Phase 6：可选向量增强

本阶段在基础 Tool Route 发布不依赖向量时才开始。

### dMem Provider Embedding

- [x] 保留 canonical document、query format 和 compatibility identity。
- [x] 验证 provider/model/format/dimension 变化会使旧向量失效并安排重建。
- [x] 保证 recall 在 provider 不可用时按 Phase 3 降级，而不是整体失败。

### 可选 Agent Embedding Adapter

- [x] 先调研目标 Agent 是否真正提供稳定、可编程的 embedding API；没有实际宿主
      支持时不实现该扩展（调研结论：不支持，因此本阶段不实现该 adapter）。
- [ ] 由 dMem 返回规范化 document/query input，client 不复制格式算法。
- [ ] 定义包含 model、dimension、document/query contract 和 compatibility identity 的
      向量信封。
- [ ] 严格验证 finite、non-zero、dimension 和 compatibility。
- [ ] 定义模型变化后的失效和重建所有者，禁止新旧向量混用。
- [ ] 增加恶意超大向量、NaN/Inf、错误模型和错误维度测试。

后五项按 Phase 6 调研结论**有意不实现**。它们只有在出现真实宿主支持时才重新进入
范围，前置条件见下方实施记录的“可选 Agent embedding adapter 决策”。

### 完成条件

- 向量增强完全可选；删除 adapter 后基础 Tool Route 的 API 和数据仍然可用；
- 任意同维但不兼容的向量不能进入活跃 semantic index。

### Phase 6 实施记录

- 状态：partial
- 完成日期：2026-09-21
- 主要变更：
  - `internal/storage/semantic.go`：`EmbeddingCompatibilityIdentity(modelCompatibility, dimension)`
    把请求维度并入向量空间指纹（provider/model 标记 + `dimensions` + 文档格式 +
    查询格式 + canonical document 版本）；新增共享语料谓词
    `eligibleSearchableCountSQL` / `eligibleEmbeddingMismatchSQL`；
    `embedEligibleCorpus` 不再按 policy generation 变化分叉，只为缺少当前空间向量的
    合格修订调用 provider；新增 `missingVectorSpaceRevisions` 并在 `claim == nil`
    的重建路径上做发布前完整性校验，缺失向量时降级而不发布缺项 generation。
  - `internal/storage/repository.go`：`ConfigureSemantic` 接收维度；
    `EmbeddingJobIdentity()` 变为导出方法并返回
    `<provider>/<model>#<compatibility_identity>`，使 provider、model、格式与维度
    变化都选定新的持久作业身份。
  - `internal/storage/indexes.go`：`semanticIndexDirty` 改为比较
    `semantic_index_metadata.compatibility_identity` 与当前指纹，并用同一语料谓词
    统计 `source_revision_count`；`EnsureSemanticRecoveryJobs` 取消绑定旧身份的
    `embed_memory`/`rebuild_semantic` 作业、按同一谓词重新入队、调用
    `repairDivergentEmbeddings` 把“标记 current 但当前空间没有向量”的投影重置为
    `stale`，再依据同一 dirty 判定安排重建。
  - `internal/config/identity.go`：`CapabilityIdentity` 纳入 embedding 输出选项
    （`dimensions`、`query_instructions`），因此维度变化会被识别为能力配置变化；
    `PolicyIdentity` 不变，维度不涉及出网授权。
  - `internal/daemon/daemon.go`：`ConfigureSemantic` 与
    `EmbeddingCompatibilityIdentity` 传入 `Options.Dimensions`，覆盖 `StartManaged`
    与 reload 两条应用路径。
  - 测试：`TestSemanticDimensionChangeInvalidatesVectorsAndRebuilds`（仅改维度的端到端
    失效、作业取代、降级与重建）、`TestEmbeddingCompatibilityIdentityBindsModelMarkerAndDimension`、
    `TestEmbeddingJobIdentityBindsTheWholeVectorSpace`、
    `TestManager_EmbeddingDimensionChangeIsVisibleToRuntime`；并修正
    `TestSemanticRecoveryJobsAreProviderBoundDeduplicatedAndOrdered`、
    `TestSemanticFailedRebuildPreservesUnchangedLiveGeneration`（显式制造需要
    provider 的重建场景）与 restore drill 的身份断言。
  - 文档：更新双路线架构（新增“Embedding 向量空间身份与失效”“可选 Agent 向量扩展
    的当前状态”）、`retrieval.md`、`processing.md`、`storage/schema.md`、
    `operations/configuration.md`、`development/testing.md`。
- 验证命令：
  - `gofmt -l internal pkg cmd`（无输出）；
  - `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`；
  - `CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./internal/storage/ ./internal/config/ ./internal/daemon/ ./internal/maintenance/`；
  - `make build`、`make vet`、`make schema-check`、`make config-check`、`make quality`、
    `make offline-default`、`make restore-drill`、`make portability-drill`、`make mcp-check`；
  - `git diff --check`；全部通过。
- 删除内容：`embedEligibleCorpus` 中按 `policyChanged` 分叉的旧查询分支，以及
  `EnsureSemanticRecoveryJobs` 只看 `provider_identity`/`model_identity` 的过时重建
  判定；维度不再排除在兼容身份之外。
- 遗留问题：
  - 可选 Agent embedding adapter 未实现，见“可选 Agent embedding adapter 决策”。
  - 旧向量空间的行保留在 `memory_embeddings` 中（按 `compatibility_identity` 隔离，
    既不参与召回也不进入活跃索引），因此回滚到旧的 provider/model/维度配置时可以直接
    复用，而不必重新嵌入；代价是切换期间存在额外的派生存储。
  - `docs/development/testing.md` 曾引用五个仓库中不存在的测试，本阶段只修正引用，
    等价覆盖属于 Phase 7（**Phase 7 已完成**：五个引用被替换为仓库中真实存在的
    等价覆盖，并删除了同一文档中不存在的 `heldProcessor` 引用）。
  - 固定检索语料与门禁未变：内置确定性检索测试使用未设置 `dimensions` 的
    provider，因此不受维度进入向量空间指纹的影响；只有显式设置 `dimensions` 的部署
    需要在改变维度时预期一次语义重建。
- 下一阶段前置条件：Phase 7 必须在向量空间身份稳定的前提下做全仓调用图清理与发布
  门禁，并且不得为 Agent embedding adapter 预留没有消费者的向量信封入口；发布验收
  需要覆盖“embedding 故障时 keyword/graph 召回继续并明确报告降级”和“同维但不兼容
  的向量不得进入活跃 semantic index”两条已有测试。

#### 可选 Agent embedding adapter 决策

调研结论（2026-09）：当前没有主流 MCP-capable / tool-calling 宿主向 server 或 tool
暴露稳定、可编程的 embedding 原语。MCP 规范只标准化工具、resources/prompts、
elicitation 与 extensions，`sampling/createMessage` 只返回文本/图像/音频补全，规范与
文档中没有 embedding 方法；extension 列表中也没有 embeddings。Anthropic 明确不提供
自有 embedding 模型，OpenAI、Google 的向量能力都需要各自 API key（即 dMem 自有
provider 配置），LangChain/LlamaIndex 的 embedding 是应用自己注入的 adapter 对象，
不由宿主传播进 tool 执行。

因此本轮不实现该扩展，也不新增依赖 sampling 的向量或重排回退。重新进入范围的
前置条件：

1. 出现真实宿主，向 MCP server 或 tool 提供稳定、可编程、有文档的 embedding 原语；
2. dMem 继续生成规范化 document/query input，client 不复制格式算法；
3. 向量信封携带 model identity、dimension、document/query contract 与完整
   `compatibility_identity`；
4. 向量必须 finite、非零、维度严格匹配，并拒绝同维但不兼容的向量；
5. 失效与重建所有者仍是 dMem：新身份向量不得与旧身份混用。

在这些条件满足之前，不接受“任意 Agent 提交一个同维向量”，也没有面向该 adapter 的
client、schema 或配置项。

## Phase 7：删除旧流程、加固与发布门禁

### 删除与整理

- [x] 再次生成全仓调用图，删除无消费者的 handler、gateway port、storage capability、
      job type、配置字段和测试 fixture。
- [x] 删除旧“冻结九路由”文档和已过时的使用示例。
- [x] 删除为旧 no-op/provider 行为存在的兼容分支。
- [x] 更新 README、架构、处理、检索、配置、运维、schema 和测试文档。
- [x] 确保文档不再同时描述互相冲突的当前/目标契约。

### 安全与可靠性

- [x] 对 HTTP 和 MCP 执行请求体、数组、字符串和超时限制。
- [x] 验证非回环部署的认证/网络边界要求；未实现认证时保持明确警告和安全默认值。
- [x] fuzz 严格 JSON、local reference、图谱 predicate、时间和幂等规范化。
- [x] 测试进程崩溃、HTTP/MCP 响应丢失、重复 tool call 和 provider lease 过期。
- [x] 验证普通日志、审计摘要和 telemetry 不泄露正文、query、vector 或 secret。
- [x] 运行 race、静态检查、完整单元/集成测试及备份恢复测试。

### 发布验收场景

- [x] MCP-only：无 provider 配置，从 remember 到 recall、feedback、forget 完整成功。
- [x] Native client：无 MCP，行为与 MCP-only 一致。
- [x] Provider-only：原始 event 经 LLM/embedding provider 进入可召回状态。
- [x] Mixed：Tool 与 Provider Route 同实例运行，没有重复提取或索引身份污染。
- [x] Offline：provider 全部关闭，重启、备份、恢复后 Tool Route 记忆仍可召回。
- [x] Degraded：embedding provider 故障时关键词/图谱召回继续并明确报告降级。
- [x] Conflict：Agent 建议与已有记忆冲突时产生安全 proposal/冲突流程，不静默覆盖。
- [x] Idempotency：HTTP、MCP 和 provider 重试不会产生重复权威写入。

### 完成条件

- 全局完成标准全部满足；
- 不存在未记录的旧 API 或兼容路线；
- 文档与可执行契约一致；
- 可以从空数据目录按文档完成两条路线的验收。

### Phase 7 实施记录

- 状态：completed
- 完成日期：2026-09-21
- 主要变更：
  - **删除无消费者的 gateway 模块**：`internal/gateway/memory`、`internal/gateway/feedback`、
    `internal/gateway/knowledge`（含各自 `*_test.go`），以及
    `internal/gateway/consolidation` 的入口服务层（`consolidation.go`、`ports.go`、
    `service.go`、`service_test.go`）。`consolidation` 保留纯分类器
    （`classifier.go`）与其 `Classifier`/`ClassifyRequest`/`Classification` 契约类型，
    供存储层整合与可选 provider 分类使用。
  - **删除无消费者的存储适配器**：`internal/storage/gateway_memory.go`、
    `gateway_feedback.go`、`gateway_knowledge.go`、`gateway_consolidation.go`、
    `gateway_uow.go`；`internal/storage/gateway_assert.go` 收敛为只断言有生产消费者的
    端口（scope、recall、extraction、policy）；`internal/storage/gateway_adapters_test.go`
    重写为只覆盖这些存活的适配器。`gateway_identity.go` 与 `gateway_errors.go` 保留，
    因为它们仍被 Agent Tool、scope、recall、extraction、policy 适配器共享。
  - **删除被取代的重复直接写入入口**：`Repository.Remember`、`Repository.ForgetMemory`
    及其 `RememberParams`/`RememberResult`，以及从未接线的
    `Repository.AppendCurrentFeedback`。Agent Tool 的 `commitAgentToolMemory`、
    修订围栏遗忘与 `appendAgentToolFeedback` 是唯一写入路径；`Repository.Remember`
    只被已删除的适配器调用，删除后未遗留不可达代码。
  - **`internal/gateway/arch_test.go`** 更新当前 gateway 模块清单。
  - **安全加固**：`internal/gateway/extraction` 新增原始事件输入边界
    （`MaxContentRunes` 65536、`MaxReferenceURIRunes` 4096、`MaxMetadataKeys` 32、
    `MaxMetadataDepth` 4、`MaxMetadataBytes` 16 KiB、`MaxJSONValueDepth` 8、
    `MaxJSONValueNodes` 4096），并校验 `declared_hash` 形状与 `media_type` 长度；
    metadata 校验只检查形状、不回显取值。`internal/gateway/agenttool/normalize.go`
    的 `normalizeText` 改为 UTF-8 安全（`strings.ToValidUTF8`），使 canonical hash 与
    FTS hint 投影不可能对同一输入产生分歧；`CanonicalFeedbackHash` 收回为包内函数。
  - **新增 fuzz 目标**：`internal/protocol/httpapi/fuzz_test.go`
    （`FuzzStrictBodyDecodingIsTotal`、`FuzzRecallWireMappingPreservesContractValues`、
    `FuzzEventIngestBoundsAreEnforced`）、`internal/gateway/agenttool/fuzz_test.go`
    （`FuzzCanonicalHashIsReplayStable`、`FuzzLocalReferencesResolveOrReject`、
    `FuzzNormalizeTextIsSafeAndIdempotent`、`FuzzFeedbackAndForgetBoundsAreEnforced`）、
    `internal/domain/fuzz_test.go`（`FuzzPredicateRegistryIsClosed`、
    `FuzzPredicatePathPositionsAreKnown`、`FuzzRevisionWindowsRejectAmbiguousIntervals`、
    `FuzzEvidenceOrderingIsTotal`）；崩溃输入保留为 `testdata/fuzz` 回归种子。
  - **新增发布验收测试**：`internal/daemon/release_acceptance_test.go`
    （Native client 闭环、Native/MCP 命令等价、响应丢失重试幂等、Agent 建议不静默
    覆盖、Mixed 双路线不重复提取、Offline 重启保持 Tool Route 记忆、日志/审计/遥测
    不泄露正文与 query），以及 `internal/gateway/extraction/service_test.go` 的
    `TestIngestEnforcesInputBounds`。
  - **文档**：重写 `docs/architecture/overview.md` 的模块表与依赖图、
    `docs/architecture/processing.md` 的审核/整合边界（provider 分类未接线、审核无
    协议入口）、`docs/architecture/domain-model.md` 的旧反馈 purpose 说明、
    `docs/architecture/retrieval.md` 的 `skip_diversification` 与分页边界、
    `docs/architecture/agent-tool-and-provider-routes.md` 的“已删除的旧流程”、
    `docs/api/http-rest-contract.md`（状态、范围、Agent Tool 字段边界表、原始事件
    边界表、传输加固表、recall 请求体例外）、`docs/api/usage.md`（补两条事件路由）、
    `docs/api/mcp-server.md`（scope 懒 ensure）、`docs/README.md`、`README.md`、
    `docs/architecture/phase-0-decisions-and-baseline.md`（明确标注历史基线）、
    `docs/development/testing.md`（发布验收场景表、fuzz 目标、不存在的测试引用改为
    真实等价覆盖）。同时删除 `internal/protocol/httpapi` 中“冻结路由集/冻结契约”的
    表述。
- 验证命令：
  - `gofmt -l internal pkg cmd`（无输出）；
  - `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`；
  - `CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...`；
  - `CGO_ENABLED=1 go vet -tags sqlite_fts5 ./...`；
  - `make build`、`make test`、`make test-race`、`make vet`；
  - `make schema-check`、`make quality`、`make offline-default`、`make config-check`、
    `make restore-drill`、`make portability-drill`、`make mcp-check`；
  - fuzz 探索：`httpapi` 三个目标（20s/15s）与 `agenttool`、`domain` 各目标
    （10–25s），无失败输入；
  - `git diff --check`；全部通过。
- 删除内容：`internal/gateway/{memory,feedback,knowledge}` 三个包；
  `internal/gateway/consolidation/{consolidation,ports,service,service_test}.go`；
  `internal/storage/{gateway_memory,gateway_feedback,gateway_knowledge,gateway_consolidation,gateway_uow}.go`；
  `Repository.Remember`、`Repository.ForgetMemory`、`Repository.AppendCurrentFeedback`、
  `RememberParams`、`RememberResult`；`CanonicalFeedbackHash` 导出名；HTTP 适配器的
  “冻结路由/冻结契约”措辞；`docs/development/testing.md` 中五个不存在的测试引用与
  不存在的 `heldProcessor` 引用。
- 遗留问题：
  - 审核、冲突解决与整合的存储层能力（`ConfirmCandidate`、`RejectCandidate`、
    `ConfirmEntityProposal`、`ConfirmFactProposal`、`ConfirmEvolutionProposal`、
    `ConfirmAggregateProposal`、`ResolveConflict`、`GetConsolidationDecision`）目前
    只有存储层测试消费者；它们的领域不变量、触发器和幂等语义完整保留，但没有 HTTP
    或 MCP 入口。目标架构要求审核存在，具体入口属于后续审核阶段。
  - `consolidation.Classifier` 端口与 `SetConsolidationClassifier` 已实现但没有生产者
    接线，因此分类当前只走确定性路径（`consolidation-v1`）。
  - Agent Tool 的 `agent_tool_proposals` 只持久化审核对象，尚未物化到
    entity/fact/relation 权威。
  - `Repository` 中仍有若干低层能力只被存储层测试或派生维护流程消费
    （例如 `ChangeMemoryState`、`ReviseMemory`、`SearchMemories`、`CreateMemory`、
    `CreateFTSRebuildJob` 等索引/调和入口）。它们服务于维护、恢复、可移植性与
    固定检索门禁，属于目标架构要求保留的能力，因此本阶段不删除。
  - `internal/server/e2e_test.go` 与 `daemon.StartStaging` 保留：它们是
    `internal/server` 包测试与恢复 staging probe 的真实运行路径，不是独立端到端
    测试体系。
- 下一阶段前置条件：全局完成标准已满足，双路线架构收敛完成。后续工作应在既有
  契约上推进，不新增第三条路线：审核/冲突的协议入口（若需要）、Agent Tool proposal
  向 entity/fact/relation 权威的物化、以及可选 Agent embedding adapter（仅当出现
  真实宿主支持时，前置条件见 Phase 6 决策）。

## Phase 8：Provider Route 候选审核入口

Phase 7 记录的“审核只有存储层消费者、没有协议入口”缺口在本阶段补齐，范围仅限记忆
候选；图谱/实体/事实/关系/演化/聚合提案与冲突解决的审核仍只有存储层能力。

### 任务

- [x] 新增 `internal/gateway/review` 协议中立模块：候选 list/get/confirm/reject。
- [x] 新增协议入口 `GET /v1/candidates`、`GET /v1/candidates/{candidate_id}`、
      `POST /v1/candidates/{candidate_id}/confirm`、`POST /v1/candidates/{candidate_id}/reject`。
- [x] 新增存储适配器 `ReviewCapability`，并让
      `Repository.ConfirmCandidate`/`RejectCandidate` 返回 operation id。
- [x] 修复整合归约为 `rejected` 的确认写入幂等状态 `200` 但重放只接受 `201` 的缺陷。

### 完成条件

- Provider Route 候选可从 HTTP 列出、读取、确认、拒绝，并保持幂等、审计与整合不变量；
- 确认后的候选由共享召回流水线检索，拒绝不写入记忆权威；
- 图谱/聚合提案与冲突解决的缺失入口被文档明确标注为后续工作。

### Phase 8 实施记录

- 状态：completed
- 完成日期：2026-10-13
- 主要变更：
  - `internal/gateway/review`：新增 `Service`/`ServiceImpl`、`CandidateStore` 端口、
    `ListCandidatesQuery`/`GetCandidateQuery`/`ReviewCandidateCommand`/
    `CandidateReviewResult`、操作名 `review.candidate_confirm`/`review.candidate_reject`
    与输入边界；只做校验、幂等写身份与错误映射。
  - `internal/storage`：新增 `gateway_review.go` 的 `ReviewCapability` 适配器并在
    `gateway_assert.go` 断言端口；`ConfirmCandidate`/`RejectCandidate` 各增返回
    operation id；新增 `readIdempotencyOperation`；`replayMemory` 接受多个状态，
    修复整合归约为拒绝时的重放缺陷。
  - `internal/protocol/httpapi`：新增 `candidates.go` 协议本地 DTO 与
    list/get/confirm/reject handler；`contract.go` 增加消费者自有 `ReviewService`、
    `Dependencies.Reviews`、读操作名与 16 KiB body 上限；`handler.go` 注册四条路由并
    更新模块注释；`arch_test.go` 依赖字段数 3→4。
  - `internal/daemon`：组装 `review.NewService(storage.NewReviewCapability(repository))`。
  - 测试：`internal/gateway/review/service_test.go`、
    `internal/protocol/httpapi/candidates_test.go`、storage
    `TestReviewCapabilityConfirmAndRejectIsIdempotent`；`release_acceptance_test.go` 的
    Mixed 场景改为经 `/v1/candidates` 确认候选后再召回。
  - 文档：更新 `docs/api/http-rest-contract.md`、`docs/api/usage.md`、
    `docs/architecture/processing.md`、`docs/architecture/agent-tool-and-provider-routes.md`、
    `docs/development/testing.md`、`docs/README.md`。
- 验证命令：
  - `gofmt -l internal pkg cmd`（无输出）；
  - `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...`；
  - `make build`、`make test`、`make test-race`、`make vet`；
  - `make schema-check`、`make quality`、`make offline-default`、
    `make config-check`、`make restore-drill`、`make portability-drill`、`make mcp-check`；
  - `git diff --check`；全部通过。
- 删除内容：无。本阶段只新增审核入口并修复一个存储层重放缺陷。
- 遗留问题：图谱/实体/事实/关系/演化/聚合提案审核与冲突解决仍只有存储层方法；候选
  审核不做 policy 授权（当前实现整体无认证），与 events 入口一致。
- 下一阶段前置条件：若需要图谱/聚合/冲突审核入口，应按同一“gateway 模块 +
  storage 适配器 + 协议路由”模式扩展，并保持候选与提案共享同一审核状态机语义。

## 跨阶段测试矩阵

| 维度 | 必测值 |
| --- | --- |
| 路线 | Agent Tool、dMem Provider、mixed |
| Provider | 全禁用、processing only、embedding only、全部启用、故障 |
| Transport | native HTTP client、MCP stdio |
| 写入结果 | authority、proposal、deduplicated、conflict、policy denied |
| 检索 | keyword、search hints、entity、graph、semantic、degraded |
| 生命周期 | current、superseded、forgotten、expired、contested |
| 重试 | client timeout、重复幂等 key、provider transient、lease lost |
| 安全 | 非法 caller/scope、敏感内容、超限载荷、未知字段、日志泄露 |
| 恢复 | daemon restart、index rebuild、backup/restore、配置 reload |

## 实施记录模板

每个 Phase 完成时在对应章节下追加简短记录：

```text
状态：completed | partial | blocked
完成日期：YYYY-MM-DD
主要变更：提交或 PR 链接
验证命令：...
删除内容：...
遗留问题：...
下一阶段前置条件：...
```

不要仅勾选任务而不记录实际验证命令和已删除的旧流程。
