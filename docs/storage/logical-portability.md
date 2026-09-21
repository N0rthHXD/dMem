# 逻辑可移植性

逻辑 Format v1 提供选择性的跨实例可移植性。它实现于内部 Go 包
`internal/portability`，不是 `dmemd` 命令或 HTTP 端点。它与 SQLite 备份/恢复
不同：备份在精确 schema 版本上替换整个数据目录，而逻辑导入把选定的领域对象
映射到既有目标空间。

## 包结构

格式族为 `dmem-logical`，`FormatVersion = 1`。一个包是一个目录，包含：

- `manifest.json`：格式族、版本、来源身份与选择信息；
- `checksums.json`：成员文件的 sha256；
- 11 个确定性的 JSONL 成员：

```text
spaces          raw_events      memories        revisions
evidence        entities        entity_mentions relations
conflicts       feedback        aggregate_provenance
```

每条记录的形状为 `{object_id, object_type, data}`。FTS、embedding、向量数据、
派生反馈聚合、作业、租约、密钥与日志不会被导出。`aggregate_provenance.jsonl`
同时携带 Agent Tool 来源溯源（`agent_tool_source`）、受 revision 约束的
search hints（`agent_tool_search_hint`）与待审核 Tool proposal
（`agent_tool_proposal`），因此它们随权威一起可移植并在导入后重建 FTS。

## 选择与一致性

选择以空间为单位，并声明是否包含祖先。导出器会闭包所需修订、证据与原始事件，
把选择之外的引用表示为 stub。敏感数据遵循显式导出策略；导出请求要求来源身份、
空间列表、`closure_policy = "strict"`、敏感度策略（`exclude` 或 `include`）与
RFC3339 参考时间。

`Exporter.DryRun` 报告计数、估算大小、敏感度、外部引用与拒绝原因。

`Exporter.Export` 读取一个 SQLite 快照，写入私有临时包，计算校验和，自我校验，
同步，然后原子发布新的目标目录。已存在的目标、不安全的所有权或权限、符号链接、
断裂的闭包与校验和失败都会被拒绝。

## 导入

`Importer.DryRun` 在写入权威之前严格校验成员、大小限制、校验和、引用、格式
版本、目标空间、确定性 ID 映射、重复项与本地修订冲突。默认限制为 13 个文件、
100,000 条记录、每文件 64 MiB、总计 512 MiB，批大小为 500。校验还要求目录权限
`0700`、文件权限 `0600`、当前用户所有权、精确的成员集合、规范化且排序的 JSON
记录，以及正确的格式族与版本。

`Importer.Import` 存储持久操作并按有界批次提交。每个批次原子地写入对象、源到
本地映射、溯源及其检查点。重试会从最后一个已提交批次继续，不会重复对象。
generation 围栏阻止旧的导入器推进已恢复的操作。`Status` 报告进度；`Cancel`
停止后续批次，但保留已提交且完整映射的对象。

重复内容映射到既有对象。被映射的本地记忆若其当前修订与导入内容冲突，会产生
演化提案，而不是覆盖权威。成功导入会入队 FTS、语义、实体、关系与反馈聚合的
重建工作，因此在这些作业完成之前健康状态可能保持降级。

## 边界

逻辑可移植性不提供任意 SQLite 兼容性、保留 ID 模式、同步、复制、远程传输，也
不提供对已提交批次的整体回滚。
