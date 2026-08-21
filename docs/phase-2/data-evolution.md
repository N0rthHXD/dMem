# dMeM 第二阶段 Schema 演进、备份与恢复

## 1. 目标

第二阶段开始保留已发布用户数据，建立三项能力：

- 从 Schema v1 到下一版本的受控前向 migration。
- 运行中数据库的一致性备份。
- 在 staging 位置完成恢复、验证和索引重建后再投入服务。

逻辑导入导出和跨实例空间映射不在本阶段范围。

## 2. Schema 版本

- `schema_metadata` 保存唯一当前版本。
- 每个 migration 有单调递增版本、稳定名称和校验值。
- daemon 声明可打开和可迁移的版本范围。
- 新 daemon 可以拒绝过旧或未知新版本，不能猜测升级路径。
- 旧 daemon 打开新版本时只报告 incompatible，不执行 DDL。
- processing version、embedding model 和 semantic index version 不等于 Schema version。

## 3. Migration 规则

Migration 默认 forward-only。每次迁移必须：

1. 持有 data directory 单实例锁。
2. 在普通 API ready 前运行，或显式进入维护模式。
3. 校验源版本和 migration 文件校验值。
4. 在可行时使用单个 SQLite 事务执行。
5. 对需要表重建或长数据回填的步骤提供可恢复计划。
6. 更新 Schema version 前验证新表、约束和引用。
7. 失败时回滚事务，或从迁移前备份恢复。
8. 写入不含业务内容的 migration audit/log。

不可事务化或耗时迁移必须拆分为“兼容 Schema → 后台回填 → 验证 → 启用能力”，daemon 在回填完成前保持对应能力 degraded，不得把部分数据视为完整索引。

## 4. V1 升级最低验证

升级包含第一阶段已有对象时必须验证：

- raw contents/events 数量和 hash 不变；
- memory current revision 均存在；
- revision 和 state event 历史不被改写；
- evidence 引用存在；
- idempotency 响应仍可重放；
- 非终态 job 可按恢复规则继续；
- projection 与 current revision 一致；
- FTS 可查询或可全量重建；
- 新 candidate、review 和 embedding metadata 初始状态明确。

## 5. 一致性备份格式

最小备份目录包含：

```text
manifest.json
database.sqlite
checksums.json
```

Manifest 至少记录：

```text
format_version
backup_id
created_at
daemon_version
schema_version
database_checksum
sqlite_integrity_result
unfinished_job_summary
processing_versions
fts_index_version
semantic_index_version
```

派生 FTS 和 semantic 索引可以不进入备份，但 manifest 必须说明恢复后需要 rebuild。配置可以记录非敏感结构，secret 只记录引用类型，不进入备份。

备份必须使用 SQLite online backup API 或等价一致性快照，不能直接复制活动 WAL 数据库并宣称一致。

## 6. 备份流程

```text
检查磁盘和目标权限
  → 创建临时备份目录
  → 获取 SQLite 一致性快照
  → 对快照执行 integrity check
  → 生成 manifest 和 checksum
  → fsync 必要文件
  → 原子发布备份目录
```

备份不要求长时间阻断 ingest，但必须设置超时和磁盘预算。失败的临时目录不作为有效备份，并产生稳定错误和 audit。

## 7. 恢复流程

Restore 是显式维护操作：

```text
停止普通服务并持有锁
  → 校验 manifest 和 checksum
  → 恢复到 staging data directory
  → 打开 SQLite 并检查 Schema 兼容性
  → 执行 integrity、外键和领域不变量检查
  → 规范化 running/过期任务
  → 重建或验证 FTS 和 semantic index
  → 启动 staging daemon/readiness probe
  → 原子切换 data directory
```

禁止覆盖当前数据库后再验证。恢复失败保留原 data directory，不进入 ready。

## 8. 任务恢复

- 备份中的 `running` attempt 不直接继续。
- 恢复时关闭旧 attempt，递增 lease generation，并按 attempt 和 cancel 状态转为 retrying、failed 或 cancelled。
- 外部 provider 可能已经产生副作用时，依赖 event、processing version、candidate hash 和 provider request ID 去重。
- confirmed candidate、idempotency record 和已创建 revision 阻止重复确认。

## 9. 恢复验证

自动化恢复演练至少检查：

- SQLite integrity 和 foreign key check；
- Schema version 与 daemon 兼容；
- projection 指向 current revision；
- evidence、candidate 和 review 引用存在；
- idempotency replay 返回原对象；
- job 状态可恢复且旧 generation 被 fence；
- FTS 和 semantic index 可以从权威数据重建；
- 隔离空间检索没有泄漏；
- daemon 在 staging UDS 上达到预期 health。

只生成备份文件而不执行恢复演练，不满足第二阶段完成标准。

## 10. 安全与保留

- backup 目录权限默认 `0700`，文件默认 `0600`。
- 拒绝 symlink 目标、非当前用户 owner 和位于未允许目录的恢复目标。
- 日志不输出数据库内容、checksum 对应原文、secret 或完整外部路径。
- 本阶段不自动删除旧备份；保留策略由运维明确执行。
- 删除 memory 不会追溯修改既有备份，manifest 和运维文档必须明确该边界。
