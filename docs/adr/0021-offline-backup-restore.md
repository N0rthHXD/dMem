# ADR 0021：一致性备份与离线 staging restore

- 状态：accepted
- 日期：2026-08-21

## 决策

备份载体是本地 `0700` 目录，固定包含 `manifest.json`、`database.sqlite` 和 `checksums.json`，文件权限为 `0600`。数据库通过 SQLite online backup API 生成一致快照，在临时同文件系统目录完成 integrity 和 SHA-256 校验、fsync 后原子 rename 发布。

创建备份和 destructive restore 使用离线维护 CLI，不开放普通 HTTP 写 API。Restore 停止 daemon 并持有 data directory 锁，只恢复到 staging data directory；完成 checksum、Schema、integrity、foreign key、领域不变量、任务 generation fencing、索引重建和 staging UDS readiness 后，才在同文件系统原子切换。失败不修改当前 data directory。

备份格式 version 1 不包含 secret；派生索引可省略并标记 rebuild。目标和 staging 路径拒绝 symlink、非当前用户 owner 和宽松权限。
