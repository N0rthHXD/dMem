# dMeM 记忆生命周期

## 1. 状态模型

### 1.1 内容状态

```text
candidate
active
contested
archived
expired
retracted
deleted
```

### 1.2 正交标记

```text
frozen
pending_review
sensitive
```

状态描述：

| 状态 | 默认检索 | 可自动修改 | 说明 |
| --- | ---: | ---: | --- |
| candidate | 否 | 是 | 提取结果待确认 |
| active | 是 | 受策略限制 | 当前有效 |
| contested | 谨慎或否 | 否 | 有冲突或不确定 |
| archived | 否 | 否 | 冷存档，可恢复 |
| expired | 否 | 否 | 已超出有效时间 |
| retracted | 否 | 否 | 明确撤回 |
| deleted | 否 | 否 | 当前服务中删除 |

## 2. 状态转换

```text
candidate → active
candidate → contested
candidate → retracted
active → contested
active → archived
active → expired
active → retracted
archived → active
archived → expired
contested → active
任意非 deleted 状态 → deleted
任意状态 + frozen
```

状态转换必须产生审计事件。除物理清除外，状态转换不应破坏版本和证据链。

第一阶段动作规则：

| 动作 | 允许来源 | 目标/效果 |
| --- | --- | --- |
| confirm | candidate, contested | active |
| contest | candidate, active | contested |
| archive | active, contested | archived |
| restore | archived, expired, retracted, deleted | 创建恢复 revision 并设 active |
| freeze | 任意非 deleted | frozen=true |
| unfreeze | 任意非 deleted | frozen=false |
| retract | active, contested, archived | retracted |
| delete | 任意非 deleted | deleted |

所有动作携带 `expected_revision_id`，即使只修改状态也用于阻止与 revision 更新竞态。使用新幂等键请求已经处于目标状态的动作时，返回当前记忆作为成功 no-op，不追加状态事件；不允许的来源状态返回 `409 invalid_state_transition`。

创建记忆写首个状态事件。普通 revise 不改变状态，因此不写状态事件。restore 同时创建 revision 和状态事件。

## 3. 状态规则

### 3.1 Candidate

模型或导入流程生成但尚未确认的记忆。默认不注入 Agent 上下文，可通过管理查询查看。

### 3.2 Active

允许参与默认检索的当前记忆。第一阶段一个逻辑记忆只有一个 current revision；冲突事实使用独立逻辑记忆表达。

### 3.3 Contested

存在相互矛盾的证据、来源或版本。默认检索可以返回，但必须带冲突标记；高风险调用方可以要求排除 contested 记忆。

### 3.4 Archived

不再作为默认上下文使用的冷数据。显式历史查询或恢复操作可以读取。

### 3.5 Expired

业务有效时间结束。过期不是物理删除；是否保留原始事件和历史版本由保留策略决定。

### 3.6 Retracted

用户、来源或管理操作明确否认或撤回。默认永不返回；证据和撤回原因按隐私策略保留。

### 3.7 Deleted

当前服务不再提供该对象。物理清除由删除策略和后台清理任务完成。

## 4. 冻结规则

冻结是正交标记，不是独立内容状态。冻结后：

- 自动提取不能修改或替代该记忆。
- 可以创建关联的冲突候选。
- 人工明确操作可以创建新版本或解除冻结。
- 查询结果应标记 frozen。

## 5. 版本与恢复

恢复历史版本时：

```text
选择历史版本
  → 创建新的恢复版本
  → 当前版本指向恢复版本
  → 原历史链保持不变
```

恢复版本的 `parent_revision_id` 指向操作前的 current revision，`restored_from_revision_id` 指向用户选择的历史 revision。内容和有效时间复制自被选择版本。恢复不是数据库回滚，也不能撤销无关记忆的同期变化。

## 6. 删除层级

完整架构中的删除分为：

```text
逻辑删除：立即排除默认检索
派生清理：删除索引和缓存
原始清理：删除原始内容
任务清理：取消等待中的处理
```

第一阶段只实现单条记忆逻辑删除和同事务 FTS 清理，不实现原始事件 purge、附件清理和备份清理。

## 7. 生命周期任务

后续后台任务可以负责：

- 过期扫描。
- 低频记忆归档。
- 派生数据清理。
- 删除任务重试。
- 索引和缓存一致性检查。
- 冻结和保留策略执行。

这些自动生命周期任务不属于第一阶段。第一阶段生命周期变化仅由显式 API 触发。
