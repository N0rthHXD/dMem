# dMeM 原始内容与附件边界

## 1. 第一阶段范围

第一阶段只托管 UTF-8 文本或 JSON 原始内容，并将其保存在 SQLite `raw_contents`。不接收、复制或管理二进制附件。

外部文件只保存引用：

```text
reference_uri
media_type
declared_size
declared_hash
```

文件引用不代表内容已由 dMeM 持久化，调用方负责目标生命周期。

## 2. 接入限制

- `text`：必须是有效 UTF-8，受请求大小限制。
- `json`：必须是合法 JSON。
- `reference`：只保存引用和声明元数据。
- `binary`：返回 `unsupported_content_type`。

## 3. 持久性语义

text/json 的 `accepted` 表示内容和事件已在同一 SQLite 事务中提交。reference 的 `accepted` 只表示引用元数据已提交，daemon 不承诺目标未来存在，也不将目标纳入备份。

`raw_contents.content_hash` 对 text/json 是保存字节的 SHA-256。对 reference，它是规范 JSON `{reference_uri, media_type, declared_size, declared_hash}` 的 SHA-256，而不是目标文件内容哈希。字段按固定顺序编码，缺失可选值编码为 null；`declared_hash` 只是调用方声明。

## 4. 安全规则

- daemon 不自动读取 reference URI 或发起网络请求。
- 本地路径不在日志和健康响应中展开。
- 第一阶段 processor 不读取引用内容。

## 5. 后续托管附件

未来支持托管二进制附件前，必须定义 staging、fsync、原子 rename、内容寻址、崩溃协调、orphan reconciliation、删除和备份协议。在此之前不创建托管附件表和文件事务承诺。
