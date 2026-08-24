# dMeM 第三阶段当前实现基线

## 1. 当前完成基线

第三阶段计划范围已实现。本文记录完成后的 Go、SQLite、OpenAPI 和测试基线，而不是阶段启动前状态。

| 领域 | 当前实现 |
| --- | --- |
| 运行 | Linux 单用户 daemon、UDS HTTP、单实例锁、优雅关闭和真实 OpenAPI 合约测试 |
| 权威数据 | 原始事件、不可变 memory revision、evidence、追加式 state event、当前 projection 和 audit |
| 异步任务 | durable job、attempt、lease、heartbeat、generation fencing、重试、取消和重启恢复 |
| 自动处理 | 强类型 processing input/result、no-op 默认、OpenAI-compatible provider、外发 allowlist 和版本重放 |
| 候选审核 | 独立 candidate、evidence、related-memory suggestion、list/get/confirm/reject、终态竞争和幂等 |
| 检索 | keyword、semantic、hybrid、RRF v1、通道并集、权威复核、稳定 cursor 和明确降级 |
| 派生索引 | 同事务 FTS 投影、embedding metadata、Go 向量索引、shadow rebuild 和版本切换恢复 |
| 数据维护 | SQLite 一致性备份、manifest/checksum、离线 staging restore、索引重建和隔离探测 |
| 记忆演进 | 独立 evolution proposal、expected revision、frozen/fencing 校验、补充/替代和原子审核 |
| 实体与关系 | 权威 entity/alias/mention、revision-bound relation、一跳查询、独立派生索引和重建 API |
| 冲突 | contradicts assertion、conflict case、contested 检索解释和显式人工 resolution |
| 反馈与聚合 | revision-bound feedback、bounded ranking v2、可重建反馈聚合、scene/topic proposal、provenance 和 freshness |
| 逻辑便携 | format v1、确定性 snapshot export、严格 package validation、dry-run mapping、批次 checkpoint、幂等 import 和冲突 proposal |

第三阶段复用了 provider、candidate、job、revision、projection、混合检索和备份恢复基础设施，并在其上增加上述对象。

## 2. 可直接复用的扩展点

- `raw_events` 和 `memory_evidence` 继续作为所有自动结论的最终来源。
- `memory_revisions`、`memory_state_events` 和 `expected_revision_id` 承载已有记忆演进。
- 独立 candidate 的不可变 specification、终态 review、幂等和审核历史可扩展为演进 proposal 模式。
- durable job 和 generation fencing 承载实体提取、关系检测、反馈聚合、场景聚合和索引重建。
- `memory_projections` 是实体、关系和聚合检索共同的当前 memory 源。
- 当前 RetrievalService 的独立通道、候选并集、RRF 和权威复核可加入 entity/relation 通道。
- backup/restore 的 staging validation hook 可加入新增权威对象与索引检查。
- OpenAPI 合约测试和 Schema 文档一致性测试继续作为公开契约门槛。

## 3. 当前产品边界

- Entity merge 仅保存 proposal，`POST /v1/entities/{entity_id}/merge` 明确返回 unsupported；不迁移 identity 或 relation。
- 不提供多跳图语言、自动冲突裁决、自动撤回/删除、自治长期画像、在线训练或跨主机同步。
- Logical format v1 已在 `internal/portability` 实现和测试，但未接入 daemon router、OpenAPI 或 `dmemd` 子命令。它是供同进程/离线工具集成的 Go library，不应被描述成现成用户命令。
- `dmemd` 的离线子命令只有 `init`、`backup`、`restore`；重建通过已声明的 UDS HTTP 路径和 durable jobs 执行。
- SQLite Schema migration 仍未实现；逻辑 format version 与 SQLite schema version 独立。

## 4. 已落实的先修

阶段实现已收紧以下边界：

- Provider adapter 保留 bounded context memories、typed evidence、sensitivity 和处理 identity。
- Corpus generation 随已提交 projection 变化推进，cursor 绑定 corpus、ranking、feedback aggregate 和索引版本。
- 新 job type 使用 durable lease/generation fencing；restore 同时 fence running jobs 和 running imports。
- Backup manifest 记录新增权威表计数和 running import 数；restore 验证 mapping/checkpoint/provenance 不变量。
- Health 从 entity/relation/feedback/aggregate metadata 和实际队列状态读取。

这些先修均以最小契约修正落实，未引入旧 API、旧 Schema 或旧 provider 行为适配层。

## 5. 保持不变的不变量

- 原始事件先可靠提交，自动处理后执行。
- Provider 不直接访问 repository，外部调用不持有 SQLite 写事务。
- 只有当前 lease owner 和 generation 可以提交异步结果。
- Revision、state event、review、relation assertion 和 feedback event 采用追加或版本化语义。
- 模型、confidence 或反馈分数不能自主覆盖、撤回、归档或删除权威记忆。
- Frozen memory 不能被自动修订。
- 作用域过滤先于相关性、关系路径或反馈排序。
- 派生索引和反馈聚合可以从 SQLite 权威对象重建。
- 所有召回结果返回前复核当前 revision、生命周期、有效时间和空间。
- 默认离线；未显式配置 provider 时不联网。
- 第三阶段仍不实现 SQLite Schema migration。
