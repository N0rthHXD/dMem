# dMeM 第三阶段逻辑导入导出

Format v1 和导入导出流程已在 `internal/portability` 实现。当前可执行入口是 Go library 的 `portability.NewExporter`、`Exporter.DryRun`、`Exporter.Export`、`portability.NewImporter`、`Importer.DryRun`、`Importer.Import`、`Importer.Status` 和 `Importer.Cancel`，以及 `make export-import-drill`。daemon router、OpenAPI 和 `cmd/dmemd` 均未暴露 export/import，因此本文中的“调用方”指 library 集成方，不表示存在 `dmemd export/import` 或 HTTP 路径。

## 1. 与备份恢复的区别

| 一致性备份/恢复 | 逻辑导入/导出 |
| --- | --- |
| 完整 SQLite 当前 Schema 快照 | 选择性领域对象包 |
| 保留内部 ID、任务和幂等状态 | 默认重映射本地 ID |
| 要求精确 Schema version | 使用独立 logical format version |
| 替换整个 data directory | 合并到现有实例和目标空间 |
| 用于灾难恢复 | 用于跨实例便携和显式迁移 |
| 不解决业务冲突 | 冲突进入 proposal/review 流程 |

逻辑导入不能接受 SQLite 文件，也不能绕过 repository 领域校验直接执行 SQL。

## 2. Format v1

包至少包含：

```text
manifest.json
spaces.jsonl
raw_events.jsonl
memories.jsonl
revisions.jsonl
evidence.jsonl
entities.jsonl
entity_mentions.jsonl
relations.jsonl
conflicts.jsonl
feedback.jsonl
aggregate_provenance.jsonl
checksums.json
```

Manifest 至少记录：

```text
format_version, export_id, source_identity, created_at
selection, closure_policy, object_counts
processing/ranking/normalization versions
sensitivity summary, omitted references
file checksums and total size
```

格式采用明确编码、稳定字段和确定性对象顺序。同一权威快照、选择和 export identity 应产生相同对象内容；时间戳和包 ID 的生成规则必须在测试中固定。

## 3. 选择与闭包

调用方显式选择一个或多个 space，并指定是否包含祖先。默认不包含 sibling 或 descendant space。

最小闭包规则：

- 包含选中 memory 的全部 revisions、state history 和直接 evidence。
- 包含 evidence 引用的 raw event；若策略省略原始 content，manifest 明确标记。
- 包含选中 revision 之间的 relation、entity mention 和 conflict case。
- 对闭包外 relation endpoint 保留 external reference stub，不隐式扩大到其他空间。
- 包含 aggregate 的直接 input revisions，或明确将 aggregate 标记为 incomplete 并默认拒绝导出。
- 导出原始 feedback event，不导出 feedback aggregate。
- 不导出 FTS、embedding/vector、entity/relation index、缓存、provider secret、job lease 或普通运行日志。

Dry-run 返回对象计数、估算大小、敏感性摘要、外部引用和拒绝原因。

## 4. 导出一致性与发布

导出从固定 SQLite read snapshot 读取，不能在不同时间分别扫描对象后拼接。流程：

```text
validate selection
  → open consistent snapshot
  → calculate closure
  → write deterministic temporary package
  → checksum and self-validate
  → fsync and atomic publish
```

磁盘不足、未知对象状态、闭包断裂或 checksum 失败时不发布包。输出目录继续执行 owner、permission 和 symlink 安全检查。

## 5. 导入规划

导入在任何权威写入前完成：

- Format version、manifest、文件成员、大小和 checksum 校验。
- 所有引用、ID 唯一性、revision 链、state history 和 relation endpoint 校验。
- Source space 到本地目标 space 的显式映射。
- Source object ID 到新 local ID 的确定性规划。
- Existing import mapping 和重复对象识别。
- 敏感性、内容类型和资源预算校验。
- 与本地 current memory 的潜在 duplicate/conflict 报告。

Dry-run 输出映射计划和将创建的 proposal，不写权威数据。

## 6. 导入提交

大包使用 durable import operation 和有界批次，不使用无限长事务。每个批次满足：

- 对象与 `(source_identity, source_object_id, export_id) → local_id` mapping 同事务写入。
- Imported provenance、idempotency 和 audit 同事务写入。
- Checkpoint 只在整批成功后推进。
- 重试已提交批次不重复创建对象。
- 依赖对象先于引用对象写入，或保持 waiting 状态直到依赖完成。
- 当前 generation 之外的 worker 不能推进 checkpoint。

导入的 memory 默认进入正常可审核策略。与本地内容重复、补充、替代或冲突时创建 evolution proposal，不直接修改本地 current revision。Imported feedback 保留原 caller/source 标识，但不自动获得更高排序权重。

## 7. 故障与恢复

- Invalid/unknown format 在写入前返回 `unsupported_format` 或 `invalid_package`。
- 处理中断后从最后已提交 checkpoint 恢复。
- Cancel 停止后续批次，不删除已经导入且有完整 mapping/provenance 的对象。
- 若产品需要全有或全无导入，应限制包规模并另行 ADR；第三阶段默认采用可恢复批次和显式 partial/cancelled 状态。
- Backup/restore 必须保存 import operation、mapping 和 checkpoint，并 fence 恢复前 running attempt。
- 导入完成后创建 FTS、semantic、entity、relation 和 feedback aggregate rebuild 工作；索引完成前 health 和查询明确降级。

## 8. 安全与隐私

- Export 默认不包含 secret、Authorization、provider 原始响应或运行日志。
- Sensitive 对象需要显式 include 选项，manifest 记录敏感对象数量但不泄露内容。
- 导入不信任 package 内路径、文件名、大小或 JSON 字段；拒绝 symlink、路径穿越、额外成员和超限记录。
- Source identity 是 provenance，不是本地授权凭据。
- 未知 extension 字段按 format contract 严格拒绝或明确忽略，不能由导入器执行动态代码。

## 9. 非目标

- 不兼容任意旧 SQLite 数据库。
- 不导入未知 format version 或猜测字段含义。
- 不提供 preserve-ID 模式。
- 不导出派生索引和 feedback aggregate。
- 不实现双向同步、增量复制、远程传输协议或合并 CRDT。
