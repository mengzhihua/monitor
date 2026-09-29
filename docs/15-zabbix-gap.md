# Zabbix 能力对标与开发计划

数据口径：main 提交 8c0125e，对照 Zabbix 7.0 LTS 功能面。状态：已有 / 部分 / 缺失。

## 1. 对标结论

Monitor 现有能力源自 Netdata 模型（秒级自动采集、health.d 规则、Agent→Hub 流式上报），与 Zabbix 的差异集中在「**配置驱动的数据模型**」（模板 / 宏 / LLD / 原型）、「**动作与自动化**」（多步升级、远程命令）、「**服务与报表**」（SLA / 拓扑图 / 报表）和「**企业级管理**」（审计、用户组、细粒度权限）。采集广度（约 233 个采集器源文件）与 Zabbix 内置项相当，但无代理（Agentless）协议深度不足（SNMPv3、SSH、通用 JMX、多步 Web 场景、值预处理）。

## 2. 差距矩阵

| 领域 | Zabbix 能力 | Monitor 现状 | 证据 |
|---|---|---|---|
| 采集 | 主动 Agent | 已有 | `core/internal/stream/` |
| 采集 | 被动 Agent（服务端拉取） | 部分：`hub.storage: proxy` 实时回查；无独立被动协议 | `core/internal/hub/cluster.go` |
| 采集 | SNMP v1/v2c/v3、Trap | 已有：v2c IF-MIB、v3 USM、自定义 OID、表 LLD、Trap | `collect/snmp.go` |
| 采集 | IPMI | 已有 | `collect/ipmi.go` |
| 采集 | JMX 通用客户端 | 已有：Jolokia HTTP 读取 + 预处理 | `collect/jolokia.go` |
| 采集 | SSH / Telnet 检查 | 已有：SSH BatchMode；Telnet 为 TCP 发送一行并读到 expect | `collect/sshcheck.go`、`telnet.go` |
| 采集 | ICMP / TCP / 简单检查 | 已有 | `collect/ping.go`、`portcheck.go` |
| 采集 | HTTP Agent + JSONPath 提取、多步 Web 场景 | 已有：httpagent 预处理、webscenario 多步提取 | `collect/httpagent.go`、`webscenario.go` |
| 采集 | ODBC / 数据库 | 已有 | `collect/odbc.go`、`sql.go` |
| 采集 | Trapper / zabbix_sender 推送 | 已有：`ZBXD\x01` sender 监听，可选 psk 与来源网段 | `trapper/trapper.go` |
| 采集 | 计算项 / 聚合项 / 依赖项 | 部分：查询期聚合仍在；依赖项读主图表维度再预处理 | `collect/dependent.go` |
| 采集 | 低级发现 LLD + 原型 | 部分：SNMP 表 LLD 开图表；无通用原型继承 | `collect/snmp.go` |
| 采集 | 值预处理（regex/JSONPath/XPath/JS） | 部分：trim/regex/jsonpath/xpath/multiplier/bool/change；无 JS | `preprocess/preprocess.go` |
| 模型 | 主机 / 主机组 | 已有 / 部分：Space→Room，无嵌套、无组级权限 | `hub/org.go` |
| 模型 | 模板（可链接、继承） | 缺失（规则按 `on:` + `chart_labels` 匹配） | `health/rule.go` |
| 模型 | 用户宏 `{$MACRO}` | 缺失 | — |
| 模型 | 主机清单（自动 + 手工） | 部分：仅自动标签 | `hub/nodes.go` |
| 模型 | 代理 Proxy | 部分：`hub.peers` 环 + proxy 存储模式 | `hub/cluster.go` |
| 触发器 | 表达式函数 `nodata/change/count/trendavg/forecast/timeleft` | 缺失（现有 average/min/max/sum/median/last/min2max） | `health/rule.go` |
| 触发器 | 依赖 | 已有 `inhibit` | `health/inhibit.go` |
| 触发器 | 事件关联 | 已有：cause 升高时抑制 symptom 通知，告警带 `correlated_cause` | `health/correlation.go` |
| 问题 | 确认 / 指派 / 进度 / 抑制 | 已有 | `operations/` |
| 问题 | 手动关闭 | 缺失 | — |
| 问题 | 维护期 | 已有 | `health/maint.go`、`plans.go` |
| 动作 | 多步升级、恢复操作 | 已有：`health.actions` 步骤延迟、角色和恢复操作 | `health/actions.go` |
| 动作 | 远程命令 / 全局脚本 | 已有：配置 argv 白名单，`POST /api/v1/commands` | `api/zplatform.go` |
| 动作 | 媒介类型 | 已有：含短信网关与脚本渠道 | `health/notify_mobile.go`、`notify_script.go` |
| 动作 | 动作条件 | 已有：动作按严重级别和告警名匹配 | `health/actions.go` |
| 可视化 | 看板 / 组件 | 已有 54 预置 + 个人看板 | `web/src/dashboards.ts` |
| 可视化 | 图类型（饼 / 堆叠） | 部分：uPlot 线 / 面 / 堆叠，无饼图 | `web/` |
| 可视化 | 网络拓扑图 | 已有：LLDP 邻居 + `topology:` 手工边，服务页展示 | `api/zplatform.go`、`ServicesPanel.vue` |
| 可视化 | 服务树 / SLA 报表 | 已有：`services` 树、24h SLA、饼图 | `sla/sla.go` |
| 可视化 | 可用性 / 定时报表 | 已有：`/api/v1/reports/availability` 与 `reports.every` CSV | `api/zplatform.go` |
| 管理 | 审计日志 | 缺失 | — |
| 管理 | 完整 CRUD API | 部分：REST + OpenAPI，非全实体 CRUD | `api/openapi.yaml` |
| 管理 | 认证 内部 / LDAP / SAML / 2FA | 已有：token、OIDC、LDAP、SAML ACS 验签、TOTP | `api/saml.go`、`api/totp.go` |
| 管理 | 用户组 + 主机组级权限 | 部分：`web.groups` 可再包含其他组的 Room；无主机组树 | `config/config.go` |
| 管理 | HA 集群 | 部分：环复制，非共识 HA | `hub/cluster.go` |
| 管理 | 加密 PSK / 证书 | 部分：TLS；`stream.psk` / `hub.psk` 额外校验 `X-Monitor-PSK` | `hub/ingest.go` |
| 管理 | Agent 自动注册 | 已有 claim token | `api/server.go` |
| 管理 | 网络发现（IP 段扫描） | 已有：mDNS，以及最多 256 地址的 TCP 扫描 | `collect/netscan.go` |
| 管理 | 模板导入导出 | 部分：规则 YAML/JSON、看板 JSON | `api/alert_config.go` |
| 管理 | 自监控 | 已有：`monitor.notify_queue` 与 `monitor.hub_nodes` | `api/zplatform.go` |

## 3. 分阶段开发计划

按「对运维价值 / 实现成本」排序。Z1–Z2 已在主干。Z3–Z6 按下面的范围落地：预处理与无代理采集、动作链与白名单命令、服务树/拓扑/可用性报表、Room 权限与 SAML/TOTP/PSK/trapper/自监控。通用 LLD 原型仍用图表 context（`on: snmp.lld.<name>`）匹配，没有单独的原型语言。JS 预处理仍不在本轮。

| 阶段 | 内容 | 主要改动 |
|---|---|---|
| Z1 触发器与规则 | `nodata / change / count / first / stddev / trendavg / forecast / timeleft` 查询方法；用户宏 `{$NAME}`（全局 + 规则级）；问题手动关闭 `POST /api/v1/alarms/close`；审计日志 `GET /api/v1/audit` | `health/`、`api/`、新增 `audit/` |
| Z2 模板与主机模型 | 模板实体（规则集 + 采集配置 + 宏，可链接节点 / Room，继承覆盖）；主机组嵌套；可编辑主机清单；模板 YAML 导入导出 | `hub/`、`health/`、`api/`、Web |
| Z3 无代理采集 | SNMPv3 USM + 自定义 OID 项 + SNMP LLD；SSH 检查；通用 JMX（Jolokia）；HTTP Agent 项（JSONPath / regex / XPath 预处理）；多步 Web 场景；值预处理管线；依赖项 | `collect/`、新增 `preprocess/` |
| Z4 动作与自动化 | 多步升级链（步骤 / 操作 / 恢复操作）；动作条件引擎；远程命令 / 全局脚本（Agent 侧白名单执行）；短信与脚本渠道；事件关联规则 | `health/`、`stream/`、`api/` |
| Z5 服务与可视化 | 服务树 + SLA 计算与报表；网络拓扑图（LLDP 自动 + 手工）；可用性报表；定时报表导出；饼图组件 | 新增 `sla/`、Web |
| Z6 企业管理 | 用户组 + Room 级权限；TOTP 2FA；SAML；IP 段扫描发现；zabbix_sender 兼容 trapper 端口；PSK；Hub 队列自监控 | `api/`、`hub/`、`discover/` |

## 4. 非目标

- 不实现 Zabbix 数据库 schema / JSON-RPC API 兼容；对外仍为 REST + OpenAPI。
- 不引入外部数据库依赖；配置实体持久化沿用 JSON/YAML 文件 + CAS。
- 被动 Agent 协议（服务端主动连 10050）不作为默认；通过 Hub proxy 模式覆盖场景。
