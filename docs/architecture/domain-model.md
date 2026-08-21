# dMeM 领域模型

## 1. 核心原则

```text
原始事件 = 外部事实依据
记忆版本 = 对事实的结构化理解
当前投影 = 当前检索使用的权威视图
索引 = 可重建的查询加速数据
```

原始事件和记忆版本都需要持久化，但职责不同。记忆版本不能替代原始事件，索引不能替代记忆版本。

## 2. 对象关系

```text
原始事件
   │
   └──< 证据关联 >── 记忆版本
                         │
                         ├── 属于记忆
                         ├── 具有生命周期状态
                         ├── 关联其他版本
                         └── 生成派生索引

记忆空间 ──< 记忆
处理任务 ──< 原始事件 / 记忆版本 / 索引
反馈事件 ──> 记忆版本或当前投影
审计事件 ──> 所有业务操作
```

## 3. 原始事件

原始事件表示外部系统报告的输入。

必需语义：

- `event_id`：稳定事件标识。
- `operation_id`：外部操作或 daemon 操作标识。
- `source_type`：消息、对话、工具、文件、采集器等。
- `source_name`：来源系统或应用。
- `occurred_at`：事件实际发生时间。
- `received_at`：daemon 接收时间。
- `content_ref`：内容或附件引用。
- `space_id`：主要记忆空间。
- `metadata`：来源相关扩展信息。

事件是追加式对象。修正来源内容时，应创建新的事件或修订事件关系，不应无历史地修改原始事实。

## 4. 记忆空间

空间是记忆的组织和查询边界，不等同于第一阶段的安全授权。

支持的空间类型：

```text
user
agent
application
project
task
session
imported
shared-local
```

空间可以有父子关系，例如：

```text
user
 └── project
      └── task
           └── session
```

查询必须明确空间或使用接口层提供的默认空间上下文。默认不跨所有项目和会话搜索。

## 5. 记忆

记忆是稳定的逻辑身份，不直接等于一段文本。

主要属性：

```text
memory_id
memory_type
space_id
current_revision_id
current_state
created_at
retention_policy
sensitivity
```

记忆类型包括：

```text
fact
preference
constraint
decision
task
event
relation
project_context
agent_experience
```

## 6. 记忆版本

记忆版本保存某一时刻的不可变语义内容。当前生命周期状态不存放在 revision 中，而由 `memories.current_state` 和追加式 `memory_state_events` 表达。

```text
revision_id
memory_id
parent_revision_id
content
content_type
valid_from
valid_until
confidence
created_at
created_by
processing_version
model_identity
```

版本创建后不可变。修改、合并、恢复和人工纠正都创建新版本。一个逻辑记忆第一阶段只有一个当前 revision，不支持多当前分支。冲突事实创建为独立逻辑记忆，并通过 contested 状态和证据说明冲突。

时间字段含义：

| 字段 | 含义 |
| --- | --- |
| `occurred_at` | 原始事件发生时间 |
| `received_at` | daemon 收到事件时间 |
| `created_at` | 记忆版本创建时间 |
| `valid_from` | 内容开始有效的业务时间 |
| `valid_until` | 内容结束有效的业务时间 |
| `updated_at` | 当前投影更新时间 |

## 7. 证据关联

证据关联说明记忆版本为什么存在：

```text
revision_id
event_id
content_ref
evidence_range
evidence_type
extraction_method
created_at
```

`evidence_type` 区分：

```text
direct_statement
tool_result
model_inference
human_confirmation
external_import
```

高层聚合记忆可以引用多个原子记忆，原子记忆最终应能下钻到原始事件。

## 8. 记忆关系

第一阶段支持：

```text
supports
contradicts
supersedes
derived_from
related_to
belongs_to
mentions
```

关系是权威业务对象，关系索引只是加速结构。关系也应关联来源、版本和状态。

## 9. 生命周期状态

当前内容状态保存在 `memories.current_state`，每次变化追加 `memory_state_events`：

```text
candidate
active
contested
superseded
archived
expired
retracted
deleted
```

正交标记：

```text
frozen
pending_review
sensitive
```

状态含义：

| 状态 | 默认是否检索 | 是否保留证据 | 含义 |
| --- | ---: | ---: | --- |
| candidate | 否或仅显式查询 | 是 | 尚未确认 |
| active | 是 | 是 | 当前可用 |
| contested | 默认谨慎 | 是 | 存在冲突 |
| superseded | 否 | 是 | 被新版本替代 |
| archived | 否 | 是 | 冷数据 |
| expired | 否 | 按策略 | 已超出有效时间 |
| retracted | 否 | 按策略 | 被明确撤回 |
| deleted | 否 | 按删除策略 | 当前服务中删除 |

状态变化不修改历史 revision。创建新 revision 和状态变化可以在同一事务发生，但它们是两个独立事实。

## 10. 处理任务

任务表示异步工作：

```text
job_id
job_type
target_type
target_id
idempotency_key
status
attempt_count
next_run_at
processing_version
model_identity
last_error
created_at
started_at
finished_at
```

任务不是权威记忆内容，但任务状态必须持久化，以便恢复和审计。

## 11. 反馈与审计

反馈事件表示用户或 Agent 对记忆的意见：

```text
confirm
correct
supplement
contest
retract
freeze
archive
irrelevant
useful
```

审计事件记录谁在何时对什么对象执行了什么操作。第一阶段 Agent 全部可信，但不能省略审计。

## 12. 不变量

- 一个 `memory_id` 可以有多个版本，但只有一个 `current_revision_id` 和一个当前状态。
- 当前投影必须指向存在的版本。
- 记忆版本不能引用不存在的原始事件或记忆。
- 版本历史不能形成循环。
- 原始事件幂等键在保留期内唯一。
- 派生索引必须能回指 `memory_id` 和 `revision_id`。
- 删除或撤回的版本不能被默认查询返回。
- 记忆版本的内容、有效时间、来源、父版本和创建时间不可通过普通修改覆盖。
- 每次当前状态变化必须追加状态事件，并与 `memories.current_state` 原子更新。
