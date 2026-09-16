# ADR 0027：Feedback aggregate v1 与 ranking v2

- 状态：superseded（被 [0035](0035-memory-retrieval-v4.md) 取代：ranking v2 与通道枚举由 v4 确定性特征 ranker 和显式 feedback purpose 取代；feedback aggregate 单一共享定义继续有效）
- 日期：2026-08-25
- 关联：被 [0035](0035-memory-retrieval-v4.md) 取代

## 决策

Feedback event 是追加式权威对象，type 固定为 `useful`、`irrelevant`、`correct`、`supplement` 或 `contest`，reason code 固定为 `answered_query`、`relevant_context`、`not_relevant`、`fact_incorrect`、`fact_incomplete`、`fact_outdated`、`conflicts_with_evidence`、`duplicate` 或 `other`。Event 固定绑定观察时 revision 和 caller；旧 revision feedback 不继承。UDS HTTP 提供 feedback write/list；`correct`、`supplement` 和 `contest` 可另建 evolution proposal，但 event 与 proposal 使用独立 ID 和结果，event 成功后不因 proposal 失败回滚。

Feedback aggregate algorithm v1 只使用过去 90 天的 `useful` 和 `irrelevant`。每个 `(caller_id, revision_id)` 只取窗口内最新一条这两类 event，`useful=1`、`irrelevant=-1`；相同时间按 `feedback_id` 最大者取值。`bounded_usage_score = clamp(sum, -8, 8) / 8`，范围为 `[-1,1]`。Aggregate 按 revision 保存 algorithm version、窗口、source watermark 和 generation，可完全从 event 重建；旧 generation 不得覆盖新结果。

Ranking v2 先对请求的独立召回通道执行 RRF，沿用 ADR 0020 的 `k=60`，再计算 `final_score = rrf_score + 0.001 * bounded_usage_score`。排序依次使用 final score 降序、RRF score 降序、最佳 channel rank 升序、memory id 和 revision id 升序。Feedback 不改变召回集合、scope、current revision、lifecycle、valid-time 或 budget eligibility。

Search 以唯一、有序规范化的 `channels` 数组表示请求通道，enum 固定为 `keyword`、`semantic`、`entity` 和 `relation`。现有 `mode=keyword|semantic|hybrid` 保留：未提供 channels 时分别映射为 `[keyword]`、`[semantic]`、`[keyword,semantic]`；两者同时提供时必须等价，否则返回 `invalid_request`。响应增加 requested/executed/degraded channels，并保留现有 mode 字段；包含 entity/relation 的多通道组合在旧 mode 字段中记为 `hybrid`。Cursor 绑定规范化 channels、corpus generation、ranking v2、feedback aggregate version 和所有请求通道 index version，任一变化返回 `invalid_request`。
