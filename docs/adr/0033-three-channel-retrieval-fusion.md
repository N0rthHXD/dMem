# ADR 0033：三通道检索融合（rrf-v3）

- 状态：accepted
- 日期：2026-09-09
- 关联：取代 ADR 0027 的通道枚举与 API 映射部分；修订 ADR 0032 的排名与融合部分（其构建主体、entity resolution 定位与图信号模型继续有效）

## 背景

ADR 0027 定义了 keyword、semantic、entity、relation 四个召回通道；ADR 0032 将顶层模型改为 keyword、semantic、graph 三通道，但实现保留了旧的 entity/relation 独立 RRF 项并追加了 graph 项，形成事实上的五路融合。同一 entity mention 或 memory relation 证据因此被重复加权（entity+graph 或 relation+graph 各贡献一次 `1/(60+rank)`），relation 通道扩展出的候选还会污染 graph seed 集，产生隐式二阶传播。同时，增加 graph RRF 项后排名公式已实质改变，却仍沿用 `rrf-v2` 标识，破坏了 versioned ranking 与 cursor 绑定的原则。项目未发布，无需兼容历史契约。

## 决策

### 1. 顶层通道严格为三个

```text
keyword
semantic
graph
```

- Entity resolution 属于 query understanding：在每次检索中确定性执行一次（normalized alias longest-match + latin 词边界 + 歧义降置信），结果作为 `resolved_entities` 返回并显式传入 graph 通道，不再有独立的 entity 召回通道。
- Entity mention / entity fact / bounded path / memory relation / graph conflict 都是 graph 通道内部信号，只在 graph 内部聚合成单一 `graph_rank`。
- 删除全局 `entity_rank`、`relation_rank` 及其独立 RRF 贡献；搜索路径删除 `entityRecall`、`entityKeywordRecall`、`relationRecall`、`relationKeywordRecall`。
- `matched_channels` 枚举收敛为 `keyword | semantic | graph`；`selection_reason` 收敛为 `keyword_match | semantic_match | graph_match | channel_fusion`。

### 2. 严格的 graph seed 集

graph 的 memory relation 扩展 seed 只能来自：

- resolved entity 的 direct mention revisions（0-hop 结果）；
- keyword 通道原始候选；
- semantic 通道原始候选。

relation-expanded 与 graph-expanded 候选禁止进入 seed 集，杜绝隐式多跳与自我强化。

### 3. Graph 内部评分（graph-score-v2）

| Signal | 基础权重 |
|---|---:|
| direct_entity_mention | 1.0 × entity_confidence |
| exact_predicate_fact | 0.9 × fact_confidence × entity_confidence |
| one_hop_fact | 0.6 × fact_confidence × entity_confidence |
| memory_relation | 0.4 × confidence |
| graph_conflict | 0.4 × confidence |
| two_hop_path | 0.5 × ∏(edge confidences) |

- 证据去重：mention 按 entity、fact 按 fact_id、relation 按 relation_id、path 按 (start, edge fact_id 序列) 去重。
- 家族封顶：每族取最强证据，次要证据按小系数（mention 0.1×（最多 2 个额外实体）、fact 族间 0.15、relation 族间 0.1）合并；总分封顶 1.2；score ≤ 0 的候选不产生 graph_rank。
- 权威断言未提供 confidence 时使用中性先验 0.5，保证人工确认但未评分的 relation/fact 仍可召回。
- fact confidence 取解析实体所在一侧（subject 或 object）的 resolution confidence。

### 4. 两跳路径完整化

- 只有存在 predicate intent 时执行；两跳均受限（每 (subject, predicate) 分组 fan-out ≤ 20，邻居总数 ≤ 100，每 query path ≤ 100）。
- 两跳均应用 fact valid-time 与 projection eligibility（requested states、memory types、validity window、current revision）在 LIMIT 前生效。
- 禁止自环与回到起点的环；`GraphPath` 携带完整 `edges`（fact_id、predicate、confidence、source memory/revision），`hop == len(edges)`。

### 5. 排名与版本

```text
base_score  = Σ 1/(60 + channel_rank)   （channel ∈ {keyword, semantic, graph}）
final_score = base_score + 0.001 × bounded_usage_score
```

- `ranking_version` 升级为 `rrf-v3`；排序 tie-break：final_score、rrf_score、best channel rank（含 graph）、memory_id、revision_id。
- Feedback aggregate v1 严格按 ADR 0027 原始定义执行：仅统计 90 天窗口内每个 (revision, caller) 最新一条 `useful`(+1)/`irrelevant`(-1)；`correct/contest/supplement` 不参与排名。实现与文档此前不一致处按文档修正。

### 6. API：channels 取代 mode

- 唯一检索入口 `POST /v1/recall`；删除 `POST /v1/search` 与 `mode` 字段。
- 请求使用显式 `channels` 数组（canonical 顺序 keyword → semantic → graph），默认 `[keyword, semantic, graph]`。
- 响应使用 `requested_channels` / `executed_channels` 取代 `requested_mode` / `executed_mode`；删除 `index_state`、`entity_index_version`、`relation_index_version`，新增必填 `graph_index_state`。
- 降级语义：不可用通道从 executed 集合剔除并报告原因（`keyword_unavailable`、`semantic_unavailable` 等）；executed 集合为空时返回 503。
- Feedback search context 的 `matched_channels` 接受同一三通道枚举（补齐此前遗漏的 `graph`）。

### 7. 其他

- Feedback 融合阶段批量加载 aggregates，消除每候选 N+1 查询。
- Cursor（v4）绑定 requested/executed channels、ranking version、FTS/semantic version、corpus/feedback generation 与 entity/relation/graph index version+generation；任意绑定变化使旧 cursor 失效。

## 后续工作（P1/P2）

- 每个改变 graph membership/rank 的写事务原子递增统一 graph generation，替代多个 metadata 的分别绑定。
- Predicate registry 增加 cardinality/evolution policy；中文 predicate/path intent 模板。
- Graph-aware 评测语料、三路/七组消融与 graph 通道规模基准。

## 后果

- 同一权威证据只计分一次，消除 entity/relation 与 graph 的重复加权与 seed 污染。
- 排名公式、通道模型与文档、OpenAPI、feedback 契约重新一致。
- 依赖 `matched_channels` 中 `entity`/`relation` 标签的调用方需迁移到 `graph_signals` 解释字段（项目未发布，直接切换）。
