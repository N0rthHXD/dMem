# dMeM 第一阶段配置

## 1. 来源和校验

```text
内置默认值 → 配置文件 → 环境变量 → 启动参数
```

未知字段、非法类型和越界值导致启动失败。持续时间使用 Go duration，容量使用整数 bytes。最终配置可查看，但敏感值脱敏。

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

第一阶段 FTS 始终启用；semantic、entity 和 relation 配置不存在。

## 7. Processor

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| processor | none | no-op processor |
| processing_version | phase1-noop-v1 | 进入 job identity |

远程 provider 不属于第一阶段。

## 8. 磁盘阈值

| 配置 | 默认 | 行为 |
| --- | --- | --- |
| disk_soft_free | 1 GiB | 非关键任务暂停，ingest 可 deferred |
| disk_hard_free | 256 MiB | 拒绝写入，保留只读查询 |

阈值按数据库所在文件系统可用空间判断。hard limit 下不能承诺执行需要 SQLite 写入的删除。

## 9. 动态配置

第一阶段除日志级别外不支持运行时修改。其他变化需要重启。
