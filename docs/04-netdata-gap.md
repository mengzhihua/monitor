# Monitor 全量移植 Netdata：差距清单与后续计划

> 对照 [Netdata](https://github.com/netdata/netdata) Agent + Cloud 全表面。
> **目标：全部搬过来**（图表 ID / 语义对齐；目标不存在则零配置自动禁用）。
> Prometheus / StatsD / OTLP / plugins.d 是**过渡覆盖**，不是终点：能原生采集的都做成 Go 采集器。

## 1. 现在有什么（M0–M6）

约 **72** 个内置采集器：`cpu` `load` `mem` `disk` `diskspace` `net` `uptime` `apps` `systemd` `docker` `nginx` `redis` `apache` `phpfpm` `memcached` `mysql` `postgres` `elasticsearch` `rabbitmq` `proc`（intr/forks/熵/fd/PSI/IPv4+IPv6 SNMP、conntrack、softnet、IPC、mdstat、battery、IPVS、NFS、ZFS、Btrfs、wireless、KSM、zram）`sensors` `netstat` `statsd` `prometheus` `otlp` `httpcheck` `portcheck` `ping` `sslcheck` `dnsquery` `nvidia` `logs` `ml` `haproxy` `lighttpd` `consul` `whoisquery` `mongodb` `pgbouncer` `chrony` `ntpd` `smartctl` `nvme` `apcupsd` `lvm` `zookeeper` `nats` `varnish` `squid` `tomcat` `traefik` `bind` `unbound` `coredns` `hdfs` `postfix` `exim` `dovecot` `fail2ban` `weblog` `squidlog` `openldap` `wireguard` `samba` `freeradius` `tor` `cgroup` `k8s_kubelet` `k8s_kubeproxy` `k8s_apiserver` `k8s_state`。

平台骨架已齐：三层 TSDB、Health 表达式、plugins.d、Child→Parent 流、Hub 查询扇出、RBAC、异常顾问（k-sigma / ks2 / volume）、Graphite/Influx/JSON/Prom remote write、Webhook/Slack/SMTP/钉钉/企微/飞书、Vue Dashboard。

Netdata 公开目录约 **850+ 集成**（go.d ≈ 150 个模块 + proc/cgroups/apps/windows/freebsd 内部插件 + Cloud）。Monitor 原生覆盖大约 **5%** 的集成名、**核心 Agent 路径约 60%**（采集→存→告警→流→查）。

## 2. 还差什么（按子系统）

### 2.1 Linux proc.plugin（装上就该有的系统图）

| 状态 | 模块 | 数据源 |
| --- | --- | --- |
| 有 | cpu/load/mem/disk/net/uptime、熵、fd、forks、intr、PSI、IPv4 SNMP、TCP 状态 | `system.go` `proc.go` `netstat.go` |
| **M7** | conntrack、softnet、IPC、mdstat、power_supply | `/proc` `/sys` |
| **M8** | IPv6 SNMP/sockstat、IPVS、NFS client/server、ZFS ARC、Btrfs、wireless、KSM、zram | `/proc` `/sys` |
| 未做 | InfiniBand、QoS/tc、SCTP、UDP-Lite、synproxy、NUMA、pagetype、interrupts 明细、softirq 明细 | proc.plugin 其余 |

### 2.2 容器 / cgroup

| 状态 | 能力 |
| --- | --- |
| 有 | Docker Engine API 每容器 cpu/mem/net/blkio；systemd `system.slice`；通用 cgroup v2 容器/VM 树（M11） |
| **M11** | kubelet / kube-proxy / k8s_state / k8s_apiserver |
| 未做 | libvirt/proxmox 专用采集 |

### 2.3 go.d 应用采集器（≈150，原生约 36）

**已有：** apache, docker, dns_query, elasticsearch, httpcheck, memcached, mysql, nginx, nvidia_smi, php-fpm, ping, portcheck, postgres, prometheus, rabbitmq, redis, sslcheck/x509, systemd（部分）, whoisquery（M7）, haproxy（M7）, lighttpd（M7）, consul（M7）, mongodb/pgbouncer/chrony/ntpd/smartctl/nvme/apcupsd/lvm（M8）, zookeeper/nats/varnish/squid/tomcat/traefik/bind/unbound/coredns/hdfs（M9）, postfix/exim/dovecot/fail2ban/weblog/squidlog/openldap/wireguard/samba/freeradius/tor（M10）, cgroup/k8s_kubelet/k8s_kubeproxy/k8s_apiserver/k8s_state（M11）。

**未做（按批次搬，每批原生实现 + 单测）：**

| 批次 | 采集器 |
| --- | --- |
| **M7** | haproxy, lighttpd, consul, whoisquery |
| **M8** | mongodb, pgbouncer, chrony, ntpd, smartctl, nvme, apcupsd, lvm |
| **M9** | zookeeper, nats, varnish, squid, tomcat, traefik, bind, unbound, coredns, hdfs |
| **M10** | postfix, exim, dovecot, fail2ban, squidlog, weblog, openldap, freeradius, tor, wireguard, samba |
| **M11（本轮）** | cgroup, k8s_kubelet, k8s_kubeproxy, k8s_apiserver, k8s_state |
| **M12 SNMP/云/杂项** | snmp, snmp_traps, snmp_topology, cloudwatch, azure_monitor, vsphere, redfish, megacli, hpssa, adaptec_raid, filecheck, monit, supervisord, 以及 init.go 其余模块 |

未轮到原生实现之前：该软件若暴露 `/metrics`，用已有 `prometheus` 采集器即可先出图（图表 ID 为 `prom.*`，与 Netdata 原生 ID 不同）。

### 2.4 API / 查询（对标 `/api/v1` + `/api/v2`）

| 状态 | 端点 |
| --- | --- |
| 有 | info, charts, chart, data, allmetrics(json/prometheus), collectors, functions, function, live, alarms, alarm_log, alarm_rules, weights, logs, ingest, nodes, stream, /metrics |
| **M7** | `GET /api/v1/contexts`；`GET\|POST /api/v1/alarms/silence`；`GET /api/v1/alarm_variables`；allmetrics `csv`/`shell` |
| 未做 | `/api/v1/data?context=` 跨图聚合；data `format=csv/ssv/jsonp`；`/api/v2`（contexts/nodes 批量、badge）；`alarm_count`；`manage/health` 其余管理项 |

### 2.5 健康 / 通知 / 导出

| 状态 | 能力 |
| --- | --- |
| 有 | 表达式引擎、delay/hysteresis/repeat、约 20 条内置规则、6 个通知渠道、4 种导出 |
| **M7** | 运行时静默 API；Telegram/Discord/PagerDuty；OpenTSDB；conntrack/md/haproxy/consul/battery/whois 规则 |
| **M8** | ntpd/chrony 失步、Mongo 连接、UPS 电池、SMART 失败、NVMe 寿命、LVM 容量 |
| **M9** | ZooKeeper outstanding、Tomcat/Traefik 错误、BIND SERVFAIL、CoreDNS panic、HDFS missing blocks |
| **M10** | 邮件队列、Dovecot 认证失败、Fail2ban 封禁、web/squid 日志 5xx、FreeRADIUS reject |
| **M11** | kubelet runtime 错误、API server 5xx、节点 NotReady、Failed pods |
| 未做 | Netdata `health.d` 其余数百条模板；维护窗口日历；告警聚合摘要；MongoDB 导出；Kinesis/Pub/Sub |

### 2.6 Hub / Cloud

| 状态 | 能力 |
| --- | --- |
| 有 | API Key 流式接入、replication 补传、节点列表、`node=` 查询、`hub.peers` 扇出、三角色 RBAC |
| 未做 | claim token、Space/Room、OIDC/LDAP、配置下发、样本环复制 HA、ACLK 语义、移动推送网关、只读分享链接 |

### 2.7 Dashboard / 客户端 / 平台

| 状态 | 能力 |
| --- | --- |
| 有 | Vue 实时图、告警/Functions/日志/异常顾问、Hub 节点切换 |
| **M7** | 告警静默按钮 |
| 未做 | context 总览页、静默倒计时、Netdata 式 metric correlation UI 深化、Flutter 五端（M3）、Android 服务端壳（M4）、Windows/FreeBSD proc 对等 |

### 2.8 ML / Functions

| 状态 | 能力 |
| --- | --- |
| 有 | k-sigma 一阶差分、anomaly-rate/ks2/volume；functions：processes、network-connections、services、logs、streaming |
| 未做 | k-means 多窗口模型（Netdata ML）；ebpf 网络观察；systemd-journal 原生库（现为 journalctl 子进程）；Windows ETW 深化 |

## 3. 分批计划（全部搬完）

原则不变：每批可合并、可测、图表 ID 对齐、目标缺失即禁用。

| 批次 | 搬什么 | 完成标准 |
| --- | --- | --- |
| **M7** | proc 五件套；haproxy/lighttpd/consul/whoisquery；contexts/silence/variables/allmetrics csv·shell；OpenTSDB；Telegram/Discord/PagerDuty；Dashboard 静默 | `go test -race`、`vue-tsc`、`smoke.sh` |
| **M8** | proc 剩余（ipv6/ipvs/nfs/zfs/btrfs/wireless/ksm/zram）；mongodb/pgbouncer/chrony/ntpd/smartctl/nvme/apcupsd/lvm | 同上 + 新采集器单测 |
| **M9** | zookeeper/nats/varnish/squid/tomcat/traefik/bind/unbound/coredns/hdfs | 默认端口探测 |
| **M10** | postfix/exim/dovecot/fail2ban/weblog/squidlog/openldap/wireguard/samba/freeradius/tor | 含日志类 |
| **M11 本轮** | 通用 cgroup + k8s_kubelet/kubeproxy/k8s_state/k8s_apiserver | kind/minikube 可选手测 |
| **M12** | snmp + go.d 长尾（每批 10–15 个直到 init.go 清空） | 清单打勾 |
| **M13** | 移植 Netdata health.d 规则全集；data context 聚合；`/api/v2` 子集 | 规则编译测试 |
| **M14** | Hub claim/Space/Room/OIDC/配置下发/环复制 | 双 Hub 冒烟 |
| **M15** | k-means ML、更多 Functions、导出 Mongo | weights 对比 |
| **M16** | Windows/FreeBSD 对等、Flutter、Android Agent | 跨平台 CI |

M12 的「长尾」按 `src/go/plugin/go.d/collector/init.go` 逐个打勾，不跳过；硬件 RAID / 云厂商 API 等需要外部密钥的，默认关闭、配置即启用。

## 4. 本轮（M11）交付清单

1. 采集器：`cgroup` `k8s_kubelet` `k8s_kubeproxy` `k8s_apiserver` `k8s_state`  
2. 默认路径/端口探测，目标缺失自动禁用  
3. 内置规则：kubelet runtime 错误、API server 5xx、节点 NotReady、Failed pods

每完成一批，把本节的「未做」改成「有」，不要另开平行文档。
