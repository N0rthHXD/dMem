# dMeM 第三阶段能力矩阵

## 1. 权威范围

本文档是第三阶段实现和验收范围的权威来源。状态定义：

```text
required       第三阶段必须实现并通过 required 测试
schema-only    保留稳定对象或字段，不提供完整高级行为
unsupported    API 可识别请求，但明确返回 unsupported
deferred       第三阶段不提供接口和实现
```

## 2. 阶段原则

- 复用当前 revision、candidate、job、retrieval 和 maintenance 基础，不建立第二套平行状态机。
- 自动处理只能提出 proposal；权威 entity、relation、revision 和 conflict resolution 必须经过显式审核或人工命令。
- Entity mention、relation、conflict、feedback 和 aggregate provenance 是权威数据；对应索引、统计和生成缓存是派生数据。
- 所有 assertion 绑定具体 revision 或原始事件范围，不把可变的 current memory identity 当作充分证据。
- Feedback 是对某次内容或检索效用的观察，不等于生命周期命令。
- 关键词、语义、实体和关系召回独立执行后取并集，任何单一 top-k 不得过滤其他通道。
- 逻辑迁移走领域校验、来源映射和冲突流程，不直接复制或写入另一个实例的 SQLite 内部结构。
- 未发布开发阶段继续重新初始化数据库，不实现 SQLite Schema migration。

## 3. 能力矩阵

| 能力 | 状态 | 第三阶段契约 |
| --- | --- | --- |
| 第二阶段能力回归 | required | 自动处理、候选审核、keyword/semantic/hybrid、备份恢复和默认离线继续通过 |
| Processor 演进 proposal | required | 输出强类型 target revision、action、entity/relation spec、typed evidence 和 provider identity |
| Provider 完整上下文契约 | required | 受限 context memories、sensitivity 和 typed evidence 在生产 adapter 中不丢失 |
| 权威 entity | required | 稳定 ID、规范名称、固定类型、alias、状态、空间和来源可查询审计 |
| Entity mention | required | 绑定具体 memory revision 或 raw event 范围；人工确认后才成为权威 assertion |
| Entity merge proposal | schema-only | 保存 merge 建议和目标，不执行自动或公开 merge |
| 自动实体合并 | deferred | 不自主合并稳定 identity 或迁移已有 relation |
| 全局 ontology | deferred | 不提供任意类型注册、约束推理或跨空间全局消歧 |
| 权威 memory relation | required | 独立 relation ID、固定类型、方向、端点 revision、状态、证据和历史 |
| Relation proposal/review | required | Processor 只创建 proposal；confirm 后原子创建 assertion 和索引任务 |
| 一跳关系查询 | required | 按实体、memory、类型和方向查询，并复核空间、revision 和生命周期 |
| 多跳图查询 | deferred | 不提供通用路径语言、推理或图遍历 API |
| 外部图数据库 | unsupported | SQLite 继续保存权威关系，不把图基础设施作为阶段依赖 |
| Conflict relation/case | required | `contradicts` assertion 与 open/resolved/dismissed case 分离并绑定参与 revision |
| Conflict 查询解释 | required | get/search 返回 conflict refs、对端 revision、状态和来源摘要 |
| 人工 conflict resolution | required | keep-both、dismiss、revise-one、supersede 和 reconciled-memory 使用显式命令 |
| 补充已有记忆 | required | 审核 proposal 时以 expected revision 创建新 revision，原子更新 projection 和索引任务 |
| 替代已有记忆 | required | 显式审核并保留 supersedes relation、旧 revision 和状态历史 |
| 多目标乐观并发 | required | 任一目标 revision 或 frozen 状态变化则整个审核事务失败 |
| 自动冲突裁决 | deferred | 模型、confidence、来源权重或反馈不能自主选择胜者 |
| 自动撤回或删除 | unsupported | 自动流程请求破坏性生命周期操作时明确拒绝 |
| Feedback event | required | 追加式、幂等，绑定观察时 memory/revision、调用方、类型和可选 search context |
| `useful`/`irrelevant` | required | 记录效用，不直接改变 memory state |
| `correct`/`supplement` feedback | required | 反馈先保存；内容变更另建 proposal 或显式 revision |
| Feedback 查询 | required | 可按 memory、revision、调用方、类型和时间查询 |
| Feedback 聚合 | required | 版本化、有限窗口、可从原事件重建，不成为权威反馈来源 |
| Feedback 排序信号 | required | 固定版本、影响有上限、可解释，cursor 绑定聚合与 ranking version |
| 在线训练或隐式遥测反馈 | unsupported | 不训练模型，不从未声明点击或访问推断反馈 |
| 场景/主题聚合 proposal | required | 有界输入 revision、固定 processing version、先审核后激活 |
| 权威 aggregate memory | required | 版本化内容，通过 `derived_from` 下钻到输入 revision 和原始 evidence |
| Aggregate stale/rebuild | required | 输入 current revision 或状态变化可检测 stale，并排入 fenced rebuild |
| 长期画像边界 | schema-only | 保留 aggregate kind、策略版本和敏感性，不默认自动生成画像 |
| 自治长期画像 | deferred | 不跨空间持续推断或静默更新用户画像 |
| 逻辑导出 v1 | required | 版本化 manifest、确定性顺序、checksum、范围和对象闭包 |
| 导出 dry-run | required | 报告对象数量、外部引用、敏感对象和省略项，不生成半成品包 |
| 逻辑导入 v1 | required | 先校验和规划映射，写入 imported provenance，幂等且可恢复 |
| 空间与 ID 映射 | required | 显式目标空间，默认生成本地 ID，保存 source identity/object ID 映射 |
| 导入冲突 | required | 创建正常 proposal/conflict，不覆盖本地 current revision |
| 未知逻辑格式或 SQLite 直接导入 | unsupported | 明确 `unsupported_format` 或 `incompatible_version` |
| 跨主机实时同步 | deferred | 导入导出不是复制或同步协议 |
| SQLite Schema migration | deferred | 继续使用完整初始化 Schema 和精确版本检查 |
| Token 预算 | unsupported | 继续以 `limit` 和 `max_chars` 为硬限制 |
| 多用户 ACL、网络监听 API | deferred | 继续采用同一 Linux 用户和 UDS 模型 |

## 4. Required 端到端场景

### 4.1 演进已有记忆

```text
new raw event
  → durable processing job
  → candidate/evolution proposal with target revision
  → human confirm with expected revision
  → validate scope, evidence and frozen state
  → create new immutable revision and relation/state events
  → update projection/FTS and enqueue derived-index jobs
  → append review, idempotency and audit
```

审核期间目标 revision 变化、目标被冻结或 evidence 失效时，事务整体失败。重复请求返回原结果，不重复创建 revision、relation 或 review。

### 4.2 冲突闭环

```text
conflict proposal
  → confirm
  → contradicts relation + conflict case
  → affected memories become contested when policy requires
  → search exposes conflict refs
  → explicit resolution
  → resolution event + optional revision/relation/state changes
```

`contested` 只是检索投影状态，不能替代冲突对象；`contradicts` 只表达 assertion，不能替代人工处理 case。

### 4.3 实体和关系辅助检索

```text
query
  → resolve explicit scopes and filters
  → keyword / semantic / entity / relation recall independently
  → union by current memory/revision
  → versioned fusion
  → authoritative relation, revision, state and valid-time check
  → limit/max_chars assembly with matched channels and conflict refs
```

实体或关系索引不可用时允许降级到其余通道，但响应必须报告 unavailable channel、实际模式和版本。纯 entity/relation 请求在对应能力不可用时返回稳定 `index_degraded`。

### 4.4 Feedback 排序

```text
search result at revision R
  → append useful/irrelevant feedback for R
  → rebuild/version feedback aggregate
  → ranking version consumes bounded usage signal
  → new cursor binds ranking and aggregate version
```

旧 revision 的反馈不自动转移到新 revision，单条反馈不能触发生命周期变化，也不能压过作用域和权威状态过滤。

### 4.5 聚合与逻辑迁移

聚合必须绑定固定输入 revision 集合并允许下钻。逻辑导出按显式选择范围计算对象闭包；导入先完成格式、checksum、空间和 ID 映射规划，再通过领域事务写入。失败批次可恢复，且不留下缺少映射或 provenance 的权威对象。

## 5. 非目标

- 不建设通用知识图谱、图查询语言或外部图数据库。
- 不让模型自动 merge entity、裁决冲突、修订 frozen memory 或执行破坏性生命周期操作。
- 不根据反馈在线训练、微调或下载模型。
- 不自动生成跨项目、跨空间的长期画像。
- 不把逻辑导入导出扩展为实时同步、数据库复制或旧 SQLite 兼容层。
- 不为第三阶段新增动态插件 ABI、网络监听或多用户授权系统。

## 6. 完成标准

- [测试矩阵](test-matrix.md)中所有 `required` 场景通过，第二阶段回归继续通过。
- 初始化 Schema 一次性包含第三阶段对象、索引和约束，OpenAPI、Go enum 和 SQL CHECK 同步。
- 自动流程无法绕过 evidence、scope、expected revision、frozen、review、idempotency 和 fencing 写入权威演进结果。
- Entity/relation/feedback/aggregate 权威对象在索引删除后仍可查询或重建，不从派生数据反推权威事实。
- Conflict case、relation、memory state 和 resolution history 不出现部分提交或互相矛盾的终态。
- 新召回通道保持 sibling-space 污染为 0，stale revision 返回为 0，单通道故障降级可见。
- Feedback 排序通过固定语料质量门槛，且不能显著损害 exact keyword、entity 和 conflict recall。
- Aggregate 均可下钻到输入 revision，输入变化可检测 stale，敏感性不因聚合降低。
- Export/import 演练证明确定性、checksum、幂等、映射、冲突保留和索引重建正确。
- Backup/restore 覆盖新增权威表、job fencing 和索引重建检查。

## 7. 开发期数据策略

第三阶段数据库仍视为可丢弃的未发布开发数据。Schema 变化直接更新完整初始化 SQL；现有开发数据库重新初始化，不提供 migration、兼容视图、双写或旧 Schema 导入。逻辑格式只承诺其独立 `format_version`，不承诺接受任意旧数据库文件。
