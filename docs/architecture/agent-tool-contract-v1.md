# Agent Tool Contract v1

## 状态

Phase 1 契约定义已实现。本文描述后续 HTTP 与 MCP 适配器必须共同使用的协议中立
契约，不表示这些适配器已经实现。

Go wire package 位于 `pkg/toolcontract`。`internal/gateway/agenttool` 包含命令、
结果、领域校验与规范幂等哈希。两个包都不导入 HTTP、MCP、storage、jobs 或
provider 代码。

## 工具与能力

稳定工具名为 `dmem_recall`、`dmem_remember`、`dmem_feedback` 和
`dmem_forget`，契约版本为 `dmem-agent-tool-v1`。能力发现同时报告 predicate
registry 版本与 `references_only` evidence 披露模式。

Recall 只表达 query、scope、可选 entity hints、reference time、limit 与 cursor，
不暴露通道开关、图谱策略、provider prompt 或存储控制。响应通过
`degraded_capabilities` 报告未执行的能力；没有 embedding 能力时必须包含
`semantic`。

## Remember 语义

`dmem_remember` 接受一个 `agent_tool` source 和一到多条结构化 memory 建议。
source type、name、conversation ID、turn ID 和 occurred time 共同绑定来源。该 source
是每条已接受 memory 的 direct evidence 来源；v1 不允许 Agent 伪造 evidence ID 或
event ID。Recall 返回的 evidence 始终是有界引用，不包含 source 正文。

每个 memory 使用调用内 `local_id`。实体和记忆引用必须恰好选择 local ID 或持久
ID；持久 memory 引用还必须携带期望 revision ID。local ID 不成为长期身份。Fact
必须使用 predicate registry 中的谓词，并且 object 恰好为 entity 或一种类型化
literal。Relation 禁止自关系。JSON Schema 之外，gateway 会校验引用解析与谓词
类型兼容性。

Agent confidence 必须为有限的 `[0,1]` 数值，只作为输入特征，不能授权覆盖、遗忘、
冲突确认、敏感度降低或跳过审核。结果状态封闭为 `authority`、`deduplicated`、
`proposal` 和 `rejected`。`scene`、`topic`、`timeline`、敏感内容及所有 knowledge
建议仍按 Phase 0 规则进入 proposal 路径。

## Feedback 与 Forget

Feedback 绑定具体 memory revision，只接受 `used`、`not_used`、`incorrect`、
`outdated` 或 `duplicate`。可选 reason 有长度上限，不能作为未审核排序特征。旧
purpose/type/reason 组合不属于该契约。Forget 是独立命令，要求 expected revision
和非空 reason，结果生命周期状态为 `deleted`。

## 边界与规范化

JSON Schema 强制严格对象字段、封闭枚举、数组与字符串上限及数值范围。
`DecodeStrict` 拒绝多个 JSON 值、尾随 JSON 和未知字段。Gateway 额外校验 UTF-8、
有限 confidence、时间窗口、local reference、谓词类型、metadata 深度/大小和重复
ID。

Canonical hash 包含 contract version 与 operation name。规范化会折叠文本空白、
把时间统一为 UTC、排序并去重集合字段、按 local ID 排序建议，并把省略 metadata
与空对象视为等价。request、correlation 与 idempotency 元数据不进入 command hash。
同 caller、operation、idempotency key 和 hash 是 replay；同 key 不同 hash 是
idempotency conflict。

## 后续集成

`pkg/toolcontract/schema/v1` 中的嵌入式 schema 是后续 HTTP、MCP 与 client 的唯一
schema 来源。Phase 2 将其映射到原子 application memory commit，并确定
`search_hints` 的持久化表示；Phase 3 将 search hints 纳入 FTS；Phase 4 将同一
schema 暴露给薄 client 和 MCP server。
