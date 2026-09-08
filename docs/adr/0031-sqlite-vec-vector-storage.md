# ADR 0031：sqlite-vec 精确向量存储与检索

- 状态：accepted
- 日期：2026-09-08

## 背景

0019 采用纯 Go、进程内的 JSON/float64 精确 cosine 索引，通过 shadow 文件 + `active.json` 原子激活。它满足正确性，但在 10 万 × 1024 的目标容量下，存在明显开销：每次查询都要把完整索引深拷贝到内存、把 `[]float64` 编码为 JSON 存储、在 Go 中做标量全扫描，并在激活时全量排序截断。这些开销随语料大小线性增长，成为内存和延迟的主要来源。

## 决策

采用 sqlite-vec 的精确标量能力，把向量作为紧凑的 `float32 BLOB` 直接存储在权威 SQLite 表 `memory_embeddings` 中，并在同一条查询里完成权威过滤 + 精确 cosine 距离 + 排序 + top-k。不再维护独立的 JSON 派生索引。

具体要点：

- **依赖**：`github.com/asg017/sqlite-vec-go-bindings/cgo`，固定 `v0.1.6`（SQLITE_VEC_VERSION `v0.1.6`）。通过 `sqlite3_auto_extension` 静态编译进进程，随每个 SQLite 连接加载，禁止任何运行时动态扩展加载（与 0014 一致）。
- **存储**：`memory_embeddings.vector_json TEXT` 替换为 `vector_blob BLOB NOT NULL`，并用 `vec_length(vector_blob) = dimension` 做完整性约束。写入用 `SerializeFloat32` 得到 little-endian `float32` blob；读取时由 `vec_length` / `vec_distance_cosine` 直接解析。
- **检索**：在权威表上执行 `vec_distance_cosine(e.vector_blob, ?)`，把 current revision、content hash、searchable、active/contested、requested states/types、valid_from/valid_until、space scope、embedding compatibility identity 与 policy generation eligibility 全部放进同一条 WHERE，在 top-k 之前过滤，再 `ORDER BY distance, memory_id, revision_id LIMIT recallLimit`。
- **相似度**：`similarity = 1 - distance`，沿用 `minimumSemanticScore = 0.2` 的语义。稳定 tie 使用 `memory_id ASC, revision_id ASC`。
- **权威性**：SQLite 仍是权威。`memory_embeddings` 通过外键绑定真实 memory/revision，query 时用 `e.content_hash = p.content_hash`、`e.compatibility_identity = ?`、`e.dimension = ?` 排除 stale revision、phantom vector、错误 content hash 与不兼容 identity。最终结果仍回查 authority。
- **Generation**：`semantic_index_metadata.active_version` 是实时 authority semantic corpus 的单调 generation，不是不可变向量快照。已发布 generation 存在后，projection 检索资格、当前 policy eligibility 或 compatible vector membership 的事务内变化都会推进 generation；cursor 绑定该值，变化后旧 cursor 失效。
- **重建**：`RebuildSemantic` 不再解析 JSON、不再构建派生索引。补齐缺失 embedding 后，用集合级 count 校验 corpus，并推进 generation、发布当前 provider/model/compatibility/policy/dimension identity。失败不回滚已由 authority 事务提交的实时 corpus 变化，而是保持 `stale`/`degraded`，直到 completeness barrier 重新建立。
- **可查询性**：semantic metadata 的 provider、model、compatibility identity、policy generation 和 dimension 必须与当前 runtime/query 一致。identity 不一致时 semantic 不可用；hybrid 明确降级到可用 channel，不用旧 identity 冒充当前语义结果。
- **降级**：启动时通过 `vec_version()` 与 `vec_length()` 做 capability check，sqlite-vec 不可用时 `Open` 明确失败，不静默产生错误检索结果。

## 为什么精确检索而不是 ANN

目标容量约 10 万 × 1024。精确 cosine 全扫描在本地单进程 SQLite 中可接受（见 performance 基准），且语义可证明正确：eligibility/authority 过滤发生在 top-k 之前，不引入 HNSW/IVF 的近似召回与额外索引一致性问题。ANN 的收益在 10 万量级不成立，其成本（近似误差、索引生命周期、dimension 固定、元数据过滤限制）反而更高。

## 为什么禁止运行时动态扩展加载

动态加载任意本地代码与 0014 冲突，且难以审计。sqlite-vec 以编译期确定、版本固定、许可证已审查的 CGO 静态链接形式进入进程，符合 0011 与 0014。

## 依赖与许可证

- `github.com/asg017/sqlite-vec-go-bindings`：MIT 许可（binding），内嵌 sqlite-vec 源码为 MIT/Apache-2.0 双许可。
- 引入 C 源码编译（`sqlite-vec.c`），需要 C 工具链、`CGO_ENABLED=1`，与既有 `sqlite_fts5` 一致。
- 主目标 Linux amd64；sqlite-vec 使用纯 C 标量实现，兼容 Linux arm64，但 arm64 未纳入主发布矩阵。

## pre-v1 版本锁定与升级策略

sqlite-vec 处于 pre-v1（`v0.x`），API 可能变动。项目固定 `v0.1.6`，升级时必须：核对 `vec_*` 函数名与 BLOB 格式，运行 `internal/storage` 全部测试、performance 基准与 restore-drill，并更新本文档。禁止在未验证的情况下跟随新版本。

## 边界

- `memory_embeddings.vector_blob` 是权威向量来源，embedding 行不携带快照 index version。backup 仅保存 SQLite authority，restore 后从 blob 重建语义能力（embed 缺失 + rebuild）。
- 不保留旧 JSON/float64 索引兼容读取；旧索引是已删除的可重建派生数据。
- 不引入独立 PostgreSQL、Qdrant、DuckDB 或其他向量服务。

## 影响

- 删除 `internal/vector` 包及其 JSON 索引生命周期。
- 查询不再深拷贝完整 corpus；heap 分配不再随向量总字节数线性增长。
- 磁盘上向量以 float32 量级存储，而非 JSON/float64。
- 0019 被取代（见下）。

## 关联

- 取代 ADR 0019。
- 实现 ADR 0014 的向量扩展白名单落地。
- 与 ADR 0005、0015、0021 保持一致。
