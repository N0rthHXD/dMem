# dMeM 维护与恢复

## 命令边界

`dmemd backup`、`dmemd restore` 和 `dmemd init` 是本地维护命令。它们不连接已运行 daemon，也不访问 provider 或网络。`backup` 可在 daemon 运行时通过 SQLite online backup API 取得一致快照；`restore` 和 `init` 要求 data directory lock。无子命令的现有调用仍启动 daemon；`dmemd daemon` 是等价的显式形式。

维护命令只读取 `DMEM_DATA_DIR`、`DMEM_DATABASE_PATH` 及对应路径参数，不读取 provider 配置或 secret。失败只输出稳定错误码，不输出数据库内容、secret 或完整路径。

## 初始化开发数据库

新建数据库：

```sh
make schema-init DATA_DIR="$HOME/.local/share/dmem-dev"
```

`init` 要求数据库不存在，并创建当前完整 Schema；它不执行 migration。Schema 变化后，应先停止 daemon 并保留或删除整个旧开发 data directory，再对新的空目录运行初始化：

```sh
mv "$DMEM_DATA_DIR" "$DMEM_DATA_DIR.old"
make schema-init DATA_DIR="$DMEM_DATA_DIR"
```

确认不再需要旧数据后再显式删除 `.old` 目录。不要只删除 `dmem.db` 而保留来源不明的同目录运行文件。

## 备份

备份使用 SQLite online backup API，可以在 daemon 运行时创建一致快照；该命令本身仍不连接 daemon、不启动服务或访问网络。备份目标必须不存在，目标父目录必须归当前用户所有且模式为 `0700`：

```sh
install -d -m 0700 "$HOME/dmem-backups"
dmemd backup --data-dir "$DMEM_DATA_DIR" \
  --target "$HOME/dmem-backups/backup-$(date +%Y%m%dT%H%M%S)"
```

成功输出 backup ID 和 Schema version。`--minimum-free` 是操作前要求的可用 bytes，默认使用 hard free 阈值；只有受控测试环境才应设为 `0`。

## 恢复

Restore 会原子替换整个 data directory，因此必须停止 daemon并显式传入 `--confirm-replace`：

```sh
dmemd restore --data-dir "$DMEM_DATA_DIR" \
  --source "$HOME/dmem-backups/backup-20260824T120000" \
  --confirm-replace
```

恢复先持有 data directory 锁，再在同一文件系统 staging 目录校验 checksum、精确 Schema、SQLite integrity、外键和领域不变量，最后才切换目录。失败不会发布 staging data directory。

Phase 3 restore 还执行：

- 对照 manifest 核验 entity、mention、relation、conflict、feedback、aggregate、import source/mapping/provenance/checkpoint 等新增权威表计数。
- 验证 mention range/space、relation endpoint、conflict case、feedback revision、aggregate provenance/freshness、import mapping/provenance/checkpoint 等跨表不变量。
- 关闭未完成 job attempt，将 running job 按重试上限改为 retrying/failed/cancelled，并递增 lease generation。
- 将 running import 改为 waiting、递增 import generation，并保持 `committed_objects` 与最后 checkpoint/provenance 一致。
- 从权威 projection 重建 FTS；刷新 entity/relation index metadata；重建 feedback aggregate 和 aggregate freshness。
- 清除 semantic active version，将 projection embedding 标为 stale，并排入新的 embed/rebuild jobs。配置可用后由 worker 完成 semantic 恢复。
- 在隔离的临时 runtime directory 启动 staging daemon probe；probe 写入的 canary 不会发布到恢复结果。

恢复返回的 CLI JSON 只报告 `backup_id`、`normalized_jobs` 和 `fts_rebuilt`。不要据此推断 semantic 已 ready；启动 daemon 后必须检查 health。

## 派生重建

当前公开重建入口位于 UDS HTTP API，不是 `dmemd` 子命令：

| 派生数据 | 已实现 HTTP 操作 | 结果 |
| --- | --- | --- |
| FTS | `POST /v1/indexes/fts/rebuild` | durable `rebuild_fts` job |
| Semantic | `POST /v1/indexes/semantic/rebuild` | durable `rebuild_semantic` job；依赖当前 embedding provider/model identity |
| Entity index | `POST /v1/indexes/entities/rebuild` | durable `rebuild_entity_index` job |
| Relation index | `POST /v1/indexes/relations/rebuild` | durable `rebuild_relation_index` job |
| Feedback aggregate | `POST /v1/feedback/aggregates/rebuild` | durable `rebuild_feedback_aggregates` job |
| Aggregate freshness/content | 无通用手工 rebuild 路径 | 输入 authority 变化时排入 `rebuild_aggregate`；restore 重建 freshness projection |

写入型 rebuild 请求必须遵循 OpenAPI 的 caller/idempotency header。已有同类 active job 时返回 `job_already_active` 和现有 job ID；通过 `GET /v1/jobs/{job_id}` 跟踪终态。不要直接删除 SQLite 派生表模拟运维重建。

## 逻辑导出导入边界

逻辑 format v1 已实现为 `internal/portability` Go library，用于选择性领域对象便携；它不是 SQLite disaster recovery。当前没有 `dmemd export`、`dmemd import` 或 HTTP export/import 路径。

- 导出入口：`portability.NewExporter` 后调用 `DryRun` 或 `Export`。导出使用一个 SQLite read snapshot，写入 owned/private 临时目录，校验后原子发布。
- 导入入口：`portability.NewImporter` 后调用 `DryRun`、`Import`、`Status` 或 `Cancel`。导入先严格验证包和映射计划，再按有界批次提交。
- 每个已提交批次将对象、source mapping、imported provenance 和 checkpoint 原子写入；中断后重跑同一 import 从 `committed_objects` 继续。
- Cancel 只停止后续批次，保留已提交且 mapping/provenance 完整的对象。
- 本地 current revision 冲突创建 evolution proposal，不覆盖当前 revision。
- 导入成功会排入 FTS、semantic、entity、relation 和 feedback aggregate rebuild jobs；完成前 health 可降级。

可执行集成演练为：

```sh
make export-import-drill
```

该 target 运行 fresh-instance export/import/retrieval 等价性和目标规模性能测试，不是面向操作者的数据迁移命令。

## Checkpoint 与中断恢复

导入中断或进程退出后：

1. 不删除 package、import operation、mapping、provenance 或 checkpoint。
2. 检查恢复/重启是否已将 running import fence 为 waiting；旧 generation 不得再提交。
3. 使用同一 package、source identity、export ID、目标空间和 mapping 请求重新调用 library `Import`。
4. 确认最终 `state=succeeded` 且 `committed_objects=total_objects`。
5. 确认重建 jobs 完成，health 中 FTS/entity/relation/feedback/aggregate 状态为 ready；如配置 semantic provider，再确认 semantic ready。
6. 运行作用域、current revision、conflict refs 和 imported provenance 抽查。

若批次失败，当前批次的 mapping、provenance、checkpoint 和 `committed_objects` 会一起回滚；已完成的更早批次保持有效。`revision_conflict` 需要重新 dry-run，不应绕过 expected revision。

## 失败处理

| 错误码 | 操作 |
| --- | --- |
| `invalid_arguments` | 检查必需参数；restore 必须包含 `--confirm-replace`；不存在 export/import/rebuild CLI 子命令。 |
| `offline_required` | 停止 daemon，确认没有其他进程使用同一 data directory 后重试。 |
| `unsafe_path` | 修正 owner、目录 `0700`、文件 `0600`，并移除路径中的 symlink。 |
| `invalid_backup` | 隔离该备份，不修改文件后重试；改用已验证备份。 |
| `incompatible_backup` | 使用与当前精确 Schema version 匹配的 daemon；不执行自动升级或降级。 |
| `insufficient_storage` | 在 data directory 和备份所在文件系统释放空间。 |
| `backup_failed` / `restore_failed` | 保留现有 data directory，检查权限、磁盘和文件系统是否支持同文件系统原子交换。 |

恢复成功后正常启动 daemon 并检查 readiness、各派生状态、queue depth 和 disk pressure。运行 `make restore-drill`；该 target 实际创建一致快照并恢复，覆盖 Phase 2 semantic 恢复以及 P3-DATA-001..003 的新增权威 inventory、job/import fencing、checkpoint 和派生重建，而不是只检查备份文件生成。

## 定期演练

建议在与部署一致的文件系统和权限模型下执行：

| 频率 | 演练 | 验收 |
| --- | --- | --- |
| 每次阶段/发布候选 | `make phase-3/check` | Traceability、完整回归、race、vet、契约、恢复、便携、质量和默认离线全通过 |
| 每月或 Schema/restore 变化后 | `make restore-drill` | 原目录在失败时保持不变；成功后 authority、fencing、FTS 和 semantic 恢复通过 |
| Logical format/import 变化后 | `make export-import-drill` | fresh import、mapping/provenance、检索等价性和性能门槛通过 |
| Ranking/index 变化后 | `make retrieval-gates` | sibling/stale 为 0，conflict refs 完整，资源门槛通过 |
| Provider/config 变化后 | `make offline-default` | 默认配置不解析 secret、不发 HTTP |
| 安全边界变化后 | `CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/maintenance ./internal/portability -count=1` | 完整 maintenance/portability 安全、恢复、checkpoint 和等价性回归通过 |

完整故障处置见[运行故障手册](failure-runbook.md)。
