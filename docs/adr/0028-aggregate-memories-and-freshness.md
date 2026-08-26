# ADR 0028：普通 Aggregate memory 与 freshness descriptor

- 状态：accepted
- 日期：2026-08-25

## 决策

审核后的 scene/topic aggregate 是普通 logical memory 和不可变 revision，不建立第二套内容 authority。它有一个一对一 aggregate descriptor，kind 固定为 `scene` 或 `topic`。Descriptor 保存 policy/processing version 和 sensitivity；每个直接输入 revision 由权威 `derived_from` relation 记录，缺少完整 provenance 或 sensitivity 低于任一输入时不能 confirm。

Aggregate proposal 是独立 resource，保存有界且排序规范化的 input revision IDs、input fingerprint、内容和 provider identity。Create/list/get/review、aggregate get/list 和 drill-down 是 UDS HTTP 公共 API。Provider 在事务外运行；confirm 原子创建普通 memory/revision、descriptor、全部 provenance、review、audit 和 index jobs。Aggregate 不能覆盖、删除或替代输入 memory，且 frozen 输入不会被 aggregate review 修改。

Freshness 是可重建 descriptor projection，固定为 `fresh` 或 `stale`。任一输入 memory 的 current revision、state、space 或 sensitivity 改变时标记 stale 并排入去重、generation-fenced rebuild；rebuild 产生新的 pending proposal，不自动替换已确认 revision。Search 和 get 默认继续返回 stale aggregate，必须设置 `aggregate_stale=true` 并返回输入变化原因和 provenance refs；调用方可显式排除 stale aggregate。历史 aggregate revision 和 `derived_from` authority 不因 stale、缓存删除或 rebuild 而删除。
