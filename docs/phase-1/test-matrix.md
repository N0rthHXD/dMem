# dMeM 第一阶段测试矩阵

## 1. 测试约定

- 使用可注入 UTC clock 和 ID generator。
- processor 使用 [处理器契约](processing-providers.md) 定义的确定性 fake。
- 每个存储测试使用独立临时 SQLite 数据库。
- SQLite 使用与发布相同的 CGO 驱动和 build profile。
- HTTP 测试通过临时 Unix Domain Socket。
- 请求和响应使用 [`openapi.yaml`](../api/openapi.yaml) 校验。

## 2. 运行与 Schema

| ID | 场景 | 预期 |
| --- | --- | --- |
| P1-RUN-001 | 新目录首次启动 | schema version=1，health ready |
| P1-RUN-002 | 同数据目录启动第二实例 | 第二实例失败，不替换有效 socket |
| P1-RUN-003 | schema 不兼容 | 不修改数据库，报告 incompatible |
| P1-RUN-004 | 完整性检查失败 | 不进入 ready |
| P1-RUN-005 | 优雅关闭 | 释放 socket 和锁，保留任务状态 |
| P1-RUN-006 | stale socket | 无活动实例时安全清理 |

## 3. 空间

| ID | 场景 | 预期 |
| --- | --- | --- |
| P1-SCOPE-001 | 创建 user 根 | 数据库只允许一个根 |
| P1-SCOPE-002 | 非法父子类型 | 422，事务无写入 |
| P1-SCOPE-003 | 重复 external ID | 幂等重放或冲突，不重复创建 |
| P1-SCOPE-004 | session + ancestors | 只包含 session、task/project、user |
| P1-SCOPE-005 | 同级项目 | P1 查询不返回 P2 记忆 |
| P1-SCOPE-006 | Agent 空间 | 项目查询不自动包含 agent 空间 |

## 4. 原始事件与幂等

| ID | 场景 | 预期 |
| --- | --- | --- |
| P1-INGEST-001 | 首次 ingest | 事件、内容、幂等、job、audit 原子提交 |
| P1-INGEST-002 | 相同 key/payload | 返回原 event/job，replay=true |
| P1-INGEST-003 | 相同 key/不同 payload | 409 idempotency_conflict |
| P1-INGEST-004 | 提交前故障 | 不存在部分记录 |
| P1-INGEST-005 | 提交后响应中断 | 重试返回原响应 |
| P1-INGEST-006 | 未知 JSON 字段 | 422 invalid_request |
| P1-INGEST-007 | binary content | unsupported_content_type |
| P1-INGEST-008 | reference content | 只保存引用，不读取目标 |
| P1-INGEST-009 | 稳定分页 | occurred_at DESC、event_id DESC，无重漏 |
| P1-INGEST-010 | event get/replay | get 返回原内容；终态后 replay 创建新 job |

## 5. 任务

| ID | 场景 | 预期 |
| --- | --- | --- |
| P1-JOB-001 | 两 worker claim | 只有一个获得 generation |
| P1-JOB-002 | 当前 worker 续租 | lease 延长，generation 不变 |
| P1-JOB-003 | lease 过期 | 新 generation，旧 worker 提交失败 |
| P1-JOB-004 | transient failure | 退避 retry，attempt 保留 |
| P1-JOB-005 | max attempts | failed，不再自动领取 |
| P1-JOB-006 | running cancel | 当前 worker 提交 cancelled |
| P1-JOB-007 | failed retry | 转 pending，保留 attempt 历史 |
| P1-JOB-008 | no processor | succeeded/no_processor，不生成记忆 |
| P1-JOB-009 | 软队列限制 | waiting job 提交，响应 deferred |
| P1-JOB-010 | 硬资源限制 | 整个 ingest 事务拒绝 |

## 6. 记忆与生命周期

| ID | 场景 | 预期 |
| --- | --- | --- |
| P1-MEM-001 | 创建记忆 | memory、revision、state、projection、FTS、audit 原子提交 |
| P1-MEM-002 | 修订 | 新 revision，旧 revision 不变 |
| P1-MEM-003 | 过期期望版本 | 409 revision_conflict |
| P1-MEM-004 | archive | 状态事件更新，FTS 删除 |
| P1-MEM-005 | restore | 创建新 revision，不修改历史 |
| P1-MEM-006 | freeze | 自动修改禁止，显式人工操作受版本检查 |
| P1-MEM-007 | retract/delete | 默认查询排除，history 可见 |
| P1-MEM-008 | 不存在证据 | 事务拒绝，不创建孤儿 revision |
| P1-MEM-009 | 状态一致性 | current_state 与最后状态事件一致 |
| P1-MEM-010 | 状态历史 API | 按 created_at DESC、event ID DESC 返回完整历史 |
| P1-MEM-011 | state event 更新或删除 | SQLite trigger 拒绝修改 |
| P1-MEM-012 | 空 evidence 创建/修订 | 422，必须先 ingest manual event |

## 7. FTS 与检索

| ID | 场景 | 预期 |
| --- | --- | --- |
| P1-SEARCH-001 | 精确术语 | 返回 active projection |
| P1-SEARCH-002 | 不可检索状态 | 默认不返回 |
| P1-SEARCH-003 | revision 更新 | 不返回旧文本 |
| P1-SEARCH-004 | 祖先查询 | 不返回兄弟和后代 |
| P1-SEARCH-005 | limit + max_chars | 同时满足，必要时 truncated |
| P1-SEARCH-006 | semantic mode | unsupported，不静默转 keyword |
| P1-SEARCH-007 | FTS degraded | health 和响应标记 degraded |
| P1-SEARCH-008 | rebuild | 重建前后结果一致 |
| P1-SEARCH-009 | 分数相同 | updated_at DESC、memory_id ASC |

## 8. API 与错误

| ID | 场景 | 预期 |
| --- | --- | --- |
| P1-API-001 | 缺 caller | 422 invalid_request |
| P1-API-002 | 写接口缺幂等键 | 422 invalid_request |
| P1-API-003 | 非法 JSON | 400 invalid_json |
| P1-API-004 | Golden API | request/response 通过 OpenAPI 校验 |
| P1-API-005 | 未知 route | 404 not_found |
| P1-API-006 | 超时后重试 | 幂等重放原结果 |
| P1-API-007 | 错误 details | 不泄露内容、SQL、凭据和完整路径 |
| P1-API-008 | 磁盘 hard limit | 507 resource_exhausted |
| P1-API-009 | 活跃 replay/rebuild job | 409 job_already_active，返回现有 job ID |

## 9. CI 最小矩阵

- Linux amd64，最低支持 Go 版本。
- Linux amd64，当前 Go 稳定版本。
- CGO 开启，固定 SQLite build profile。
- 单元测试、race、SQL 初始化、OpenAPI 校验和 Go 漏洞检查。
