# 测试与质量门禁

Linux 测试需要 C 编译器、`CGO_ENABLED=1` 与 `sqlite_fts5` 构建标签。向量存储
静态链接 sqlite-vec（`github.com/asg017/sqlite-vec-go-bindings` `v0.1.6`）；其
C 源码通过 `sqlite3_auto_extension` 编译进同一二进制，因此不需要外部 `.so` 或
运行时扩展加载。测试使用确定性 provider 或本地 `httptest` 服务器，不需要真实
provider 凭据。

## 核心检查

```sh
go mod download
make build
make test
make test-race
make vet
```

`make schema-check` 验证文档化 SQL 与内嵌 schema 完全一致，并初始化一个临时
数据库。

Fuzz 目标的种子语料随 `make test` 一起执行，因此上面四个门禁已经覆盖它们。要
对某个目标做探索式 fuzz：

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/protocol/httpapi -run '^$' -fuzz 'FuzzStrictBodyDecodingIsTotal'
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/gateway/agenttool -run '^$' -fuzz 'FuzzCanonicalHashIsReplayStable'
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/domain -run '^$' -fuzz 'FuzzPredicateRegistryIsClosed'
```

`make performance` 与 `make govulncheck` 不在 `make check` 内，需单独运行。

## Gateway 与协议测试

- Gateway 模块测试只为模块声明的能力使用窄 fake 端口，覆盖语义校验、错误分类、
  取消、幂等、编排与不变量，尽量不涉及 HTTP 或 SQLite。
- `internal/gateway/arch_test.go` 强制 gateway 依赖规则：不导入 `net/http`、不
  导入协议包、不导入 storage 或 jobs、不导入兄弟模块，且不导出暴露 `storage.*`
  选择器的契约。它同时固定当前 gateway 模块清单；Phase 7 删除 `memory`、
  `feedback`、`knowledge` 与 consolidation 入口服务后，该清单只包含有生产消费者
  的模块。
- `internal/protocol/httpapi` 测试使用 fake gateway 模块，覆盖严格单值 JSON
  解码、DTO 转换、路由一致性（调用方身份与幂等策略）、响应编码、gateway 错误
  映射、panic 恢复、取消与安全错误。
- HTTP/2 兼容性与多路并发通过配置了 `EnableHTTP2` 的测试专用 `httptest.Server`
  证明。测试不添加任何生产监听器、TLS 或 h2c 配置。
- `internal/protocol/httpapi/arch_test.go` 强制协议树不导入 storage、jobs、
  provider、config、retrieval 或应用包，且永不拥有监听器或检查 HTTP 协议版本。
- `pkg/toolcontract` 测试证明工具定义与记忆策略提示来自唯一版本化 schema，宿主
  默认身份只机械放宽 scope/source，且派生结果确定。
- `pkg/client` 测试用 `httptest` 覆盖 caller/请求/关联/幂等身份传递、网络重试复用
  同一幂等 key 与请求 ID、可重试与不可重试错误分类、`retry_after_ms`、请求与响应
  契约校验、响应大小上限、取消与超时，以及日志不泄露正文/query。`pkg/client/arch_test.go`
  强制它只依赖标准库与 `pkg/toolcontract`。
- `internal/protocol/mcp` 测试用进程内管道驱动 stdio 连接，覆盖 initialize 版本
  协商、tools/list schema、scope 与 source 注入、scope 不匹配拒绝、未知工具与未知
  字段、结构化错误传播、取消抑制响应、超限消息、以及日志不泄露工具参数。
  `internal/protocol/mcp/arch_test.go` 强制它不导入任何 `internal/` 包、不使用 MCP
  sampling、不拥有监听器。
- `internal/daemon` 的 `TestMCPToolRouteClosesMemoryLoopWithoutProvider` 在真实
  SQLite 运行时上通过 `httptest` 提供 HTTP，再经 `pkg/client` 与 `dmem-mcp` 完成
  remember -> recall -> feedback -> forget 闭环，并验证无 provider 时的
  `semantic` 降级。
- `internal/server` 测试在 `127.0.0.1:0` 上启动真实监听，覆盖请求收发、绑定冲突
  的同步报错、优雅关闭排空在途请求、请求头大小上限、自签证书 TLS 与 HTTP/2
  协商，以及无效/符号链接 TLS 文件的拒绝。`internal/server/arch_test.go` 强制
  传输层只依赖 `net/http`/`crypto/tls`，不导入 storage、jobs、provider、config、
  retrieval、daemon、gateway 或 protocol。`internal/server/e2e_test.go` 通过真实
  TCP 监听驱动组装后的 handler，属于 `internal/server` 包测试，不是独立端到端
  测试体系。

### 发布验收场景（Phase 7）

`internal/daemon/release_acceptance_test.go` 固定发布验收场景。它们通过组装后的
运行时、真实 SQLite 与真实 loopback 监听运行，不使用独立测试体系、不使用
`.dmem-e2e/`：

| 场景 | 测试 | 证明内容 |
| --- | --- | --- |
| MCP-only | `TestMCPToolRouteClosesMemoryLoopWithoutProvider` | 无 provider 配置时经 `dmem-mcp` 完成完整记忆闭环。 |
| Native client | `TestReleaseAcceptanceNativeClientClosesMemoryLoop` | 无 MCP、无 provider 时经 `pkg/client` 与真实 HTTP 完成同等闭环，并报告 `semantic` 降级。 |
| Native/MCP 等价 | `TestReleaseAcceptanceNativeClientMatchesMCPCommand` | 同一工具命令在两个传输上产生相同状态词汇与独立权威身份。 |
| Provider-only | `TestProviderRouteEventToCandidateReviewEmbeddingAndRecall` | 原始事件经 durable job、processor、审核、embedding 进入可召回状态。 |
| Mixed | `TestReleaseAcceptanceMixedRoutesShareOneAuthority` | 同实例两条路线并存：Tool 写入零模型 job，collector 事件恰好一个 job 且被 worker 消费为候选，候选经 `/v1/candidates` 审核入口确认后可召回。 |
| Offline | `TestReleaseAcceptanceOfflineRestartKeepsToolRouteMemories` | provider 全关时重启后 Tool Route 记忆仍可召回，且零模型 job。 |
| Degraded | `TestHybridFallbacksAndNeitherChannelAvailable`、`TestSemanticDimensionChangeInvalidatesVectorsAndRebuilds` | embedding 故障或旧向量空间时 keyword/graph 召回继续并明确报告降级。 |
| Conflict | `TestReleaseAcceptanceAgentSuggestionCannotSilentlyOverwrite` | 敏感内容与聚合 kind 进入 proposal 且不发布权威；重复内容 deduplicated 到既有 revision。 |
| Idempotency | `TestReleaseAcceptanceLostResponseRetryIsNotDuplicated` | 响应丢失后的重试 replay 原结果，同 key 不同命令返回 `idempotency_conflict`，召回只有一条权威。 |
| 泄露防护 | `TestReleaseAcceptanceLogsAndTelemetryDoNotLeakContent` | 普通日志、`query_telemetry`、`audit_events` 都不含正文、query、search hints 或 feedback reason。 |

### Fuzz 目标（Phase 7）

Fuzz 目标使用标准库 `testing.F`，其种子语料就是回归测试；`go test` 会执行种子，
`-fuzz` 执行探索。它们不引入独立测试体系：

- `internal/protocol/httpapi`：`FuzzStrictBodyDecodingIsTotal`（任意字节要么完整
  映射为命令，要么以有界协议错误拒绝且不回显请求体）、
  `FuzzRecallWireMappingPreservesContractValues`（契约接受的值要么忠实映射，要么
  `invalid_input`）、`FuzzEventIngestBoundsAreEnforced`（事件入口边界）。
- `internal/gateway/agenttool`：`FuzzCanonicalHashIsReplayStable`（幂等规范化对
  字段顺序、等价空值、时区不敏感且可重放）、`FuzzLocalReferencesResolveOrReject`
  （`local_id` 解析全函数）、`FuzzNormalizeTextIsSafeAndIdempotent`（search hint
  规范化不产生非法 UTF-8 且幂等）、`FuzzFeedbackAndForgetBoundsAreEnforced`。
- `internal/domain`：`FuzzPredicateRegistryIsClosed`（图谱谓词注册表封闭且自洽）、
  `FuzzPredicatePathPositionsAreKnown`、`FuzzRevisionWindowsRejectAmbiguousIntervals`
  （revision 时间窗口与量值边界）、`FuzzEvidenceOrderingIsTotal`（证据稳定全序）。

发现过的崩溃输入保留在对应包的 `testdata/fuzz/<Target>/` 下作为回归种子。

## 恢复与质量

```sh
make schema-check
make restore-drill
make portability-drill
make quality
make performance
make offline-default
make config-check
make mcp-check
```

- `restore-drill` 创建并恢复真实的一致性快照，校验权威与检查点，围栏被中断的
  工作，并检查派生状态恢复。
- `portability-drill` 检查全新实例的逻辑导入、映射、溯源、检索等价性、检查点、
  吞吐量与临时空间边界。完整包测试覆盖畸形与恶意输入、回滚、取消、幂等与过时
  generation。
- `quality` 针对固定语料运行关键词、语义、混合、scope 隔离、冲突引用、反馈
  投毒与过时修订行为，并强制广泛的共享 Linux CI 质量阈值。
- 知识图谱测试覆盖图谱抽取调度/幂等、provider 输出校验（未知字段、rune 范围、
  非法谓词、NaN 置信度、自关系）、构建期实体解析、自动确认与待审核、事实时间
  演化、实体事实/列表/提案行为、查询期实体解析（英文与中文别名）、0/1/2 跳与
  记忆关系召回、图谱游标失效，以及并发事实确认。
- `performance` 检查延迟、分配、重建成本、可移植性吞吐量与存储增长的共享 Linux
  CI 边界。这些是回归门禁，不是生产容量承诺。墙钟与分配门禁在默认与 race 套件
  中跳过；它们只在设置 `DMEM_PERFORMANCE=1` 时运行，`performance` 目标会设置该
  变量。
- `offline-default` 验证禁用的 provider 不读取密钥、不发起 HTTP 请求。
- `config-check` 验证 `docs/operations/config.example.toml` 可以通过校验。
- `mcp-check` 运行 `pkg/toolcontract`、`pkg/client`、`internal/protocol/mcp` 的测试
  与 daemon 的无 provider MCP 闭环测试。

### 配置与调和

配置重载与调和测试位于 `internal/daemon` 与 `internal/storage`。它们使用托管
守护进程（`StartManaged` 配合 `config.NewManager`）与包内辅助函数写入严格 TOML
文件，在不使用真实 provider 凭据的情况下切换 provider 启用状态与策略分配。

- `TestRuntimeManagedReloadAppliesSnapshots`、`TestRuntimeConcurrentReloadsAreSerialized`
  与 `TestRuntimeApplySnapshotFailurePreservesPreviousConfiguration` 验证原子重载、
  并发串行化，以及应用失败时回退到先前快照。
- `TestReconciliationPersistsLineageAndResumesBoundedBatches` 证明调和血缘、游标与
  受限批次能持久化并续跑；`TestReconciliationRuntimeLimitsCompletesWithoutScanning`
  与 `TestEmptyReconciliationCompletesDurably` 覆盖不扫描与空扫描的终态。
- `TestSC004ReconciliationEnqueuesEligibleJobsPromptly` 验证 30 秒的 SC-004 边界：
  95% 的新符合条件阻塞作业及时进入调和，同时已完成作业不被重复。

本文档早期版本引用的 `TestManagedDaemonReloadsCapabilitiesAtomically`、
`TestSC014ReloadBecomesEffectiveWithinTenSeconds`、`TestPolicyCorrectionRecoversBlockedProcessing`、
`TestPolicyExpansionDoesNotDuplicateCompletedWork` 与 `TestReconciliationResumesAfterDaemonRestart`
不在仓库中。Phase 7 重新核对后确认：这五个名字没有任何现存文件定义，因此引用已
修正为仓库中真实存在的等价覆盖：

| 原引用边界 | 当前覆盖 |
| --- | --- |
| 原子重载身份与失败回退 | `TestRuntimeManagedReloadAppliesSnapshots`、`TestRuntimeApplySnapshotFailurePreservesPreviousConfiguration` |
| 并发重载串行化 | `TestRuntimeConcurrentReloadsAreSerialized` |
| 策略纠正翻转与不重复已完成工作 | `TestSC004ReconciliationEnqueuesEligibleJobsPromptly`、`TestReconciliationPersistsLineageAndResumesBoundedBatches` |
| 调和不扫描与空扫描终态 | `TestReconciliationRuntimeLimitsCompletesWithoutScanning`、`TestEmptyReconciliationCompletesDurably` |
| 跨重启调和恢复与租约围栏 | `internal/storage` 的 `TestJOB007ExpiredLeaseRecoveryClosesAttemptAndFencesWorker`、`TestJobMaintenancePromotionRecoveryTerminalStatesAndExpiration`，以及 `internal/maintenance` 的 `TestRecoveryDrillsFenceInterruptedWork` |
| 「十秒生效」时限 | 未保留为独立断言。生效时限由 `TestRuntimeManagedReloadAppliesSnapshots` 的同步激活语义覆盖；本项目不再主张一个单独的 SC-014 十秒数值门禁。 |

确定性 provider 演练完全在进程内完成，不访问真实端点：

- `internal/provider` 用 `httptest.NewTLSServer` 提供 OpenAI-compatible stub；
- `internal/daemon` 用进程内确定性 processor（`routeProcessor`、
  `mixedRouteProcessor`）与 `routeEmbedding` 替代模型传输，durable job、claim、
  围栏、候选发布与语义索引仍然真实；
- `internal/jobs` 用包内 processor fake 驱动认领、心跳、失败与取消路径。

调和恢复由守护进程维护循环（1 秒间隔）或直接仓储调用
（`ClaimReconciliationRun` + `Reconcile`）驱动，以便精确控制时序。

### Provider Route 收敛（Phase 5）

- `internal/config` 覆盖 `[processing].enabled`/`[embedding].enabled` 的显式语义：
  禁用时不要求也不校验 provider 连接、密钥或策略；启用时缺少完整 provider 配置会
  被拒绝；处理版本由 provider identity 派生；`disable` 重载产生
  `processing:disable` 调和类。
- `internal/storage` 的 `TestEventCapabilityIngestWithoutProcessingStoresEventWithoutJob`
  与 `TestProcessingDisableReconciliationSupersedesModelJobs` 证明禁用时事件保留为
  `stored`、不创建模型 job，禁用调和取代无法再运行的模型 job 并请求运行中的 job
  取消。
- `TestEmbedMemoryClassifiesProviderFailures` 证明 embedding provider 的永久拒绝、
  暂时故障与畸形输出分别映射为 `ErrProviderPermanent`、`ErrProviderTransient` 与
  `ErrProviderInvalidOutput`；`internal/jobs` 的
  `TestPermanentIndexFailureDoesNotConsumeRetryBudget` 证明永久错误不进入重试预算。
- `TestAggregateRebuildUsesProcessingPolicyAndRecordsAttempt` 与
  `TestAggregateRebuildPolicyDenialBlocksBeforeProviderAttempt` 证明 aggregate
  rebuild 经过 processing policy 与 provider attempt 记录，被拒绝时在调用前阻塞。
- `internal/protocol/httpapi` 覆盖 `POST /v1/events`、`GET /v1/events/{event_id}`
  以及候选审核路由 `/v1/candidates`、`/v1/candidates/{candidate_id}` 与
  `.../confirm`、`.../reject` 的路由元数据、DTO 映射、未知字段与畸形 body 拒绝。
- `internal/gateway/review` 的 `TestConfirmCandidateReturnsMemoryOnlyWhenConfirmed`
  与 `TestReviewValidatesReasonAndNote` 覆盖候选审核命令校验、幂等写身份与错误映射；
  `internal/storage` 的 `TestReviewCapabilityConfirmAndRejectIsIdempotent` 证明存储
  适配器的确认/拒绝幂等与冲突分类。
- `internal/daemon` 的 `TestProviderRouteEventToCandidateReviewEmbeddingAndRecall`
  用真实 SQLite 与进程内确定性 processor/embedding 驱动 event -> durable job ->
  candidate -> review -> revision -> semantic index -> recall 全链路；
  `TestProviderDisabledEventIngestStoresWithoutModelJob` 证明禁用时无模型 job。
- `TestToolCommitAndProviderEventDoNotDoubleExtractOneSource` 证明同一实例中 Tool
  Route commit 与 Provider Route event 是不同来源，同一来源不会被双重提取。

### 候选自动确认

- `internal/gateway/review` 的 `TestAutoConfirmPolicyDefaultsToDisabled`、
  `TestAutoConfirmPolicyCanonicalizesTheClosedKindSet`、
  `TestAutoConfirmPolicyValidityBounds`、`TestAutoConfirmScreenEnforcesEveryBound`
  与 `TestAutoConfirmPublishesOnlyNonDestructiveDeterministicOutcomes` 覆盖显式关闭
  或零值策略必须无效（存储以此视为关闭）、kind 封闭集合规范化、全部边界校验、
  静态筛选的每个拒绝原因，以及"只发布
  `create`/`attach_evidence`、永不发布破坏性动作与 provider 分类结果"的不变量。
- `internal/config` 的 `TestReviewConfigDefaultsAndBounds` 覆盖 `[review.auto_confirm]`
  的默认值（默认开启）、kind/版本允许列表、阈值与限流边界，并证明拼写错误在策略
  关闭时也会被拒绝，而显式关闭允许清空 kinds 集合；
  `TestAutoConfirmBoundsMatchReviewModule` 防止配置边界与 review 模块发布的边界
  漂移；`TestNewSectionsAffectEffectiveIdentity` 证明自动确认开关进入
  `EffectiveIdentity`，因此一次重载即可生效。
- `internal/storage` 的
  `TestAutoConfirmPublishesEligibleCandidateWithSystemReviewIdentity` 证明合格候选在
  抽取提交的同一事务内发布，审核记录、审计事件、修订 `created_by` 与候选
  `auto_review_version` 都归因到 `system:auto-review-v1` 及策略版本；
  `TestAutoConfirmLeavesIneligibleCandidatesPending` 逐项
  证明关闭、kind、敏感度、置信度、`direct` 证据与处理版本边界都会让候选留在人工
  队列且不写入任何审核、决策或权威记录；
  `TestAutoConfirmRefusesDestructiveConsolidationOutcome` 与
  `TestAutoConfirmPublishesAttachEvidenceOutcome` 区分不可发布与可发布的整合结果；
  `TestAutoConfirmRateLimitBoundsPublicationsPerSpace` 证明每空间限流生效且不跨空间
  泄漏；`TestAutoConfirmPolicyChangeDoesNotPublishBacklog` 证明策略变更不追溯发布积压；
  `TestAutoConfirmPolicyDoesNotBlockHumanReview` 证明人工确认仍可用且不会写入
  `auto_review_version`。
- `internal/storage` 的 `TestCandidateAutoReviewVersionIsBoundToConfirmedState`
  证明 schema 本身围栏化该归因：`pending` 与 `rejected` 候选携带版本会被拒绝，
  空字符串版本会被拒绝，只有 `confirmed` 候选可以携带或省略版本。
- `internal/daemon` 的 `TestProviderRouteAutoConfirmPublishesWithoutHumanReview` 用真实
  SQLite 与进程内 processor 驱动 配置 -> ingest -> job -> 自动确认 -> 召回 全链路，
  且全程没有任何人工审核调用；`TestAutoConfirmPolicyConversionFollowsConfiguration`
  证明关闭的配置不会产出有效策略，聚合 kind 会被丢弃。

### 可选向量增强（Phase 6）

- `internal/config` 的 `TestManager_EmbeddingDimensionChangeIsVisibleToRuntime`
  证明只改 `[embedding.provider.options]` 的 `dimensions` 也是一次可激活变更，并且
  能力快照的 compatibility identity 随之改变，因此运行时会选定一个新的向量空间；
  `CapabilityIdentity` 覆盖 embedding 输出选项，而 `PolicyIdentity` 不覆盖。
- `internal/storage` 的
  `TestEmbeddingCompatibilityIdentityBindsModelMarkerAndDimension` 与
  `TestEmbeddingJobIdentityBindsTheWholeVectorSpace` 证明 provider/model 标记与请求
  维度都进入向量空间指纹，持久 embedding 作业身份绑定完整指纹，且未配置 provider
  时不存在作业身份。
- `TestSemanticDimensionChangeInvalidatesVectorsAndRebuilds` 证明仅改变维度就会使旧
  向量不可查询、旧身份作业被取代、新身份重新入队 `embed_memory` 与
  `rebuild_semantic`，并且召回在重建前以 keyword/graph 降级且不返回 semantic score；
  重建后索引维度与向量身份都切换到新空间。
- `TestEmbeddingCompatibilityBindsDocumentAndQueryFormats` 与
  `TestEmbeddingDocumentAndQueryFormatsAreVersioned` 固定 canonical document、query
  格式版本及其在指纹中的绑定；修改任一格式必须同时更新这两个测试与
  [检索与排序](../architecture/retrieval.md) 的身份表。
- `TestSemanticCompatibilityChangeRebuildsEligibleCorpus`、
  `TestSemanticEligibilityExpansionReusesCompatibleVectors` 与
  `TestSemanticEligibilityNarrowingFiltersLiveCorpusImmediately` 证明兼容身份变化会
  重建语料，而资格范围变化在同一个向量空间内复用未受影响的向量、并立即过滤收窄的
  语料。
- `TestSemanticFailedRebuildPreservesUnchangedLiveGeneration` 覆盖 provider 输出畸形
  的失败路径：重建失败保留原有活跃 generation 并标记为 stale，召回继续由未被替换的
  语料提供结果；`TestHybridFallbacksAndNeitherChannelAvailable` 覆盖 provider 故障与
  畸形输出时语义通道降级到 keyword/graph。

固定检索语料使用未设置 `dimensions` 的确定性 provider，因此不因维度进入向量空间
指纹而改变；`make quality` 的 Phase 3 门禁阈值保持不变。

可选 Agent embedding adapter 未实现（决策见
[双路线架构](../architecture/agent-tool-and-provider-routes.md)），因此没有对应的向量
信封测试；完成条件中的“同维但不兼容的向量不得进入活跃 semantic index”目前由
compatibility identity 与 dimension 隔离测试覆盖。

固定检索语料与版本化门禁文件位于 `internal/storage/testdata`。修改语料、排序、
索引格式、可移植格式、规模或阈值时，必须更新对应身份并记录可复现基线。

Phase 3 的 Agent Tool 无 provider 检索门禁由
`internal/storage/testdata/agent-tool-recall-corpus-v1.json` 与
`agent-tool-recall-gates-v1.json` 定义，覆盖同义表达、实体别名、时间问题、偏好、
决策与程序性记忆。门禁要求 macro `Recall@10 >= 0.80`、canonical exact recall
`= 1.0`、兄弟 scope 污染 `= 0`，并验证 hint-only 召回不会压过 canonical content
匹配、无 semantic 执行时不返回 semantic score、explain 与无内容 telemetry 不泄露
hint/query/正文。`make quality` 会运行这些门禁。

## 漏洞扫描

漏洞扫描独立执行，因为它需要下载漏洞数据：

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
make govulncheck
```

`make check` 运行完整的必需套件，但不包括单独调用的 `performance` 与
`govulncheck` 目标。性能门禁也在 `test` 与 `test-race` 中跳过；受控 runner 或
CI 作业通过 `DMEM_PERFORMANCE=1` 显式启用。
