# ADR 0023：稳定 Entity identity 与 revision-level mention

- 状态：accepted
- 日期：2026-08-25

## 决策

Entity 是空间内不随名称变化的权威 identity，不版本化；其 `entity_id` 永不因 alias 或 canonical name 纠正而复用。Entity type 固定为 `person`、`organization`、`place`、`project`、`product`、`event`、`concept` 或 `other`，state 固定为 `active` 或 `retired`。Canonical name、alias 和 state 的确认与纠正使用追加式事件；alias normalization 使用显式版本。第三阶段只持久化 merge proposal，不公开或自动执行 merge。

Entity mention 是独立权威 assertion，固定绑定 `(memory_id, revision_id)` 的 UTF-8 code point range，或绑定 `event_id` 的 UTF-8 code point range，二者必须且只能选择一个。Mention type 固定为 `explicit` 或 `implicit`；来源固定为 `processor` 或 `human_confirmation`。越界、跨 space、不可见来源和仅绑定可变 `memory_id` 的 mention 均拒绝。旧 revision mention 保留用于历史解释，但不能召回旧 revision 内容。

Entity、alias、mention proposal 的 create/list/get/review 和 entity query 是 UDS HTTP 公共 API。Confirm 在一个事务中写 entity/alias/mention、review、idempotency、audit 和 index job；任一目标 memory frozen 时返回 `revision_conflict` 且 details reason 为 `frozen`，proposal 保持 pending，只有显式 unfreeze 后才能重试。Reject 不修改目标。Sibling space 不自动消歧或合并。
