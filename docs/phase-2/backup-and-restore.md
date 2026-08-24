# dMeM 第二阶段备份与恢复

## 1. 目标

第二阶段建立两项当前 Schema 下的可靠性能力：

- 运行中数据库的一致性备份。
- 在 staging 位置完成恢复、验证和索引重建后再投入服务。

逻辑导入导出和跨实例空间映射不在本阶段范围。

## 2. 开发期 Schema 规则

- 第二阶段及后续阶段在项目明确发布前均不实现 schema migration。
- Schema 变化直接更新完整初始化 SQL和代码模型，旧开发数据库重新初始化。
- Restore 只接受与当前 daemon 精确匹配的 Schema version，不升级、不降级也不转换备份。
- 不提供旧 Schema 读取、双写、兼容视图、字段回填或版本适配层。
- processing version、embedding model 和 semantic index version 不等于 Schema version。

## 3. 一致性备份格式

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

## 4. 备份流程

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

## 5. 恢复流程

Restore 是显式维护操作：

```text
停止普通服务并持有锁
  → 校验 manifest 和 checksum
  → 恢复到 staging data directory
  → 打开 SQLite 并检查 Schema version 精确匹配
  → 执行 integrity、外键和领域不变量检查
  → 规范化 running/过期任务
  → 从权威 projection 重建并验证 FTS；在同一事务将 semantic 标记为 absent/rebuild-required，为当前可检索 revision 写入 provider/model 绑定的 embedding job，并写入 waiting rebuild job
  → 在隔离 staging UDS 启动无 provider、无 worker 的生产服务装配
  → 验证 readiness/health、keyword 一致性、hybrid 显式降级和 sibling-space 隔离
  → 原子切换 data directory
```

禁止覆盖当前数据库后再验证。恢复失败保留原 data directory，不进入 ready。

## 6. 任务恢复

- 备份中的 `running` attempt 不直接继续。
- 恢复时关闭旧 attempt，递增 lease generation，并按 attempt 和 cancel 状态转为 retrying、failed 或 cancelled。
- 外部 provider 可能已经产生副作用时，依赖 event、processing version、candidate hash 和 provider request ID 去重。
- confirmed candidate、idempotency record 和已创建 revision 阻止重复确认。

## 7. 恢复验证

自动化恢复演练至少检查：

- SQLite integrity 和 foreign key check；
- Schema version 与当前 daemon 精确匹配；
- projection 指向 current revision；
- evidence、candidate 和 review 引用存在；
- idempotency replay 返回原对象；
- job 状态可恢复且旧 generation 被 fence；
- FTS 和 semantic index 可以从权威数据重建；
- 隔离空间检索没有泄漏；
- daemon 在 staging UDS 上达到预期 health。

默认恢复探测禁止网络/provider 调用。显式 validation hook 可以在 staging 内使用受控的本地 provider 验证 semantic/hybrid；未配置 provider 时 semantic jobs 不计入普通队列容量、不阻塞 manual/keyword，并通过 health 报告 rebuild_required。任一 readiness、检索一致性或空间隔离检查失败时，先关闭 staging daemon 并丢弃 staging data directory，原 data directory 保持不变。

只生成备份文件而不执行恢复演练，不满足第二阶段完成标准。

## 8. 安全与保留

- backup 目录权限默认 `0700`，文件默认 `0600`。
- 拒绝 symlink 目标、非当前用户 owner 和位于未允许目录的恢复目标。
- 日志不输出数据库内容、checksum 对应原文、secret 或完整外部路径。
- 本阶段不自动删除旧备份；保留策略由运维明确执行。
- 删除 memory 不会追溯修改既有备份，manifest 和运维文档必须明确该边界。
