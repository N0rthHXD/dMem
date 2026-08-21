# dMeM 检索与上下文装配

## 1. 检索目标

从当前调用方指定的记忆空间和时间范围中，找到相关、有效、可解释且不超过预算的记忆。检索索引只负责产生候选，权威记忆和生命周期规则负责最终确认。

## 2. 检索流程

```text
查询请求
  → 解析空间、项目、任务、会话和时间
  → 结构化过滤
  → 关键词召回
  → 语义召回
  → 实体和关系召回
  → 时间召回
  → 候选并集
  → 统一排序
  → 当前版本确认
  → 生命周期和冲突处理
  → 去重和预算装配
  → 返回来源与解释
```

## 3. 作用域过滤

作用域过滤优先于相似度排序。查询可以指定：

```text
space_id
project_id
task_id
session_id
agent_id
memory_type
valid_time
status
```

默认不跨项目和会话搜索。第一阶段 Agent 可信不代表查询可以忽略作用域。

## 4. 召回通道

### 4.1 结构化召回

使用 SQLite 条件、时间范围、类型、状态和空间关系过滤。它是安全边界和精确筛选的基础。

### 4.2 关键词召回

适用于：

- 文件名。
- 命令。
- 错误码。
- 包名和标识符。
- 精确术语。

第一阶段优先支持 SQLite 内的全文关键词能力。

### 4.3 语义召回

适用于同义表达、概念查询和自然语言偏好。嵌入属于派生数据，必须记录模型和维度版本。

### 4.4 实体和关系召回

后续支持名称、项目、人员、文件、工具和关系路径。实体索引不是完整知识图谱，不能在第一阶段作为权威事实。

### 4.5 时间召回

区分：

```text
事件发生时间
记忆创建时间
版本有效时间
查询参考时间
```

“当前状态”查询优先有效时间和 active 投影；“历史上发生什么”查询允许读取 superseded 和 expired 版本。

## 5. 候选并集

候选集合为：

```text
结构化候选
  ∪ 关键词候选
  ∪ 语义候选
  ∪ 实体候选
  ∪ 关系候选
  ∪ 时间候选
```

关键词、实体或关系命中的记录不能因为没有进入语义 top-k 而丢失。

## 6. 排序

排序可以使用以下信号：

```text
semantic_score
keyword_score
entity_score
relation_score
space_match_score
source_reliability
status_score
recency_score
usage_score
conflict_penalty
content_cost
```

排序公式是业务规则，应保持可配置、可解释和可测试。索引实现只提供基础分数。

## 7. 当前版本确认

候选命中后必须回到 SQLite 权威数据：

1. 根据 `memory_id` 读取当前投影。
2. 确认候选 `revision_id` 是否仍然有效。
3. 排除 deleted、retracted 和默认不可见状态。
4. 根据查询参考时间处理 valid time。
5. 加载冲突和替代关系。

派生索引中的旧文本不能直接返回为当前记忆。

## 8. 冲突处理

查询可以选择：

```text
只返回当前有效
返回当前有效和冲突提示
返回完整历史
排除 contested
```

当多个版本都对查询相关时，结果必须显示时间或冲突关系，不能把互相矛盾的内容合并成一句无来源摘要。

## 9. 去重和多样性

装配阶段去除：

- 相同记忆版本重复命中。
- 同一逻辑记忆的低优先级重复版本。
- 被高层摘要完整覆盖的原子内容。

同时保留：

- 不同来源的独立证据。
- 不同时间段的有效状态。
- 冲突版本的必要提示。

## 10. 上下文预算

预算包括：

```text
最大结果数
最大总字符数
最大检索时长
```

第一阶段 `limit` 和 `max_chars` 都是硬限制，必须同时满足。字符数按 Unicode code point 统计，仅计算返回的记忆 content；metadata 和 evidence reference 不计入。单条内容超过剩余预算时不截断，跳过并设置 `truncated=true`。第一阶段不接受 Token 预算。

FTS 先按 BM25 相关性排序，再以 `updated_at DESC, memory_id ASC` 打破同分。cursor 是带版本的 opaque base64url JSON，包含查询哈希、最后一项分数、updated at 和 memory ID；cursor 查询哈希不匹配时返回 invalid_request。有效区间采用 `[valid_from, valid_until)`；未提供 reference time 时使用查询开始时的 daemon UTC 时间。

装配优先级建议：

```text
项目或任务高层上下文
  → 当前有效原子事实
  → 时间和冲突提示
  → 必要来源证据
```

超出预算时，必须返回已截断或未完整装配的状态，不得让调用方误认为返回了完整历史。

## 11. 检索结果

每条结果包含：

```text
memory_id
revision_id
content
memory_type
space_id
status
valid_from
valid_until
relevance
evidence_refs
conflict_refs
selection_reason
```

整体结果包含：

```text
degraded
index_state
freshness
budget_used
truncated
```

## 12. 索引不可用

| 故障 | 降级行为 |
| --- | --- |
| FTS 不可用 | list/get 仍可用，search 返回 index_degraded |
| 权威 SQLite 不可用 | 拒绝伪造结果，返回存储错误 |

语义、实体和关系检索属于后续阶段；请求 semantic mode 返回 unsupported，不静默改用关键词。
