# dMeM 第一阶段能力矩阵

## 1. 权威范围

本文档是第一阶段实现和验收范围的权威来源。其他文档描述的完整架构能力，如与本矩阵冲突，以本矩阵为准。

状态定义：

```text
required       第一阶段必须实现并验收
schema-only    schema 保留稳定对象，不提供完整业务能力
unsupported    API 可识别请求，但明确返回 unsupported
deferred       第一阶段不提供接口和实现
```

## 2. 能力矩阵

| 能力 | 状态 | 第一阶段契约 |
| --- | --- | --- |
| Go 用户级 daemon | required | 单实例、本地 UDS、优雅关闭和重启 |
| SQLite 初始化 | required | `schema-v1.sql` 新库初始化和精确版本检查 |
| SQLite schema migration | deferred | 不自动升级旧数据库 |
| 原始文本事件写入 | required | 幂等、事务提交后返回 accepted |
| 原始事件 get/list | required | 按空间、来源和时间分页查询 |
| 原始事件重放 | required | 为事件创建新的处理任务，不复制事件 |
| 托管二进制附件 | deferred | 第一阶段只接受外部文件引用，不复制文件内容 |
| 记忆空间 create/get/list | required | 单父节点树和确定性祖先查询 |
| 手工创建记忆 | required | 创建逻辑记忆、首个 revision、证据和状态事件 |
| 记忆 get/list/history | required | 当前状态、不可变 revision 和状态历史 |
| 记忆修订 | required | expected revision 条件更新和新 revision |
| archive/restore/freeze/retract/delete | required | 幂等写操作，追加状态事件 |
| 自动模型提取 | unsupported | 未配置 processor 时事件为 stored；接口不伪造记忆 |
| 外部或本地 processor 插件 | schema-only | 保存 processor 和任务字段，具体 provider 后续实现 |
| 候选自动激活 | deferred | 第一阶段只允许手工创建或修订记忆成为 active |
| SQLite FTS 关键词检索 | required | 只索引当前可检索记忆投影 |
| 结构化过滤 | required | 空间、类型、状态和有效时间 |
| 向量语义检索 | unsupported | 请求 semantic mode 返回 unsupported |
| 实体、关系和图检索 | deferred | 不进入 schema v1 权威模型 |
| 上下文条数和字符预算 | required | 同时满足两个硬限制 |
| Token 预算 | unsupported | 第一阶段无 tokenizer，不接受 max_tokens |
| 来源证据返回 | required | 返回原始事件引用，不自动展开完整内容 |
| 持久化任务队列 | required | lease、attempt、重试、取消和重启恢复 |
| 任务依赖图 | deferred | 第一阶段任务彼此独立 |
| 索引重建 | required | FTS 全量重建任务 |
| 逻辑删除和派生清理 | required | 默认检索立即排除，FTS 同事务清理 |
| 原始内容物理清除 | deferred | 第一阶段保留原始事件；空间 purge 不提供 |
| 完整备份和恢复工具 | deferred | 文档保留设计，不作为第一阶段验收 |
| 逻辑导入导出 | deferred | 开发期数据库视为可丢弃，不承诺升级保留 |
| 多用户 ACL | deferred | 同一 Linux 用户下 Agent 全部可信 |
| 网络监听模式 | deferred | 只支持 Unix Domain Socket |
| 遥测上传 | deferred | 默认不存在远程遥测实现 |

## 3. 第一阶段垂直切片

### 切片 A：运行与存储

- Go module 和包骨架。
- CGO SQLite 驱动和固定 build profile。
- `schema-v1.sql` 初始化。
- schema 版本精确校验。
- UDS HTTP server、health 和单实例。

### 切片 B：空间与原始事件

- 空间 create/get/list。
- 幂等 ingest。
- 原始事件 get/list。
- 初始 durable job 创建。
- duplicate response replay。

### 切片 C：任务执行

- 原子 claim、lease、heartbeat 和 attempt。
- cancel、retry 和过期 lease 恢复。
- 默认 no-op processor 将任务完成为 `no_processor` 结果。

### 切片 D：手工记忆

- 创建、获取、列出和修订记忆。
- revision、state event、evidence 和 optimistic concurrency。
- archive、restore、freeze、retract 和 logical delete。

### 切片 E：FTS 检索

- 当前投影和 FTS 同事务维护。
- 空间祖先解析。
- 关键词搜索、稳定分页、条数和字符预算。
- FTS 重建任务和降级状态。

## 4. 第一阶段完成标准

- `docs/phase-1-test-matrix.md` 中所有 required 测试通过。
- OpenAPI 与实现响应结构一致。
- schema v1 初始化结果通过完整性和不变量检查。
- 所有写接口满足幂等冲突和重放契约。
- daemon 重启后不会重复创建原始事件或记忆 revision。
- 同级项目、任务和会话之间不存在默认查询泄漏。
- 未配置 processor 时系统保持可用并明确报告未处理状态。
- unsupported 能力返回稳定错误，不静默降级为不同语义。

## 5. 开发期数据策略

第一阶段不实现 schema migration 和逻辑导入导出。开发期 schema v1 发生不兼容变化时，旧数据库视为可丢弃并重新初始化。首个需要保留已发布用户数据的版本发布前，必须重新评估并引入 schema migration。
