# dMeM 第二阶段测试矩阵

## 1. 测试约定

- `required` 是阶段发布门槛，`optional` 用于评估或环境相关测试。
- 所有第一阶段测试继续运行。
- Processor、EmbeddingProvider、VectorIndex 和 clock/ID 均可注入确定性 fake。
- Provider contract test 不访问真实外部服务；真实 provider 使用显式凭据的独立集成环境。
- SQLite、FTS 和向量实现使用与发布相同的 build profile。
- HTTP request/response 使用真实 OpenAPI validator 校验。
- 故障注入覆盖 provider 返回后、权威事务提交前和提交后的边界。

## 2. 契约与兼容

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-COMPAT-001 | required | 运行第一阶段测试 | 全部通过 |
| P2-COMPAT-002 | required | OpenAPI request/response/error/header | 所有公开样例和端到端响应通过 Schema 校验 |
| P2-COMPAT-003 | required | 未配置 provider | no-op 行为与第一阶段一致，无网络请求 |
| P2-COMPAT-004 | required | 旧 semantic 请求 | 新契约按版本明确支持，不静默改变 keyword 语义 |

## 3. Processor

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-PROC-001 | required | `no_memory` | job succeeded，event processed，无 candidate |
| P2-PROC-002 | required | 合法候选 | candidate/evidence/audit/job result 原子提交 |
| P2-PROC-003 | required | 非法结构 | invalid_output，不创建候选，不伪装 no_memory |
| P2-PROC-004 | required | transient failure | 退避重试，attempt 和 event 状态正确 |
| P2-PROC-005 | required | permanent failure | job/event failed，不自动重试 |
| P2-PROC-006 | required | deadline/cancel | provider context 取消，未提交晚到结果 |
| P2-PROC-007 | required | lease 丢失 | 旧 generation 无法提交 candidate 或完成 job |
| P2-PROC-008 | required | provider 成功、事务失败 | 无部分 candidate/event/job 结果 |
| P2-PROC-009 | required | replay 同 processing version | 返回 active job 或既有结果，不重复候选 |
| P2-PROC-010 | required | replay 新 processing version | 新候选集建立，旧结果不变 |
| P2-PROC-011 | required | evidence 越界/跨作用域 | 输出被拒绝 |
| P2-PROC-012 | required | 输出超过策略预算 | policy_rejected 或 invalid_output，行为稳定 |

## 4. 数据外发

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-EGRESS-001 | required | 默认配置 | 不解析云凭据、不发现服务、不发网络请求 |
| P2-EGRESS-002 | required | space 不在 allowlist | provider 不被调用，记录 policy_rejected |
| P2-EGRESS-003 | required | content type 不允许 | provider 不被调用 |
| P2-EGRESS-004 | required | 输入最小化 | 只发送契约允许字段且满足字符/上下文上限 |
| P2-EGRESS-005 | required | 日志和错误 | 不含完整内容、Authorization、secret 和 provider 原始响应 |
| P2-EGRESS-006 | required | provider telemetry disabled | contract test 证明无额外遥测端点请求 |

## 5. 候选审核

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-CAND-001 | required | list/get | 按空间和状态稳定分页，返回 evidence 和处理身份 |
| P2-CAND-002 | required | confirm | active memory、revision、state、projection、FTS、audit 原子创建 |
| P2-CAND-003 | required | confirm 幂等重放 | 返回原 memory/revision，不重复创建 |
| P2-CAND-004 | required | 并发 confirm | 只有一个成功结果 |
| P2-CAND-005 | required | confirm 与 reject 竞争 | 只有一个终态，历史一致 |
| P2-CAND-006 | required | reject | 不创建 memory，候选和审核记录保留 |
| P2-CAND-007 | required | confirm 中途失败 | candidate 仍 pending，无孤儿 memory |
| P2-CAND-008 | required | candidate 默认检索 | memory list、FTS 和 semantic 均不返回 |
| P2-CAND-009 | required | frozen related memory | 不发生自动 revise 或 lifecycle 变化 |

## 6. Embedding 与索引

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-EMB-001 | required | active revision | 记录 memory/revision/content hash/model/dimension/version |
| P2-EMB-002 | required | revision 更新 | 旧向量不再作为当前结果 |
| P2-EMB-003 | required | archive/delete | semantic 默认查询立即排除 |
| P2-EMB-004 | required | provider 维度错误或 NaN | 索引不写入，任务稳定失败 |
| P2-EMB-005 | required | rebuild | shadow build 验证后原子切换 active version |
| P2-EMB-006 | required | rebuild 失败 | 旧 active version 继续服务 |
| P2-EMB-007 | required | 索引删除 | 可从 SQLite 权威 projection 完整重建 |
| P2-EMB-008 | required | rebuild 并发 revision | 切换后无错误 revision/content hash |

## 7. 检索

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-SEARCH-001 | required | 同义语义查询 | semantic 返回相关当前 revision |
| P2-SEARCH-002 | required | 精确标识符只命中 FTS | hybrid 保留关键词结果 |
| P2-SEARCH-003 | required | 只命中 semantic | hybrid 保留语义结果 |
| P2-SEARCH-004 | required | 双通道同一 revision | 去重并保留 matched channels |
| P2-SEARCH-005 | required | 同级项目 | keyword/semantic/hybrid 污染率均为 0 |
| P2-SEARCH-006 | required | stale/phantom vector | 权威复核后不返回 |
| P2-SEARCH-007 | required | semantic unavailable + hybrid | 返回 keyword 结果并明确 degraded/executed mode |
| P2-SEARCH-008 | required | semantic unavailable + semantic | 返回稳定 index_degraded |
| P2-SEARCH-009 | required | FTS unavailable + hybrid | 可返回 semantic 结果并明确 degraded |
| P2-SEARCH-010 | required | 两通道不可用 | 返回错误，不伪造结果 |
| P2-SEARCH-011 | required | stable cursor | 同 index/ranking version 无重漏 |
| P2-SEARCH-012 | required | cursor 后索引切换 | invalid_request，不跨版本续页 |
| P2-SEARCH-013 | required | limit + max_chars | 同时满足第一阶段硬限制 |

## 8. Migration、备份与恢复

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-DATA-001 | required | 真实 Schema v1 fixture 升级 | 权威数据和幂等响应保留 |
| P2-DATA-002 | required | migration 中途故障 | 回滚或可从迁移前备份恢复，无半版本 |
| P2-DATA-003 | required | 未知新 Schema | daemon 拒绝打开且不修改 |
| P2-DATA-004 | required | 活动数据库备份 | 快照通过 integrity 和 checksum 校验 |
| P2-DATA-005 | required | 磁盘不足/目标故障 | 不发布不完整备份 |
| P2-DATA-006 | required | staging restore | 验证完成前不替换当前 data directory |
| P2-DATA-007 | required | restore running jobs | 旧 attempt 关闭且 generation 被 fence |
| P2-DATA-008 | required | restore 后索引重建 | keyword/semantic 结果与备份时权威 projection 一致 |
| P2-DATA-009 | required | manifest/checksum 损坏 | restore 拒绝，原数据库不变 |
| P2-DATA-010 | required | 自动化恢复演练 | staging daemon health 达到预期且隔离测试通过 |

## 9. 质量与性能

| ID | 状态 | 场景 | 预期 |
| --- | --- | --- | --- |
| P2-QUAL-001 | required | 固定检索语料 | exact keyword recall 不低于第一阶段 |
| P2-QUAL-002 | required | 固定语义语料 | 达到发布配置中声明的 recall@k 和 nDCG/MRR 门槛 |
| P2-QUAL-003 | required | 无答案语料 | 无关召回率不超过声明门槛 |
| P2-QUAL-004 | required | 空间隔离语料 | 跨同级空间返回为 0 |
| P2-PERF-001 | required | 目标规模查询 | keyword/semantic/hybrid p95 达到发布门槛 |
| P2-PERF-002 | required | 目标规模 rebuild | 时间、内存、磁盘放大达到发布门槛 |
| P2-PERF-003 | optional | 真实远程 provider | 记录延迟、限流和恢复行为，不进入默认离线 CI |

## 10. CI 最小矩阵

- Linux amd64，最低支持 Go 版本和当前稳定 Go 版本。
- CGO 开启，固定 SQLite 和向量 build profile。
- unit、integration、race、OpenAPI validation、migration fixtures、restore drill 和 vulnerability scan。
- 默认 CI 禁止外网；所有 provider 使用本地 fake/server。
- 向量实现的许可证、固定版本和可重建测试是发布门槛。
