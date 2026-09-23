# Monitor 实时监控平台 · 2.0.1

对标 [Netdata](https://www.netdata.cloud/) 的实时（每秒）、零配置、边缘优先的基础设施监控平台。

- **服务端** `monitord`（Go 单二进制，配置 `mode: agent` 或 `mode: hub`）：macOS / Linux / Windows / FreeBSD / Android
- **客户端**：内嵌 Web Dashboard（Vue3）+ Monitor App（Flutter）：macOS / Linux / Windows / Android / iOS

代码仓库：[GitHub · mengzhihua/monitor](https://github.com/mengzhihua/monitor) · [GitLab · mengzhihua/netdata](https://gitlab.tly.life:20443/mengzhihua/netdata)。完成验证后将改动合入 `main` 并[双仓库推送](#双仓库推送)，从 GitHub 拉取更新。

## 2.0 运维工作台

内嵌 Web Dashboard 新增「运维总览」：集中查看本机及 Hub 节点的 CPU、内存、在线状态、数据新鲜度和严重问题；可按主机、级别、状态和待确认条件筛选，并直接跳转到对应图表。缺失或过期指标明确标注，不显示为正常数值。

「问题中心」支持管理员/排障人员确认、撤销确认和添加备注，保存操作者及处理记录，重启可读取；严重级别变化或恢复后再次触发需要重新确认。确认不会停止通知或改变告警状态。支持保存个人视图、导出当前 JSON 快照，刷新失败时显示上次成功时间。

新增责任人指派、分配给我和「待处理 / 排查中 / 观察中」进度，可筛选个人队列、未分配问题和处理阶段，并保存为个人视图。多人同时修改会提示冲突并保留草稿；责任人变更与进度流转写入历史，观察中不代表告警恢复。旧版处置记录自动兼容读取，首次保存升级存储格式，降级前需恢复升级前备份。

「批量处置」可显式选择最多 50 个当前问题，核对范围后统一确认、撤销确认、指派、取消指派、更新进度或补充备注。整批一次保存，任一问题已恢复、版本冲突或写盘失败都不产生部分修改；改变筛选会清空选择，后台刷新不会自动更新所选版本。每个问题独立保留操作者和历史，权限与单条处置一致。

「通知诊断」显示本机发送队列、进行中的调用、各通道接受/失败次数，以及静默、维护、无匹配通道和队列满等结果；支持关键词、通道和结果筛选。记录最近 500 条结果，累计计数属于当前进程，重启后清空，不汇总远端 Agent。通道调用成功不代表用户收件，HTTP 2xx 不验证响应业务码；诊断不自动重试或发测试消息，错误信息不包含通知地址、令牌及响应正文。详见[通知诊断说明](docs/06-monitor-2.0.md#通知诊断)。

「计划维护窗口」支持管理员为本机所有告警或指定图表/告警安排维护，预览范围后创建，记录原因和操作者，可提前取消。计划重启保留，到期只解除本计划的通知抑制，采集与告警求值继续运行；重叠计划独立生效，不向远端 Agent 下发。修复了首次通知被抑制或发送失败后，配置的重复提醒可能永远不再执行的问题；提醒按配置周期调度，不补发维护期间的旧事件。详见[计划维护说明](docs/06-monitor-2.0.md#计划维护窗口)。

「处理记录查询」可检索全部已保存的处理阶段，支持关键词、责任人、操作者、级别、进度、确认状态及时间范围筛选。按页加载历史，按完整筛选结果导出 JSON/CSV；处理记录变化或服务重启后要求重新查询，避免翻页和导出时漏项。界面明确显示存储用量及操作历史裁剪情况。

「个人视图」现在保存到服务端，同一凭据或身份账号连接同一服务后可跨浏览器、跨设备使用。每个账号最多 10 个视图，支持删除、刷新和手动导入旧浏览器视图；并发修改会提示冲突并保留草稿。只读账号也能保存自己的筛选，不能因此处理告警。静态密码/API token 更换后使用新的视图空间；OIDC 按已验证的签发者与主体、LDAP 按目录配置及精确登录名识别。详见 [个人视图说明](docs/06-monitor-2.0.md#个人视图与跨设备使用)。

「指标图表 → 常用聚合看板」内置 54 个可搜索、按场景分类的样板，覆盖研发总览、数据库与缓存、容器、Kubernetes、入口与证书、容量、硬件、云平台及操作系统等场景，按当前节点实际采集到的图表自动匹配，缺少指标时明确提示。可以复制为个人看板，配置分组、具体图表、顺序、时间范围与列数，并通过 JSON 备份和选择性导入。详见[聚合看板说明](docs/07-preset-dashboards.md)。

本轮 Web 看板收尾更新整合中文指标说明、收藏与分组导航、编辑草稿恢复、整个看板统一历史时间、图表放大与采样统计、相邻时段对比及 CSV 导出。个人看板导入先预览内容与当前节点覆盖情况，再勾选保存为副本；保留未采集的图表配置。使用方式及验收范围见 [Web 看板更新说明](docs/09-dashboard-final-update.md)。

本轮交付 **2.0.1 服务端与 Web 正式版本**。运维工作台位于内嵌 Web，原有 Flutter 客户端保持已有功能和独立版本。新增能力、升级与回退步骤、发布包和验证边界见 [2.0.1 发布说明](docs/08-release-2.0.md)；API 与持久化限制见 [2.0 说明](docs/06-monitor-2.0.md)。`make all` 使用根目录 `VERSION` 构建，`make package-server` 生成带源码提交及 SHA-256 校验的服务端包。

本轮性能与发布收尾将进程快照、历史查询优化与最新看板整合，并补齐跨平台打包依赖和验收入口，详见 [最终整合更新](docs/10-final-integration.md)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/01-netdata-capability-study.md](docs/01-netdata-capability-study.md) | Netdata 能力调研：组件、数据流水线、平台覆盖、部署拓扑、安全模型 |
| [docs/02-architecture.md](docs/02-architecture.md) | Monitor 架构设计：总体架构、技术选型、服务端模块（采集/TSDB/健康/ML/流式/API/Functions）、Hub、Android 服务端专项、客户端、仓库结构、路线图、能力对照表 |
| [docs/03-plugins-d-protocol.md](docs/03-plugins-d-protocol.md) | plugins.d 外部采集器协议：命令语法、进程生命周期、配置、示例插件 |
| [docs/04-netdata-gap.md](docs/04-netdata-gap.md) | 与 Netdata 的全量差距清单与 M7–M26 移植计划（M19 起为后续批次） |
| [docs/05-acceptance.md](docs/05-acceptance.md) | 五阶段交付、验收命令、实测结果与未验证边界 |
| [docs/06-monitor-2.0.md](docs/06-monitor-2.0.md) | 2.0 运维总览、问题处置、权限、持久化与竞品对照 |
| [docs/07-preset-dashboards.md](docs/07-preset-dashboards.md) | 54 个常用聚合样板、个人看板配置、中文说明与历史对比 |
| [docs/09-dashboard-final-update.md](docs/09-dashboard-final-update.md) | Web 看板收尾更新、导入预览与选择、功能范围及验证边界 |
| [docs/10-final-integration.md](docs/10-final-integration.md) | 性能与发布收尾、升级说明及本轮验证范围 |
| [docs/08-release-2.0.md](docs/08-release-2.0.md) | 2.0.1 版本说明、离线升级/回退、发布校验和验收边界 |
| [scripts/README.md](scripts/README.md) | 构建、测试、打包脚本与输出位置 |

## 快速开始

依赖：Go 1.25+（CI/本轮验证固定 1.27.1）、Node 24.19.0（仅构建 Dashboard 时需要）。

```bash
make all          # 1) 构建 Vue Dashboard 并嵌入  2) 编译 core/bin/monitord
./core/bin/monitord -listen :19999 -data-dir ./data
# 在另一终端读取本机生成的登录密码（无需用户名）：
cat ./data/web-password
# 浏览器打开 http://localhost:19999/，输入上面的密码
```

也可以只用 Go（不构建前端则 Dashboard 为空页，API 正常）：

```bash
cd core && go run ./cmd/monitord -listen :19999
```

配置：复制 [`monitor.example.yaml`](monitor.example.yaml) 为 `monitor.yaml`，或 `-config <path>`。命令行 `-listen` / `-data-dir` / `-log-level` 可覆盖配置文件。

### 默认登录密码

未配置 `web.token`、`web.users`、OIDC 或 LDAP 时，首次启动会生成**每台部署独立的随机管理员密码**，无需用户名。重启沿用该密码；旧的无认证部署升级后也会要求登录。已有认证配置继续生效，不会额外生成管理员密码。

密码位于 `<data_dir>/web-password`。`data_dir` 来自 `-data-dir` 或 `global.data_dir`，默认是**启动时工作目录**下的 `./data`，不是可执行文件所在目录。按上面的启动命令，在另一终端读取：

```bash
# macOS / Linux / FreeBSD，在启动命令所在目录执行
cat ./data/web-password
```

```powershell
# Windows PowerShell
Get-Content .\data\web-password
```

在 Web 或 Flutter 客户端的“登录密码 / API token”输入框填写密码。Android **服务端**启动后点击“查看登录密码”，详见 [Android 服务端说明](android/README.md)。API 和 Prometheus 抓取使用 `Authorization: Bearer <密码>`；登录页静态资源及 `/healthz` 健康检查仍可公开访问，监控数据、日志、指标及实时连接需要认证。

密码文件在 Unix 上使用 `0600` 权限，启动日志只提示位置，不打印密码。文件损坏或无法保存时服务拒绝启动。Windows 部署请通过数据目录 ACL 限制其他用户读取；备份包含该密码，应按敏感数据保管。

### 修改或重置密码

自定义密码：在服务器私有的 `monitor.yaml` 中设置一个足够长的随机值，然后重启服务。该配置不提交到 Git：

```yaml
web:
  token: "替换为你生成的长随机密码"
```

重置**自动生成的密码**：先停止服务，删除实际数据目录中的 `web-password`，再启动并读取新文件。旧密码立即失效。若已配置 `web.token`、`web.users`、OIDC 或 LDAP，应修改对应的认证配置，删除文件不会重置这些凭据。

远程部署应通过 HTTPS 反向代理或可信加密网络访问；密码认证本身不加密 HTTP。不要把真实密码写进 URL、文档或仓库。

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
| Functions | `GET /api/v1/functions` / `function`；内置 `processes`（top）、`network-connections`、`services`、`logs`、`containers`、`disks`、`mounts`、`network-interfaces`；采集忙碌时仍保持函数入口可用，手动禁用后撤销；Hub 上 `streaming` |
| API | `/api/v1/info` `/charts` `/chart` `/data` `/allmetrics` `/contexts` `/collectors` `/functions` `/function` `/weights` `/logs`；`POST /api/v1/ingest/openmetrics` `/otlp`；`/api/v1/alarms` `/alarm_log` `/alarm_rules` `/alarm_variables` `/alarms/silence`；`/metrics` Prometheus 格式；`/api/v1/live` WebSocket 每秒推送；默认密码认证与可选 `allow_from` CIDR 访问控制 |
| Dashboard | Vue3 + uPlot，按 family 分组，历史图表随视口加载、滚出后暂停请求、周期刷新错峰且复用画布，WebSocket 实时增量刷新；采集器状态、告警、Functions 面板 |

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
| Prometheus 抓取 | `collectors.modules.prometheus.jobs` 定期拉取任意 `/metrics`；点名 profile 出原生 ID，其余前缀 `prom.` |
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
| 告警 | 剩余系统模板：CPU steal/guest、FD、blocked、forks、disk await、IO pressure、IPv4/IPv6 UDP/TCP/IP 错误、page faults、committed（仅 `vm.overcommit_memory=2`）、writeback、TIME_WAIT、Docker exited |

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
| Windows | `windows` 采集器：进程/线程/句柄/上下文切换（WMI + gopsutil）；Perflib 主路径 WMI `Win32_PerfFormattedData_*`（可选 `typeperf_scan`）→ CPU 队列、内核池、逻辑/物理磁盘、网卡、IIS 站点/应用池、ASP.NET、.NET CLR、Hyper-V、SMB、NUMA、thermal、传感器、`cpu.temperature`、AD/ADCS/ADFS、Exchange、Terminal Services、`powersupply.capacity`；`windows.service_state.*` 出图 + Function `windows-services`；角色/对象缺失自动跳过；非 Windows 自动禁用 |
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
| 采集器 | `cups`（IPP，失败再 lpstat）、`xenstat`（xenstore，失败再 xl）、`ioping`（直接读，失败再 ioping）、`nftables`（netlink，失败再 nft）、`podman`（REST + Function `podman-containers`）、`ipmi`（ipmi-sensors，失败再 ipmitool） |
| API / 导出 | `/api/v2/q`、`/api/v2/alert_transitions`；Kafka REST JSON records |

### 已实现能力（M19：内核深度）

| 模块 | 说明 |
| --- | --- |
| eBPF | bpftool 库存图 + 程序族 `ebpf.cachestat/dcstat/fd/vfs/oomkill/process/shm/swap/disk/mount/hardirq`（kprobe_profile 优先，否则 /proc 近似）；无源则禁用子图而不是整模块 |
| perf | `perf stat -a` → `perf.cpu` / `perf.instructions` / `perf.cache_misses`；无权限自动禁用 |
| proc | NUMA `mem.extfrag.*`、`audit.backlog`（`auditctl -s`） |
| 其它 | `idlejitter`（`system.idlejitter`）；`nfacct`；apps `cpu/mem/processes` 按 user / user group |
| 告警 | oomkill、extfrag 高、audit backlog |

### 已实现能力（M20：日志与查看器）

| 模块 | 说明 |
| --- | --- |
| journald | `journalctl -f` 跟随（可关）；Function 过滤 unit / priority / boot / cursor |
| Windows Events | `wevtutil` XPath + EventRecordID 游标；非 Windows 不调用 |
| macOS | `macos` 内存压力 / swap / 温度档 / 电池；持续跟随 Unified Log，显式历史范围使用 `log show`；其它系统自动禁用 |
| 网络 | `network-connections` 增加 cmdline、inode |
| systemd | `systemd.service_units` / `systemd.service_restarts`；Function 带单位状态 |
| 告警 | `systemd_units_failed` |

### 已实现能力（M21：Windows Perflib）

| 模块 | 说明 |
| --- | --- |
| Perflib | 直播 WMI `Win32_PerfFormattedData_*`（无 CGO PDH；`typeperf_scan` 可选）：`system.cpu_queue`、内核池/swapio、逻辑/物理磁盘、网卡、IIS 站点 + `iis.application_pool_*`、ASP.NET、.NET CLR、Hyper-V、SMB、NUMA、thermal、`cpu.temperature`、`system.hw.sensor.temperature.*`、AD/ADCS/ADFS、Exchange、RDS、`powersupply.capacity` |
| 服务 | 每服务 `windows.service_state.*` 状态图 + 汇总 `windows.services`；Function `windows-services` 仍可用 |
| 告警 | `system_m21.yaml`：CPU 队列、IIS 404、ASP.NET 排队、热区温度、Exchange poison queue、电池容量 |

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
| ibm.d | `db2`（db2 CLI）、`as400`（isql）、`mq`（dspmq/runmqsc；`mq.queue.depth` current/max、`mq.qmgr.status`）、`websphere`（PMI JSON / Prometheus）；无 DSN/命令/URL 则自动禁用；默认无 CGO |
| python.d 残留 | `pandas`（JSON/CSV 首行，不 eval Python）、`go_expvar`（`/debug/vars` memstats）、`am2320`（sysfs I2C） |
| 容器 | `lxc`（lxc-ls / cgroup）、`ecs`（task metadata v4）、`containerd`（ctr）；Functions `lxc-containers` / `ecs-containers` / `containerd-containers` |

### 已实现能力（M24：查询 API 深度）

| 模块 | 说明 |
| --- | --- |
| `/api/v3` | info / data / q / contexts / context / nodes / weights / alerts / alert_transitions / alert_config / functions / badge / allmetrics（`api: 3`） |
| 分组 | `group_by=dimension` 按维度合并实例；`group_by=node,dimension` 列名为 `node.dim` |
| alert_config | `GET\|PUT\|POST\|DELETE /api/v3/alert_config` YAML/JSON 规则 CRUD，`?hash=` / `?name=` |
| 每维 anomaly | data 的 `dimension_anomaly`（0–100）与 `anomaly`（0/1）；`options=anomaly-bit` 返回 0–100；health `lookup: … anomaly-bit`；Dashboard 异常维度标红 |

### 已实现能力（M25：ACLK / Cloud 控制台 / 异常高亮 / Correlations）

| 模块 | 说明 |
| --- | --- |
| ACLK | MQTT 3.1.1 over WSS（`GET /api/v1/aclk`）承载既有 JSON Frame；默认仍是 `/api/v1/stream`。`stream.protocol: mqtt\|aclk`；Hub `hub.storage: full\|proxy`（proxy 走 TypeQuery 反查 Agent） |
| Cloud 控制台 | `GET /api/v1/hub/console`：Space→Room→节点拓扑、ACLK 摘要、告警路由；Vue Cloud 面板 |
| 异常高亮 | 每维 anomaly bit：`/charts` `/chart` `/data.anomaly`；图上红色加粗 |
| Correlations | `weights?method=ks2\|volume&group=chart\|context\|dimension&top=`；完整窗口/分数条/点选筛选 UI |

### 已实现能力（M26：Prometheus 点名原生 ID）

| 模块 | 说明 |
| --- | --- |
| 采集 | `prometheus.jobs[].profile` 把 etcd/minio/vault/jenkins/grafana/prometheus/alertmanager/kafka/blackbox/gitlab/harbor/argocd/cert-manager/cilium/istio/vllm/litellm 等族映射成原生图表 ID；fastapi/go_runtime/python_gc 需显式 `profile`；其余仍 `prom.*` |
| API | `GET /api/v1/prometheus/catalog` 列出 profile / 已有原生采集器 / fallback 策略 |
| 告警 | etcd 无 leader、Vault sealed、MinIO、blackbox、Kafka brokers、Alertmanager、Jenkins 队列 |

### 开发

```bash
make test         # go vet + go test -race + 前端 typecheck
./plugins.d/example.plugin 1 | head   # 手工查看示例插件的协议输出
make lint         # gofmt + vet
./scripts/smoke.sh    # 启动 monitord 并验证 API / metrics / dashboard
cd web && npm run dev # 前端热更新，API 代理到 127.0.0.1:19999
```

### 页面响应与资源占用

Web 标签页隐藏后暂停轮询与实时连接，回到前台补读数据；长历史窗口只读聚合历史，不再订阅逐秒指标。滚动离屏的图表最多缓存8张画布和历史，超过后回收，滚回时重新加载。Functions、日志及目录刷新等待上一轮完成；切换时间窗口时取消过期的历史请求。

服务端的慢采集器探测不再阻塞状态读取；图表目录共用一次异常检测快照，Context 只读取需要的边界信息；没有匹配订阅的实时样本跳过编码。TSDB 按机器字读取压缩数据，并合并磁盘块头读取，减少历史查询开销。性能对照、缓存上限和复现命令见[验收记录](docs/05-acceptance.md#2026-09-23-响应延迟与后台资源占用)。

进程查询返回最近一次完整采集快照，慢系统调用不再阻塞查询；`result.collected_at` 是实际采集的 Unix 秒时间戳，首次成功前省略。采样取消时保留上一轮快照和 CPU/IO 基线，错误仍见采集器状态；单个进程漏采后的 CPU 百分比按两次有效样本间隔计算。

进程表按 `apps.top`（默认200）保留候选行，再排序返回；分组过滤在复制前完成，总数仍包含全部匹配进程。固定10,000行输入、返回200行的本机查询微基准中，耗时中位数由3.45 ms降至0.25 ms，单次累计分配由803 KiB降至35 KiB；该数据不包含HTTP编码和系统采集，详见[测量范围与复现](docs/05-acceptance.md#2026-09-23-进程查询按返回上限选择)。

历史查询只为命中的块索引和内存样本分配快照，短窗口不再按全部历史容量预留空间。固定1,000个块索引和1,000个内存桶、只读取最近3个桶的本机微基准中，快照分配由96 KiB降至144 B；这是快照阶段的累计分配，详见[历史窗口测量](docs/05-acceptance.md#2026-09-23-历史查询按时间窗口复制)。

异常历史查询对时间有序的记录使用二分定位，减少 `anomaly-bit` 图表和告警查找时重复扫描整个缓存。迟到样本或时钟回拨时保留原来的扫描行为，乱序记录被覆盖后自动恢复；默认120条有序记录全部读取的微基准分配由1,984 B降至1,024 B。测量范围及写入成本见[异常历史验收](docs/05-acceptance.md#2026-09-23-异常历史范围查询)。

`anomaly-bit` 图表直接使用相同的查询时间网格填入异常率，省去随后会被丢弃的普通指标读取、解码及聚合；普通指标查询保持原路径。固定8条序列、每条3,600个样本的请求处理基准中，耗时由12.1–15.9 ms降至2.58–2.85 ms，含JSON编码、不含网络，详见[异常图表请求验收](docs/05-acceptance.md#2026-09-23-异常图表跳过普通指标读取)。

macOS 默认日志采集使用单个持续运行的 `log stream`，停止每秒启动 `log show`。最近日志表复用采集启动后收到的记录，最多2000条且字符串内容不超过2 MiB；断流明确报错、自动退避重连，受影响的采样区间保留缺口。指定 `after` / `before` 的历史查询仍读取系统日志；`collectors.modules.logs.follow: false` 可使用原有单次查询方式。

macOS 进程采集每轮合并读取 CPU、RSS 和线程数，动态库和时基只初始化一次；系统指标默认只查询所需的3个 `sysctl` 键。Darwin/FreeBSD 网络指标共用一次 TCP/UDP 扫描，Unix 套接字另行采集；连接表先筛选、排序和截取，再读取返回行的进程详情。同一不可访问的 PID 每次请求只尝试一次，失败来源保留缺失值。以上优化不降低采样频率。

### 客户端（Flutter，M3 起步）

`app/` 是 macOS / Windows / Linux / Android / iOS 五端客户端：填入 Agent 或 Hub 地址和登录密码 / API token 即可连接，Hub 模式下可切换节点；图表页按 family 分组，仅为屏幕附近的图表加载历史和订阅实时数据，滚动或筛选复用已有 WebSocket（断线 3s 自动重连并补读历史）。切到其他页签时暂停图表轮询和实时连接；1 小时及更长窗口每 30 秒读取聚合历史，不接收逐秒实时点。告警页显示当前告警与最近状态变化；Functions 页调用 `/api/v1/function`（进程/连接/服务/日志等表）。

图表保留缺失样本和断线间隔，孤立有效样本以圆点显示，最新值缺失时图例显示 `-`。历史刷新保留响应时间范围之后收到的实时点；首次读取失败会显示自动重试提示，后续刷新失败则保留已有曲线并显示状态图标。

告警与 Functions 页仅在当前页签可见时轮询；上一轮完成后分别等待 10 秒和 2 秒再刷新，慢接口不会叠加定时请求。切换节点、函数或服务端查询时丢弃旧响应，日志搜索等待输入停顿 300 毫秒再发送。告警首次加载失败显示状态不可用，刷新失败标明仍在展示上一次成功结果；Functions 列表为空或加载失败后会自动重试。

Flutter 每张图表单独隔离重绘，某张图收到新样本时不再连带重绘同排其它图表；连续曲线跳过逐点标记计算，缺口旁的孤立样本仍显示圆点。采样频率、图表点数和时间窗口保持原有规则。

退出连接立即清空界面状态，未完成的登录、节点查询和自动恢复结果不会重新建立旧会话。密码保存、退出清理和再次登录按顺序写入系统安全存储，避免旧密码在退出后被迟到的写入重新保存。设置或安全存储失败时保留已验证的当前连接并显示提示；凭据清理失败会在登录页明确提示。并发节点刷新共用一次请求，已移除的节点自动回退到本机并保存选择。

应用隐藏或进入后台后暂停页面轮询与实时连接，回到前台只恢复当前页。离屏历史缓存最多保留12个图表、12,000个绘图点，超出后回收，重新滚动到该图表时补读历史；可见图表不受离屏预算限制。图表目录的慢请求共用一次执行，不因定时器重复发起。

```bash
cd app && flutter pub get
flutter run -d macos      # 或 linux / windows / <android-device> / <ios-device>
flutter analyze && flutter test
```

Linux 桌面运行需要 GTK 3、**libEGL** 和 **libsecret**（安全存储插件依赖）；缺少动态库会导致客户端无法启动：

```bash
# Debian / Ubuntu
sudo apt install libegl1 libgtk-3-0 libsecret-1-0
# Fedora
sudo dnf install mesa-libEGL gtk3 libsecret
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

目前所有包均未签名/公证；配置 `ANDROID_KEYSTORE_B64` 等 secrets 后 Android 服务端 APK 会自动签名，Apple / Windows 签名后续接入。Linux 客户端请先安装 `libegl1`（见上文）。

## 仓库结构

```text
.
├── core/                       Go 服务端模块
│   ├── cmd/monitord/           程序入口，Agent / Hub 共用
│   └── internal/              采集、TSDB、认证/API、Hub、流协议等实现
├── web/                        Vue Dashboard 源码与浏览器测试
│   ├── src/
│   └── e2e/
├── app/                        Flutter 五端客户端
│   ├── lib/
│   ├── test/                  单元测试
│   ├── integration_test/      原生安全存储测试
│   └── android/ ios/ macos/ windows/ linux/   客户端平台工程
├── android/                    独立 Android 服务端（Kotlin 前台服务）
├── plugins.d/                  外部采集器示例
├── scripts/                    构建、打包、版本处理和运行验收脚本
├── docs/                       架构、协议、差距与验收记录
├── .github/workflows/          CI 与 Release
├── .agents/skills/             开发助手的本地验收指引
├── monitor.example.yaml        可提交的配置模板
└── Makefile                    根目录统一构建入口
```

`app/android/` 构建 Flutter **客户端**；根目录 `android/` 构建 **服务端**，二者是独立应用。协议实现位于 `core/internal/stream/`，HTTP API 位于 `core/internal/api/`。详细职责见[架构文档 §8](docs/02-architecture.md#8-代码仓库结构monorepo)。

### 生成文件与本地数据

| 路径 | 用途 | Git 管理 |
| --- | --- | --- |
| `core/bin/` | 本机与跨平台服务端二进制 | 忽略 |
| `core/internal/api/ui/dist/` | `web/` 构建产物，供 Go 嵌入 | 只保留 `.gitkeep` |
| `app/build/`、`android/app/build/` | Flutter / Android 构建产物 | 忽略 |
| `android/app/src/main/jniLibs/` | Android 服务端打包时生成的 Go 二进制 | 忽略 |
| `dist/` | Release 压缩包、DMG、APK 等交付产物 | 忽略 |
| `reports/` | 持续采集、Hub 负载等验收报告 | 忽略 |
| `web/test-results/`、`web/playwright-report/` | 浏览器截图和测试报告 | 忽略 |
| `data/`、`core/data/` | 本地监控数据和默认密码 | 忽略 |
| `monitor.yaml`、`web-password` | 部署私有配置与凭据 | 忽略 |

从根目录运行 `make all`，包括 `make -j all`，会先构建前端再编译服务端。`make core` 用于已准备好嵌入资源的场景（例如 CI 下载前端构建产物后）；直接使用它不会更新 Dashboard。`make clean` 只清理服务端二进制及嵌入的前端构建文件，不删除运行数据、配置或密码。

## 路线图

M0–M26 已合入：骨架 → Agent → Hub → 客户端 → Android → ML/摄入 → 日志/OTLP → go.d 全目录 → API/Health → Cloud 骨架 → k-means → 跨平台骨架 → 原生插件补齐 → 内核深度 → 日志/查看器 → Windows Perflib → FreeBSD 插件剩余 → IBM/pandas/容器运行时 → 查询 API → ACLK / Cloud 控制台 → Prometheus 点名原生 ID。

后续仍不进默认二进制的是带 BTF 的 eBPF CO-RE 重定位，以及 850 个 Prometheus 集成名（继续 `prom.*`）。见 [docs/04-netdata-gap.md](docs/04-netdata-gap.md) §2.7。

## 双仓库推送

每次完成开发和必要验证后，将本次负责的改动提交并安全合入 `main`，再将 `main` 同步到 GitHub、GitLab 两边。开发分支或独立工作树中的改动应先完成整合和验证，不得夹带其他开发会话尚未完成的改动。用户当次明确指定其他分支或不推送时，以当次要求为准。GitHub 上的 Issue、PR、Release 安装包和工作流运行记录不会随 Git 推送复制到 GitLab。

### 首次配置

远程配置保存在本地 Git 配置中，不随代码克隆。新克隆或独立配置的工作树先运行 `git remote -v` 检查；以下命令将 `origin` 设为从 GitHub 拉取，并只补充缺少的推送地址，保留已有有效配置：

```bash
git remote set-url origin git@github.com:mengzhihua/monitor.git
for target in git@github.com:mengzhihua/monitor.git https://oauth2@gitlab.tly.life:20443/mengzhihua/netdata.git; do
  if ! git config --local --get-all remote.origin.pushurl | grep -Fxq "$target"; then
    git config --local --add remote.origin.pushurl "$target"
  fi
done
git remote -v
```

GitHub 使用已获授权的 SSH 密钥；GitLab 使用 HTTPS，用户名为 `oauth2`，密码为具有仓库写入权限的访问令牌。将令牌保存到系统凭据管理器（macOS 使用钥匙串），不要将令牌写入远程 URL、文档或提交。自定义名称的 SSH 密钥需在本机 SSH 配置中指定。

### 日常提交与验证

检查实际分支、工作区与远程配置，只暂存本次需要交付的文件。若在开发分支中完成工作，先提交本次改动，再在干净的 `main` 工作树中拉取最新代码并安全合并开发分支，解决冲突后重新完成必要验证。确认当前分支为 `main` 后推送：

```bash
git status --short --branch
git remote -v
# git add <本次改动的文件>
# git commit -m "说明本次改动"
test "$(git branch --show-current)" = main && git push -u origin main
```

该命令将 `main` 推送到两个仓库，并设置上游；不会推送其他分支或全部标签。推送后分别核对两边 `main` 返回的提交 SHA 与本地 `main` 一致，两边均匹配才算双仓库同步完成：

```bash
git rev-parse main
git ls-remote git@github.com:mengzhihua/monitor.git refs/heads/main
git ls-remote https://oauth2@gitlab.tly.life:20443/mengzhihua/netdata.git refs/heads/main
```

两边推送独立执行，一边成功不代表另一边成功。如果失败，保留已成功的一边，先处理认证、网络或远程历史分歧，再补推失败的一边并重新核对。不要使用强制推送覆盖远程提交。推送 `main` 会触发 GitHub 的自动 Release 流程；代码同步完成后仍需单独检查工作流和安装包发布状态。GitHub 工作流自动生成的标签只存在于其创建位置，需要同步标签时再显式拉取并推送对应标签。

## 开发验收

五阶段交付范围、性能数字、升级/降级注意事项和复现命令见[验收记录](docs/05-acceptance.md)。工具链和浏览器就绪后运行 `make acceptance`；跨平台编译及本机模拟负载不等同于真实设备或72小时验收。
