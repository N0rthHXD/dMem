# dMeM 第二阶段能力矩阵

## 1. 权威范围

本文档是第二阶段实现和验收范围的权威来源。状态定义：

```text
required       第二阶段必须实现并通过 required 测试
schema-only    保留稳定数据对象或字段，不提供完整业务能力
unsupported    API 可识别请求，但明确返回 unsupported
deferred       第二阶段不提供接口和实现
```

## 2. 阶段原则

- 以第一阶段现有数据库和 API 行为为兼容基线，不重写可靠接入与任务基础设施。
- 默认离线、默认 no-op；自动处理和数据外发必须显式配置。
- 自动结果先成为可审核候选，不默认改写 active 记忆。
- 语义索引是可重建派生数据，SQLite 继续保存权威记忆、版本和处理状态。
- 关键词和语义召回独立执行后取并集，不以语义 top-k 过滤关键词命中。
- 任一外部依赖失败不能导致原始事件丢失，也不能阻断手工记忆和关键词检索。

## 3. 能力矩阵

| 能力 | 状态 | 第二阶段契约 |
| --- | --- | --- |
| 第一阶段 API 与数据兼容 | required | 已有 v1 行为保持兼容；破坏性变化必须版本化 |
| OpenAPI 自动合约校验 | required | 所有公开 request/response、错误和 header 在测试中校验 |
| Schema migration | required | 支持从仓库发布的 Schema v1 前向升级，失败不留下半迁移数据库 |
| 一致性备份 | required | 使用 SQLite 一致性快照并生成版本、校验值和任务清单 |
| 恢复与验证 | required | 在 staging 位置恢复、检查后切换；索引可重建 |
| Processor 抽象端口 | required | 强类型输入输出、deadline、取消、identity、version 和错误分类 |
| 生产 processor provider | required | 至少一个经过 ADR 准入的实现；默认关闭且可完全禁用联网 |
| 远程数据外发策略 | required | 使用远程 provider 时按空间和数据类型 allowlist，最小化内容并审计 |
| 原始事件处理状态 | required | `stored/processing/processed/failed` 与 job 最终提交一致 |
| 候选持久化 | required | 候选、证据、confidence、provider 和 processing version 可查询和审计 |
| 候选审核 | required | list/get/confirm/reject；写操作幂等且保留审核历史 |
| 候选自动激活 | deferred | 第二阶段默认必须人工确认，processor 不能直接创建 active 记忆 |
| 自动修订已有记忆 | deferred | 可保存重复/补充/冲突建议，但不自动创建新 active revision |
| Processing version 重放 | required | 同一事件可按新版本重处理，不覆盖旧结果且不重复创建候选 |
| Embedding provider 端口 | required | 记录 provider/model、维度和内容哈希；支持批量、超时和取消 |
| 语义索引 | required | 只索引当前可检索 revision，记录 revision 和 index version，可全量重建 |
| Semantic 检索 | required | 受空间、状态、类型和有效时间过滤，并回查当前权威 revision |
| Hybrid 检索 | required | FTS 与 semantic 候选并集、确定性融合、稳定分页和选择原因 |
| Keyword 检索 | required | 保持第一阶段行为；语义不可用时可独立工作 |
| 语义降级 | required | health 和响应标明状态；hybrid 可明确降级为 keyword |
| Token 预算 | unsupported | 本阶段仍以 `limit` 和 `max_chars` 为硬限制 |
| 实体、关系和图检索 | deferred | 保留到第三阶段，不为混合检索引入关系权威模型 |
| 冲突自动裁决 | deferred | 候选可以标记建议类型，但不得自主覆盖或删除历史 |
| 托管二进制附件 | deferred | processor 只接收允许的文本、JSON 或外部引用元数据 |
| 逻辑导入导出 | deferred | 本阶段只实现完整备份和恢复，不定义跨实例便携格式 |
| 原始内容物理清除 | deferred | 不在本阶段引入 purge 和级联删除 |
| 多用户 ACL | deferred | 继续采用同一 Linux 用户可信模型 |
| 网络监听 API | deferred | daemon 继续只监听 UDS；远程 provider 是出站依赖 |
| 遥测上传 | deferred | 不引入产品遥测；provider 自带遥测必须可关闭 |

## 4. Required 端到端场景

### 4.1 自动处理

```text
ingest raw event
  → durable extract job
  → 获取受限输入并执行 policy
  → processor 在事务外运行
  → 校验强类型输出
  → 当前 generation 原子写入 candidate、evidence、audit 和 job result
  → raw event processing_status=processed
```

`no_memory` 是成功结果并将事件标为 `processed`；`no_processor` 保持第一阶段兼容行为并将事件保留为 `stored`；非法输出不得伪装成 `no_memory`。

### 4.2 候选审核

```text
candidate
  → 人工 confirm
  → 创建 active memory、首个 revision、evidence 和 state event
  → 更新 projection、FTS 并排入 embedding job
```

reject 只改变候选审核状态并保留记录。第二阶段不允许 confirm 操作隐式修订已有 memory；重复或冲突建议由调用方选择显式的现有手工 revise/lifecycle API。

### 4.3 混合检索

```text
query
  → 解析作用域和结构化过滤
  → FTS 候选
  → semantic 候选
  → memory_id/revision_id 并集与去重
  → 版本化融合排序
  → SQLite 当前 revision 和生命周期复核
  → limit/max_chars 装配
```

hybrid 请求在语义索引不可用时允许返回关键词结果，但必须设置 `degraded=true`、明确降级原因和实际执行模式。纯 semantic 请求在没有语义能力时返回稳定的 `index_degraded`，不能伪装为 semantic 结果。

## 5. 非目标

- 不自动训练、下载或更新模型。
- 不让 provider 直接写 SQLite 或管理任务状态。
- 不让 confidence 单独触发 active、revise、retract 或 delete。
- 不在第二阶段解决实体关系、长期画像、反馈学习和多模态。
- 不承诺分布式 worker、外部消息队列或多实例写入。
- 不引入通用插件 ABI 或运行时加载任意动态库。

## 6. 完成标准

- [测试矩阵](test-matrix.md)中所有 `required` 场景通过。
- 第一阶段 required 测试继续通过，兼容性变化已在 OpenAPI 和 migration 中明确记录。
- 现有 Schema v1 数据库可升级；故障注入证明迁移失败可安全恢复。
- provider 默认关闭，未配置时第一阶段 no-op 行为不变。
- processor 输出无法绕过校验、作用域、evidence、幂等和 fencing 写入权威数据。
- candidate confirm/reject 在超时和重试后不重复创建 memory、revision 或审核事件。
- keyword、semantic 和 hybrid 行为可由 OpenAPI 区分；所有降级均在响应和 health 中可见。
- 语义索引可从当前 projection 全量重建，旧 revision 或损坏索引不会作为权威结果返回。
- 远程 provider 测试证明默认无外发、allowlist 生效、日志不包含完整内容或凭据。
- 至少完成一次自动化备份恢复演练，并在恢复库上通过完整性、引用和索引重建检查。

## 7. 数据兼容承诺

从第二阶段开始，仓库发布的数据库不再默认视为可丢弃。Schema 变化必须通过已提交、可重复测试的 migration 前向升级。降级到不支持新 Schema 的旧 daemon 不属于承诺范围，旧 daemon 必须拒绝打开新数据库而不是尝试修改。
