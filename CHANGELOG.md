# 更新日志

## 2026-10-20

### 外部检查 `POST /api/v1/checks`

请求体可以带 `output`。它和 `message` 是同一个说明字段。

- 只传 `output`：当作说明保存。以前因此返回 400 的外部脚本现在可以提交。
- 只传 `message`：行为不变。
- 两个都传且文字不同：返回 400，不写入。
- 两个都传且文字相同：保存这一份说明。

示例：

```json
{"name":"nightly-backup","status":"warning","output":"late","ttl":"5m"}
```

说明最多一行、240 个字符。详情见 [外部服务检查](docs/06-monitor-2.0.md)。
