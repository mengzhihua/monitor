# 邮件、飞书与其他预警通道

在运行 monitord 的机器配置 `monitor.yaml` 中的 `health.notify`，重启后进入「运维总览 → 通知诊断」检查通道。Hub 与 Agent 各自发送本机规则的告警；Hub 页面上的通道测试不会发送到选中的远端 Agent。

## 邮件 SMTP

将以下字段合并到现有配置，不要重复定义 `health` 或 `notify`：

```yaml
health:
  enabled: true
  silent: false
  notify:
    email:
      server: smtp.example.com:587
      from: monitor@example.com
      to: [oncall@example.com, backup@example.com]
      username: monitor@example.com
      password_env: MONITOR_SMTP_PASSWORD
      tls_mode: starttls
      insecure_skip_verify: false
```

`MONITOR_SMTP_PASSWORD` 是示例环境变量名称，将邮箱服务商提供的 SMTP 密码或授权码通过服务管理器注入 **monitord 进程**。不需要在终端历史、仓库文件或网页中填写真实密码。环境变量引用覆盖同名明文字段，变量不存在或为空会阻止配置加载，不会悄悄使用旧密码。仅导出到当前终端不会更新已运行的 systemd / launchd / 容器进程；修改后需要重启对应服务。

- `tls_mode: starttls`：要求服务器支持 STARTTLS，否则失败；587 端口通常使用此模式。
- `tls_mode: tls`：连接时即启用 TLS，可用于 465 或自定义 TLS 端口。
- `tls_mode: auto`（默认）：465 使用 TLS，其余端口尝试 STARTTLS。无 TLS 时默认拒绝发送认证凭据；无认证的旧式内网 SMTP 仍可明文发送。需要始终加密正文时显式选择 `starttls` 或 `tls`。
- `from` 与 `to` 必须是纯邮箱地址，`to` 可有多项；不支持 `显示名 <地址>` 格式。不完整或非法邮件配置会阻止健康引擎启动并终止服务启动。
- 保持 `insecure_skip_verify: false`。邮件主题按 MIME 编码，正文采用 UTF-8 quoted-printable；SMTP DATA 被服务器接受后视为通道接受，不能保证未进入垃圾箱或最终送达。

## 飞书群机器人

按[飞书自定义机器人指南](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)在目标群创建机器人，保存 Webhook；若启用签名校验，同时保存签名密钥。飞书可能在 HTTP 200 中返回签名、关键词或 IP 拒绝，程序会将这类业务失败记为失败，参见[官方返回信息说明](https://www.feishu.cn/content/7298688341381546012)。

```yaml
health:
  notify:
    feishu:
      webhook_url_env: MONITOR_FEISHU_WEBHOOK
      secret_env: MONITOR_FEISHU_SECRET
```

通过服务环境注入对应变量；未开启签名校验时省略 `secret_env`。也可在本机私有配置中使用 `webhook_url`、`secret`，不要提交真实值。程序为每条签名消息生成秒级时间戳与 HMAC-SHA256/Base64 签名；服务器时钟需准确。机器人关键词、出口 IP 白名单由飞书侧配置，测试消息包含 `Monitor 通知测试`，实际告警包含规则名称和说明，两者都需要满足关键词策略。

飞书只将明确的 `code: 0`（兼容旧 `StatusCode: 0`）作为成功；空正文、缺少业务码、非法或超限 JSON 均为失败。钉钉、企业微信同样要求 `errcode: 0`。错误正文不进入诊断与分发日志，故障时请检查服务端配置及机器人平台设置。

## ntfy、Gotify、Bark

三种新增通道均支持自建服务。填写完整发送地址：ntfy 用服务根地址（不含主题），Gotify 用 `/message`，Bark 用 `/push`；反向代理前缀也需包含在地址中。它们默认不连接任何公共服务，不自动跟随重定向，地址中不能包含用户名、密码、查询参数或片段。接口必须直接返回成功 JSON，登录页面、空响应、缺少确认字段等均视为失败。

```yaml
health:
  notify:
    ntfy:
      url: https://ntfy.example.com/
      topic_env: MONITOR_NTFY_TOPIC
      token_env: MONITOR_NTFY_TOKEN
    gotify:
      url: https://gotify.example.com/message
      token_env: MONITOR_GOTIFY_TOKEN
    bark:
      url: https://bark.example.com/push
      device_key_env: MONITOR_BARK_KEY
      group: 运维告警
    roles:
      oncall: [email, feishu, ntfy, gotify, bark]
```

以上域名和环境变量名均为占位示例；在服务管理器中注入实际值并重启 monitord。也可用本机私有配置的 `topic`、`token`、`device_key` 字段，环境变量引用优先，引用不存在或为空时拒绝加载。部分填写的通道不会被静默忽略：缺少发送地址、主题、必需令牌或设备 Key 会阻止服务启动。

| 通道 | 准备与认证 | 严重级别与成功条件 |
| --- | --- | --- |
| ntfy | 在客户端订阅相同服务和主题；需要认证时使用 Bearer token，无认证的自建服务可省略 `token_env`。主题限制为 1–64 个英文字母、数字、`_`、`-`。 | CRITICAL/WARNING/其他状态的优先级为 5/4/3；要求响应有消息 ID、`event: message` 且主题一致。 |
| Gotify | 创建应用并使用应用令牌（不是客户端令牌），通过 `X-Gotify-Key` 请求头认证。 | CRITICAL/WARNING/其他状态优先级为 8/5/2；要求响应返回正整数消息 ID。 |
| Bark | 从 Bark 客户端取得 Device Key，使用 JSON `/push`，Key 放在正文中；消息可按 `group` 分组，默认 Monitor。 | 严重级别与主机写入标题，不额外启用强提醒；要求响应 `code: 200`。 |

普通告警、恢复通知和管理员测试都会使用同一发送器，配置的规则路由、静默、维护与重复提醒机制继续有效。手动测试仍会绕过静默/维护/规则路由，并遵守全局每 30 秒一次的限制。三种通道在网页中独立显示，支持筛选和管理员测试；主题、地址、令牌、设备 Key 与供应商响应正文均不暴露到诊断中。通道接受仅表示服务端确认，手机最终展示还取决于订阅、权限和客户端连接。

协议依据：[ntfy JSON 发布说明](https://docs.ntfy.sh/publish/#publish-as-json)、[Gotify 推送说明](https://gotify.net/docs/pushmsg)、[Bark 官方 API](https://github.com/Finb/bark-server/blob/master/docs/API_V2.md)。

## 规则路由、恢复与重复提醒

```yaml
health:
  notify:
    roles:
      oncall: [email, feishu]
      chat: [feishu, wecom]
  alarms:
    - name: high_memory
      on: system.ram
      lookup: average -1m percentage of used
      units: '%'
      warn: '$this > 85'
      crit: '$this > 95'
      every: 10s
      repeat: warning 1h critical 15m
      info: 系统内存使用率过高，请检查进程
      to: oncall
```

示例阈值仅供部署时调整。告警状态变化和恢复沿用规则引擎现有通知策略；静默、维护、延迟防抖与重复提醒仍生效。`to: silent` 不发送；已定义的非空角色列表仅发到对应已配置通道。未定义角色或空角色列表沿用原行为，发至所有已配置通道，请检查拼写。投递失败不会立即自动重试；需要持续提醒时设置 `repeat`。

其他现有通道还包括 Webhook、Slack、钉钉、企业微信、Telegram、Discord、PagerDuty 等，配置项见仓库根目录 `monitor.example.yaml`。

## 手动测试与诊断

1. 配置并重启后，以管理员登录，在通道卡片点击「测试 邮件 SMTP」或「测试 飞书」。查看将发送的通道和说明，点击「发送测试消息」。只读与排障角色不显示按钮，也不能调用发送接口。
2. 测试使用已保存的收件人和固定文本，**绕过静默、维护和规则路由**，即使处于维护仍会发送。它只验证通道连接，不能证明真实规则能匹配或触发；不会创建、确认或恢复真实告警。
3. 成功排队不代表发送成功。刷新「最近通知结果」，查看 `[测试]` 标记、接受/失败及安全错误分类，然后在邮箱或群中确认收件。测试计入通道次数，结果随进程重启清空。
4. 同一实例全部用户和通道共享每 30 秒一次的测试限制，队列满或服务停止会拒绝请求。多项同类型配置会分别调用；不接受请求提供任意收件人、URL、凭据或自定义内容。

接口：`POST /api/v1/operations/notifications/test`，管理员 Bearer 认证，JSON `{"channel":"email"}` 或 `{"channel":"feishu"}`。成功返回 202 和 `{"queued":true}`；参数错误 400，权限不足 403，频率超限 429（`Retry-After: 30`），队列或引擎不可用 503。发送操作记录管理员名称；认证凭据、地址和响应正文不记录。

## 本地验收

执行 `make all` 后运行 `python3 scripts/verify-notification-services.py`。脚本只使用随机端口的本机 SMTP/HTTP 模拟服务、临时配置和数据，检查真实内存告警的邮件与飞书发送、签名、业务码失败、管理员测试、限流和秘密信息脱敏。它不连接真实邮箱或飞书，不代表真实账号、机器人权限或最终收件已经验收。

运行 `python3 scripts/verify-push-services.py` 可验收 ntfy、Gotify、Bark：隔离的真实内存告警触发、环境变量认证、三个独立测试入口、HTTP 200 中的拒绝响应、权限/限流与日志脱敏。全部流量只到本机随机端口，不证明手机已真实收件。
