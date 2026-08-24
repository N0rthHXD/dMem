# dMeM 第二阶段文档

## 1. 阶段目标

第二阶段将第一阶段的“可靠保存、手工记忆和关键词检索”扩展为“可控自动处理、候选审核和语义混合检索”，并增加当前 Schema 下的备份和恢复验证能力。

阶段完成后，dMeM 应满足：

- 原始事件可以由显式配置的 processor 异步转换为可审核候选。
- processor 输出不能绕过证据、作用域、生命周期和 fencing 规则直接修改权威记忆。
- 调用方可以审核候选，并通过幂等操作确认或拒绝候选。
- 关键词和语义候选独立召回后取并集，精确术语不会因语义 top-k 被丢弃。
- processor 或语义索引不可用时，原始事件、手工记忆和关键词检索仍可用，并明确报告降级。
- 当前 Schema 数据库可以生成和验证一致性备份。

第二阶段仍是未发布的初始开发阶段，不实现第一阶段数据库迁移，不保留旧 API、旧 Schema 或旧内部实现的适配层。不兼容变更直接更新当前契约并重新初始化开发数据库。

## 2. 设计依据

本文档集依据：

- [第一阶段能力矩阵](../phase-1/scope.md)和当前 Go/SQLite 实现。
- [总体架构](../architecture/overview.md)中“阶段二：语义检索与自动处理”的边界。
- [记忆处理设计](../architecture/memory-processing.md)。
- [检索与上下文装配](../architecture/retrieval.md)。
- [异步任务处理](../operations/job-processing.md)。
- [备份、恢复与迁移](../storage/backup-and-migration.md)。
- 现有 ADR 中关于原始事件、不可变 revision、可重建索引、本地默认和非破坏性更新的约束。

## 3. 文档权威顺序

第二阶段发生描述冲突时，按以下顺序判断：

1. [第二阶段能力矩阵](scope.md)。
2. 第二阶段实施时更新后的 OpenAPI 和初始化 Schema。
3. 本目录中的专题设计和[测试矩阵](test-matrix.md)。
4. 现有领域主题文档和总体架构。
5. ADR。

当前仓库尚未包含第二阶段 OpenAPI 和初始化 Schema，因此本目录是下一阶段的设计与验收基线，不表示相关能力已经可用。

## 4. 文档索引

- [当前实现基线](current-baseline.md)：说明第二阶段从什么状态开始。
- [能力矩阵](scope.md)：定义 required、schema-only、unsupported 和 deferred 能力。
- [目标架构](architecture.md)：定义组件边界、数据流和共同不变量。
- [处理器与候选审核](processing-and-review.md)：定义 processor 端口、候选模型和审核事务。
- [语义与混合检索](semantic-retrieval.md)：定义 embedding、索引、候选并集、排序和降级。
- [备份与恢复](backup-and-restore.md)：定义当前 Schema 的一致性备份和恢复门槛。
- [测试矩阵](test-matrix.md)：定义 required 验收场景。
- [交付计划](delivery-plan.md)：定义决策门、垂直切片和退出条件。

## 5. 阶段入口决策

以下决策必须在对应切片编码前形成 ADR：

| 决策 | 最迟时间 | 必须回答的问题 |
| --- | --- | --- |
| Processor 部署形态 | 切片 B 前 | 进程内、子进程或远程 HTTP；凭据和取消如何处理 |
| 候选持久化模型 | 切片 B 前 | 独立 candidate 对象还是 memory candidate 状态；拒绝历史如何保留 |
| 向量索引实现 | 切片 D 前 | SQLite 白名单扩展或 Go 内嵌实现；构建、许可证和重建成本 |
| 混合排序版本 | 切片 E 前 | 分数归一化、权重、cursor 版本和解释字段 |
| 备份载体和命令面 | 切片 F 前 | 本地目录格式、CLI/API 边界和原子恢复流程 |

具体供应商、模型或向量扩展不在本文档中预选。选型不得改变 [scope.md](scope.md) 中的隐私、可重建、降级和验收要求。
