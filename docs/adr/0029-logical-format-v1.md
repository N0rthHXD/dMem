# ADR 0029：确定性 Logical format v1

- 状态：accepted
- 日期：2026-08-25

## 决策

Logical format v1 是严格的领域 package，不是 SQLite backup。它使用 UTF-8、LF、每行一个 canonical JSON object、按 object ID byte order 排序的固定 JSONL members，以及 canonical `manifest.json` 和 `checksums.json`。闭包遵循第三阶段 logical portability 规则，导出权威 history、evidence、entity mention、relation、conflict、feedback、aggregate provenance 和 external stubs，不导出 secret、lease、日志、派生索引或 feedback aggregate。未知 member 或字段均拒绝。

Export request 必须包含 source identity、space selection、closure policy、sensitive policy 和 reference time，可选提供 `export_identity`。未提供时，export identity 为 format version、source identity、canonical request、reference time 和固定 read snapshot corpus generation 的 SHA-256；manifest `export_id` 直接使用该 identity，`created_at` 使用 request reference time。相同 authority snapshot 和 request 因而产生逐字节相同 package 和 checksum。Export/dry-run 可通过 UDS HTTP 提交不含文件路径的 operation；读取或发布本地 package path、symlink/owner/permission 检查和原子 rename 只由 offline CLI 执行。

Import 先完整验证再规划 local IDs，默认生成 local ID，并将 `(source_identity, source_object_id, export_id)` mapping、object hash 和 imported provenance 与对象同事务提交。大包使用 durable bounded batches、checkpoint 和 generation fencing；重复 mapping 和相同 hash 返回已有对象，本地内容差异创建独立 evolution proposal，不覆盖 current revision。Import dry-run 和不含路径的 operation/status 是 UDS HTTP 公共 API；打开本地 package path 只由 offline CLI 执行。

Import 错误按以下优先顺序精确映射，并在首个权威写入前完成 package 类错误检查：非 dMem logical package、SQLite 文件或未知 format family 返回 `unsupported_format`；识别为 dMem logical package但 `format_version != 1` 返回 `incompatible_version`；v1 缺少或多出 member、JSON/字段/顺序/ID/reference/checksum/closure 不合法返回 `invalid_package`；调用方 target space、mapping、policy 或 path 参数不合法返回 `invalid_request`；package/record/临时空间或磁盘超限返回 `resource_exhausted`；既有 source mapping 的 object hash 不同返回 `idempotency_conflict`；规划后本地 expected revision 改变返回 `revision_conflict` 并要求 replan；输入 path 不存在返回 `not_found`，不可读或 storage 不可用返回 `storage_unavailable`。中断和 worker generation 过期更新 operation 状态，不改写为 package 格式错误。
