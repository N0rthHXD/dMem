# dMeM 第二阶段交付计划

## 1. 交付策略

按可独立验证的垂直切片交付，不同时铺开 provider、候选、向量和恢复实现。每个切片完成后保持 daemon 可运行，未启用能力默认关闭。

## 2. 切片 A：第一阶段封板与数据演进基础

交付：

- 修正文档和当前实现的契约差异。
- 引入真实 OpenAPI validation 测试。
- 明确 processor、FTS 和 semantic health 状态模型。
- ADR：Schema migration 策略、候选模型、processor 形态。
- migration runner 和真实 Schema v1 fixture。
- 一致性 backup 基础及 manifest。

退出条件：

- 第一阶段测试全部通过。
- Schema v1 fixture 可升级且故障注入不产生半迁移。
- 默认启动不联网，no-op 行为不变。

## 3. 切片 B：Processor 端口与处理状态

交付：

- 强类型 ProcessorInput、ProcessorOutcome 和 CandidateDraft。
- ProcessingService，分离准备、外部执行和 fenced commit。
- raw event processing status 转换。
- provider identity、processing version、usage 和错误分类。
- 确定性 fake processor 和 contract tests。

退出条件：

- `no_memory`、candidate、transient、permanent、invalid、cancel 和 lease lost 场景通过。
- provider 无法直接访问 repository。
- 外部调用期间没有 SQLite 长写事务。

## 4. 切片 C：候选持久化与人工审核

交付：

- candidate、evidence 和 review Schema/migration。
- candidate list/get/confirm/reject API 及 OpenAPI。
- confirm 原子创建 memory、revision、state、projection 和 FTS。
- processing replay 去重和审核 audit。

退出条件：

- 并发 confirm/reject 只有一个终态。
- 幂等重试不重复创建 memory 或 revision。
- candidate 不进入默认检索。

## 5. 切片 D：生产 Provider 与数据外发

交付：

- 至少一个 ADR 批准的生产 provider。
- 显式配置、secret reference、allowlist 和输入最小化。
- timeout、cancel、并发和速率限制。
- processor 动态 health 和安全日志/audit。

退出条件：

- 默认配置无网络请求。
- 未允许空间和数据类型不能调用 provider。
- 日志、错误和 metrics 不泄露内容或凭据。
- provider 不可用时 ingest、手工 memory 和 keyword search 正常。

## 6. 切片 E：Embedding 与 Semantic Search

前置 ADR：向量索引实现和 embedding provider 形态。

交付：

- EmbeddingProvider 与 VectorIndex 端口。
- embedding durable job、identity、dimension、content hash 和 index version。
- semantic index shadow build、验证、切换和 rebuild。
- semantic mode API、权威复核、health 和降级。

退出条件：

- 当前 revision 可语义检索，旧 revision 和跨空间数据不能返回。
- 索引删除后可完整重建。
- rebuild 失败时旧 active version 继续服务。

## 7. 切片 F：Hybrid Search

前置 ADR：融合排序和 cursor version。

交付：

- FTS 与 semantic 独立召回和候选并集。
- 版本化确定性融合、解释字段和稳定 cursor。
- 单通道降级和 executed mode。
- 固定评测语料、质量及性能基准。

退出条件：

- 精确关键词命中不因 semantic top-k 丢失。
- 空间污染和 stale revision 返回均为 0。
- 质量、延迟、内存和磁盘达到发布配置门槛。

## 8. 切片 G：恢复验证与发布

交付：

- staging restore、任务恢复和索引重建。
- 自动化 backup/restore drill。
- 从第一阶段到第二阶段的升级说明。
- 运维故障手册和最终 OpenAPI/Schema 文档。

退出条件：

- 恢复失败不修改当前 data directory。
- 恢复库通过 integrity、引用、幂等、任务 fencing、索引和空间隔离检查。
- [能力矩阵](scope.md)完成标准和[测试矩阵](test-matrix.md)全部 required 项通过。

## 9. 依赖关系

```text
切片 A
  → 切片 B
  → 切片 C
  → 切片 D

切片 A
  → 切片 E
  → 切片 F

切片 C + 切片 F
  → 切片 G
```

生产 provider 与 semantic 路线可在切片 A 后并行，但 Hybrid Search 不能在 semantic index 契约稳定前开始，最终发布不能在候选审核和恢复验证完成前进行。

## 10. 风险与控制

| 风险 | 控制 |
| --- | --- |
| Provider 输出幻觉或非法结构 | 强类型校验、evidence、候选审核、默认不自动激活 |
| 外部数据泄露 | 默认关闭、allowlist、最小化、secret reference、安全日志测试 |
| 晚到 worker 重复提交 | 沿用 lease generation fencing，并将结果与 job 原子提交 |
| 向量实现绑定或损坏 | 端口隔离、固定版本、派生数据、shadow rebuild 和 keyword fallback |
| Hybrid 排序不可解释 | 简单版本化融合、matched channels、固定离线语料 |
| Schema 升级损坏用户数据 | migration fixture、事务、迁移前备份和恢复演练 |
| 范围扩张到关系和自动演进 | 严格遵守 scope 中 deferred 项，第三阶段另行设计 |
