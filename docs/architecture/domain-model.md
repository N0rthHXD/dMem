# 领域模型与权威数据

dMem 的核心不变量是：模型建议与经审核的权威分离，权威只追加、永不原地编辑，
所有派生数据都可重建。本页描述权威数据的形状与规则。

## Scope（空间）

每次写入都必须指定一个显式 scope。Scope 构成类型化层级，类型为 `user`、
`agent`、`application`、`project`、`task`、`session`。Scope 通过
`external_id` 与 `display_name` 提供稳定外部身份；同一父级下 `external_id`
唯一。

召回可以包含祖先 scope，但兄弟 scope 不会隐式可见。Scope 约束在相关性评分与
关系遍历之前应用。公开 HTTP 术语统一使用 scope；内部存储可以继续使用 `space`，
适配器在协议边界重命名。

## 记忆与修订

一条记忆拥有稳定的逻辑 ID 与当前投影；内容存在于不可变修订中。修订永不原地
编辑，也不会被自动处理物理删除。当前状态通过仅追加状态事件演进。

支持的记忆 kind 为 `fact`、`event`、`preference`、`plan`、`procedure`、
`scene`、`topic`、`timeline`。其中 `scene`、`topic`、`timeline` 同时是合法的
聚合 kind。

修订携带规范化检索字段：

| 字段组 | 内容 |
| --- | --- |
| 身份 | `revision_id`、`memory_id`、`parent_revision_id`、`restored_from_revision_id` |
| 内容 | `kind`、`display_content`、`canonical_content`、`canonical_hash` |
| 主体/客体 | `subject_entity_ids`、`object_entity_ids` |
| 事件 | `event_type`、`attributes` |
| 时间 | `event_time_start`、`event_time_end`、`time_precision`、`time_source`、`valid_from`、`valid_until` |
| 语境 | `source_speaker`、`language`、`sensitivity` |
| 质量 | `specificity`、`confidence` |
| 溯源 | `evidence`、`created_by`、`processing_version`、`processor_identity`、`created_at` |

`time_precision` 取值为 `exact`、`day`、`month`、`year`、`approximate`、
`unknown`。`confidence` 与 `specificity` 必须位于 `[0, 1]`。事件时间窗口与
有效时间窗口必须严格递增。

发布为权威前，修订必须通过确定性校验：kind 合法、canonical content 非空、
置信度与具体度在范围内、时间精度合法、窗口合法、证据片段合法，且至少包含一条
`direct` 或 `derived` 证据。

记忆内容是文本。原始事件可以包含文本、JSON 或外部引用；二进制内容与托管附件
存储尚未实现。被接受的外部引用只记录元数据，不复制也不保证所引用的资源。

## 证据

证据片段通过角色描述它如何支持修订：

- `direct`：直接支持。
- `derived`：由其他权威推导。
- `temporal_anchor`：提供时间锚点。
- `context`：仅提供语境。

至少需要一条 `direct` 或 `derived` 证据才能建立权威；仅含 `context` 的证据
不能。证据的稳定顺序由事件时间、事件序号、起始偏移、证据 ID 依次决定；缺失值
排在最后。

## 生命周期

当前生命周期状态包括 `active`、`archived`、`contested`、`expired`、
`superseded`、`retracted`、`deleted`。生命周期状态是存储层的字符串，`frozen`
是独立的布尔标记。

- 记忆写入时状态为 `active`。
- 默认召回只接受 `active` 与 `contested`。
- 逻辑删除（`forget`）把状态置为 `deleted`，将记忆移出召回，同时保留修订、
  证据、状态事件与审计记录。
- 冻结记忆拒绝自动变更。
- 乐观更新携带期望修订；并发更新必须使用期望修订或等价的冲突检测。

## 实体、别名与提及

实体是带 scope 的权威，拥有规范名称、类型化别名、敏感度、状态与来源溯源。
提及把实体绑定到具体的记忆修订或原始事件，并校验来源范围与 scope。别名规范化
可以提示身份，但不会自动合并实体；公开的实体合并路由不受支持。

实体身份解析发生在构建期与查询期：构建期先按规范化名称、再按规范化别名解析，
歧义会被标记；查询期按最长活跃别名匹配并结合词边界与逆频率先验。规范化版本为
`v1`。自动审核规则版本为 `auto-review-v1`。

## 记忆关系

记忆关系是具体修订之间的独立权威。支持的类型为 `supports`、`contradicts`、
`supersedes`、`derived_from`。语义相似度是派生的检索特征，不是存储的关系类型。

关系断言要求证据或显式人工确认，拒绝自关系，并强制端点 scope。公开的关系查询
最多跟随一跳；门控图谱特征计算会在显式谓词意图下额外运行有界类型化两跳实体
事实路径。历史端点可以解释历史，但不会让过时修订文本成为当前结果。

## 实体事实

实体事实（`entity_facts`）区别于修订级关系：它们描述已解析实体之间的类型化
关系，例如 `Alice --participates_in--> Alpha` 或 `Alice --works_for--> Acme`。

一条事实包含主体实体、受控谓词，以及客体实体或类型化字面量之一（二者恰好一个），
外加来源记忆/修订溯源、置信度、状态与有效时间。谓词来自
`internal/domain/graph_predicates.go` 中的封闭类型化注册表（版本
`predicate-registry-v2`），例如 `participates_in`、`works_for`、`owned_by`、
`located_at`、`depends_on`；自由形式的 provider 谓词会被拒绝。

事实是仅追加权威：确认较新事实会取代同一主体与谓词的较旧活跃事实，但不删除其
历史。默认查询只返回当前有效事实，而历史 `reference_time` 可以呈现当时有效的
事实。谓词注册表同时定义方向、基数、可传递性、时间策略、允许的路径位置与自动
确认策略。

## 演化提案

演化提案覆盖重复（duplicate）、补充（supplement）、替换（replacement）与冲突
（conflict）。新记忆候选、实体提及与关系使用各自的提案资源。

确认前会校验每个期望目标修订与冻结状态。补充会创建不可变子修订。替换必须显式
选择同一记忆的新修订，或通过 `supersedes` 连接的新记忆。

没有任何置信度阈值会自动确认演化提案。审核、产生的修订、证据、关系、状态变更、
审计记录与后续作业要么原子提交，要么全部不提交。

## 冲突用例

确认的冲突保留双方、创建 `contradicts` 断言，并打开一个冲突用例。被争议的记忆
默认仍可召回，并携带冲突引用。

解决是显式的，支持 `keep_both`、`dismiss`、`revise_one`、`supersede` 与
`reconcile`。解决会校验所有期望参与方修订，追加解决历史，永不编辑历史修订，
也不静默删除被拒绝的断言。自动冲突裁决与无界图遍历尚未实现。

## 聚合记忆

dMem 支持经审核的 `scene`、`topic`、`timeline` 聚合记忆，建立在显式、有界、
去重的输入修订列表之上。创建首先产生提案。确认会重新校验 scope、当前输入、
敏感度、溯源与处理身份，然后创建一个普通不可变记忆修订，并通过 `derived_from`
关系连接来源。

聚合永不替换其来源记忆。过时聚合会被排除在默认召回之外，而不是带警告返回；
显式调试包含会报告过时原因与溯源。输入变化会把聚合新鲜度标记为过时，并入队
去重的重建工作。重建内容返回审核，而不是自动替换已确认内容。溯源可以一直查询
到输入修订及其证据。聚合生成永不隐式跨 scope。

## 反馈权威

反馈是关于具体记忆修订的仅追加观察。权威记录包含调用方、操作、用途、类型、
原因、时间与可选搜索上下文。

Agent Tool Route 只接受 `used`、`not_used`、`incorrect`、`outdated`、`duplicate`
五个封闭类别，具体决策见 [Phase 0 决策与基线](phase-0-decisions-and-baseline.md)。
Phase 2 已把这些类别持久化为 `agent_tool_feedback` purpose，并保留原始 `kind`，不
把 `not_used` 映射为 `irrelevant`。Phase 7 已删除产生旧反馈事件的 `gateway/feedback`
模块与其存储适配器，因此当前唯一的反馈写入入口是 `dmem_feedback`。以下
purpose/type/reason 组合只作为历史记录与旧格式导入路径保留；它们不是 Agent 工具
契约：

反馈按用途分离，用途/类型/原因是封闭组合：

| 用途 | 允许的 type | 允许的 reason |
| --- | --- | --- |
| `memory_quality_feedback` | `confirm`、`correct`、`contest`、`supplement` | `fact_incorrect`、`fact_incomplete`、`fact_outdated`、`conflicts_with_evidence`、`duplicate`、`other` |
| `retrieval_feedback` | `useful`、`irrelevant` | `answered_query`、`relevant_context`、`not_relevant`、`other` |
| `answer_feedback` | `supported`、`unsupported` | `evidence_supported`、`evidence_insufficient`、`evidence_contradicted`、`other` |

只有 `memory_quality_feedback` 的 `correct`、`supplement`、`contest` 可以请求
演化提案，但反馈事件与提案拥有各自独立的身份与结果。反馈不会直接归档、撤回、
删除或修订记忆。

`retrieval_feedback` 与 `answer_feedback` 必须绑定决策上下文（搜索操作、查询
哈希与类型、结果排名、排序/特征/上下文身份、选择状态、命中的信号类型），而不
存储查询文本。

## 幂等与审计

`idempotency_records` 以写身份（调用方身份 + 操作名 + 幂等 key + 规范化命令
哈希）记录已提交的写结果，使重放返回原始结果，而同一 key 上的不同命令返回
冲突。`audit_events` 记录操作级审计轨迹。两者都是权威，不会被自动处理删除。
