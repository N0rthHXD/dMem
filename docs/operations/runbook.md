# 运维手册

## 运行时

`dmemd` 作为本地单用户守护进程运行。数据目录必须由当前用户拥有、权限为 `0700`，
且不能经过不安全的符号链接。数据目录中的锁文件阻止第二个写入者。

守护进程只读取一个严格 TOML 文件。默认位置为
`${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`；`--config PATH` 是唯一与
设置相关的命令行选项。建议从
[config.example.toml](config.example.toml) 开始，替换示例中的绝对路径，并通过
把私有临时文件原子重命名到目标位置来发布修改。文件不包含密钥值：启用的 provider
引用一个环境变量，该变量只在策略批准某次请求之后才解析。

```sh
dmemd --config /absolute/path/config.toml daemon
```

`dmemd daemon` 组装 SQLite 能力适配器、gateway 服务、provider、持久 worker 与
Agent 中立 REST+JSON `http.Handler`，然后在 `server.listen_address` 上启动 TCP 监听
并等待关闭。默认监听 `127.0.0.1:7373`。服务层只负责传输：它应用固定的读/写/空闲
超时与请求头大小上限，可选终止 TLS（TLS 1.2+，经 ALPN 协商 HTTP/2），并按关闭宽
限期排空在途请求。**它不提供认证、网络边缘授权或限流**；非回环绑定会在日志中产生
警告。运维可见性来自进程日志、监听日志与 SQLite 中的持久状态。

启动顺序：先绑定监听地址（失败时以退出码 `2` 退出），再报告 `daemon ready`。
关闭顺序：先停止接受新连接并排空在途请求，再关闭 worker 与存储。

重要默认值：

| 设置 | 默认值 |
| --- | --- |
| 关闭宽限期 | 15 秒 |
| worker / 读连接数 | 2 |
| 队列软/硬上限 | 10,000 / 20,000 |
| 作业租约 / 超时 / 最大尝试 | 30 秒 / 2 分钟 / 3 |
| 磁盘软/硬空闲阈值 | 1 GiB / 256 MiB |
| 上下文结果/token/字符预算 | 20 条 / 4,000 token / 16,000 字符 |

HTTP 请求体上限按路由声明在当前 HTTP 契约与 `internal/protocol/httpapi` 路由元数据
中，它们不是守护进程设置。

Provider 需要显式启用、endpoint、model、默认拒绝策略与环境变量密钥引用。禁用的
provider 不解析密钥、不发起 HTTP 请求。`instance.data_dir`、
`instance.database_filename`、`jobs.worker_count`、`server.listen_address` 与
`server.tls_cert_file`/`server.tls_key_file` 是重启绑定字段；改变其中任何一项的
重载会被整体拒绝。

## 命令行

| 命令 | 说明 |
| --- | --- |
| `dmemd [--config PATH] config validate` | 离线校验配置，输出 source/effective identity。 |
| `dmemd [--config PATH] init` | 初始化一个新数据库，输出 `schema_version`。 |
| `dmemd [--config PATH] daemon` | 运行守护进程。 |
| `dmemd [--config PATH] backup --target DIR` | 创建一致性备份。 |
| `dmemd [--config PATH] restore --source DIR --confirm-replace` | 用备份替换数据目录。 |

`backup`、`restore`、`init` 接受 `--timeout`（默认 5 分钟，必须大于零且不超过
1 小时）。退出码：用法错误与守护进程启动失败为 `2`，其他维护失败为 `1`，成功为
`0`。维护失败在 stderr 输出 `maintenance: <code>`，例如 `maintenance:
offline_required`、`maintenance: unsafe_path`、`maintenance: database_exists`。

没有 `status` 或 `reload` 子命令：`SIGHUP` 请求一次串行化重载流水线，重复信号
会被合并。失败或被拒绝的重载保留先前完整快照；修正文件后再次发送信号即可。

## 健康与故障

由于运行时没有专用健康检查端点，健康状态通过日志与持久状态观察。请区分
权威故障与派生索引故障：

- SQLite 完整性、schema 或权威检查失败时，停止写入并保留数据目录。
- 派生索引过时或不可用时，保持权威不变并重新运行相应重建。
- 通过持久作业跟踪已在进行中的重建，而不是制造重建风暴。
- 让启动时的租约恢复来围栏被中断的 worker；永不直接编辑作业状态或 generation。
- 达到磁盘硬阈值时，写入会失败，而只读查询仍可继续。

SQLite 完整性检查在启动后于后台运行，因此大型数据库（或 FTS5 索引）不会阻塞
就绪。失败时守护进程记录错误并使存储不可用。此时应重启或停止守护进程、保留
数据目录，然后从副本诊断再恢复写入。

派生索引重建（FTS、语义、图谱与反馈聚合）是持久内部作业，不属于初始 Agent
HTTP 契约。

语义向量以紧凑 `float32` BLOB 存储在权威表 `memory_embeddings` 中，并通过
sqlite-vec 精确余弦检索。守护进程在启动时校验 sqlite-vec 扩展（`vec_version()`
加 `vec_length()` 健全性探针），不可用时拒绝启动，因此缺失或损坏的原生构建会
明确失败，而不是返回错误的检索结果。要诊断运维机器上链接的扩展，可使用相同的
`sqlite_fts5` 构建标签对权威数据库执行 `SELECT vec_version()`。

## 初始化、备份与恢复

只初始化新数据库：

```sh
dmemd --config /absolute/path/config.toml init
```

没有 schema 迁移命令。schema 不兼容的数据库会被拒绝，必须由新初始化的数据目录
替代。`init` 拒绝已存在的数据库，并要求守护进程离线（锁可用）。

备份使用 SQLite 在线备份 API，可以在守护进程运行时执行。目标必须不存在，其父
目录必须是当前用户拥有的私有目录：

```sh
install -d -m 0700 "$HOME/dmem-backups"
dmemd --config /absolute/path/config.toml backup \
  --target "$HOME/dmem-backups/backup-$(date +%Y%m%dT%H%M%S)"
```

恢复会替换整个数据目录。请先停止守护进程：

```sh
dmemd --config /absolute/path/config.toml restore \
  --source "$HOME/dmem-backups/backup-20260826T120000" \
  --confirm-replace
```

恢复会在同一文件系统的 staging 目录中校验校验和、精确 schema、SQLite 完整性、
外键、权威计数、导入检查点与领域引用，然后才发布。它会围栏被中断的作业/导入，
重建权威派生的投影，清理过时的语义激活并入队语义恢复。恢复后：启动守护进程，
检查日志与持久作业，然后通过 gateway 或 HTTP 适配器测试运行有代表性的 scope
召回。在已配置的 embedding provider 完成工作之前，语义状态可以保持缺失。

逻辑导出/导入是独立的库工作流，见
[逻辑可移植性](../storage/logical-portability.md)；没有导出/导入 CLI 或 HTTP
路由。

## 隐私

默认部署是本地且离线的。日志不得包含密钥、Authorization 请求头、原始记忆/事件
内容、反馈备注、查询文本或 provider 响应。provider 允许列表在出网前限制空间、
来源/内容类型与敏感度。备份与逻辑包需要与权威数据库相同的文件系统保护。
