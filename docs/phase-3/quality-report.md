# dMeM 第三阶段质量报告

## 结论

第三阶段计划范围和 65 个 required test ID 映射已实现。2026-08-25 的最终 `make phase-3/check` 执行通过 traceability、第二阶段完整回归、test、race、vet、OpenAPI、Schema initialization、backup/restore、logical export/import、P2/P3 retrieval gates 和默认离线检查，当前阶段验收状态为通过。

该结论是当前未发布开发基线的阶段验收，不是发布声明、生产容量承诺或向后兼容承诺。可执行契约仍以当前 [OpenAPI](../api/openapi.yaml)、[Schema v1](../storage/schema-v1.sql) 和 [Makefile](../../Makefile) 为准。

## 执行环境

```text
OS: Linux
Go profile: CGO_ENABLED=1, build tag sqlite_fts5
Commands: make phase-3/check; uncached package verification
Gate identity: phase3-gates-v1 / phase3-corpus-v1 / rrf-v2
Date: 2026-08-25
```

`phase-3/traceability`、所有 drill 和质量 gate 使用 `-count=1`；最终还执行了 uncached tagged package verification。性能和恢复数据来自实际执行，不依赖 Go test cache。

## 检查结果

| Check | 结果 | 覆盖 |
| --- | --- | --- |
| `make phase-3/traceability` | PASS | 65 个 required ID 均映射到真实断言或第二阶段回归 gate |
| `make test` | PASS | 全部 package unit/integration/contract tests |
| `make test-race` | PASS | 全部 package race run |
| `make vet` | PASS | 当前 tagged build profile |
| `make openapi` | PASS | handler 与 OpenAPI contract，62 operations / 215 operation-status combinations |
| `make schema-check` | PASS | 文档/嵌入 SQL 一致；临时空库初始化 |
| `make restore-drill` | PASS（强制重跑） | backup/restore、P3 authority、job/import fencing、派生恢复 |
| `make export-import-drill` | PASS（强制重跑） | fresh import/retrieval equivalence、P3-PERF-003 |
| `make retrieval-gates` | PASS（强制重跑） | P2/P3 quality and performance gates |
| `make offline-default` | PASS（强制重跑） | 默认 provider 禁用，不解析 secret、不发 HTTP |

`phase-3/traceability` 使用与生产测试一致的 `CGO_ENABLED=1` 和 `sqlite_fts5` build profile。`make phase-3/check` 不包含 `make govulncheck`；漏洞扫描不属于本报告的通过项。

## 质量门槛

| 指标 | 门槛 | 本次结果 |
| --- | ---: | ---: |
| Sibling-space contamination | `0` | PASS，`0` |
| Stale revision recall | `0` | PASS，`0` |
| Conflict recall | `>= 1.0` | PASS |
| Conflict reference completeness | `>= 1.0` | PASS |

## 性能门槛

| 指标 | 门槛 | 本次观测 |
| --- | ---: | ---: |
| P3 multichannel query p95，300 memories/25 samples | `<= 200 ms` | `6.801 ms` |
| P3 query cumulative allocation | `<= 64 MiB` | `22,621,928 B`（约 `21.6 MiB`） |
| P3 query candidates | `<= 20` | `20` |
| Authority-derived rebuild time | `<= 10 s` | `17.633 ms` |
| Authority-derived rebuild allocation | `<= 128 MiB` | `24,336 B` |
| Authority-derived database size amplification | `<= 4.0x` | `1.00x` |
| Portability throughput | `>= 20 objects/s` | `1526.2 objects/s` |
| Portability temporary amplification | `<= 4.0x` | `0.00x` |
| Portability checkpoints | `<= 25` | `16` |

P3-PERF-003 的实际 export plan 包含 409 个对象；fixture 额外创建 200 个 memory 及其 revision。数值只用于当前 CI 风格环境的回归比较，不外推生产负载。

## 能力证据

- P3-CONTRACT/ENTITY/REL/EVOLVE/CONFLICT/FEEDBACK/RANK/AGG 场景由完整 `make test` 覆盖，测试 ID 与[测试矩阵](test-matrix.md)对应。
- P3-DATA-001..003 由 `make restore-drill` 强制重跑。
- P3-PORT-010 和 P3-PERF-003 由 `make export-import-drill` 强制重跑；其余 P3-PORT security/checkpoint/idempotency 场景由完整 test suite 覆盖。
- P3-QUAL-001..003 和 P3-PERF-001..002 由 `make retrieval-gates` 强制重跑。

## 公开边界

- Phase 3 entity、relation、evolution、conflict、feedback、aggregate 和 rebuild 操作已在 UDS HTTP OpenAPI 中声明。
- Offline `dmemd` 子命令只有 `init`、`backup` 和 `restore`。
- Logical export/import 已实现并经过测试，但当前入口仅为 `internal/portability` Go library 和 `make export-import-drill`，没有公开 CLI 或 HTTP 路径。
