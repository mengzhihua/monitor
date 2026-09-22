# Monitor 实时监控平台

对标 [Netdata](https://www.netdata.cloud/) 的实时（每秒）、零配置、边缘优先的基础设施监控平台。

- **服务端** `monitord`（Go 单二进制，`--mode agent|hub`）：macOS / Linux / Windows / FreeBSD / Android
- **客户端**：内嵌 Web Dashboard（Vue3）+ Monitor App（Flutter）：macOS / Linux / Windows / Android / iOS

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/01-netdata-capability-study.md](docs/01-netdata-capability-study.md) | Netdata 能力调研：组件、数据流水线、平台覆盖、部署拓扑、安全模型 |
| [docs/02-architecture.md](docs/02-architecture.md) | Monitor 架构设计：总体架构、技术选型、服务端模块（采集/TSDB/健康/ML/流式/API/Functions）、Hub、Android 服务端专项、客户端、仓库结构、路线图、能力对照表 |
| [docs/03-plugins-d-protocol.md](docs/03-plugins-d-protocol.md) | plugins.d 外部采集器协议：命令语法、进程生命周期、配置、示例插件 |
| [docs/04-netdata-gap.md](docs/04-netdata-gap.md) | 与 Netdata 的全量差距清单与 M7–M26 移植计划（M19 起为后续批次） |

## 快速开始（M0）

依赖：Go 1.25+（CI/本轮验证固定 1.27.1）、Node 24.19.0（仅构建 Dashboard 时需要）。

```bash
make all          # 1) 构建 Vue Dashboard 并嵌入  2) 编译 core/bin/monitord
./core/bin/monitord -listen :19999 -data-dir ./data
# 浏览器打开 http://localhost:19999/
```

也可以只用 Go（不构建前端则 Dashboard 为空页，API 正常）：

```bash
cd core && go run ./cmd/monitord -listen :19999
```

配置：复制 [`monitor.example.yaml`](monitor.example.yaml) 为 `monitor.yaml`，或 `-config <path>`。命令行 `-listen` / `-data-dir` / `-log-level` 可覆盖配置文件。

跨平台构建：`make cross` 生成 linux(amd64/arm64)、darwin(amd64/arm64)、windows(amd64)、freebsd(amd64/arm64)、android(arm64) 二进制。

### 已实现能力（M0）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `cpu`（总量 + 每核）、`load`、`mem`（RAM/Swap/kernel/writeback/committed/pgfaults）、`disk`（IO/ops/util/await/avgsz）、`diskspace`（空间/inode）、`net`（带宽/包/错误/丢弃）、`uptime`、`sensors`（温度）；Linux `proc`（中断/forks/熵/fd/PSI/IPv4 SNMP）；基于 gopsutil，设备/网卡/挂载点运行时动态注册 |
| Registry | Host → Chart → Dimension 模型，`absolute` / `incremental` / `percentage-of-*-row` 算法，计数器回绕处理 |
| TSDB tier0 | 每秒原始数据，Gorilla 压缩（典型指标 ≈ 2 bit/样本），追加式块文件 + 重启恢复，按时长/大小保留，范围查询 + 聚合（avg/min/max/sum/median/last） |
| TSDB tier1/tier2 | 每分钟 / 每小时降采样层（每桶 min/max/sum/last/count，均值 = sum/count），写入时同步折叠、按层独立保留（默认 90 天 / 2 年）、重启恢复；`/api/v1/data` 按 `(before-after)/points` 自动选层（`tier=auto|0|1|2` 可强制），粗层无数据时自动回退到细层；`/api/v1/info.db.tiers` 暴露各层 update_every/保留/序列/块/字节 |
| plugins.d | 外部采集器进程（任意语言）通过 stdout 文本协议接入：`CHART/DIMENSION/CLABEL/BEGIN/SET/END/FLUSH/VARIABLE/DISABLE/EXIT`；自动发现 `plugins.d/*.plugin`，也可在 `plugins.list` 显式声明；崩溃自动重启（1s→60s 指数退避）、无输出看门狗、进程组回收、`DISABLE` 自禁用；状态在 `/api/v1/collectors.plugins` 与 `/api/v1/info.plugins` |
| 应用/服务采集器 | `apps`：进程按应用分组 → `apps.cpu/mem/processes/threads/io_*`；`systemd`：cgroup v2 每服务 CPU/内存/IO；`docker`：每容器 cpu/mem/net/blkio；`nginx`（stub_status）；`apache`（server-status?auto）；`phpfpm`；`redis`（内置 RESP）；`memcached`（STATS）。目标不可达时自动禁用 |
| Functions | `GET /api/v1/functions` / `function`；内置 `processes`（top）、`network-connections`、`services`、`logs`、`containers`、`disks`、`mounts`、`network-interfaces`；Hub 上 `streaming` |
| API | `/api/v1/info` `/charts` `/chart` `/data` `/allmetrics` `/contexts` `/collectors` `/functions` `/function` `/weights` `/logs`；`POST /api/v1/ingest/openmetrics` `/otlp`；`/api/v1/alarms` `/alarm_log` `/alarm_rules` `/alarm_variables` `/alarms/silence`；`/metrics` Prometheus 格式；`/api/v1/live` WebSocket 每秒推送；可选 `token` 与 `allow_from` CIDR 访问控制 |
| Dashboard | Vue3 + uPlot，按 family 分组，1m/5m/15m/1h 时间窗，WebSocket 实时增量刷新，采集器状态面板，告警面板，Functions 面板（进程/连接/服务表） |

### 已实现能力（M1：健康/告警）

| 模块 | 说明 |
| --- | --- |
| 规则 | YAML 告警规则（`name/on/lookup/calc/warn/crit/every/delay/repeat/to`），语义对齐 Netdata health.d；`on:` 可指定 chart ID 或 context（模板，自动绑定所有匹配图表，支持 `chart_labels` 过滤）；内置系统规则（CPU/iowait/load/RAM/swap/磁盘/网络/熵/PSI/TCP 重传/HTTP check），`health.d/*.yaml` 或 `health.alarms:` 同名覆盖 |
| 表达式 | `$this` `$status` `$WARNING/$CRITICAL/$CLEAR` 与图表维度、其他告警值、`$cpus/$ram_total`；四则/比较/逻辑/三元、`abs min max isnan isinf`，支持 Netdata 式滞回写法 `$this > (($status >= $WARNING) ? (75) : (85))` |
| 引擎 | 每秒调度、`lookup` 直接查 TSDB（average/min/max/sum/median/last，`percentage`/`absolute` 选项），状态 CLEAR/WARNING/CRITICAL 迁移，`delay up/down multiplier max` 抑制抖动（回到原状态则丢弃通知），`repeat` 周期重复提醒，事件持久化到 `data/health/alarm-log.jsonl` |
| 通知 | Webhook、Slack/Mattermost、SMTP、钉钉、企业微信、飞书；`to:` 角色 → 通道路由，`health.silent` 静默 |
| API | `/api/v1/alarms`（`?all=true` 含 CLEAR）、`/api/v1/alarm_log?after=<id>`、`/api/v1/alarm_rules`；`/api/v1/info.alarms` 汇总；`/api/v1/live` 推送 `{"alarm":{...}}` 事件 |

### 已实现能力（M2：Hub 集中模式）

| 模块 | 说明 |
| --- | --- |
| 流式上报 | Agent 出站 WebSocket（`stream.destinations`，支持 `ws://` / `wss://`，多目标顺序尝试）连接 Hub 的 `/api/v1/stream`，API Key 鉴权；`hello/welcome` 握手后发送主机信息、图表定义（含删除）、每秒样本、告警迁移，并响应 Hub 下发的 Functions 调用；每次连上后先发送完整告警快照，离线期间的告警变化在 Hub 端收敛 |
| 断线续传 | 有界队列（不阻塞采集），1s→60s 指数退避重连；Hub 在 `welcome` 中返回每张图各维度中最旧的最后时间戳（重复样本幂等丢弃），Agent 从本地 TSDB 回放缺口（`stream.replicate` / `hub.replicate` 限制回填范围），再接续实时数据 |
| Hub 节点管理 | 每个节点独立 Registry + TSDB 命名空间（`node:<id>|` 前缀，共用同一存储与分层降采样），元数据/图表定义/Functions/告警状态持久化到 `data/hub/nodes.json`，重启后离线节点仍可查历史与告警；状态 `live` / `stale` / `offline`；节点绑定首次注册它的 API Key（其它 Key 不能顶替同一节点 ID，建议每台 Agent 独立 Key）；`hub.max_nodes` / `max_charts_per_node` / `max_dims_per_chart` 限额 |
| API | `GET /api/v1/nodes`（`?status=` 过滤）、`DELETE /api/v1/nodes?node=`（仅离线节点）；`charts/chart/data/allmetrics/alarms/alarm_log/functions/function/live` 均支持 `node=<id>` 选择远端节点，`/metrics` 带 `node` 标签导出全部节点；`/api/v1/function?node=` 透传到 Agent 执行（如远端 `processes`） |
| RBAC | `web.users` 命名凭据 + 角色：`admin`（全部）、`troubleshooter`（只读 + Functions）、`viewer`（只读，不可执行 Functions）；`web.token` 仍视为 admin；`/api/v1/info.user` 返回当前身份 |
| Dashboard | Hub 模式顶部节点选择器（在线/全部计数、live/stale/offline 标识），切换后图表、告警、Functions、WebSocket 实时流全部切到所选节点；Agent 端显示到 Hub 的上报状态 |

两进程示例（同一台机器）：

```bash
# Hub
cat > hub.yaml <<'EOF'
mode: hub
web: { listen: ":19999" }
hub: { api_keys: ["dev-stream-key"] }
EOF
./core/bin/monitord -config hub.yaml -data-dir ./data-hub

# Agent → Hub
cat > agent.yaml <<'EOF'
web: { listen: ":19998" }
stream: { enabled: true, destinations: ["127.0.0.1:19999"], api_key: "dev-stream-key" }
EOF
./core/bin/monitord -config agent.yaml -data-dir ./data-agent
curl -s localhost:19999/api/v1/nodes | jq '.nodes[] | {id, hostname, status}'
```

### 已实现能力（M5：摄入 / 合成检查 / 导出 / ML）

| 模块 | 说明 |
| --- | --- |
| OpenMetrics 摄入 | `POST /api/v1/ingest/openmetrics` 解析 Prometheus / OpenMetrics 文本，按 metric 建图、按 label 建维度（上限 500 图 × 200 维） |
| Prometheus 抓取 | `collectors.modules.prometheus.jobs` 定期拉取任意 `/metrics`，图表前缀 `prom.` |
| StatsD | 默认监听 `127.0.0.1:8125` UDP，`name:value\|c\|g\|ms` → `statsd.counter/gauge/timer` |
| 合成检查 | `httpcheck`（状态/耗时/长度/状态码/证书到期）、`portcheck`（TCP）、`ping`（ICMP 或 TCP RTT） |
| 导出 | `export.destinations`：Graphite TCP、InfluxDB line protocol、JSON HTTP、OpenTSDB、Prometheus remote write、MongoDB |
| ML | 每维度 k-means（k=2，lag 差分窗口，多模型投票）+ k-sigma 回退；`anomaly_detection.anomaly_rate`；`GET /api/v1/weights?method=anomaly-rate\|kmeans` |
| IPv4 / 连接 | `ipv4.*`（/proc/net/snmp）、`ip.tcpsock` TCP 状态；Function `network-connections` |

### 已实现能力（M6：日志 / OTLP / 应用采集 / 集群 / 关联分析）

| 模块 | 说明 |
| --- | --- |
| 日志 | Function `logs` 与 `GET /api/v1/logs`：journald（`journalctl`）、Windows Event Log、`/var/log/syslog` 等文件；`logs.written` / `logs.severity` 图 |
| OTLP | `POST /api/v1/ingest/otlp` 与标准 `POST /v1/metrics`（JSON 或 protobuf）；采集器默认监听 `127.0.0.1:4318` |
| 应用采集器 | `mysql`（SHOW GLOBAL STATUS）、`postgres`（pg_stat_*）、`elasticsearch`、`rabbitmq`；不可达时自动禁用 |
| 合成 / GPU | `sslcheck`（证书剩余天数）、`dnsquery`、`nvidia`（nvidia-smi） |
| 导出 | Prometheus remote write（snappy + protobuf，`export.destinations.type: prometheus`） |
| 关联分析 | `GET /api/v1/weights?method=ks2\|volume` 比较故障窗口与基线窗口 |
| Hub 集群 | `hub.peers` 发现对端节点，未知 `node=` 反代到拥有该节点的 Hub |
| Dashboard | 日志面板、异常顾问（anomaly-rate / ks2 / volume） |

### 已实现能力（M7：proc 扩展 / 静默 / OpenTSDB / 更多应用）

| 模块 | 说明 |
| --- | --- |
| Linux proc | conntrack 表与计数器、softnet、SysV IPC、mdstat RAID、power_supply 电池 |
| 应用采集器 | `haproxy`（stats CSV）、`lighttpd`、`consul`、`whoisquery`（域名到期）；不可达自动禁用 |
| API | `GET /api/v1/contexts`；`GET\|POST /api/v1/alarms/silence`；`GET /api/v1/alarm_variables`；`/api/v1/allmetrics?format=csv\|shell` |
| 导出 / 通知 | OpenTSDB `/api/put`；Telegram / Discord / PagerDuty |
| Dashboard | 告警面板全部静默 / 单条静默 |
| 差距清单 | [docs/04-netdata-gap.md](docs/04-netdata-gap.md) 全量移植对照与 M19–M26 后续批次 |

### 已实现能力（M8：proc 剩余 / 存储 / 时间 / 硬件）

| 模块 | 说明 |
| --- | --- |
| Linux proc | IPv6 SNMP/sockstat、IPVS、NFS 客户端/服务端、ZFS ARC、Btrfs、wireless、KSM、zram |
| 应用采集器 | `mongodb`（serverStatus）、`pgbouncer`、`chrony`、`ntpd`、`smartctl`、`nvme`、`apcupsd`、`lvm`；目标缺失自动禁用 |
| 告警 | ntpd 失步、chrony 未同步、Mongo 连接、UPS 电池、SMART 失败、NVMe 寿命、LVM 容量 |

### 已实现能力（M9：队列 / DNS / 代理）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `zookeeper`（mntr :2181）、`nats`（/varz :8222）、`varnish`（varnishstat）、`squid`（cachemgr）、`tomcat`（status XML）、`traefik`（/metrics）、`bind`（stats JSON）、`unbound`（unbound-control）、`coredns`（:9153/metrics）、`hdfs`（NameNode JMX）；目标缺失自动禁用 |
| 告警 | ZooKeeper outstanding、Tomcat/Traefik 错误、BIND SERVFAIL、CoreDNS panic、HDFS missing blocks |

### 已实现能力（M10：邮件 / 安全 / 日志）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `postfix`（postqueue）、`exim`（-bpc）、`dovecot`（EXPORT :24242）、`fail2ban`、`weblog`（access.log）、`squidlog`、`openldap`（cn=Monitor）、`wireguard`（wg dump）、`samba`（smbstatus -P）、`freeradius`（radclient status）、`tor`（control :9051）；目标缺失自动禁用 |
| 告警 | 邮件队列、Dovecot 认证失败、Fail2ban 封禁、web/squid 日志 5xx、FreeRADIUS reject |

### 已实现能力（M11：cgroup / Kubernetes）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `cgroup`（cgroup v2 容器/VM：cpu/mem/io/pids）、`k8s_kubelet`（:10250/:10255/metrics）、`k8s_kubeproxy`（:10249/metrics）、`k8s_apiserver`（:6443/metrics）、`k8s_state`（API nodes/pods）；目标缺失自动禁用 |
| 告警 | kubelet runtime 错误、API server 5xx、节点 NotReady、Failed pods |

### 已实现能力（M12：SNMP / 存储 / 长尾应用）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `snmp`（snmpwalk IF-MIB）、`proxysql`、`clickhouse`、`cockroachdb`、`pulsar`、`envoy`、`upsd`（NUT :3493）、`zfspool`、`dmcache`、`filecheck`、`supervisord`、`monit`；目标缺失自动禁用 |
| 告警 | ZFS degraded、NUT 电池、ProxySQL slow、Envoy 5xx、文件缺失、Supervisord/Monit、SNMP ifDown、Cockroach live nodes |

### 已实现能力（M12 续：日志栈 / 存储 / DNS）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `fluentd`（/api/plugins.json）、`logstash`（/_node/stats）、`cassandra`（JMX Prometheus :7072）、`ceph`（`ceph status --format json`）、`couchdb`、`couchbase`、`hddtemp`（:7634）、`openvpn`（management :7505）、`beanstalk`（:11300）、`uwsgi`（stats :1717）、`powerdns`（HTTP API）、`dnsmasq`（CHAOS TXT）；目标缺失自动禁用 |
| 告警 | Fluentd retry、Logstash heap、Cassandra failures、Ceph ERR、CouchDB 5xx、Couchbase quota、HDD 温度、Beanstalk buried、uWSGI exceptions、PowerDNS latency |

### 已实现能力（M12 续 2：RAID / BMC / 更多应用）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `megacli`、`hpssa`、`adaptecraid`、`redfish`、`activemq`、`gearman`、`geth`、`ipfs`、`pihole`、`powerdns_recursor`、`rspamd`、`typesense`；目标缺失自动禁用 |
| 告警 | MegaRAID degraded、HPSSA nok、Adaptec LD critical、Redfish Critical、ActiveMQ backlog、Geth RPC fail、Recursor drops、Typesense unhealthy |

### 已实现能力（M12 续 3：DNS / Web / RAID / DB）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `storcli`、`nginxvts`、`tengine`、`nsd`、`dnsdist`、`dnsmasq_dhcp`、`isc_dhcpd`、`puppet`、`openvpn_status_log`、`rethinkdb`、`yugabytedb`、`vernemq`；目标缺失自动禁用 |
| 告警 | StorCLI unhealthy、nginx VTS 5xx、Tengine 5xx、NSD drops、dnsdist drops、ISC dhcpd pool、Yugabyte over-limit、VerneMQ socket close |

### 已实现能力（M12 续 4：Web / DB / 应用）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `icecast`、`phpdaemon`、`pika`、`maxscale`、`nginxplus`、`nginxunit`、`docker_engine`、`riakkv`、`litespeed`、`boinc`、`spigotmc`、`w1sensor`；目标缺失自动禁用 |
| 告警 | phpDaemon idle、MaxScale errors、NGINX Plus dropped、Docker health fails、Riak FSM、BOINC compute_error、SpigotMC TPS、w1sensor hot |

### 已实现能力（M12 续 5：硬件 / REST 存储）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `ap`、`dockerhub`、`ethtool`、`intelgpu`、`logind`、`dcgm`、`panos`、`powerstore`、`powervault`、`s3check`、`scaleio`、`smbios_memory`；目标缺失自动禁用 |
| 告警 | 光模块温度、Intel GPU busy、DCGM GPU 温度、PAN-OS session、PowerStore/PowerVault health、S3 check、ScaleIO capacity |

### 已实现能力（M12 续 6：云 / SQL / SNMP traps）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `vcsa`（REST health）、`mssql`（sqlcmd）、`oracledb`（sqlplus）、`sql`（mysql/psql/sqlcmd/sqlplus）、`cloudwatch`（SigV4）、`azure_monitor`（OAuth）、`vsphere`（SOAP /sdk）、`cato_networks`（GraphQL）、`snmp_traps`（UDP）、`snmp_topology`（snmpwalk LLDP）；缺凭证或目标则自动禁用 |
| 告警 | VCSA red、MSSQL blocked、Oracle sessions、vSphere disconnected、Cato site、SNMP trap flood、topology 无邻居、SQL 慢查询 |

### 已实现能力（M13：查询 API / 系统 health 模板）

| 模块 | 说明 |
| --- | --- |
| 查询 | `/api/v1/data?context=` 同名维度跨图求和；`format=csv\|ssv\|jsonp`；`GET /api/v1/alarm_count`；`GET /api/v1/badge.svg` |
| `/api/v2` | `contexts` / `nodes` / `data` / `badge.svg`（`api: 2` 包装） |
| 告警 | 剩余系统模板：CPU steal/guest、FD、blocked、forks、disk await、IO pressure、IPv4/IPv6 UDP/TCP/IP 错误、page faults、committed、writeback、TIME_WAIT、Docker exited |

### 已实现能力（M14：Hub Cloud）

| 模块 | 说明 |
| --- | --- |
| 认领 | `POST /api/v1/hub/claim-tokens` 签发一次性 token；Agent `stream.claim_token` 在连接时 `POST /api/v1/claim` 换成长生命周期 stream key，并加入指定 Room |
| 组织 | Space → Room → nodes；`GET/POST/DELETE /api/v1/hub/spaces`、`…/rooms`；节点列表带 `space_id` / `room_id` |
| 配置下发 | `PUT /api/v1/hub/config` 写 per-node `disabled` 采集器列表；Agent 握手后拉 `GET /api/v1/agent/config` 并 `Scheduler.SetEnabled` |
| 环复制 | `hub.peers` 周期 `POST /api/v1/hub/ring` 推送非 replica 节点的最近样本；对端以 replica 节点展示；本地 live 连接优先 |
| 登录 | `web.oidc` 授权码流程；回调铸造本地 session token（默认 viewer）；浏览器带 `Accept: text/html` 时跳转 `/?token=` |
| Dashboard | Hub 面板：创建 Space/Room、签发/复制 claim、下发禁用列表；节点选择器标注 replica |

### 已实现能力（M15：k-means ML / Functions / Mongo 导出）

| 模块 | 说明 |
| --- | --- |
| ML | 每维度 k=2 k-means（lag 窗口一阶差分、多训练窗口投票）；未训练完时回退 k-sigma；`GET /api/v1/weights?method=kmeans` |
| Functions | `containers`（Docker）、`disks`、`mounts`、`network-interfaces` |
| 导出 | `export.destinations.type: mongodb`（OP_MSG insert，`database`/`collection`） |

### 已实现能力（M16：Windows / FreeBSD / Flutter Functions / Android logcat）

| 模块 | 说明 |
| --- | --- |
| FreeBSD | `freebsd` 采集器：sysctl → `system.ctxt/intr/softirq/forks`、`mem.wired/laundry`、IPC 信号量/共享内存/消息队列、`freebsd.cpu.temperature`；M22 补 syscalls/pgfaults/swapio/RAM/ZFS ARC/ipfw/net.inet*/gstat/df/netstat；非 FreeBSD 自动禁用；`GOOS=freebsd` 交叉编译 |
| Windows | `windows` 采集器：进程/线程/句柄/上下文切换（WMI `Win32_PerfRawData_PerfOS_System` + gopsutil）；Function `windows-services`（`sc query`）；非 Windows 自动禁用 |
| Flutter | 客户端增加 Functions 页（`/api/v1/functions` + `/function` 表），与 Web 面板同一套 API |
| Android | Function `logs` 走 `logcat`；服务端壳默认关掉 Linux 专用采集器，声明 `READ_LOGS` |

### 已实现能力（M17：剩余缺口一次补齐）

| 模块 | 说明 |
| --- | --- |
| Linux proc | InfiniBand、QoS/tc、SCTP、UDP-Lite、synproxy、NUMA、pagetypeinfo、per-IRQ / softirq 明细 |
| 采集器 | `libvirt`（virsh）、`proxmox`（PVE REST）、`ebpf`（bpftool prog show + Function `ebpf-programs`）；目标缺失自动禁用 |
| API | `GET\|PUT /api/v1/manage/health`（pause / 静默 / 维护截止）；`GET /api/v1/alarm_summary`；v2 `data?group_by=node`；v2 `nodes?contexts=`；`POST /api/v1/auth/ldap`；`POST /api/v1/share` 只读链接；`info.aclk` |
| 导出 / 通知 | Kinesis / Pub/Sub HTTP JSON；`health.notify.push`；维护窗口日历 |
| Dashboard | Context 总览、静默倒计时、ks2/volume 窗口输入、Hub 只读分享链接 |
| 日志 | Windows ETW / Event Log `channel=` 参数 |

### 已实现能力（M18：原生插件补齐）

| 模块 | 说明 |
| --- | --- |
| Linux proc | EDAC ECC、SLAB、zswap、RAPL powercap、DRM GPU busy/freq、bcache、adjtimex 时钟同步状态 |
| 采集器 | `cups`（lpstat）、`xenstat`（xl list）、`ioping`、`nftables`（nft counters）、`podman`（REST + Function `podman-containers`）、`ipmi`（ipmitool sdr） |
| API / 导出 | `/api/v2/q`、`/api/v2/alert_transitions`；Kafka REST JSON records |

### 已实现能力（M22：freebsd.plugin 剩余）

| 模块 | 说明 |
| --- | --- |
| sysctl | `system.syscalls`、`mem.pgfaults` / `mem.swapio` / `mem.available`、`system.ram` 明细、`system.active_processes`、`cpu.scaling_cur_freq`、`system.interrupts`（hw.intrcnt）、`system.softnet_stat` |
| 网络 | `ipv4.tcpsock/tcppackets/tcperrors/tcphandshake`、UDP/ICMP/IP、`ipv6.packets/errors/icmp`；点分 sysctl 键（opaque `net.inet.*.stats` 结构体无 CGO 不解码） |
| ZFS | `kstat.zfs.misc.arcstats` → `zfs.arc_size` 等 + `zfs.l2_size/bytes/memory_ops/important_ops/arc_size_breakdown/trim_bytes/trim_requests` |
| ipfw | `ipfw -a list` → `ipfw.mem/packets/bytes/active/expired` |
| 磁盘/挂载/网卡 | `gstat` / `df -kP` / `netstat -ibn` 近似 devstat / getmntinfo / getifaddrs；gopsutil 已占用同 ID 则跳过 |
| health | `zfs_memory_throttle`、`freebsd_ipfw_drops`、`freebsd_softnet_drops` |

### 已实现能力（M23：IBM / pandas / 容器运行时）

| 模块 | 说明 |
| --- | --- |
| ibm.d | `db2`（db2 CLI）、`as400`（isql）、`mq`（dspmq/runmqsc）、`websphere`（PMI JSON / Prometheus）；无 DSN/命令/URL 则自动禁用；默认无 CGO |
| python.d 残留 | `pandas`（JSON/CSV 首行，不 eval Python）、`go_expvar`（`/debug/vars` memstats）、`am2320`（sysfs I2C） |
| 容器 | `lxc`（lxc-ls / cgroup）、`ecs`（task metadata v4）、`containerd`（ctr）；Functions `lxc-containers` / `ecs-containers` / `containerd-containers` |

### 开发

```bash
make test         # go vet + go test -race + 前端 typecheck
./plugins.d/example.plugin 1 | head   # 手工查看示例插件的协议输出
make lint         # gofmt + vet
./scripts/smoke.sh    # 启动 monitord 并验证 API / metrics / dashboard
cd web && npm run dev # 前端热更新，API 代理到 127.0.0.1:19999
```

### 客户端（Flutter，M3 起步）

`app/` 是 macOS / Windows / Linux / Android / iOS 五端客户端：填入 Agent 或 Hub 地址（可选 Bearer token）即可连接，Hub 模式下可切换节点；图表页按 family 分组，历史数据走 `/api/v1/data`，实时点走 `/api/v1/live` WebSocket（断线 3s 自动重连）；告警页显示当前告警与最近状态变化；Functions 页调用 `/api/v1/function`（进程/连接/服务/日志等表）。

```bash
cd app && flutter pub get
flutter run -d macos      # 或 linux / windows / <android-device> / <ios-device>
flutter analyze && flutter test
```

### Android 服务端（M4 起步）

`android/` 是原生 Kotlin 壳：前台服务拉起随包分发的静态 `monitord`（`jniLibs/arm64-v8a/libmonitord.so`），可设置监听端口、可选上报到 Hub、开机自启，并直接打开内嵌 Dashboard。Android 沙箱限制 `/proc/net` 等接口，网络类图表可能缺失；日志走 `logcat`。

```bash
./scripts/build-android-server.sh assembleRelease   # 需要 Go、JDK 17、ANDROID_HOME（SDK 35）
```

### 发布（GitHub Release）

每次推送到 `main` 会自动打 tag（在最新 `v*` 的基础上 patch +1，如 `v0.1.3` → `v0.1.4`）并构建、发布全部安装包；需要升 minor/major 时手动推 tag 即可，也可手动运行 Release 工作流重建某个已有 tag：

| | 产物 |
|---|---|
| 服务端 `monitord` | macOS arm64（Apple 芯片）/ amd64（Intel）、Windows amd64、Linux amd64 / arm64、FreeBSD amd64 / arm64、Android arm64 APK |
| 客户端 `Monitor` | macOS arm64 / amd64（.dmg + .zip）、Windows amd64（.zip）、Linux amd64（.tar.gz）、Android（.apk）、iOS（未签名 .ipa） |

```bash
git tag v0.2.0 && git push origin v0.2.0   # 可选：手动指定版本号
```

目前所有包均未签名/公证；配置 `ANDROID_KEYSTORE_B64` 等 secrets 后 Android 服务端 APK 会自动签名，Apple / Windows 签名后续接入。

## 仓库规划

```
core/      Go：monitord（agent/hub）、monitorctl、gomobile 绑定
web/       Vue3 Dashboard（embed 进 monitord）
app/       Flutter 五端客户端
android/   Android 服务端壳（Kotlin 前台服务，运行随包分发的 monitord）
scripts/   smoke 测试、Android 服务端打包、macOS 分架构打包
.github/   CI 与 Release 工作流
plugins/   外部采集器（plugins.d 文本协议）
proto/     节点↔Hub 流协议
api/       OpenAPI 定义
packaging/ 安装包与安装脚本
```

## 路线图

M0–M18 已合入：骨架 → Agent → Hub → 客户端 → Android → ML/摄入 → 日志/OTLP → go.d 全目录 → API/Health → Cloud 骨架 → k-means → 跨平台骨架 → 原生插件补齐。

后续：M19 内核深度（eBPF/perf）→ M20 日志/查看器 → M21 Windows.plugin → M24 API v3 → M25 Cloud 产品面 → M26 集成目录。M22 freebsd.plugin 剩余见本页；M23 IBM/残留已合入 main。详见 [docs/04-netdata-gap.md](docs/04-netdata-gap.md) 与架构文档 §11。
