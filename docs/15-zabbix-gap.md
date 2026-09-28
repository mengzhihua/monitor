# Zabbix 能力对标与开发计划

数据口径：main 提交 8c0125e，对照 Zabbix 7.0 LTS 功能面。状态：已有 / 部分 / 缺失。

## 1. 对标结论

Monitor 现有能力源自 Netdata 模型（秒级自动采集、health.d 规则、Agent→Hub 流式上报），与 Zabbix 的差异集中在「**配置驱动的数据模型**」（模板 / 宏 / LLD / 原型）、「**动作与自动化**」（多步升级、远程命令）、「**服务与报表**」（SLA / 拓扑图 / 报表）和「**企业级管理**」（审计、用户组、细粒度权限）。采集广度（约 233 个采集器源文件）与 Zabbix 内置项相当，但无代理（Agentless）协议深度不足（SNMPv3、SSH、通用 JMX、多步 Web 场景、值预处理）。

## 2. 差距矩阵

| 领域 | Zabbix 能力 | Monitor 现状 | 证据 |
|---|---|---|---|
| 采集 | 主动 Agent | 已有 | `core/internal/stream/` |
| 采集 | 被动 Agent（服务端拉取） | 部分：`hub.storage: proxy` 实时回查；无独立被动协议 | `core/internal/hub/cluster.go` |
| 采集 | SNMP v1/v2c/v3、Trap | 部分：v2c IF-MIB + Trap 监听；**无 v3 USM、无自定义 OID 项** | `collect/snmp*.go` |
| 采集 | IPMI | 已有 | `collect/ipmi.go` |
| 采集 | JMX 通用客户端 | 部分：仅 hdfs/cassandra/tomcat 等特定 HTTP JMX | `collect/hdfs.go` 等 |
| 采集 | SSH / Telnet 检查 | 缺失 | — |
| 采集 | ICMP / TCP / 简单检查 | 已有 | `collect/ping.go`、`portcheck.go` |
| 采集 | HTTP Agent + JSONPath 提取、多步 Web 场景 | 部分：单请求 httpcheck，无正文提取、无场景 | `collect/httpcheck.go` |
| 采集 | ODBC / 数据库 | 已有 | `collect/odbc.go`、`sql.go` |
| 采集 | Trapper / zabbix_sender 推送 | 部分：`POST /api/v1/checks`（语义对齐、协议不兼容） | `api/checks.go` |
| 采集 | 计算项 / 聚合项 / 依赖项 | 部分：查询期 `context=`/`group_by=`；无持久化计算项、无依赖项 | `api/data.go` |
| 采集 | 低级发现 LLD + 原型 | 部分：运行期自动注册替代；无用户定义 LLD 规则 | registry |
| 采集 | 值预处理（regex/JSONPath/XPath/JS） | 缺失 | — |
| 模型 | 主机 / 主机组 | 已有 / 部分：Space→Room，无嵌套、无组级权限 | `hub/org.go` |
| 模型 | 模板（可链接、继承） | 缺失（规则按 `on:` + `chart_labels` 匹配） | `health/rule.go` |
| 模型 | 用户宏 `{$MACRO}` | 缺失 | — |
| 模型 | 主机清单（自动 + 手工） | 部分：仅自动标签 | `hub/nodes.go` |
| 模型 | 代理 Proxy | 部分：`hub.peers` 环 + proxy 存储模式 | `hub/cluster.go` |
| 触发器 | 表达式函数 `nodata/change/count/trendavg/forecast/timeleft` | 缺失（现有 average/min/max/sum/median/last/min2max） | `health/rule.go` |
| 触发器 | 依赖 | 已有 `inhibit` | `health/inhibit.go` |
| 触发器 | 事件关联 | 缺失（仅指标相关性 ks2/volume） | `api/weights` |
| 问题 | 确认 / 指派 / 进度 / 抑制 | 已有 | `operations/` |
| 问题 | 手动关闭 | 缺失 | — |
| 问题 | 维护期 | 已有 | `health/maint.go`、`plans.go` |
| 动作 | 多步升级、恢复操作 | 部分：`repeat` + 单步 `escalate_to` | `health/engine.go` |
| 动作 | 远程命令 / 全局脚本 | 缺失 | — |
| 动作 | 媒介类型 | 已有 12+ 渠道；无短信、无自定义脚本渠道 | `health/notify*.go` |
| 动作 | 动作条件 | 部分：仅按角色路由 | `notify.roles` |
| 可视化 | 看板 / 组件 | 已有 54 预置 + 个人看板 | `web/src/dashboards.ts` |
| 可视化 | 图类型（饼 / 堆叠） | 部分：uPlot 线 / 面 / 堆叠，无饼图 | `web/` |
| 可视化 | 网络拓扑图 | 缺失（有 LLDP 数据无视图） | `collect/snmp_topology.go` |
| 可视化 | 服务树 / SLA 报表 | 缺失 | — |
| 可视化 | 可用性 / 定时报表 | 部分：JSON/CSV 导出 | `operations_history.go` |
| 管理 | 审计日志 | 缺失 | — |
| 管理 | 完整 CRUD API | 部分：REST + OpenAPI，非全实体 CRUD | `api/openapi.yaml` |
| 管理 | 认证 内部 / LDAP / SAML / 2FA | 部分：token、OIDC、LDAP；无 SAML、无 2FA | `api/oidc.go` |
| 管理 | 用户组 + 主机组级权限 | 缺失（全局三角色） | `web.users` |
| 管理 | HA 集群 | 部分：环复制，非共识 HA | `hub/cluster.go` |
| 管理 | 加密 PSK / 证书 | 部分：TLS，无 PSK | — |
| 管理 | Agent 自动注册 | 已有 claim token | `api/server.go` |
| 管理 | 网络发现（IP 段扫描） | 部分：仅 mDNS | `discover/mdns.go` |
| 管理 | 模板导入导出 | 部分：规则 YAML/JSON、看板 JSON | `api/alert_config.go` |
| 管理 | 自监控 | 部分：profile 图表，无队列指标 | `collect/profile.go` |

## 3. 分阶段开发计划

按「对运维价值 / 实现成本」排序，每阶段独立 PR、可独立发布。

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
