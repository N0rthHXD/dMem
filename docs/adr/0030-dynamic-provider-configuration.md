# ADR 0030：动态 provider 配置与在线恢复

- 状态：accepted
- 日期：2026-08-28

## 决策

所有 daemon 设置来源于一个严格 TOML 文件，通过 XDG 默认路径或单一 `--config` 定位符选择。移除所有 setting-specific 命令行参数和 `DMEM_*` setting 环境变量；provider secret 环境变量仍是 secret 来源而非配置来源。只接受 `config_version = 1`，无兼容、迁移或回退解析。reload（`POST /v1/config/reload`、`dmemd reload` 或 `SIGHUP`）读取一个完整文件修订、严格解码、规范化、验证 secret 引用、计算 identity、验证 restart-bound 字段，然后原子发布一个不可变有效快照。任何失败保持旧快照不变。

为每个能力分别计算 source identity（文件字节 SHA-256）、effective identity（规范化非 secret 值 SHA-256）、capability identity（kind/endpoint/model）、compatibility identity（model/dimensions/query instructions）和 policy identity（规范化策略规则）。成功转换获得单调递增的 activation/configuration/policy generation。Secret 值不进入任何 identity、数据库、status、audit 或日志。Secret 引用使用 `{ source = "environment", name = "..." }` 类型化形式；v1 仅支持环境引用，不接受明文值、命令或文件 secret。每个外部请求在策略验证后解析一次 secret，因此 secret 轮换不需要 reload。

策略 identity 由规范化规则生成而非操作者撰写。分配可以指向精确 space、稳定 space type/external-id 选择器或带 descendant inheritance 的 ancestor。当前 space 覆盖优先；否则最近 ancestor 优先；同等特异性下 deny 优先；未匹配 space 默认 deny；sibling 永不继承。每个 space 在 provider 工作评估前同步解析当前 generation 决策，因此启动后创建的 space 不需要在文件中列出 UUID。

被策略拒绝的 target 进入 `blocked_policy` 状态，不解析 secret、不消费 provider retry 预算、不发网络请求。被拒绝的可选 context 在 secret 解析前 omit 并计数。修正策略创建一个 durable 有界 reconciliation run，按 diff class（policy_expand、policy_narrow、compatibility、enable、disable、runtime_limits）只扫描受影响的 blocked/eligibility 记录。reconciliation 使用 crash-safe cursor 分批提交，只修改 job、eligibility projection 和 index metadata，不修改 authority 记录。兼容 embedding 在策略变更后复用；不兼容 identity 触发重建。空 eligible corpus 是有效的 ready corpus。

每次 provider 尝试记录 configuration generation、policy generation、compatibility identity、effective space decision 和现有 lease generation。authority 事务中的发布验证所有当前 fence。过期尝试成为 `stale_generation` 并重新评估，不能在新 identity 下发布候选、embedding、aggregate 或 index。

Searchable authority 和 embedding eligibility 保持独立。有效 semantic corpus 是当前 searchable revision、当前策略 eligibility 和兼容 embedding 的交集。策略 narrowing 立即排除 vector，物理重建随后进行。策略 expansion 复用兼容 vector 或调度缺失 embedding。Semantic query 是独立的策略 action：规范化、排序并展开完整请求 scope 后授权一个 `semantic_query` action，使用 all-or-nothing scope 语义；hybrid 可显式降级到 keyword。不根据首个 input space 或数组顺序改变授权结果。

restart-bound 字段（data/runtime/database/socket path、worker count、max connections、server request timeout）变更时整个 candidate 被拒绝为 `config_restart_required`。reload 不忽略变更字段也不部分激活文件。status 和 diagnostics 通过认证 UDS 路由暴露 identity、generation、state、secret 引用可用性、策略规则计数、blocked 计数和 reconciliation state；不暴露 secret 值、内容、query text、provider response 或源码片段。
