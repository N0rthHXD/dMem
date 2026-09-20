# 配置参考

dMem 的守护进程设置来自**一个**严格 TOML 文件。它决定数据目录、队列与 worker
参数、磁盘阈值、provider、策略、排序与上下文预算。没有针对单个设置的命令行
标志或环境变量覆盖；`--config PATH` 只用于选择文件本身。

## 文件位置与加载

默认路径为 `${XDG_CONFIG_HOME:-$HOME/.config}/dmem/config.toml`。使用
`--config /absolute/path/config.toml`（或 `--config=/absolute/path`）选择其他
实例。

加载规则：

- 解码严格：未知字段、重复键与语法错误都会使整个文件被拒绝，并给出诊断路径。
- 文件必须是当前用户拥有的普通文件，不能是符号链接；最大 1 MiB。
- `config_version` 必须等于 `1`。
- 必需 section 与键必须存在；可选的调优 section 省略时使用默认值。

离线校验：

```sh
dmemd --config /absolute/path/config.toml config validate
```

成功时输出 `{"source_identity": "...", "effective_identity": "..."}`。

## 必需配置

### `[instance]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `data_dir` | string | 必需，必须是绝对路径。 |
| `database_filename` | string | 必需，只能是文件名，不能包含目录。 |

数据目录必须是当前用户拥有、权限 `0700`、且不经过不安全符号链接的目录。

### `[server]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `listen_address` | string | 可选，`host:port`，默认 `127.0.0.1:7373`。host 为空、IP 字面量或 `localhost`；端口 `1`–`65535`。 |
| `tls_cert_file` | string | 可选，绝对路径，非符号链接；与 `tls_key_file` 同时出现。 |
| `tls_key_file` | string | 可选，绝对路径，非符号链接；与 `tls_cert_file` 同时出现。 |
| `shutdown_grace_period` | duration | 必需，非零。 |

服务层在 `listen_address` 上启动 TCP 监听。设置 TLS 后启用 TLS 1.2+，并通过 ALPN
协商 HTTP/2；明文监听只提供 HTTP/1.1。读头、读、写、空闲超时与请求头大小上限是
传输加固默认值，不是配置项。服务层不提供认证、网络边缘授权或限流：非回环绑定
会在日志中产生警告。

### `[jobs]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `worker_count` | int | `1`–`128`。 |
| `queue_soft_limit` | int | `≥ 1` 且小于硬上限。 |
| `queue_hard_limit` | int | `1`–`1,000,000`。 |
| `max_attempts` | int | `1`–`20`。 |
| `lease_duration` | duration | `5s`–`10m`。 |
| `per_job_timeout` | duration | 不小于 `lease_duration`。 |
| `retry_initial_backoff` | duration | 大于零。 |
| `retry_max_backoff` | duration | 不小于 `retry_initial_backoff`。 |

### `[storage]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `disk_soft_free_bytes` | uint64 | 大于零。 |
| `disk_hard_free_bytes` | uint64 | 大于零且小于软阈值。 |

### `[processing]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `version` | string | 必需。启用处理 provider 时不得为 `builtin-noop-v1`。 |
| `graph_enabled` | bool | 可选，默认 `true`。为 `false` 时处理照常运行但不做图谱抽取。 |

## Provider 配置

处理与 embedding provider 默认禁用。省略整个 provider section 等同于
`enabled = false`。禁用状态下不解析密钥、不发起 HTTP 请求。

### `[processing.provider]` 与 `[embedding.provider]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `enabled` | bool | 显式启用 provider。 |
| `kind` | string | 启用时必须为 `openai-compatible`。 |
| `endpoint` | string | 启用时必须是 https URL，且不含用户信息、查询或片段。 |
| `model` | string | 启用时必需。 |
| `telemetry_disabled` | bool | 启用时必须为 `true`。 |

### `[<provider>.secret]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `source` | string | 必须为 `environment`。 |
| `name` | string | 环境变量名。 |

密钥值永不写入配置文件、数据库、状态或日志。它只在策略批准某次请求之后从环境
变量解析。

### `[<provider>.limits]`

处理 provider：

| 键 | 含义 |
| --- | --- |
| `max_egress_input_chars` | 单次出网输入字符上限。 |
| `max_aggregate_input_chars` | 聚合输入字符上限。 |
| `max_context_items` | 上下文条目上限。 |
| `max_candidate_chars` | 单个候选字符上限。 |
| `max_total_candidate_chars` | 候选总字符上限。 |
| `max_response_bytes` | 响应字节上限。 |
| `request_timeout` | 请求超时（启用处理 provider 时不小于 `1s`）。 |
| `max_concurrent` | 并发上限。 |
| `requests_per_minute` | 每分钟请求上限。 |

embedding provider 使用相同的 `max_egress_input_chars`、`max_response_bytes`、
`request_timeout`、`max_concurrent`、`requests_per_minute`，但没有聚合与候选
相关字段。

### `[embedding.provider.options]`

| 键 | 含义 |
| --- | --- |
| `dimensions` | 向量维度。 |
| `query_instructions` | 查询前缀指令。 |

### `[<provider>.policy]`

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `default` | string | 启用时必须为 `deny`。 |
| `query` | string | 仅 embedding，语义查询的单独决策。 |
| `content_types` | string[] | 允许的来源内容类型，启用时非空。 |
| `source_types` | string[] | 允许的来源类型，启用时非空。 |
| `sensitivities` | string[] | 允许的敏感度，启用时非空。 |
| `assignments` | table[] | 显式分配，启用时非空。 |

每个 `[[<provider>.policy.assignments]]` 条目：

| 键 | 类型 | 规则 |
| --- | --- | --- |
| `selector` | string | `space`、`space_type` 或 `external_id`。 |
| `space_id` | string | `selector = "space"` 时使用。 |
| `space_type` | string | `selector = "space_type"` 时使用。 |
| `external_id` | string | `selector = "external_id"` 时使用。 |
| `effect` | string | `allow` 或 `deny`。 |
| `include_descendants` | bool | 是否把决策继承给后代空间。 |
| `priority` | int | 同等具体度下的优先级。 |

匹配规则：当前空间覆盖优先，否则最近祖先优先；同等具体度下显式拒绝优先；未匹配
的空间默认拒绝；兄弟空间永不继承。

## 可选调优配置

以下 section 可以整体省略，此时使用括号中的默认值。

### `[consolidation]`

| 键 | 默认 | 规则 |
| --- | --- | --- |
| `version` | `consolidation-v1` | 非空。 |
| `max_candidates` | `32` | `1`–`256`。 |
| `exact_hash_enabled` | `true` | 精确哈希整合开关。 |
| `same_source_event_cosine` | `0.90` | `0`–`1`。 |
| `same_subject_kind_cosine` | `0.94` | `0`–`1`，且不小于 `same_source_event_cosine`。 |

### `[semantic]`

| 键 | 默认 | 规则 |
| --- | --- | --- |
| `calibration_version` | `semantic-calibration-v1` | 非空。 |
| `absolute_floor` | `0.35` | `0`–`1`。 |
| `top_margin` | `0.08` | `0`–`1`。 |

### `[graph]`

| 键 | 默认 | 规则 |
| --- | --- | --- |
| `policy` | `features_only` | `off`、`features_only`、`direct` 或 `bounded_path`。 |
| `min_entity_confidence` | `0.60` | `0`–`1`。 |
| `min_fact_confidence` | `0.70` | `0`–`1`。 |
| `boost_cap` | `0.08` | 大于 `0` 且不超过 `1`。 |

### `[ranking]`

| 键 | 默认 | 规则 |
| --- | --- | --- |
| `version` | `ranking-weights-v1` | 非空。 |
| `semantic_weight` | `1.0` | `0`–`10`，且大于 `0`。 |
| `keyword_weight` | `0.35` | `0`–`10`。 |
| `exact_weight` | `0.15` | `0`–`10`。 |
| `temporal_weight` | `0.10` | `0`–`10`。 |
| `quality_weight` | `0.10` | `0`–`10`。 |
| `agreement_weight` | `0.05` | `0`–`10`。 |

### `[context]`

| 键 | 默认 | 规则 |
| --- | --- | --- |
| `max_context_tokens` | `4000` | `1`–`1,000,000`。 |
| `max_context_chars` | `16000` | 不小于 `max_context_tokens`。 |
| `max_results` | `20` | `1`–`1000`。 |

完整可复制示例见 [config.example.toml](config.example.toml)。

## 配置身份与重载

每次加载都会计算：

- **SourceIdentity**：原始文件字节的 sha256。
- **EffectiveIdentity**：规范化 JSON 的 sha256。
- 独立的 capability、compatibility 与 policy 身份。

`SIGHUP` 请求一次串行化的重载；重复信号会被合并。结果可能是：

| 结果 | 含义 |
| --- | --- |
| `activated` | 有效变更已原子激活，generation 递增。 |
| `no_change` | 有效身份未变，先前快照保持不变。 |
| `rejected` | 文件无效或包含重启绑定变更，先前快照保持不变。 |
| `failed` | 激活后的应用失败，运行时回退到先前快照。 |

重启绑定字段为 `instance.data_dir`、`instance.database_filename`、`jobs.worker_count`、
`server.listen_address` 与 `server.tls_cert_file`/`server.tls_key_file`。重载改变其中
任何一项都会拒绝整个候选文件，错误码为 `config_restart_required`。运行时没有
`reload` 子命令或重载 HTTP 路由；只使用 `SIGHUP`。
