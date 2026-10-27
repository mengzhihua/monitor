# Zabbix 能力对标与开发计划

数据口径：对照 Zabbix 7.0 LTS 功能面，并按当前代码复核。状态：已有 / 部分 / 缺失。矩阵里已经落地的 Z1–Z6 不再标成缺失。

## 1. 对标结论

Monitor 现有能力源自 Netdata 模型（秒级自动采集、health.d 规则、Agent→Hub 流式上报），与 Zabbix 的差异集中在「**配置驱动的数据模型**」（模板 / 宏 / LLD / 原型）、「**动作与自动化**」（多步升级、远程命令）、「**服务与报表**」（SLA / 拓扑图 / 报表）和「**企业级管理**」（审计、用户组、细粒度权限）。采集广度与 Zabbix 内置项相当。无代理采集、模板、宏、清单、动作、SLA 和审计已经落地；触发器表达式以矩阵「表达式函数」行为准（主干已含 stddev、`percentile` 等基础查找，扩展函数随 PR 合入）。通用 LLD 原型、图表饼图，以及带时间上限且不能访问主机的 JavaScript 预处理已经落地。仍开放的是非目标里的被动 Agent 协议和 Zabbix JSON-RPC，以及矩阵里标成部分的代理、共识 HA、全实体 CRUD 和独立主机组树。计算项已支持 `{图表:维度}` 的四则运算，以及最多 1 小时的 `avg` / `min` / `max` / `sum`。

## 2. 差距矩阵

| 领域 | Zabbix 能力 | Monitor 现状 | 证据 |
|---|---|---|---|
| 采集 | 主动 Agent | 已有 | `core/internal/stream/` |
| 采集 | 被动 Agent（服务端拉取） | 部分：`hub.storage: proxy` 实时回查；无独立被动协议 | `core/internal/hub/cluster.go` |
| 采集 | SNMP v1/v2c/v3、Trap | 已有：v2c IF-MIB、v3 USM、自定义 OID、表 LLD、Trap。`timeout` 会按秒取整后传给 snmpwalk 的 `-t`（至少 1 秒，至多 60 秒，默认 3 秒）。`retries` 为 0 时沿用 snmpwalk 的默认重试；1 到 5 会传给 `-r`。多目标里留空则继承上一级 | `collect/snmp.go` |
| 采集 | IPMI | 已有 | `collect/ipmi.go` |
| 采集 | JMX 通用客户端 | 已有：Jolokia HTTP 读取 + 预处理。请求失败或 JSON `status` 不是 200 时该项记为 `unsupported`，下一次成功采集会清除 | `collect/jolokia.go` |
| 采集 | SSH / Telnet 检查 | 已有：SSH BatchMode；Telnet 为 TCP 发送一行并读到 expect | `collect/sshcheck.go`、`telnet.go` |
| 采集 | ICMP / TCP / 简单检查 | 已有：`httpcheck` 可用 Zabbix 风格 `status_codes`（如 `200` 或 `200-299`），也可用旧字段 `status_accepted` 列举整数码。`follow_redirects: true` 时跟随跳转，默认只看首跳响应。状态码通过后用 `contains` 要求正文包含一段文本，最长 256 字节 | `collect/ping.go`、`portcheck.go`、`httpcheck.go` |
| 采集 | HTTP Agent + JSONPath 提取、多步 Web 场景 | 已有：httpagent 预处理。`status_codes` 可限制 `200` 或 `200-299`，不匹配则该项记为不支持。`retrieve` 取 `body`、`headers` 或 `status`。`follow_redirects` 默认跟随跳转。webscenario 多步提取；步骤可用 `required` 要求响应正文包含一段文本，变量替换后再比较，最长 256 字节；状态码仍由 `expect` 决定，默认 200 | `collect/httpagent.go`、`webscenario.go` |
| 采集 | ODBC / 数据库 | 已有：`odbc` 驱动查询；`sql` 模块用 CLI 执行查询。`sql` 查询失败时 `sql.*_query` 与 `sql.*_query_time` 记为 `unsupported`，下一次成功采集会清除 | `collect/odbc.go`、`sql.go` |
| 采集 | MySQL | 已有：`SHOW GLOBAL STATUS`。连接或状态读取失败时，各 mysql 图表记为 `unsupported`，下一次成功采集会清除 | `collect/mysql.go` |
| 采集 | PostgreSQL | 已有：内置协议读 `pg_stat_*`。连接或查询失败时，各 postgres 图表记为 `unsupported`，下一次成功采集会清除 | `collect/postgres.go` |
| 采集 | Redis | 已有：`INFO`。读取失败时，各 redis 图表记为 `unsupported`，下一次成功采集会清除 | `collect/redis.go` |
| 采集 | Nginx | 已有：`stub_status`。状态页失败或无法解析时，各 nginx 图表记为 `unsupported`，下一次成功采集会清除 | `collect/nginx.go` |
| 采集 | Memcached | 已有：`stats`。统计读取失败时，各 memcached 图表记为 `unsupported`，下一次成功采集会清除 | `collect/memcached.go` |
| 采集 | MongoDB | 已有：`serverStatus`。连接或命令失败时，各 mongodb 图表记为 `unsupported`，下一次成功采集会清除 | `collect/mongodb.go` |
| 采集 | Elasticsearch | 已有：集群/节点 REST。请求失败或 JSON 无法解析时，各 elasticsearch 图表记为 `unsupported`，下一次成功采集会清除 | `collect/elasticsearch.go` |
| 采集 | RabbitMQ | 已有：管理 API。请求失败时，各 rabbitmq 图表记为 `unsupported`，下一次成功采集会清除 | `collect/rabbitmq.go` |
| 采集 | Consul | 已有：agent/metrics API。请求失败时，各 consul 图表记为 `unsupported`，下一次成功采集会清除 | `collect/consul.go` |
| 采集 | HAProxy | 已有：统计套接字/CSV。读取失败时，各 haproxy 图表记为 `unsupported`，下一次成功采集会清除 | `collect/haproxy.go` |
| 采集 | Apache | 已有：`server-status`。状态页失败时，各 apache 图表记为 `unsupported`，下一次成功采集会清除 | `collect/apache.go` |
| 采集 | lighttpd | 已有：状态页。读取失败时，各 lighttpd 图表记为 `unsupported`，下一次成功采集会清除 | `collect/lighttpd.go` |
| 采集 | PHP-FPM | 已有：status 页。读取失败时，各 phpfpm 图表记为 `unsupported`，下一次成功采集会清除 | `collect/phpfpm.go` |
| 采集 | Traefik | 已有：Prometheus 指标端点。抓取失败时，各 traefik 图表记为 `unsupported`，下一次成功采集会清除 | `collect/traefik.go` |
| 采集 | BIND | 已有：`rndc stats`。统计失败时，各 bind 图表记为 `unsupported`，下一次成功采集会清除 | `collect/bind.go` |
| 采集 | Unbound | 已有：控制套接字统计。读取失败时，各 unbound 图表记为 `unsupported`，下一次成功采集会清除 | `collect/unbound.go` |
| 采集 | CoreDNS | 已有：Prometheus 指标。抓取失败时，各 coredns 图表记为 `unsupported`，下一次成功采集会清除 | `collect/coredns.go` |
| 采集 | HDFS | 已有：NameNode JMX。读取失败时，各 hdfs 图表记为 `unsupported`，下一次成功采集会清除 | `collect/hdfs.go` |
| 采集 | ZooKeeper | 已有：`mntr`/`srvr`。四字命令失败时，各 zookeeper 图表记为 `unsupported`，下一次成功采集会清除 | `collect/zookeeper.go` |
| 采集 | NATS | 已有：监控 HTTP。请求失败时，各 nats 图表记为 `unsupported`，下一次成功采集会清除 | `collect/nats.go` |
| 采集 | Squid | 已有。计数器页失败或没有 `client_http.requests` 时，各 squid 图表记为 `unsupported`，下一次成功采集会清除 | `collect/squid.go` |
| 采集 | Tomcat | 已有。管理状态页失败或 XML 无法解析时，各 tomcat 图表记为 `unsupported`，下一次成功采集会清除 | `collect/tomcat.go` |
| 采集 | Varnish | 已有。`varnishstat -j` 失败或没有计数器时，各 varnish 图表记为 `unsupported`，下一次成功采集会清除 | `collect/varnish.go` |
| 采集 | Trapper / zabbix_sender 推送 | 已有：`ZBXD\x01` sender 监听，可选 psk 与来源网段 | `trapper/trapper.go` |
| 采集 | 计算项 / 聚合项 / 依赖项 | 已有：依赖项读一个主维度再预处理；计算项用 `{图表:维度}` 做四则运算，并用 `avg`/`min`/`max`/`sum` 读取最多 1 小时的已存储样本。查询期聚合仍在 | `collect/dependent.go`、`collect/calculated.go` |
| 采集 | 低级发现 LLD + 原型 | 已有：SNMP 表 LLD；通用 `{#MACRO}` 原型可按实体开图表，或合成一张图 | `collect/snmp.go`、`collect/lld.go` |
| 采集 | 值预处理（regex/JSONPath/XPath/JS） | 已有：trim/ltrim/rtrim/replace/regex/jsonpath/xpath/multiplier/bool/matches/not_match/hex/octal/in_range/valuemap/discard_unchanged/discard_unchanged_heartbeat/throttle/change/change_per_second/javascript/error_json/error_xml/error_regex/prometheus_to_json/xml_to_json/csv_to_json/snmp_walk_to_json/not_supported。`xml_to_json` 把 XML 转成 JSON（属性 `@` 前缀、文本 `#text`、同名子节点为数组），不解析 DOCTYPE。`csv_to_json` 把 CSV 转成 JSON（`header: true` 时首行作字段名），再用 JSONPath 取值。`snmp_walk_to_json` 把 snmpwalk 文本按字段 OID 收成带 `{#SNMPINDEX}` 的 JSON。`error_*` 在载荷报告错误时丢弃样本，字段不存在时继续。`not_supported` 命中后图表带 `unsupported`，后续成功样本会清掉。`discard_unchanged_heartbeat` 与 `throttle` 需要采集器保存步骤状态（SNMP/HTTP/Jolokia/SSH/Telnet/依赖项等）。Prometheus 文本可转成 JSON 再用 JSONPath 取值；步骤可用 `on_fail`。JavaScript 最长 4096 字节、100ms，无主机 API | `preprocess/preprocess.go`、`preprocess/state.go`、`collect/preprocess_state.go`、`registry/registry.go` |
| 模型 | 主机 / 主机组 | 已有：Space→Room；`web.groups` 可递归包含其他组的 Room，环被忽略 | `config/config.go` |
| 模型 | 模板（可链接、继承） | 已有：Hub 模板可按节点、Room、标签链接，带宏、标签和采集覆盖 | `hub/templates.go` |
| 模型 | 用户宏 `{$MACRO}` | 已有：全局与规则级 `{$NAME}` | `config/config.go`、`health/` |
| 模型 | 主机清单（自动 + 手工） | 已有：自动标签，以及 `GET/PUT /api/v1/hub/inventory` 手工字段 | `api/templates.go` |
| 模型 | 代理 Proxy | 部分：`hub.peers` 环 + proxy 存储模式 | `hub/cluster.go` |
| 触发器 | 表达式函数 `nodata/change/count/trendavg/forecast/timeleft` | 已有，并含 stddev、`percentile`、`mad`、`diff` 和 `abschange`。`percentile` 按 `ceil(n*p/100)-1` 取名次。`change` 是窗口末值减首值。`mad -5m` 是绝对偏差的中位数，升序后取下标 `n/2`。一个样本为 0，空窗口为 NaN。`diff` 在最近两个样本不同时为 1，否则为 0。`abschange` 是这两个样本的绝对差。只有一个样本时 `diff` 和 `abschange` 为 0。多个维度分别计算后相加 | `health/metric_lookup.go` |
| 触发器 | 依赖 | 已有 `inhibit` | `health/inhibit.go` |
| 触发器 | 事件关联 | 已有：cause 升高时抑制 symptom 通知，告警带 `correlated_cause`。`equal` 可要求同一图表、上下文或 `label:<key>`；留空则任一 cause 都生效 | `health/correlation.go` |
| 问题 | 确认 / 指派 / 进度 / 抑制 | 已有：确认会暂停同一状态的重复通知，取消确认或状态变化后恢复。指派和进度仍记在操作记录里 | `operations/`、`health/engine_transition.go` |
| 问题 | 手动关闭 | 已有：`POST /api/v1/alarms/close` | `api/alarms.go` |
| 问题 | 维护期 | 已有：全局、精确图表/告警，以及按图表标签 `label=value` 静默。标签维护不会波及没有该标签的告警 | `health/plans.go` |
| 动作 | 多步升级、恢复操作 | 已有：`health.actions` 步骤延迟、角色和恢复操作 | `health/actions.go` |
| 动作 | 远程命令 / 全局脚本 | 已有：配置 argv 白名单，`POST /api/v1/commands` | `api/zplatform.go` |
| 动作 | 媒介类型 | 已有：含短信网关与脚本渠道。邮件主题和正文可用 `{HOST.NAME}`、`{EVENT.NAME}`、`{EVENT.STATUS}`、`{EVENT.VALUE}`、`{EVENT.UNITS}`、`{EVENT.CHART}`、`{EVENT.INFO}` | `health/message.go`、`health/notify.go` |
| 动作 | 动作条件 | 已有：严重级别、告警名和图表通配，以及规则上的触发器标签与 `ack: yes` / `ack: no`。标签必须都存在，值可用末尾 `*`。确认后只运行 `ack: yes` 的动作；未确认时 `ack: no` 才运行。留空则两种状态都匹配，确认本身不触发它。条件按 AND 组合 | `health/actions.go`、`health/rule.go` |
| 可视化 | 看板 / 组件 | 已有 54 预置 + 个人看板 | `web/src/dashboards.ts` |
| 可视化 | 图类型（饼 / 堆叠） | 已有：uPlot 线 / 面 / 堆叠；图表类型 `pie` 按末行正值画占比；服务 SLA 另有饼图 | `web/src/features/metrics/chartRenderer.ts`、`ServicesPanel.vue` |
| 可视化 | 网络拓扑图 | 已有：LLDP 邻居 + `topology:` 手工边，服务页展示 | `api/zplatform.go`、`ServicesPanel.vue` |
| 可视化 | 服务树 / SLA 报表 | 已有：`services` 树、24h SLA、饼图 | `sla/sla.go` |
| 可视化 | 可用性 / 定时报表 | 已有：`/api/v1/reports/availability` 与 `reports.every` CSV。告警日志可用 `GET /api/v1/alarm_log?format=csv` 导出，公式文本会加前缀避免被表格软件执行 | `api/zplatform.go`、`api/alarms.go` |
| 管理 | 审计日志 | 已有：`GET /api/v1/audit` | `api/audit.go` |
| 管理 | 完整 CRUD API | 部分：REST + OpenAPI，非全实体 CRUD | `api/openapi.yaml` |
| 管理 | 认证 内部 / LDAP / SAML / 2FA | 已有：token、OIDC、LDAP、SAML ACS 验签、TOTP | `api/saml.go`、`api/totp.go` |
| 管理 | 用户组 + 主机组级权限 | 部分：`web.groups` 递归授予 Room；没有独立的主机组树 | `config/config.go` |
| 管理 | HA 集群 | 部分：环复制，非共识 HA | `hub/cluster.go` |
| 管理 | 加密 PSK / 证书 | 部分：TLS；`stream.psk` / `hub.psk` 额外校验 `X-Monitor-PSK` | `hub/ingest.go` |
| 管理 | Agent 自动注册 | 已有 claim token | `api/server.go` |
| 管理 | 网络发现（IP 段扫描） | 已有：mDNS，以及最多 256 地址的 TCP 扫描 | `collect/netscan.go` |
| 管理 | 模板导入导出 | 部分：规则 YAML/JSON、看板 JSON | `api/alert_config.go` |
| 管理 | 自监控 | 已有：`monitor.notify_queue` 与 `monitor.hub_nodes` | `api/zplatform.go` |

## 3. 分阶段开发计划

按「对运维价值 / 实现成本」排序。Z1–Z2 已在主干。Z3–Z6 按下面的范围落地：预处理与无代理采集、动作链与白名单命令、服务树/拓扑/可用性报表、Room 权限与 SAML/TOTP/PSK/trapper/自监控。通用 LLD 使用 `{#MACRO}` 替换标题、键、值和单位。`layout: item` 为每个实体一张图，`layout: graph` 把实体收成一张图，类型可以是 `pie`。健康规则按 context `lld.<规则名>` 或 `snmp.lld.<name>` 匹配。JavaScript 预处理只运行配置里的函数体，限时且没有主机、网络和文件接口。预处理步骤的 `on_fail` 可以在失败时丢掉样本、改用 `fail_value`，或换成自定义错误。`prometheus_to_json` 把文本暴露格式转成 JSON 数组，可用指标名和标签选择器过滤。`xml_to_json` 把一个 XML 文档转成 JSON：属性以 `@` 开头，混合文本放在 `#text`，同名子节点收成数组；不解析 DOCTYPE。`csv_to_json` 把 CSV 转成 JSON，默认逗号分隔、双引号转义；`header: true` 时首行成为字段名。`snmp_walk_to_json` 把 snmpwalk 文本按字段 OID 收成 JSON，每行带 `{#SNMPINDEX}`。`not_supported` 的模式命中后，该项图表记上 `unsupported`，下一次成功采集会清除它。HTTP Agent 可以用 `status_codes` 要求状态码，并用 `retrieve` 取正文、响应头或状态码。`httpcheck` 可用 `status_codes` 限制状态码，并用 `follow_redirects` 跟随跳转；`contains` 在状态码通过后检查正文。多步 Web 场景的 `required` 在状态码通过后检查正文是否包含指定文本。预处理还支持 `valuemap`、`change_per_second`、`discard_unchanged_heartbeat` 与 `throttle`（依赖项与 SNMP/HTTP/Jolokia/SSH/Telnet 项会保存步骤状态），以及 `error_json` / `error_xml` / `error_regex` 载荷错误检查。计算项可以用 `{图表:维度}` 做加、减、乘、除和括号，也可以用 `avg`、`min`、`max`、`sum` 读取 1 秒到 1 小时的历史。每个公式最多 8 个引用。触发器查询 `percentile -5m p 95` 取窗口百分位。`mad -5m` 计算窗口的绝对偏差中位数。`diff -5m` 看最近两个样本是否不同，`abschange -5m` 取它们的绝对差；只有一个样本时为 0，空窗口为 NaN。确认一个未恢复的问题会暂停它的重复通知，直到取消确认或状态变化。动作 `ack: yes` 在确认时另发一步通知，`ack: no` 只处理尚未确认的问题。SNMP 的 `timeout` 会传给 snmpwalk 的 `-t`（秒，向上取整）。`retries` 为 1 到 5 时传给 `-r`，0 则沿用 snmpwalk 的默认重试。Jolokia 请求失败或返回的 `status` 不是 200 时，该项记为 `unsupported`，下一次成功采集会清除。Squid 计数器页失败或没有 `client_http.requests` 时，各 squid 图表记为 `unsupported`，下一次成功采集会清除。被动 Agent 协议和 Zabbix JSON-RPC 保持非目标。Tomcat 状态页失败或 XML 无法解析时，各 tomcat 图表记为 `unsupported`，下一次成功采集会清除。Varnish `varnishstat` 失败或没有计数器时，各 varnish 图表记为 `unsupported`，下一次成功采集会清除。`sql`/MySQL/PostgreSQL/Redis/Nginx 及 Memcached、MongoDB、Elasticsearch、RabbitMQ、Consul、HAProxy、Apache、lighttpd、PHP-FPM、Traefik、BIND、Unbound、CoreDNS、HDFS、ZooKeeper、NATS 等应用采集在连接或解析失败时，会把该插件下全部图表标为 `unsupported`，下一次成功采集会清除。代理、共识 HA、全实体 CRUD 和独立主机组树仍是部分能力。

| 阶段 | 内容 | 主要改动 |
|---|---|---|
| Z1 触发器与规则 | `nodata / change / diff / abschange / count / first / stddev / percentile / mad / trendavg / forecast / timeleft` 查询方法；用户宏 `{$NAME}`（全局 + 规则级）；问题手动关闭 `POST /api/v1/alarms/close`；审计日志 `GET /api/v1/audit` | `health/`、`api/`、新增 `audit/` |
| Z2 模板与主机模型 | 模板实体（规则集 + 采集配置 + 宏，可链接节点 / Room，继承覆盖）；主机组嵌套；可编辑主机清单；模板 YAML 导入导出 | `hub/`、`health/`、`api/`、Web |
| Z3 无代理采集 | SNMPv3 USM + 自定义 OID 项 + SNMP LLD；SSH 检查；通用 JMX（Jolokia）；HTTP Agent 项（JSONPath / regex / XPath 预处理）；多步 Web 场景；值预处理管线；依赖项 | `collect/`、新增 `preprocess/` |
| Z4 动作与自动化 | 多步升级链（步骤 / 操作 / 恢复操作）；动作条件引擎；远程命令 / 全局脚本（Agent 侧白名单执行）；短信与脚本渠道；事件关联规则 | `health/`、`stream/`、`api/` |
| Z5 服务与可视化 | 服务树 + SLA 计算与报表；网络拓扑图（LLDP 自动 + 手工）；可用性报表；定时报表导出；饼图组件 | 新增 `sla/`、Web |
| Z6 企业管理 | 用户组 + Room 级权限；TOTP 2FA；SAML；IP 段扫描发现；zabbix_sender 兼容 trapper 端口；PSK；Hub 队列自监控 | `api/`、`hub/`、`discover/` |

## 4. 非目标

- 不实现 Zabbix 数据库 schema / JSON-RPC API 兼容；对外仍为 REST + OpenAPI。
- 不引入外部数据库依赖；配置实体持久化沿用 JSON/YAML 文件 + CAS。
- 被动 Agent 协议（服务端主动连 10050）不作为默认；通过 Hub proxy 模式覆盖场景。
