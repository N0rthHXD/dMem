# dMeM 记忆空间与作用域

## 1. 模型

每个原始事件和逻辑记忆只属于一个主要空间。空间组成单父节点树，不支持多父节点和隐式标签继承。

第一阶段空间类型：

```text
user
agent
application
project
task
session
```

每个数据库只有一个 `user` 根空间。

## 2. 父子约束

| 子类型 | 允许的父类型 |
| --- | --- |
| user | 无 |
| agent | user |
| application | user |
| project | user |
| task | project |
| session | project 或 task |

空间创建后 `space_id`、`space_type` 和 `parent_space_id` 第一阶段不可修改。需要移动空间时创建新空间，不自动搬迁记忆。

## 3. 外部标识

`project_id`、`task_id`、`session_id`、`agent_id` 和 `application_id` 是适配层接收的外部键。接口层将它们解析为 `memory_spaces.external_id`，核心 repository 和查询只使用 `space_id`。

同一父空间下，`space_type + external_id` 唯一。不同项目可以拥有相同的 task external ID。

## 4. 写入解析

第一阶段写入必须提供一个明确的目标 `space_id`。适配器可以使用外部键查找空间，但不得自动创建缺失空间，除非调用方先调用空间创建接口。

```text
显式 space_id
  → 校验空间存在
  → 校验可写状态
  → 原始事件或记忆绑定该空间
```

不存在默认“当前项目”全局状态，避免不同 Agent 会话互相污染。

## 5. 查询解析

查询字段：

```text
space_ids          一个或多个显式起点
include_ancestors  默认 true
```

算法：

1. 校验所有 `space_ids` 存在。
2. 候选空间初始为显式空间集合。
3. `include_ancestors=true` 时，为每个起点加入全部祖先直到 user 根。
4. 不加入任何子空间、兄弟空间、agent 空间或 application 空间。
5. 多个起点产生空间集合并集。
6. 其他 memory type、status 和 time filter 与空间集合取交集。

示例：

```text
user U
├── project P1
│   ├── task T1
│   │   └── session S1
│   └── session S2
└── project P2

query S1 + ancestors
  = {S1, T1, P1, U}

不会包含 S2 或 P2
```

## 6. Agent 和应用空间

Agent 和 application 空间是 user 的直接子节点，不会自动加入项目、任务或会话查询。调用方需要这些记忆时，必须把对应空间显式加入 `space_ids`。

这避免“Agent 全部可信”被误解为“所有 Agent 记忆默认注入所有项目”。

## 7. 删除和归档

第一阶段只支持：

- 删除或归档单条记忆。
- 删除单条原始事件的普通 API 不提供。
- 删除空间仅允许空空间；不级联删除事件或记忆。

空间 purge 和级联删除延期，避免共享祖先查询和证据引用产生不可解释结果。

## 8. 不变量

- 空间树不能形成循环。
- 非 user 空间必须有父空间。
- 每个数据库只能有一个 user 根。
- 原始事件和记忆的 `space_id` 必须存在。
- 查询永不隐式包含兄弟和后代空间。
- 外部键只用于解析，权威外键使用内部 `space_id`。
