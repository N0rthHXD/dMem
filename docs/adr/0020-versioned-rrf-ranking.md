# ADR 0020：固定版本 RRF 混合排序与 cursor

- 状态：accepted
- 日期：2026-08-21

## 决策

Hybrid ranking v1 使用 reciprocal rank fusion：每个独立召回通道按稳定顺序排名，候选并集的分数为 `1/(60+keyword_rank) + 1/(60+semantic_rank)`，缺失通道贡献为零。排序依次使用融合分数降序、最佳通道 rank 升序、memory id 和 revision id 升序。

Keyword 和 semantic 必须独立 bounded recall 后取并集，禁止 semantic top-k 过滤 keyword 结果。响应保留 matched channels、各通道 relevance、选择原因、ranking version 和索引版本。

Cursor v2 绑定 query hash、requested/executed mode、ranking version、FTS version、semantic active version、reference time、limit、max chars 和最后 sort key。任一绑定值变化返回 `invalid_request`，不静默续页。
