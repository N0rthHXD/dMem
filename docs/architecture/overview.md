# 架构总览

dMem 以单个 Go 进程运行，由一个 Linux 用户拥有。数据目录中的文件锁保证每个
数据目录只有一个写入者。SQLite 是领域数据、幂等、审计历史、作业与索引元数据的
权威。应用语义位于协议无关的 Memory Gateway 模块中，第一个入站协议适配器是
Agent 中立的 REST+JSON `http.Handler`。运行时组装该 handler，但不启动监听器；
传输、TLS、认证与网络暴露属于后续独立工作。

## 分层与依赖方向

```text
+-------------------------------------------------------------+
|                        Agent 客户端                          |
+-------------------------------+-----------------------------+
                                |
                     标准交互协议（后续扩展）
             HTTP/2 REST+JSON / MCP / REST+SSE / WebSocket
                                |
                                v
+-------------------------------------------------------------+
|                        协议适配器                            |
|   HTTP API（当前）   MCP（后续）   SSE（后续）   WebSocket    |
+-------------------------------+-----------------------------+
                                | 内部 Go 接口
                                v
+-------------------------------------------------------------+
|                       Memory Gateway                        |
|   Memory     Retrieval   Extraction   Consolidation         |
|   Scope      Feedback    Knowledge    Policy                |
+-------------------------------+-----------------------------+
                                | 能力端口
                                v
+-------------------------------------------------------------+
|                       运行时适配器                           |
|   SQLite 持久化   Jobs   Processing provider   Embedding     |
|   FTS5            sqlite-vec                  Configuration  |
+-------------------------------------------------------------+
```

依赖方向：

```text
Agent 客户端 -> 协议适配器 -> gateway 模块接口 -> gateway 服务
            -> 能力端口 -> 运行时适配器
```

以下方向被禁止，并由架构测试强制：

```text
protocol -> storage
protocol -> jobs
gateway -> net/http
gateway -> 协议 DTO
gateway 契约 -> storage DTO
storage -> gateway service
模块 -> 另一个模块的具体服务
```

### 协议适配器

协议适配器把线上协议翻译为 gateway 调用。它们拥有方法/路径路由、表示层严格
解码、请求体与消息大小上限、协议元数据到 gateway 调用上下文的转换、请求标识与
取消传播、命令/查询构造、结果序列化、错误映射，以及协议边界的 panic 恢复。

适配器不拥有领域校验、记忆生命周期规则、scope 可见性语义、检索规划与融合、
抽取或整合决策、provider 出网策略、事务管理与仓储调用。

第一个适配器是 `internal/protocol/httpapi` 中的 Agent 中立 REST+JSON handler。
它返回 `http.Handler`，不得创建监听器，也不得要求 `r.ProtoMajor == 2`；强制
HTTP/2 由未来的服务层负责。

### Memory Gateway

Memory Gateway 拥有应用语义与编排。它是一组模块，而不是单个庞大的 `Gateway`
接口。每个模块发布自己的命令、查询、结果、服务接口与所需能力端口。模块可以
消费由其他能力实现的窄接口，但不得持有另一个模块的具体服务。接口在可行时
归属消费方，这保证模块可独立测试并防止包依赖环。

### 运行时适配器

运行时适配器实现 gateway 的能力端口。一个 SQLite 支持的实现可以满足多个端口，
逻辑分离不意味着物理上分离的数据库。Provider 适配器保持为进程内的强类型端口；
provider 网络调用位于 SQLite 写事务之外，并通过既有的租约、配置、策略与修订
围栏发布结果。

## Gateway 模块

| 模块 | 职责 | 稳定操作名 |
| --- | --- | --- |
| `memory` | 直接记忆权威的写入与读取 | `memory.remember`、`memory.forget` |
| `scope` | Scope 层级与稳定外部身份 | `scope.create` |
| `feedback` | 反馈追加与聚合健康 | `feedback.append` |
| `retrieval` | 召回规划、融合、排序、上下文选择、游标、explain | `retrieval.recall` |
| `extraction` | 原始事件入口与面向 worker 的处理 | `extraction.ingest` |
| `consolidation` | 候选确认/拒绝与确定性分类 | `consolidation.confirm`、`consolidation.reject` |
| `policy` | 能力授权与披露分区 | 无独立写操作，作为服务被消费 |
| `knowledge` | 实体/关系/事实/提案/冲突的读取与审核变更 | `knowledge.review_entity`、`knowledge.review_relation`、`knowledge.review_fact`、`knowledge.review_evolution`、`knowledge.review_aggregate`、`knowledge.resolve_conflict` |

各模块声明自己的能力端口，例如 `memory.AuthorityStore`、`scope.Store`、
`feedback.Store`、`retrieval.CandidateStore`、`extraction.EventStore`/
`extraction.WorkStore`、`consolidation.Store`、`policy.Store`、
`knowledge.ReadStore`/`knowledge.ReviewStore`。SQLite 适配器通过
`internal/storage/gateway_*.go` 中的 capability 结构满足这些端口。

## 共享 gateway 契约

`internal/gateway` 只包含跨模块共享的值类型与错误契约：

- `CallContext`：`RequestID`、`CorrelationID`、`Caller`、`Idempotency`。
- `CallerIdentity`：`SubjectID`、`ClientID`。
- `Idempotency`：`Key`。
- `WriteIdentity`：由调用方身份 + 操作名 + 幂等 key + 规范化命令哈希构成。协议
  适配器不得把 HTTP 方法或路由纳入核心幂等语义。
- `Error` 与 8 个 `ErrorCode`：`invalid_input`、`not_found`、`conflict`、
  `idempotency_conflict`、`permission_denied`、`resource_exhausted`、
  `capability_unavailable`、`internal`。错误是协议中立的；适配器负责映射为线上
  状态码与错误体。
- `PageRequest`：默认 `limit` 50，最大 200。

## 权威与派生

原始事件在可选自动处理之前提交。记忆拥有稳定的逻辑 ID、不可变修订、仅追加状态
事件、证据与当前投影。

权威数据包括空间、原始内容与事件、记忆与修订、修订证据与属性、候选与审核记录、
整合决策、实体/别名/提及与提案、记忆关系、实体事实、演化与冲突记录、反馈事件、
聚合提案与描述符、导入导出操作记录、配置与策略记录、幂等与审计记录。权威记录
由触发器保护为仅追加或不可变。

以下数据是派生且可重建的：FTS 行、embedding 向量、语义语料元数据、记忆投影、
质量与重复聚类、各类索引元数据（FTS/语义/实体/关系/图谱）、实体图谱统计、反馈
聚合与查询聚合、聚合新鲜度、有效 scope 策略、embedding 资格、有效配置状态、
作业与作业尝试、调和运行、查询遥测与上下文决策。

派生数据永不覆盖权威。恢复流程会清空或标记派生状态为重建中，并重新入队相应
作业。

## 配置与策略

所有守护进程设置来自一个严格 TOML 文件，由 XDG 默认位置或单个 `--config`
定位符选择。文件存储 provider 端点、模型、密钥引用、限制、策略、队列阈值与
路径。密钥值永不进入文件、数据库、状态或日志，只持久化类型化的环境变量引用。

重载（`SIGHUP` 或进程内重载请求）读取、严格解码并原子激活一个完整的文件修订。
有效变更发布一个不可变的有效快照，并带有单调递增的激活、配置与策略 generation。
无效或无变化的变更保持先前快照不变。重启绑定字段（`instance.data_dir`、
`instance.database_filename`、`jobs.worker_count`）会使整个候选文件被拒绝，而
不是被静默忽略。

存在两类策略，默认值不同，必须区分：

- **gateway policy** 是进程内的能力授权与披露分区。未配置时默认允许
  （`default_allow`），用于在本地单用户部署中不引入额外摩擦。
- **provider 出网策略** 是版本化的出网决策集合，默认拒绝。作业分配目标可以是
  精确空间、稳定空间属性或祖先（带后代继承）。当前空间覆盖优先，否则最近祖先
  优先；同等具体度下显式拒绝优先；未匹配的空间默认拒绝；兄弟空间永不继承。

被策略拒绝的目标会把作业移动到 `blocked_policy`，且不解析密钥、不发起网络
调用。策略纠正会创建持久调和运行，在崩溃安全游标下分批重新评估被阻塞的工作。
调和只改变作业、资格投影与索引元数据，永不修改权威记录。

## 作业与恢复

SQLite 存储用于抽取、embedding、索引重建、反馈聚合与聚合重建的持久作业。worker
使用租约、尝试次数、心跳、有界重试与 generation 围栏。只有当前租约持有者与
generation 可以提交结果。启动时会围栏过期工作并把它恢复到有效的重试或终态。
`blocked_policy` 作业不被普通重试调度器选择，也不消耗 provider 重试；
调和会在当前策略 generation 下重新评估它们。

队列应用软上限与硬上限。软上限内的写入可以被接受并延迟工作；硬上限或磁盘硬
上限下的写入会被原子拒绝，而不是存储缺少必需作业的权威数据。

## 可观测性

运行时不提供健康检查端点；运维可见性来自进程日志与 SQLite 中的持久状态。每次
召回都会追加一条有界、无内容的决策遥测记录，覆盖查询计划类型与身份、请求与
实际执行的能力、各来源候选数量、门控拒绝、分数摘要、图谱种子/边/路径/提升
计数、重复聚类、上下文 token 与字符预算、排序/上下文/反馈身份、降级原因与各
阶段延迟。遥测永不记录查询文本、记忆正文、证据片段或 provider 载荷。

详见 [领域模型与权威数据](domain-model.md)、[处理、审核与演化](processing.md) 与
[检索与排序](retrieval.md)。
