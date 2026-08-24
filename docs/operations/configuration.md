# dMeM 生产配置

## 1. 来源和校验

配置来源为内置默认值、指定的环境变量和启动参数；启动参数优先。当前版本不读取配置文件，也不提供运行时配置查看接口。未知参数、非法类型和越界值导致启动失败。持续时间使用 Go duration，容量使用整数 bytes。

## 2. 路径

| 配置 | 默认值 | 规则 |
| --- | --- | --- |
| data_dir | 用户数据目录下 `dmem` | 必须归当前用户所有 |
| runtime_dir | 用户运行时目录下 `dmem` | 模式 0700 |
| database_path | `${data_dir}/dmem.db` | 显式值覆盖默认拼接 |
| endpoint | `${runtime_dir}/dmem.sock` | socket 模式 0600 |
| log_dir | `${data_dir}/logs` | 首版可只输出 stderr |

路径转为绝对规范路径，不跟随到其他用户拥有的 symlink。socket 路径超过平台限制时启动失败。

## 3. 服务

| 配置 | 默认 | 范围 |
| --- | --- | --- |
| request_timeout | 30s | 1s..5m |
| max_request_size | 4194304 | 1 KiB..16 MiB |
| max_response_size | 8388608 | 1 KiB..32 MiB |
| max_connections | 64 | 1..1024 |
| shutdown_grace_period | 15s | 1s..2m |

## 4. SQLite 固定策略

第一阶段不可关闭：

```text
foreign_keys=ON
journal_mode=WAL
synchronous=FULL
busy_timeout=5s
trusted_schema=OFF
write connections=1
read connections=max(1, worker_count)
```

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| integrity_check | quick | 启动 quick_check；显式诊断可 full |
| wal_checkpoint_interval | 30s | 被动 checkpoint |
| expected_schema_version | 1 | 编译时固定，不允许覆盖 |

## 5. 任务

| 配置 | 默认 | 范围/语义 |
| --- | --- | --- |
| worker_count | 2 | 1..32；0 非法 |
| queue_soft_limit | 10000 | 1..1000000 |
| queue_hard_limit | 20000 | 必须大于 soft |
| lease_duration | 30s | 5s..10m |
| per_job_timeout | 2m | 不小于 lease duration |
| max_attempts | 3 | 1..20 |
| retry_initial_backoff | 1s | 100ms..1m |
| retry_max_backoff | 5m | 不小于 initial |

queue soft limit 统计 pending、waiting 和 retrying；queue hard limit 统计全部非终态任务，即 pending、waiting、running 和 retrying。

## 6. 检索

| 配置 | 默认 | 范围 |
| --- | --- | --- |
| default_result_limit | 10 | 1..100 |
| max_result_limit | 100 | 1..200 |
| default_max_chars | 12000 | 1..100000 |
| retrieval_timeout | 5s | 100ms..30s |

FTS 始终启用。semantic/hybrid 仅在 embedding provider 显式启用且当前语义索引可用时执行；entity 和 relation 配置不存在。

## 7. Processor 与 Embedding Provider

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| processing_provider | disabled | 自动提取使用的 OpenAI-compatible provider |
| embedding_provider | disabled | embedding 和 semantic/hybrid 查询使用的 OpenAI-compatible provider |
| processing_version | phase2-noop-v1 | `--processing-version` / `DMEM_PROCESSING_VERSION`；进入 job identity |

`processing_version` 必须是 1 到 128 个 ASCII 字母、数字、点、下划线或连字符组成的稳定标识，且首字符必须是字母或数字。优先级为启动参数、非空环境变量、内置默认值。默认值只标识第二阶段 no-op processor；启用 processing provider 时必须通过启动参数或非空环境变量显式配置非默认稳定版本，并在 processor 规则、model 或影响候选结果的配置变化时更新。只启用 embedding provider 不要求修改 processing version。

两类 provider 独立启用并使用独立 client、endpoint、model、secret 引用、并发限制和速率限制。processing 配置使用 `--processing-provider-*`，embedding 配置使用 `--embedding-provider-*`。两者共同支持：

```text
enabled
kind
endpoint
model
secret-env
allowed-spaces
allowed-content-types
allowed-source-types
allowed-sensitivities
telemetry-disabled
egress-policy-version
max-content-chars
max-response-size
request-timeout
max-concurrent
requests-per-minute
```

只有 processing provider 额外支持 `--processing-provider-max-context-items`。旧的 `--provider-*` 参数不再接受，也不作为兼容别名。`secret-env` 保存的是环境变量名称；secret 值只在实际请求前解析，provider 禁用时不读取。

四种组合均有效：两者都关闭、仅 processing、仅 embedding、两者都开启。processing provider 的变化不使现有 embedding 失效；embedding kind/model 的变化形成新的 semantic job identity，并通过恢复任务重新生成 embedding 和索引。

daemon 重启不改变已创建 job 固定的版本。使用新版本重启后 replay 同一 event 会创建独立 job 和结果；相同版本 replay 返回该版本已有的 active job 或终态结果，不执行版本兼容或迁移逻辑。

## 8. 磁盘阈值

| 配置 | 默认 | 行为 |
| --- | --- | --- |
| disk_soft_free | 1 GiB | 非关键任务暂停，ingest 可 deferred |
| disk_hard_free | 256 MiB | 拒绝写入，保留只读查询 |

阈值按数据库所在文件系统可用空间判断。hard limit 下不能承诺执行需要 SQLite 写入的删除。

## 9. 动态配置

第一阶段除日志级别外不支持运行时修改。其他变化需要重启。
