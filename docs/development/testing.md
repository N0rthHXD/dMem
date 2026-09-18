# 测试与质量门禁

Linux 测试需要 C 编译器、`CGO_ENABLED=1` 与 `sqlite_fts5` 构建标签。向量存储
静态链接 sqlite-vec（`github.com/asg017/sqlite-vec-go-bindings` `v0.1.6`）；其
C 源码通过 `sqlite3_auto_extension` 编译进同一二进制，因此不需要外部 `.so` 或
运行时扩展加载。测试使用确定性 provider 或本地 `httptest` 服务器，不需要真实
provider 凭据。

## 核心检查

```sh
go mod download
make build
make test
make test-race
make vet
```

`make schema-check` 验证文档化 SQL 与内嵌 schema 完全一致，并初始化一个临时
数据库。

## Gateway 与协议测试

- Gateway 模块测试只为模块声明的能力使用窄 fake 端口，覆盖语义校验、错误分类、
  取消、幂等、编排与不变量，尽量不涉及 HTTP 或 SQLite。
- `internal/gateway/arch_test.go` 强制 gateway 依赖规则：不导入 `net/http`、不
  导入协议包、不导入 storage 或 jobs、不导入兄弟模块，且不导出暴露 `storage.*`
  选择器的契约。
- `internal/protocol/httpapi` 测试使用 fake gateway 模块，覆盖严格单值 JSON
  解码、DTO 转换、路由一致性（调用方身份与幂等策略）、响应编码、gateway 错误
  映射、panic 恢复、取消与安全错误。
- HTTP/2 兼容性与多路并发通过配置了 `EnableHTTP2` 的测试专用 `httptest.Server`
  证明。测试不添加任何生产监听器、TLS 或 h2c 配置。
- `internal/protocol/httpapi/arch_test.go` 强制协议树不导入 storage、jobs、
  provider、config、retrieval 或应用包，且永不拥有监听器或检查 HTTP 协议版本。

## 恢复与质量

```sh
make schema-check
make restore-drill
make portability-drill
make quality
make performance
make offline-default
make config-check
```

- `restore-drill` 创建并恢复真实的一致性快照，校验权威与检查点，围栏被中断的
  工作，并检查派生状态恢复。
- `portability-drill` 检查全新实例的逻辑导入、映射、溯源、检索等价性、检查点、
  吞吐量与临时空间边界。完整包测试覆盖畸形与恶意输入、回滚、取消、幂等与过时
  generation。
- `quality` 针对固定语料运行关键词、语义、混合、scope 隔离、冲突引用、反馈
  投毒与过时修订行为，并强制广泛的共享 Linux CI 质量阈值。
- 知识图谱测试覆盖图谱抽取调度/幂等、provider 输出校验（未知字段、rune 范围、
  非法谓词、NaN 置信度、自关系）、构建期实体解析、自动确认与待审核、事实时间
  演化、实体事实/列表/提案行为、查询期实体解析（英文与中文别名）、0/1/2 跳与
  记忆关系召回、图谱游标失效，以及并发事实确认。
- `performance` 检查延迟、分配、重建成本、可移植性吞吐量与存储增长的共享 Linux
  CI 边界。这些是回归门禁，不是生产容量承诺。墙钟与分配门禁在默认与 race 套件
  中跳过；它们只在设置 `DMEM_PERFORMANCE=1` 时运行，`performance` 目标会设置该
  变量。
- `offline-default` 验证禁用的 provider 不读取密钥、不发起 HTTP 请求。
- `config-check` 验证 `docs/operations/config.example.toml` 可以通过校验。

### 配置与调和

配置重载与调和测试位于 `internal/daemon` 与 `internal/storage`。它们使用托管
守护进程（`StartManaged` 配合 `config.NewManager`）与包内 `reloadConfig` 辅助
函数写入严格 TOML 文件，在不使用真实 provider 凭据的情况下切换 provider 启用
状态与策略分配。

- `TestManagedDaemonReloadsCapabilitiesAtomically` 与
  `TestSC014ReloadBecomesEffectiveWithinTenSeconds` 验证原子重载、无变化、拒绝，
  以及 10 秒的 SC-014 时间边界。
- `TestPolicyCorrectionRecoversBlockedProcessing` 与
  `TestPolicyExpansionDoesNotDuplicateCompletedWork` 证明策略扩展会把
  `blocked_policy` 作业翻转为 `pending`，而不改变处理兼容身份、也不重复已完成
  工作。
- `TestReconciliationResumesAfterDaemonRestart` 证明有效配置身份、被阻塞工作、
  调和游标与语义 provider 状态能跨守护进程重启存活并正确恢复。
- `TestSC004ReconciliationEnqueuesEligibleJobsPromptly` 验证 30 秒的 SC-004 边界：
  95% 的新符合条件阻塞作业及时进入调和，同时已完成作业不被重复。

确定性 provider 演练使用进程内 `heldProcessor`（阻塞直到释放通道关闭）与
`httptest.NewTLSServer` stub；它们永不访问真实端点。调和恢复由守护进程维护循环
（1 秒间隔）或直接仓储调用（`ClaimReconciliationRun` + `Reconcile`）驱动，以便
精确控制时序。

固定检索语料与版本化门禁文件位于 `internal/storage/testdata`。修改语料、排序、
索引格式、可移植格式、规模或阈值时，必须更新对应身份并记录可复现基线。

## 漏洞扫描

漏洞扫描独立执行，因为它需要下载漏洞数据：

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
make govulncheck
```

`make check` 运行完整的必需套件，但不包括单独调用的 `performance` 与
`govulncheck` 目标。性能门禁也在 `test` 与 `test-race` 中跳过；受控 runner 或
CI 作业通过 `DMEM_PERFORMANCE=1` 显式启用。
