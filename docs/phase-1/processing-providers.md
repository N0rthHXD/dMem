# dMeM 处理器契约

## 1. 第一阶段默认行为

dMeM 默认离线运行，不内置模型，也不自动向外部服务发送数据。

未配置 processor 时：

- 原始事件正常持久化。
- 初始 `extract_memory` job 由 no-op processor 完成。
- job 结果为 `no_processor`。
- 原始事件保持 `stored` 状态，不生成候选记忆。
- API 和健康状态明确报告自动提取未配置。
- 用户仍可通过手工记忆 API 创建和修订记忆。

`no_processor` 是成功结果，不触发重试。

## 2. Processor 端口

后续 processor 接收 processor identity、processing version、目标事件、受限上下文和 deadline，返回 outcome、候选、使用量、外部请求标识和错误类别。processor 不直接访问 repository 或写 SQLite。

Outcome：

```text
no_processor
no_memory
candidates_extracted
transient_failure
permanent_failure
invalid_output
policy_rejected
```

只有 `transient_failure` 自动重试。`invalid_output` 必须和 `no_memory` 区分。

## 3. 自动激活

第一阶段不支持 processor 候选自动激活。唯一能创建 active 记忆的方式是手工创建、手工修订或恢复已有 revision。

## 4. 数据外发

远程 processor 启用前必须配置 provider/model identity、凭据引用、允许外发的空间和数据类型、超时重试及数据最小化策略。普通日志不保存完整请求内容。

## 5. 取消和测试

worker 向 processor 传递可取消 context。只有持有当前任务 fencing token 的 worker 能提交结果。

测试提供确定性 fake processor，用于模拟 no_memory、候选、临时失败、非法输出和取消；fake 不成为生产默认实现。
