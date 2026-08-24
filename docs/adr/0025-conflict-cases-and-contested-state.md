# ADR 0025：Conflict case 与 contested projection 分工

- 状态：accepted
- 日期：2026-08-25

## 决策

冲突使用三个分离对象：pending `conflict` evolution proposal、确认后的 `contradicts` relation，以及需要人工处理的 `conflict_case`。Case state 固定为 `open`、`resolved` 或 `dismissed`；resolution type 固定为 `keep_both`、`dismiss`、`revise_one`、`supersede` 或 `reconcile`。Case、participant 和 resolution event 是追加式权威历史；一个 active `contradicts` relation 最多属于一个 open case。

`contested` 只是 memory 当前检索 projection，不代表 relation、case 或解决历史。Confirm conflict proposal 原子创建 relation 和 open case；参与 revision 仍为 current 时，将对应 memory 标为 contested，历史 revision participant 不改变当前 memory state。Case 关闭后，仅当该 memory 不再参与其他 open case 时才能通过同一 resolution 事务离开 contested；目标状态由 resolution 命令显式给出，不从 relation 推断。

Conflict proposal review、case get/list 和 resolution 是 UDS HTTP 公共 API。Confirm 和 resolution 必须携带全部 current participant 的 expected revision；任一 revision 改变或 participant frozen 时整个事务返回 `revision_conflict`，details reason 分别为 `revision_changed` 或 `frozen`，不写部分结果，proposal/case 保持原状态。Frozen 不由 review 自动清除，必须先显式 unfreeze。Search 默认返回 contested 结果并附 case、relation 和对端 revision refs；调用方可以显式排除，且不得把冲突双方合成为无来源结论。
