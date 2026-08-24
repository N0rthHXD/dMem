# dMeM 测试与质量评估

## 1. 测试层次

```text
领域规则测试
SQLite repository 测试
任务处理和恢复测试
接口契约测试
daemon 生命周期测试
端到端数据流测试
性能和资源测试
故障注入测试
记忆质量评估
```

## 2. 领域规则测试

验证：

- 记忆版本不可变。
- 当前投影只指向有效版本。
- 版本条件更新检测冲突。
- 状态转换符合生命周期规则。
- 冻结记忆不能被自动替代。
- 删除、撤回、过期和归档的检索行为不同。
- 证据和关系不形成非法引用。

## 3. SQLite 测试

验证：

- 原始事件和任务在同一事务提交。
- 记忆版本、证据、投影和索引任务原子提交。
- 幂等键防止重复写入。
- 外键、唯一约束和必要索引有效。
- 初始化 SQL 能创建完整 schema，且 schema 版本正确。
- 不兼容 schema 被拒绝，数据库内容不被自动修改。
- 数据库重新打开后数据和任务状态一致。

## 4. 任务测试

场景：

- daemon 在任务运行时崩溃。
- 外部模型超时或限流。
- 模型返回非法结构。
- 索引更新部分成功。
- 同一任务重复投递。
- 队列达到上限。
- 任务取消和过期。
- 重试达到最大次数。

验证原始事件始终保留，重复执行不产生重复权威版本。

## 5. 接口契约测试

- API 版本协商。
- 未知字段行为。
- 请求大小和超时限制。
- `accepted`、`processed` 和 `indexed` 语义。
- 错误类别和重试标志。
- 分页、排序和预算截断。
- 并发修订返回 `revision_conflict`。
- 降级响应包含索引和新鲜度信息。

## 6. daemon 生命周期测试

- 第二个实例不能写同一数据目录。
- stale endpoint 可以安全恢复。
- 启动完整性检查失败时不返回 ready。
- 优雅关闭停止接收新任务并保存中断状态。
- 强制退出后下一次启动能恢复任务。
- 磁盘低水位进入受限模式。
- 维护模式拒绝普通写入。

## 7. 端到端场景

```text
事件写入 → 原始数据查询 → 记忆提取 → 版本提交 → 索引 → 查询
新事实 → 冲突检测 → contested → 人工确认 → active
记忆修订 → 历史版本查询 → 恢复为新版本
删除空间 → 默认检索排除 → 派生清理 → 备份清理待处理
索引删除 → 降级查询 → 重建 → 正常查询
```

## 8. 故障注入

- SQLite 写入前、事务中和提交后终止进程。
- 附件写入失败。
- 外部处理连接失败、超时和非法响应。
- 索引文件损坏。
- 数据库进入只读。
- 磁盘空间不足。
- 备份中断和恢复中断。
- 数据库 schema 版本不兼容。

每种故障必须验证确认语义、恢复行为和健康状态。

## 9. 性能与资源

测量：

- 原始事件接收延迟。
- SQLite 事务吞吐。
- 关键词和混合查询延迟。
- 查询 p50、p95 和 p99。
- 任务积压恢复速度。
- 索引重建时间和额外空间。
- daemon 空闲和负载内存。
- 数据、索引、日志和备份增长。

性能目标由需求迭代确定，测试应先建立可重复基线，不以牺牲持久性换取数字。

## 10. 记忆质量评估

评估集覆盖：

- 单事实。
- 用户偏好和约束。
- 当前状态和历史状态。
- 跨会话事实。
- 多跳关系。
- 冲突和修订。
- 过期、撤回和归档。
- 无答案和不相关查询。

指标：

```text
提取准确率
提取遗漏率
幻觉率
证据覆盖率
检索召回率
排序质量
跨空间污染率
时间判断准确率
冲突识别率
上下文信息密度
上下文 Token 成本
```

## 11. 回归要求

处理规则、模型、嵌入或排序权重变化前后，使用相同评估集对比：

- 质量变化。
- 延迟变化。
- Token 和资源变化。
- 新旧处理版本兼容性。
- 是否需要重建索引或重新处理原始事件。

质量下降或行为变化必须可归因到明确版本。

## 12. 第二阶段离线检索门槛

`make retrieval-gates` 运行 P2-QUAL-001..004 和 P2-PERF-001..002，不访问网络。固定语料位于 `internal/storage/testdata/retrieval-corpus-v1.json`，数值门槛和 corpus/provider/index/ranking 版本位于 `internal/storage/testdata/retrieval-gates-v1.json`。质量判断与 deterministic provider 的词表分开声明，避免用 embedding 实现自身生成相关性标签。

性能目标规模为 500 条可检索 memory，每种模式 25 次查询。门槛为 keyword p95 100 ms、semantic p95 250 ms、hybrid p95 300 ms；shadow semantic rebuild 为 15 s、额外峰值 Go heap 128 MiB、相对原始 float64 embedding payload 的峰值磁盘放大 16 倍。这些是共享 Linux CI 的阶段完成上限，不是微基准；测试不并行，预热后使用 wall-clock p95，并以宽裕上限避免调度抖动导致假失败。改变规模、provider、索引格式、ranking 或阈值时必须升级配置版本并记录基线理由。

## 13. 第二阶段构建门槛

Linux 开发环境需要 C 编译器；所有命令都以 `CGO_ENABLED=1` 和 `sqlite_fts5` build tag 运行。默认测试不读取真实 provider 凭据，也不访问 live provider；provider contract test 只使用进程内 fake 或本机 `httptest` server。依赖下载是独立的准备步骤：

```sh
go mod download
make phase-2/check
```

`make phase-2/check` 依次覆盖 `make test`、`make test-race`、`make vet`、`make openapi`、`make schema-check`、`make restore-drill`、`make retrieval-gates` 和 `make offline-default`。`make offline-default` 明确验证默认 provider 禁用、不会读取 secret 且不会发出 HTTP 请求。`make schema-check` 在临时 data directory 中执行当前完整 Schema 初始化，并检查文档 SQL 与嵌入 SQL 完全相等；需要保留开发数据库时使用：

```sh
make schema-init DATA_DIR="$HOME/.local/share/dmem-dev"
```

漏洞扫描需要先安装当前 `govulncheck`，然后运行固定 build profile：

```sh
go install golang.org/x/vuln/cmd/govulncheck@latest
make govulncheck
```

GitHub Actions 在 Linux amd64 上使用 `go.mod` 声明的最低 Go `1.24.x` 和当前 stable Go 运行离线门槛；漏洞扫描使用当前 stable Go。CI 不注入 provider secret，dependency 和 vulnerability database 下载仅属于 CI setup/scan，不属于测试期 provider 网络访问。

## 14. 第三阶段质量与性能门槛

`make retrieval-gates` 同时运行 P2 和 P3 固定门槛。Phase 3 fixture 位于 `internal/storage/testdata/phase3-corpus-v1.json`，版本化阈值位于 `internal/storage/testdata/phase3-gates-v1.json`；当前 identity 为 `phase3-corpus-v1`、`phase3-gates-v1` 和 ranking `rrf-v2`。

质量门槛：

| Test | 门槛 |
| --- | ---: |
| P3-QUAL-001 sibling-space contamination | `0` |
| P3-QUAL-002 stale revision recall | `0` |
| P3-QUAL-003 conflict recall | `>= 1.0` |
| P3-QUAL-003 conflict reference completeness | `>= 1.0` |

性能门槛：

| Test | 规模 | 门槛 |
| --- | --- | --- |
| P3-PERF-001 multichannel query | 300 memories，25 samples | p95 `<= 200 ms`；累计 Go allocation `<= 64 MiB`；返回 candidates `<= 20` |
| P3-PERF-002 authority-derived rebuild | FTS、entity、relation、feedback aggregate、aggregate freshness | 总时间 `<= 10 s`；累计 Go allocation `<= 128 MiB`；数据库 size amplification `<= 4.0x` |
| P3-PERF-003 export/import | 至少 200 个附加 memory 对象，batch size 25 | `>= 20 objects/s`；临时空间 amplification `<= 4.0x`；checkpoints `<= 25` |

这些是共享 Linux CI 的阶段上限，不是生产容量承诺。测试使用 wall-clock 和 `runtime.MemStats.TotalAlloc` 差值；P3-PERF-002 的磁盘值是同一 SQLite 文件重建前后大小比；P3-PERF-003 的 object 数是实际 export plan 总对象数，可能高于 fixture 中的附加 memory 数。修改 corpus、ranking、rebuild 算法、logical format、规模或门槛时必须升级 gate 配置并记录理由。

## 15. 第三阶段恢复与便携门槛

- `make restore-drill` 运行 `TestBackupRestoreDrill` 和 P3-DATA 测试，验证 Phase 3 authority inventory、running job/import generation fencing、checkpoint 一致性、FTS/entity/relation/feedback/freshness 重建和 semantic 恢复路径。
- `make export-import-drill` 运行 P3-PORT-010 和 P3-PERF-003，验证 fresh instance 导入后的 authority/mapping/provenance、FTS 检索等价性、吞吐、checkpoint 和临时空间。
- 完整 `make test` 还覆盖 P3-PORT-001..011 的 dry-run、snapshot、checksum、恶意 package、幂等、批次回滚、cancel、冲突 proposal 和 stale generation；drill target 不是全部 portability security tests 的替代。
- Logical portability tests 直接调用 `internal/portability` library。它们不验证不存在的 CLI 或 HTTP export/import contract。

## 16. 第三阶段检查顺序

准备依赖后运行：

```sh
go mod download
make phase-3/check
```

`make phase-3/check` 按当前 Makefile 顺序执行：

1. `make phase-3/traceability`
2. `make phase-2/check`
3. `make export-import-drill`

其中 `phase-2/check` 依次运行 `test`、`test-race`、`vet`、`openapi`、`schema-check`、`restore-drill`、`retrieval-gates` 和 `offline-default`。Traceability gate 从[第三阶段测试矩阵](../phase-3/test-matrix.md)读取 65 个 required ID，扫描 `TestP3...` 测试函数并确认每项都有直接 assertion 或显式 Phase 2 regression gate。

所有 Go 步骤使用 `CGO_ENABLED=1` 和 `sqlite_fts5` build tag。`phase-3/check` 不包含 `make govulncheck`，漏洞数据库下载和扫描应作为独立 CI/security 步骤执行。最新阶段证据见[第三阶段质量报告](../phase-3/quality-report.md)。
