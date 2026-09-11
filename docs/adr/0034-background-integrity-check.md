# ADR 0034：启动时后台执行 SQLite 完整性检查

- 状态：accepted
- 日期：2026-09-11
- 关联：修订 ADR 0014 的启动期 fail-fast 行为范围；与 ADR 0015、0021、0031 一致

## 背景

`storage.Open` 对已有数据库在建立 HTTP socket 之前同步执行 `PRAGMA quick_check`。该检查名为 quick，实际成本是 O(数据库大小)：它扫描全部 table/index b-tree；并且从 SQLite 3.44 起，完整性检查会对虚拟表调用 `xIntegrity`，而 FTS5 的实现忽略 quick 标志，会重新读取并重新分词 `memory_fts` 的全部内容，再遍历整个倒排索引校验 checksum。

`memory_fts` 是 contentful FTS5（`tokenize = 'trigram'`），内容在 `%_content` 中再存一份，trigram 倒排索引随内容线性膨胀。在约 600MB 的权威库上，这一同步检查使 daemon 启动被阻塞约两分钟，期间服务不可用；restore 的 staging probe（`daemon.StartStaging`）也会支付同样的成本。

## 决策

### 1. 完整性检查模式

`storage.Options` 新增 `IntegrityCheckMode`：

- `IntegrityCheckSync`（零值）：保持原行为，`Open` 返回前完成检查，失败即拒绝启动。测试与需要 fail-fast 的调用方使用。
- `IntegrityCheckBackground`：检查在独立 goroutine 中执行，`Open` 立即返回。
- `IntegrityCheckOff`：跳过检查。

daemon 使用 background 模式。检查在专用的 `mode=ro` 连接上执行，不占用 writer，也不占用 reader 池（`worker_count` 最小为 1，占用 reader 会阻塞 health 与查询）。

### 2. 状态与生命周期

- `Store.IntegrityStatus()` 返回 `off | pending | ready | failed` 及失败错误；`WaitIntegrity(ctx)` 供等待与测试使用。
- `Store.Close()` 取消后台检查并等待其退出后再关闭连接。SQLite 的完整性检查循环会检查 `isInterrupted`，go-sqlite3 的 context 取消会调用 `sqlite3_interrupt`，因此取消可及时生效。
- daemon 在后台等待检查结果，失败时记录 `slog.Error`。

### 3. 失败语义

- `Repository.StorageStatus` 在检查失败时返回 `storage_state = unavailable`，health 的 `ready` 随之为 false。
- `/v1/health` 新增 `storage_integrity_state` 字段暴露 `off | pending | ready | failed`。
- 检查为 `pending` 时仍按 `storage_state = ready` 处理，避免 restore probe 与启动期 health 抖动。
- 失败不自动阻断写入（最小方案）：检查是启动时的点状判定，实际损坏仍由 SQLite 访问时报告。运维按 runbook 在 integrity 失败时停止 daemon 并保留数据目录。

## 后果

- daemon 启动不再等待完整性检查；600MB 库的检查仍在后台消耗约两分钟的 CPU/磁盘，并因长读快照延迟 WAL checkpoint，但不阻塞请求。
- 启动期 fail-fast 语义弱化为启动后检测；schema 兼容性与 sqlite-vec capability 仍是同步 fail-fast。
- `storage_integrity_state` 成为公开 health 契约的一部分（OpenAPI 已更新）。

## 关联

- ADR 0014：sqlite-vec/扩展策略与 capability 检查仍然同步 fail-fast。
- ADR 0021：restore 先在 staging 上执行完整 `integrity_check`，staging daemon 的后台检查与其冗余但无害。
- ADR 0031：FTS5/向量存储形态决定了 quick_check 的实际成本。
