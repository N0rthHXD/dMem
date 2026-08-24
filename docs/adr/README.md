# 架构决策记录

| ADR | 决策 |
| --- | --- |
| [0001](0001-local-single-user-daemon.md) | 本地单用户 daemon |
| [0002](0002-go-and-sqlite-baseline.md) | Go + SQLite 基础框架 |
| [0003](0003-raw-events-as-source-of-truth.md) | 原始事件作为事实依据 |
| [0004](0004-versioned-memory-revisions.md) | 版本化记忆修订 |
| [0005](0005-derived-indexes-are-rebuildable.md) | 派生索引可重建 |
| [0006](0006-asynchronous-memory-processing.md) | 异步记忆处理 |
| [0007](0007-trusted-agents-first-phase.md) | 第一阶段 Agent 全部可信 |
| [0008](0008-local-only-default-exposure.md) | 默认仅本地暴露 |
| [0009](0009-no-autonomous-destructive-memory-updates.md) | 禁止自主破坏性更新 |
| [0010](0010-hybrid-retrieval-and-context-budget.md) | 混合检索与上下文预算 |
| [0011](0011-cgo-sqlite-driver.md) | 允许 CGO SQLite 驱动 |
| [0012](0012-orm-boundaries.md) | 允许 ORM 并限制使用边界 |
| [0013](0013-local-http-over-unix-socket.md) | HTTP/JSON over Unix Domain Socket |
| [0014](0014-sqlite-extension-policy.md) | SQLite 扩展白名单 |
| [0015](0015-no-schema-migration-in-phase-one.md) | 未发布开发阶段不实现 SQLite schema migration |
| [0016](0016-phase-two-schema-migrations.md) | 第二阶段前向事务 migration（rejected） |
| [0017](0017-independent-review-candidates.md) | 独立候选记忆与人工审核 |
| [0018](0018-in-process-provider-ports.md) | 进程内强类型 provider 端口 |
| [0019](0019-go-vector-index.md) | Go 内置可重建向量索引 |
| [0020](0020-versioned-rrf-ranking.md) | 固定版本 RRF 混合排序与 cursor |
| [0021](0021-offline-backup-restore.md) | 一致性备份与离线 staging restore |
| [0022](0022-explicit-openai-compatible-providers.md) | 显式 OpenAI-compatible provider |
| [0023](0023-entity-identity-and-mentions.md) | 稳定 Entity identity 与 revision-level mention |
| [0024](0024-authoritative-revision-relations.md) | 独立权威 revision relation |
| [0025](0025-conflict-cases-and-contested-state.md) | Conflict case 与 contested projection 分工 |
| [0026](0026-distinct-evolution-proposals.md) | 独立 Evolution proposal resource |
| [0027](0027-feedback-and-ranking-v2.md) | Feedback aggregate v1 与 ranking v2 |
| [0028](0028-aggregate-memories-and-freshness.md) | 普通 Aggregate memory 与 freshness descriptor |
| [0029](0029-logical-format-v1.md) | 确定性 Logical format v1 |

ADR 状态使用：`proposed`、`accepted`、`superseded` 或 `rejected`。
