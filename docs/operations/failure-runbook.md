# dMeM 运行故障手册

## 总原则

1. 先停止扩大损害：对 restore 停止 daemon；对磁盘/authority 故障停止普通写入；保留原 data directory、backup 或 logical package 原样。
2. 区分权威故障和派生故障。不得用索引结果反写 entity、relation、feedback 或 memory authority。
3. 不直接修改 SQLite 表、manifest、checksums、import checkpoint 或 job generation 以“修复”状态。
4. 使用 health、稳定错误码和 job/import status 判断恢复，不解析日志中的内容或猜测内部状态。
5. 失败的 restore staging 不发布；失败 import batch 回滚当前批次，但更早 checkpoint 可能已合法提交。

## 快速分类

| 现象 | 初始分类 | 首要动作 |
| --- | --- | --- |
| `storage_state` 非 ready、integrity/foreign-key 失败 | 权威存储故障 | 停止 daemon，保全 data directory，选择已验证 backup |
| FTS/entity/relation/feedback state 非 ready | 派生故障 | 保持 authority，调用对应 OpenAPI rebuild operation |
| Semantic absent/degraded after restore | 预期恢复阶段或 provider 故障 | 检查 provider identity 和 embed/rebuild jobs，不重复 restore |
| Aggregate stale/pending rebuild | 输入 authority 已变化 | 跟踪 `rebuild_aggregate` job；核对 provenance，不手工改 freshness |
| Job 长期 running/lease lost | worker/进程故障 | 重启或等待 lease recovery，确认 generation 已推进 |
| Import waiting/failed/cancelled | logical import 故障 | 保留 package/checkpoints，按本手册重新 dry-run/resume |
| `unsafe_path`、checksum、unknown member | 安全/完整性故障 | 隔离输入，不修改后重试，不放宽权限或 symlink 校验 |
| `incompatible_backup` / `incompatible_version` | 版本故障 | 使用精确兼容 binary/package；不执行自动升级或降级 |

## Backup/Restore 失败

### Backup 失败

1. 检查目标不存在，父目录属于当前用户且不可被 group/other 访问，路径不含 symlink。
2. 检查源 data directory 和数据库是当前用户拥有的安全路径。
3. 检查 `--minimum-free` 和目标文件系统空间；不要在真实环境用 `0` 绕过空间保护。
4. 确认失败目标未被发布。保留日志中的稳定错误码，不记录数据库内容或完整敏感路径。
5. 修复环境后创建新目标目录名重试，不复用可能残留的目标。

### Restore 失败

1. 保持 daemon 停止，确认 data directory lock 没有被其他实例持有。
2. 不修改 backup 的 manifest、database 或 checksums。`invalid_backup` 时隔离该备份并改用另一份已验证备份。
3. `incompatible_backup` 时使用支持精确 Schema version 的 binary；当前没有 SQLite migration。
4. `restore_failed` 时检查 data/backup 文件系统空间、owner/mode 和同文件系统原子替换能力。
5. 确认原 data directory 仍存在且未被 staging 替换。不要删除原目录来“帮助”重试。
6. 在修复环境后重新运行受支持的 `dmemd restore ... --confirm-replace`。
7. 成功后启动 daemon，检查 `/v1/health`、queue、FTS/entity/relation/feedback/aggregate 状态；semantic 可先 absent/degraded，随后由 embed/rebuild jobs 恢复。

回归演练：

```sh
make restore-drill
```

## 派生索引故障

按 OpenAPI 创建 durable rebuild job：

| 故障 | 操作 |
| --- | --- |
| FTS | `POST /v1/indexes/fts/rebuild` |
| Semantic | `POST /v1/indexes/semantic/rebuild` |
| Entity | `POST /v1/indexes/entities/rebuild` |
| Relation | `POST /v1/indexes/relations/rebuild` |
| Feedback aggregate | `POST /v1/feedback/aggregates/rebuild` |

1. 使用 OpenAPI 要求的 caller 和 idempotency headers。
2. 保存返回的 job ID，通过 `GET /v1/jobs/{job_id}` 跟踪。
3. `job_already_active` 时跟踪返回的现有 job，不创建重建风暴。
4. Rebuild 失败时按 job error 分类处理 provider、磁盘或 authority 问题；不要删除权威表。
5. Job succeeded 后确认 health version 推进，并运行代表性 scoped search 检查 current revision 和 conflict refs。

Aggregate 没有通用手工 rebuild HTTP/CLI。输入变化自动排入 `rebuild_aggregate`；restore 会重建 freshness。若持续 stale，检查 job 和输入 revision/state/sensitivity，不直接修改 freshness。

## Job 与 Generation 恢复

1. 进程崩溃后重新启动 daemon，让 lease recovery 关闭旧 attempt 并递增 generation。
2. 确认旧 attempt outcome 为 lease lost 语义，任务进入 retrying/failed/cancelled 中符合重试上限的状态。
3. 不复用旧 worker generation，也不手工将 running 改为 succeeded。
4. 对有外部 provider 的任务，先确认 provider identity 未变化且默认外发策略允许，再 retry。
5. 权威事务已提交但响应丢失时，使用原 idempotency key 或 get/status 查询，不生成新 identity 猜测重放。

## Logical Import 恢复

当前操作边界是 `internal/portability` library，不存在 `dmemd import` 或 HTTP import endpoint。

1. 保留 owned `0700` package directory 和全部 `0600` members，不修改 checksum 或 JSON。
2. 用相同 package、target space、source/export identity 和 object mappings 再次执行 `Importer.DryRun`。
3. 若 plan/package hash 或 expected revision 已变化，停止并人工审核；不要绕过 `revision_conflict`。
4. 对 waiting operation 再次调用 `Importer.Import`。它从 `committed_objects`/checkpoint 继续并以新 generation fence 旧执行者。
5. 对 cancelled operation，已提交批次是保留状态，不应假设自动回滚整个 import。
6. 成功后确认 `committed_objects=total_objects`、mapping 与 provenance 完整，并等待五类重建 job。
7. 验证目标空间隔离、current revision 未被冲突导入覆盖、冲突已成为 pending evolution proposal。

回归演练：

```sh
make export-import-drill
```

## 磁盘与资源故障

1. Hard pressure 时停止新增普通写入和大对象处理，保留查询及恢复空间。
2. 统计 data directory、semantic index、backup 和 logical package 的独立占用；不要删除正在使用的数据文件。
3. 优先将已验证的旧 backup/package 按保留策略移动到安全文件系统；不要删除唯一恢复副本。
4. 释放空间后检查 SQLite health 和 queue，再逐步恢复 worker。
5. 运行 `make retrieval-gates` 验证重建资源回归；实际生产规模需另做容量评估。

## 安全事件

遇到 symlink、owner/mode 不符、额外 package member、路径穿越、checksum 或 strict JSON 失败：

1. 将 backup/package 视为不可信并隔离，记录 hash、来源和稳定错误码。
2. 不 chmod 为宽松权限、不跟随 symlink、不删除未知成员后继续导入。
3. 从可信来源重新生成 package 或改用已验证 backup。
4. 确认目标 authority hash/count 未变化；malicious/invalid package 必须在 authority write 前失败。
5. 运行安全回归：

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 \
  ./internal/maintenance ./internal/portability -count=1
make offline-default
```

## 恢复完成标准

- `/v1/health` 显示 storage/schema ready/compatible，disk pressure 正常。
- FTS/entity/relation/feedback/aggregate 派生状态 ready；需要 semantic 时 semantic ready。
- 无非预期 running attempt 或 import generation；waiting/retrying 项均可解释。
- Scoped search 不跨 sibling space，不返回 stale revision，conflict refs 完整。
- Backup restore 或 logical import 后 mapping/provenance/checkpoint 数量一致。
- 日志和故障记录不包含 secret、原始内容、provider 原始响应或不必要的完整路径。
