# 处理、审核与演化

> **状态**：本文描述当前已实现的 dMem Provider Route。Phase 5 已用显式
> `[processing].enabled` 与 `[embedding].enabled` 表达能力状态，删除了
> `builtin-noop` 消费模型 job 的流程，并让禁用状态把原始事件保留为 `stored`。
> Agent Tool Route 不需要本文任何模型配置；两条路线的目标见
> [Agent Tool 与 dMem Provider 双路线架构](agent-tool-and-provider-routes.md)。

dMem 把外部模型输出当作建议，而不是权威。处理在后台异步进行，产出独立可审核的
候选或提案；只有显式确认才会改变记忆权威。

## 事件摄取

`POST /v1/events`（`extraction.ingest`）摄取一条原始事件。事件包含 scope、来源类型
与名称、可选来源事件 ID/流键/序号、可选发生时间、内容与元数据。内容 kind 为：

- `text`：文本内容。
- `json`：结构化 JSON 值。
- `reference`：外部引用，只记录元数据（URI、声明大小与哈希），不复制也不保证
  所引用的资源。

摄取在 processing 启用时于同一事务中持久化事件并入队一条 `extract_memory` 工作；
未启用时不创建任何模型 job，事件以 `stored` 状态保留，响应 `status` 为 `stored`、
`job_ids` 为空。二进制内容与托管附件存储尚未实现。摄取返回事件 ID、处理状态与
作业 ID 列表；`GET /v1/events/{event_id}` 返回处理状态。

## 处理 Provider

处理与 embedding provider 分别配置、独立限流、默认禁用。启用状态由
`[processing].enabled` 与 `[embedding].enabled` 显式表达；禁用时不要求 endpoint、
model、secret 或出网策略，也不解析密钥、不发起 HTTP 请求。provider 类型为
`openai-compatible`，启用后必须提供 endpoint、model、默认拒绝的出网策略与环境
变量密钥引用。

不存在 `builtin-noop` 处理器：禁用时不会创建模型 job；若某个模型 job 因配置变化
在无处理器时被认领，它会以 `processing_disabled` 永久失败，而不是被记录为成功。
配置重载把 processing 从启用改为禁用时，调和会取代无法再运行的模型 job，并把它
们的原始事件保留为 `stored`。

处理 provider 同时驱动两类工作：

- `extract_memory`：记忆候选抽取，遵循 `processing-structured-v1` 契约。
- `extract_graph`：知识图谱抽取，遵循 `graph-typed-v1` 契约，并额外受
  `[processing].graph_enabled` 门控。

`rebuild_aggregate` 也调用处理 provider，并同样经过 processing policy 与 provider
attempt 记录。embedding provider 遵循 `embedding-document-v2` / `embedding-query-v1`
文档与查询格式。provider 调用在 SQLite 写事务之外执行；provider/model 与处理身份
随工作一起存储，使结果始终可归因。provider 错误分类
（disabled/policy/invalid_input/transient/permanent/invalid_output）统一映射为领域
结果：策略拒绝进入 `blocked_policy`，transient 进入有界重试，permanent 与
invalid_output 直接终止，避免永久错误被无差别重试。

## 候选与整合

抽取产生候选，候选在确认或拒绝之前是独立的审核对象。整合
（consolidation）把新候选与现有修订比较，产生以下动作之一。

> **当前实现边界**：`internal/gateway/consolidation` 只提供纯分类器
> （`classifier.go`）与 `Classifier` 端口；其旧入口服务（`Confirm`/`Reject`/
> `Decision` 命令）在 Phase 7 被删除。记忆候选的确认与拒绝现在通过
> `internal/gateway/review` 模块与 HTTP 入口 `/v1/candidates`、
> `/v1/candidates/{candidate_id}`、`/v1/candidates/{candidate_id}/confirm`、
> `/v1/candidates/{candidate_id}/reject` 暴露；`internal/gateway/review` 只做输入
> 校验、幂等写身份与错误映射，审核状态机、整合分类与审计仍由存储层的
> `Repository.ConfirmCandidate`/`RejectCandidate` 执行。图谱/实体/事实/关系/演化/
> 聚合提案与冲突解决的审核仍只有存储层方法，没有协议入口。Agent Tool 提交的知识
> 建议不进入候选表，而是持久化为 `agent_tool_proposals` 审核对象。

| 动作 | 含义 |
| --- | --- |
| `attach_evidence` | 把候选作为证据附加到现有修订。 |
| `refine` | 提出对现有记忆的细化。 |
| `supersede` | 提出用候选取代现有修订。 |
| `contest` | 提出争议并打开冲突路径。 |
| `create` | 创建新记忆。 |
| `reject` | 拒绝候选。 |

确定性分类已实现并生效（`consolidation-v1`）：使用精确哈希与余弦阈值（默认同一
来源事件 0.90、同一主体与 kind 0.94），候选上限为 32。余弦阈值本身永不取代、
争议或删除任何内容；它们只产生建议，破坏性变更仍需审核。

可选 provider 分类**尚未接线**：`consolidation.Classifier` 端口与
`Repository.SetConsolidationClassifier` 存在，且 `ValidateProviderProposal` 会拒绝
在没有确定性冲突的情况下提出的破坏性动作，但当前没有任何类型实现该端口，也没有
任何生产者调用 `SetConsolidationClassifier`。在出现真实消费者前，分类只走确定性
路径。

## 审核与自动确认

实体、别名、提及、记忆关系、实体事实、演化提案与聚合提案各自拥有独立的提案
资源与审核记录。提案字段不可变，审核状态是终态，记录不可删除。这些都由 schema
触发器与存储层评审方法强制，不依赖任何协议入口。

满足封闭自动确认策略的图谱事实可以自动确认，并记录 `auto-review-v1` 规则版本；
其余记录为待审核状态。谓词的自动确认策略取值为 `never`、`exact_match` 或
`threshold`，并受谓词自身的最低置信度约束。

记忆候选默认由 `[review.auto_confirm]` 策略把关：满足全部封闭边界的候选在抽取提交
的同一事务内直接发布，其余留在审核队列等待显式审核确认：

- 只发布确定性整合结果中的 `create` 与 `attach_evidence`；`refine`、`supersede`、
  `contest`、`reject` 以及 provider 分类结果永不自动发布；
- 只发布配置 kind 集合内的候选，聚合 kind 永不接受；
- 只发布 `sensitivity = normal` 且含至少一条 `direct` 证据的候选；
- `confidence` 必须不低于配置阈值，可选用 `processing_version` 允许列表限定来源；
- 每个空间每小时有强制发布上限，计数来自不可变的审核记录；
- 审核记录使用系统身份 `system:auto-review-v1`，并写入策略版本与判定所用阈值，
  因此自动发布与人工审核在审计上可区分；

候选表以 `auto_review_version` 记录策略版本。schema CHECK 把它限制为只能出现在
`confirmed` 候选上且不得为空字符串，因此 `pending`、`rejected` 以及被整合归约为
拒绝的候选都为 NULL；人工确认的候选也为 NULL，因为只有存储层的自动确认路径会
写入该列，协议审核入口从不设置它。

策略在候选创建时评估，因此变更策略只影响之后创建的候选，永不追溯发布已积压的
审核队列；策略默认开启。启用或调整策略时，`EffectiveIdentity` 变化并触发一次
配置激活，但不会产生新的调和类别。

没有任何置信度阈值会自动确认演化提案。模型置信度不能自主修订或删除记忆；自动
确认策略同样只能新增权威——它不能修订、取代、争议或删除既有权威。

## 出网策略与密钥

provider 出网策略是版本化的默认拒绝决策集合。作业分配目标可以是：

- 精确空间（`selector = "space"`）；
- 稳定空间属性（`selector = "space_type"`）；
- 稳定外部标识（`selector = "external_id"`）。

匹配规则：当前空间覆盖优先，否则最近祖先优先；同等具体度下显式拒绝优先；
未匹配的空间默认拒绝；兄弟空间永不继承。每个空间在 provider 工作被评估之前都会
获得当前 generation 的决策，因此启动后新建的空间无需在文件中列出 UUID。

策略同时限制来源/内容类型与敏感度。被拒绝的作业移动到 `blocked_policy`，且不
解析密钥、不发起网络调用。密钥只以类型化环境变量引用的形式持久化，并且只在
策略批准某次请求之后解析；密钥值永不进入配置、数据库、状态或日志。provider
出网输入会被裁剪到配置的 `max_egress_input_chars` 上限。

## 作业系统

SQLite 存储持久作业。worker 处理的工作类型包括：

```text
extract_memory              extract_graph
rebuild_fts                 rebuild_semantic
rebuild_entity_index        rebuild_relation_index
rebuild_graph_index         embed_memory
aggregate_feedback          rebuild_feedback_aggregates
rebuild_aggregate
```

作业使用租约、尝试次数、心跳、有界重试与 generation 围栏：

- 认领会选择到期的 `pending`/`retrying` 作业并递增尝试次数与租约 generation，
  设置租约到期时间。
- 心跳延长租约并报告取消请求；租约丢失会放弃该次执行。
- 失败时，可重试且未超最大尝试次数则回到 `retrying` 并按指数退避安排
  （从初始退避翻倍，封顶于最大退避），否则进入 `failed`。
- 过期租约在启动时恢复为 `retrying`/`cancelled`/`failed`。
- 只有当前租约持有者与 generation 可以提交结果。

队列应用软上限与硬上限：软上限内的写入可以接受并延迟工作；硬上限或磁盘硬上限
下的写入会被原子拒绝，而不是存储缺少必需作业的权威数据。磁盘压力达到软阈值时
worker 暂停认领，达到硬阈值时写入失败而只读查询仍可继续。

## 配置重载与调和

`SIGHUP` 请求一次串行化的配置重载流水线，重复信号会被合并。重载读取、严格解码
并原子激活一个完整的文件修订：

- 结果可能是 `activated`、`no_change`、`rejected` 或 `failed`。
- 有效变更发布不可变的有效快照，并递增激活、配置与策略 generation。
- 无效或无变化的变更保持先前快照不变。
- 重启绑定字段（`instance.data_dir`、`instance.database_filename`、
  `jobs.worker_count`）的变化会使整个候选文件被拒绝，返回
  `config_restart_required`。
- 若激活后的应用失败，运行时会回退到先前快照并重新应用。

策略纠正会创建持久调和运行，在崩溃安全游标下分批重新评估被阻塞的工作。调和
只改变作业、资格投影与索引元数据，永不修改权威记录。策略变化后兼容的 embedding
会被复用；身份不兼容时触发重建。

processing 或 embedding 的 `enabled` 变化会产生明确的调和类别：`enable` 重新评估
被阻塞的工作，`disable` 取代无法再运行的模型 job（原始事件保持 `stored`，运行中的
job 收到取消请求），`runtime_limits` 不扫描。禁用不会删除已有记忆、证据、proposal
或审计。

embedding 的 provider、model、请求维度或文档/查询格式变化会选定一个新的向量空间
身份（见 [双路线架构](agent-tool-and-provider-routes.md#embedding-向量空间身份与失效)）。
恢复逻辑会把绑定旧身份的 `embed_memory` 与 `rebuild_semantic` 作业取消，为缺少
当前空间向量的合格修订重新入队 embedding，并安排一次新的 `rebuild_semantic`。重建
只有在每个合格修订都拥有当前空间的向量时才会发布新的 index generation；否则保留
原活跃 generation 并标记为降级，召回继续以 keyword/graph 执行。

维护循环每秒执行一次，负责恢复过期租约、使过期作业到期、提升等待中的作业并
推进调和运行；worker 以 100 毫秒间隔轮询认领，在磁盘压力下跳过。
