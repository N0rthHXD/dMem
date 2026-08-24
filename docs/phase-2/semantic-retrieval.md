# dMeM 第二阶段语义与混合检索

## 1. 范围

第二阶段增加 semantic 和 hybrid 两种模式，并保持 keyword 模式兼容：

| Mode | 候选通道 | 不可用行为 |
| --- | --- | --- |
| `keyword` | FTS | FTS 实际不可查询时返回 `index_degraded` |
| `semantic` | Vector | Semantic index 不可查询时返回 `index_degraded` |
| `hybrid` | FTS ∪ Vector | 单通道可用时返回降级结果；两者均不可用时返回错误 |

实体、关系和图召回不进入本阶段。

## 2. Embedding 生成

- 只为当前 `active` 或允许检索的 `contested` projection 生成 embedding。
- 输入是规范化 memory content；规范化版本进入 content hash。
- provider/model、维度、processing time 和 index version 必须持久化。
- provider 返回维度、NaN/Inf 或批次数量不符时视为 invalid output。
- revision 或状态改变后，旧 embedding 立即在权威复核中失效。
- embedding job 使用 durable queue、attempt、取消和 fencing，不单独引入消息队列。
- searchable projection 的 revision、space、content hash 或 searchable 变化时，在权威写事务内创建 embedding job 并合并调度一个 waiting rebuild；不改变语义源的生命周期操作保留当前 embedding。
- processing provider 和 embedding provider 独立配置。processing 配置变化不改变语义 identity；embedding provider/model 变化触发当前 projection 的恢复任务。

## 3. VectorIndex 端口

无论最终使用 SQLite 扩展还是 Go 内嵌实现，端口必须支持：

```text
Upsert(memory_id, revision_id, vector, metadata)
Delete(memory_id, revision_id)
Search(vector, allowed_space_ids, eligible_revision_hashes, limit, index_version)
Build(version, snapshot)
Activate(version)
Drop(version)
Status(version)
```

实现必须固定版本和许可证，不允许运行时下载或加载任意动态库。索引内部格式不是权威数据，删除索引后必须能从 SQLite projection 和 embedding provider 重建。

若索引不支持高效的空间前过滤，可以过取候选再由 SQLite 复核，但必须通过评测证明在数据规模上不会造成越界返回或不可接受的召回损失。空间过滤永远不能只依赖近似索引元数据。

## 4. 索引版本与重建

Semantic index 状态：

```text
absent
building
ready
degraded
stale
```

重建采用 shadow version：

1. 固定 projection snapshot 和目标 embedding identity。
2. 构建新 version，不覆盖 active version。
3. 验证记录数、维度、抽样 revision/content hash 和查询可用性。
4. 原子切换 active version。
5. 在保留窗口后清理旧 version。

构建失败保留旧 active version；没有旧版本时 semantic unavailable。重建任务可重试，同一 provider/model identity 最多有一个活跃 rebuild。若构建激活后 projection 再次变化，完成事务创建一个 waiting 后继任务，避免遗漏并发写入。

## 5. 候选召回

检索先解析：

- 请求 mode；
- 起始 spaces 及其祖先；
- memory types 和 states；
- reference time；
- limit、max_chars 和内部 recall 上限；
- active FTS/semantic index version；
- ranking version。

Keyword 和 semantic 各自返回有界候选。SQLite 在有界截断前应用 space、state、type、reference time 和 current revision 过滤，并为向量索引提供 `(memory_id, revision_id, content_hash)` 合格集合；向量层在 cosine 排序和 top-k 前应用该集合。候选使用 `(memory_id, revision_id)` 去重，并保留：

```text
keyword_score
semantic_score
matched_channels
index_versions
```

关键词命中不得因为不在 semantic top-k 中被丢弃，语义命中也不得要求同时命中 FTS。

查询 embedding 在 SQLite read transaction 之外执行。短事务只做通道预检；provider 返回后使用新的权威快照解析 scope、过滤、FTS/semantic version 和候选。向量查询始终指定该快照记录的 index version，返回阶段仍执行权威复核。

## 6. 分数融合

不同通道的原始分数不能直接相加。第一版融合应采用简单、确定且可解释的版本化规则，例如 reciprocal rank fusion；最终公式通过 ADR 和离线评测确定。

融合输出至少包含：

- 版本化 relevance；
- matched channels；
- selection reason；
- 统一 tie-break；
- 是否因通道不可用而降级。

不在第二阶段加入在线学习或基于用户反馈的动态权重。

## 7. 权威复核

返回前逐项确认：

- memory 和 revision 仍存在；
- candidate revision 等于 current revision；
- projection content hash 与索引记录一致；
- 当前状态可检索；
- memory 位于允许作用域；
- reference time 位于有效区间；
- requested type/state 仍匹配。

不满足者被丢弃，不用索引旧文本填充结果。若 stale 命中过多导致无法满足页大小，响应可以少于 limit，并报告 freshness/degraded，不放宽作用域或生命周期过滤。

## 8. 分页和预算

`limit` 和 `max_chars` 沿用第一阶段硬限制。cursor 至少绑定：

```text
cursor_version
query_hash
requested_mode
executed_mode
ranking_version
fts_version
semantic_index_version
reference_time
last_sort_key
```

索引 active version、ranking version、filter 或预算改变时 cursor 无效。分页过程中不自动切换到新 semantic version。

## 9. 响应与解释

每条结果在现有字段上增加：

```text
matched_channels
selection_reason
keyword_relevance       optional
semantic_relevance      optional
```

整体响应增加：

```text
requested_mode
executed_mode
degraded
degradation_reasons[]
fts_index_version
semantic_index_version
ranking_version
```

公开 relevance 是排序用途的相对值，不声明为事实置信度。语义相似度不能替代 evidence 或 candidate confidence。

## 10. 质量门槛

发布前使用固定、版本化语料评估：

- exact identifier keyword recall 不低于第一阶段；
- semantic recall@k 和 hybrid recall@k；
- hybrid nDCG 或 MRR；
- 同级空间污染率必须为 0；
- stale revision 返回率必须为 0；
- 无答案查询的无关召回率；
- keyword、semantic、hybrid p50/p95；
- rebuild 时间、峰值内存和磁盘放大。

具体数值在向量实现和基准数据确定后写入测试配置，不允许只用人工示例作为阶段完成依据。
