# dMeM 第三阶段实体、关系与冲突演进

## 1. 目标

本专题将“模型认为内容相关、重复、补充、替代或冲突”从弱 related-memory suggestion 扩展为可审核、可追溯的权威演进操作。任何自动判断默认停留在 proposal，不直接改写 active memory。

## 2. Proposal 分类

| Action | 审核后行为 |
| --- | --- |
| `new` | 沿用第二阶段语义，创建新 active memory |
| `duplicate` | 不创建 revision；可确认 `related_to` 或 `supports` relation |
| `supplement` | 对目标 memory 创建新 revision，保留 parent 和 evidence |
| `replacement` | 创建新 revision 或新 memory，并显式创建 `supersedes` relation |
| `conflict` | 保留双方，创建 `contradicts` relation 和 conflict case |
| `entity_mention` | 创建或绑定 entity，并确认具体来源 mention |
| `relation` | 创建指定类型的 revision-level relation assertion |

Proposal specification 创建后不可变。同一 processing version、target revision、action 和规范哈希不能重复创建。

## 3. Entity 规则

- Entity 至少有 stable ID、space、type、canonical name、sensitivity、state 和创建来源。
- Entity type 使用固定枚举；未知类型作为 proposal validation error，不动态扩展 ontology。
- Alias 的规范化算法有独立版本；规范化相等只能产生绑定或 merge proposal，不能自主合并。
- Mention 引用具体 revision 或 raw event，并保存 UTF-8 code point range；越界、跨作用域或不存在来源时拒绝。
- Entity 默认只在其空间和显式祖先规则内可见，不跨 sibling space 自动消歧。
- Entity 删除或 merge 不在第三阶段公开能力内；状态纠正通过显式审核和历史事件表达。

## 4. Relation 规则

- Relation 是独立权威对象，不是 candidate related ID，也不是普通文本 memory。
- Source/target 均绑定具体 revision；逻辑 memory ID 作为便捷引用但不能替代 revision identity。
- `supports`、`contradicts`、`supersedes`、`derived_from` 有方向；`related_to` 可按规范化端点顺序保存；`mentions` 连接 memory revision 与 entity。
- Relation 必须有证据或明确 `human_confirmation` 来源。
- 禁止 self relation，除非类型契约明确允许；第三阶段固定类型均不允许同 revision 自关联。
- Relation endpoint 必须在调用方显式作用域内。跨 sibling relation 默认拒绝，而不是创建后依赖查询过滤。
- 重复 relation confirm 返回既有 assertion；相反方向或不同 revision 是不同 specification。

## 5. Conflict case

Conflict case 至少保存：

```text
case_id
space_id
state: open | resolved | dismissed
relation_ids
participant memory/revision ids
opened_by and processing_version
resolution_type and resolution refs
created_at, resolved_at
```

Conflict case 事件追加保存，不原地覆盖解决历史。一个 relation 可以加入一个未关闭 case；重复检测应返回该 case。

## 6. Review 事务

### 6.1 Supplement

```text
validate pending proposal
  → check expected current revision and frozen=false
  → insert immutable child revision and evidence
  → update current pointer and projection
  → create relation/entity assertions selected by reviewer
  → enqueue embedding/entity/relation/aggregate invalidation jobs
  → confirm proposal, append review/idempotency/audit
```

### 6.2 Replacement

Replacement 必须明确选择：

- 同一 logical memory 新 revision；或
- 新 logical memory，并将旧 memory/revision 与新 revision 通过 `supersedes` 连接。

系统不能从文本相似度隐式选择。若旧 memory 需要转为 `superseded`，状态变化与 relation、新 revision 在同一事务完成。

### 6.3 Conflict

Confirm 原子创建 `contradicts` relation、conflict case 和必要的 contested state events。若 reviewer 选择 keep-both，双方保持可检索但结果带 conflict refs；若只创建 assertion 而不改变状态，API 必须显式记录该策略。

## 7. Resolution

支持以下显式解决类型：

| 类型 | 行为 |
| --- | --- |
| `keep_both` | 关闭 case，保留 relation 和双方当前状态；时间或作用域说明并存原因 |
| `dismiss` | 将 conflict assertion 标为 dismissed，保留检测与审核历史 |
| `revise_one` | 对一个目标创建新 revision，并重新评估 contested 状态 |
| `supersede` | 创建 supersedes relation，并显式改变旧 memory 状态 |
| `reconcile` | 创建有完整 evidence 的新 memory/revision，并关联双方 |

Resolution 请求携带所有参与 memory 的 expected revision。任一目标变化、被冻结或已关闭时返回稳定 conflict，不做部分解决。

## 8. 检索语义

- Entity 和 relation 通道只返回当前可检索 memory revision。
- Relation 命中旧 endpoint revision 时可用于历史解释，但不能直接将旧文本作为当前结果。
- Contested 结果默认可返回并附 conflict refs；调用方可显式排除 contested。
- 关系路径第三阶段只支持一跳；不得在 handler 内循环模拟无界多跳。
- 所有 selection reason 标明命中 entity/relation、对应 assertion ID 和降级状态。

## 9. 禁止行为

- Processor 直接创建、更新或删除 entity/relation 权威表。
- Confidence 阈值自动 confirm proposal。
- 将 context memory 自动作为新 revision 的 evidence。
- 自动修改 frozen memory。
- 用 entity alias 相等替代人工 merge。
- 用 contested flag 反推缺失的 conflict relation 或 case。
- Conflict resolution 修改历史 revision 或删除被否决 assertion。
