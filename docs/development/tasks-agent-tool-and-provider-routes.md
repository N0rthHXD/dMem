# 双路线架构开发任务

## 目标

按 [Agent Tool 与 dMem Provider 双路线架构](../architecture/agent-tool-and-provider-routes.md)
把 dMem 收敛为：

1. 默认的 Agent Tool Route，包括薄 client 和 MCP server；
2. 可选的 dMem Provider Route，包括 dMem 自有 LLM/embedding provider 和持久 job。

项目尚未发布，本任务不保留当前九条 HTTP 路由或 DTO 的兼容性。每个阶段都应删除
已经失去消费者的接口、实现和测试，避免在新架构旁长期保留旧路线。

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

- [ ] 设计新的 application HTTP API，路径由资源语义决定，不受当前九路由约束。
- [ ] 实现原子 memory commit：source、memories、knowledge proposals、evidence、audit、
      idempotency response 在同一事务提交。
- [ ] 复用或重构现有 memory、knowledge、consolidation 能力，保持 revision 和 proposal
      不变量。
- [ ] 确保 Tool Route commit 不创建 `extract_memory`/`extract_graph` job。
- [ ] 写入后只调度必要的确定性派生工作和已启用 Provider Route 的 embedding 工作。
- [ ] 实现 recall、feedback、forget 和 scope bootstrap 的目标 API。
- [ ] 明确 recall 的幂等策略；若继续写遥测，修复当前重复请求重复写遥测的问题。
- [ ] 生成或手写严格 DTO 映射，保持 protocol 不能直接访问 storage/jobs。
- [ ] 用新 API 测试替换旧 HTTP 契约测试。
- [ ] 删除被替换的路由、DTO、转换器和仅为旧路由存在的 gateway 方法。

### 数据与 schema 工作

- [ ] 评估现有 revision attributes 是否足以存放 search hints；不足时更新 schema。
- [ ] 为 tool origin、contract version、conversation/turn identity 增加必要 provenance。
- [ ] 确保 source/event 与 memory commit 的关系可审计但不会重复触发 provider extraction。
- [ ] 更新 schema 一致性、触发器和逻辑导入导出；项目未发布时直接更新 Schema v1。

### 完成条件

- 单次 remember 可原子产生多条 memory 和关联知识建议；
- HTTP 重试不会产生重复 authority、proposal、evidence 或 audit；
- Tool Route 集成测试证明没有模型 job 被创建；
- 旧路由已经移除，或有明确仍被目标架构使用的保留理由。

## Phase 3：无 Provider 的检索闭环

### 任务

- [ ] 将 canonical memory content 和 search hints 纳入 FTS 文档，保留字段来源区分。
- [ ] 验证 entity names、aliases 和 typed graph expansion 在无 embedding 时可参与召回。
- [ ] 调整 capability selection，使 semantic 不可用时明确降级到 keyword/graph。
- [ ] 禁止在无 semantic 执行时返回 semantic score 或宣称 hybrid/vector recall。
- [ ] 使用 Agent Tool 典型查询建立固定测试集，覆盖同义表达、实体别名、时间问题、
      偏好、决策和程序性记忆。
- [ ] 调整确定性排序，使 search hints 召回不会压过 canonical exact match。
- [ ] 验证 scope、敏感度、生命周期、current revision、时间和预算过滤在所有通道前
      或返回前生效。
- [ ] 验证 explain 和无内容 telemetry 不泄露 search hints、query 或正文。

### 质量门禁

- [ ] 无 processing/embedding provider 的全新实例通过 remember -> recall 集成测试。
- [ ] 固定测试集达到在 Phase 0 确定的最低 Recall@K；指标未达标时先改进 FTS 文档和
      query compiler，不把 embedding 重新变成硬依赖。
- [ ] 语义降级原因稳定、可测试并出现在 response capability metadata 中。

### 完成条件

- Agent Tool Route 在空 provider 配置下是完整可用的产品路径，而不只是存储降级模式。

## Phase 4：薄 Client 与 MCP Server

### Client 任务

- [ ] 在 dMem 仓库实现供 `dmem-mcp` 复用的 Go application client，覆盖 recall、
      remember、feedback、forget 和 scope bootstrap；原生 client 位于 Agent 应用或
      对应 SDK 中，不要求使用 Go。
- [ ] 自动传递 caller、request/correlation 和 idempotency identity。
- [ ] 对网络重试复用同一个 idempotency key，禁止每次 retry 重新生成。
- [ ] 提供 tool definitions 和标准 memory policy prompt；支持 `opportunistic` 与
      `checkpoint` 集成指导。
- [ ] 保留 dMem 结构化错误和 retryability，不把所有错误压成字符串。
- [ ] 限制日志，默认不输出工具参数、记忆正文或召回内容。

### MCP 任务

- [ ] 新增 `dmem-mcp` 可执行程序。
- [ ] 第一版实现 MCP stdio transport 和四个 tools。
- [ ] MCP handler 只依赖公共 application client，不导入 storage/jobs/provider。
- [ ] tool input/output schema 直接来自 Phase 1 的契约来源。
- [ ] 将 caller/client identity、scope 默认值和 dMem endpoint 作为 MCP server 配置。
- [ ] 正确传播取消、超时和结构化错误。
- [ ] 增加 MCP 协议测试以及 MCP handler 与 application client 的集成测试。
- [ ] 文档提供最小 Agent 配置和工具使用示例，但不绑定特定商业模型。

### 完成条件

- 一个支持 MCP tools 的 Agent 只配置 `dmem-mcp` 和 scope，即可完成完整记忆闭环；
- MCP server 不需要 LLM API key，不调用 MCP sampling，不维护后台 job worker；
- 原生 client 与 MCP 对相同输入产生相同 application 命令和错误语义。

## Phase 5：dMem Provider Route 收敛

### 配置任务

- [ ] 用显式 `processing.enabled` 和 `embedding.enabled` 表达 Provider Route 能力状态。
- [ ] disabled 状态不要求 endpoint、model、secret 或 provider policy。
- [ ] enabled 状态严格要求 provider 配置和默认拒绝出网策略。
- [ ] 移除把 `builtin-noop` 当作正常模型执行结果的模糊流程。
- [ ] 保证配置 reload、identity 和 reconciliation 对 enabled/disabled 变化行为明确。

### 处理任务

- [ ] raw event ingest 只在 processing enabled 时创建模型提取 job，或以明确的 stored
      状态保留；不得创建注定由 no-op 消费的 job。
- [ ] 保留 memory extraction 的 candidate/review 边界和严格输出验证。
- [ ] 保留 graph extraction 的 proposal、predicate registry 和 revision fence。
- [ ] 审查 aggregate provider 的可达性；无目标产品消费者则删除同步/异步流程，
      有消费者则统一 provider attempt、policy 和错误分类。
- [ ] 统一 embedding 和 aggregate 的 transient/permanent/invalid-output 分类，避免
      永久错误被无差别重试。
- [ ] 统一 provider attempt provenance 和外部调用计数。
- [ ] 验证 Tool Route commit 与 Provider Route event 不会对同一 source 双重提取。

### 完成条件

- Provider Route 显式启用时通过 event -> candidate -> review 和 revision -> embedding
  集成测试；
- Provider Route 禁用时没有模型 job 被 no-op worker 消费；
- 两条路线写出的 memory 可由同一个 recall 流水线检索。

## Phase 6：可选向量增强

本阶段在基础 Tool Route 发布不依赖向量时才开始。

### dMem Provider Embedding

- [ ] 保留 canonical document、query format 和 compatibility identity。
- [ ] 验证 provider/model/format/dimension 变化会使旧向量失效并安排重建。
- [ ] 保证 recall 在 provider 不可用时按 Phase 3 降级，而不是整体失败。

### 可选 Agent Embedding Adapter

- [ ] 先调研目标 Agent 是否真正提供稳定、可编程的 embedding API；没有实际宿主
      支持时不实现该扩展。
- [ ] 由 dMem 返回规范化 document/query input，client 不复制格式算法。
- [ ] 定义包含 model、dimension、document/query contract 和 compatibility identity 的
      向量信封。
- [ ] 严格验证 finite、non-zero、dimension 和 compatibility。
- [ ] 定义模型变化后的失效和重建所有者，禁止新旧向量混用。
- [ ] 增加恶意超大向量、NaN/Inf、错误模型和错误维度测试。

### 完成条件

- 向量增强完全可选；删除 adapter 后基础 Tool Route 的 API 和数据仍然可用；
- 任意同维但不兼容的向量不能进入活跃 semantic index。

## Phase 7：删除旧流程、加固与发布门禁

### 删除与整理

- [ ] 再次生成全仓调用图，删除无消费者的 handler、gateway port、storage capability、
      job type、配置字段和测试 fixture。
- [ ] 删除旧“冻结九路由”文档和已过时的使用示例。
- [ ] 删除为旧 no-op/provider 行为存在的兼容分支。
- [ ] 更新 README、架构、处理、检索、配置、运维、schema 和测试文档。
- [ ] 确保文档不再同时描述互相冲突的当前/目标契约。

### 安全与可靠性

- [ ] 对 HTTP 和 MCP 执行请求体、数组、字符串和超时限制。
- [ ] 验证非回环部署的认证/网络边界要求；未实现认证时保持明确警告和安全默认值。
- [ ] fuzz 严格 JSON、local reference、图谱 predicate、时间和幂等规范化。
- [ ] 测试进程崩溃、HTTP/MCP 响应丢失、重复 tool call 和 provider lease 过期。
- [ ] 验证普通日志、审计摘要和 telemetry 不泄露正文、query、vector 或 secret。
- [ ] 运行 race、静态检查、完整单元/集成测试及备份恢复测试。

### 发布验收场景

- [ ] MCP-only：无 provider 配置，从 remember 到 recall、feedback、forget 完整成功。
- [ ] Native client：无 MCP，行为与 MCP-only 一致。
- [ ] Provider-only：原始 event 经 LLM/embedding provider 进入可召回状态。
- [ ] Mixed：Tool 与 Provider Route 同实例运行，没有重复提取或索引身份污染。
- [ ] Offline：provider 全部关闭，重启、备份、恢复后 Tool Route 记忆仍可召回。
- [ ] Degraded：embedding provider 故障时关键词/图谱召回继续并明确报告降级。
- [ ] Conflict：Agent 建议与已有记忆冲突时产生安全 proposal/冲突流程，不静默覆盖。
- [ ] Idempotency：HTTP、MCP 和 provider 重试不会产生重复权威写入。

### 完成条件

- 全局完成标准全部满足；
- 不存在未记录的旧 API 或兼容路线；
- 文档与可执行契约一致；
- 可以从空数据目录按文档完成两条路线的验收。

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
