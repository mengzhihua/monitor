# Monitor 实时监控平台

对标 [Netdata](https://www.netdata.cloud/) 的实时（每秒）、零配置、边缘优先的基础设施监控平台。

- **服务端** `monitord`（Go 单二进制，`--mode agent|hub`）：macOS / Linux / Windows / Android
- **客户端**：内嵌 Web Dashboard（Vue3）+ Monitor App（Flutter）：macOS / Linux / Windows / Android / iOS

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/01-netdata-capability-study.md](docs/01-netdata-capability-study.md) | Netdata 能力调研：组件、数据流水线、平台覆盖、部署拓扑、安全模型 |
| [docs/02-architecture.md](docs/02-architecture.md) | Monitor 架构设计：总体架构、技术选型、服务端模块（采集/TSDB/健康/ML/流式/API/Functions）、Hub、Android 服务端专项、客户端、仓库结构、路线图、能力对照表 |
| [docs/03-plugins-d-protocol.md](docs/03-plugins-d-protocol.md) | plugins.d 外部采集器协议：命令语法、进程生命周期、配置、示例插件 |

## 快速开始（M0）

依赖：Go 1.23+、Node 22+（仅构建 Dashboard 时需要）。

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

跨平台构建：`make cross` 生成 linux(amd64/arm64)、darwin(amd64/arm64)、windows(amd64)、android(arm64) 六个二进制。

### 已实现能力（M0）

| 模块 | 说明 |
| --- | --- |
| 采集器 | `cpu`（总量 + 每核）、`load`、`mem`（RAM/Swap）、`disk`（IO/ops）、`diskspace`（空间/inode）、`net`（带宽/包/错误/丢弃）、`uptime`；基于 gopsutil，Linux/macOS/Windows/Android 通用，设备/网卡/挂载点运行时动态注册 |
| Registry | Host → Chart → Dimension 模型，`absolute` / `incremental` / `percentage-of-*-row` 算法，计数器回绕处理 |
| TSDB tier0 | 每秒原始数据，Gorilla 压缩（典型指标 ≈ 2 bit/样本），追加式块文件 + 重启恢复，按时长/大小保留，范围查询 + 聚合（avg/min/max/sum/median/last） |
| TSDB tier1/tier2 | 每分钟 / 每小时降采样层（每桶 min/max/sum/last/count，均值 = sum/count），写入时同步折叠、按层独立保留（默认 90 天 / 2 年）、重启恢复；`/api/v1/data` 按 `(before-after)/points` 自动选层（`tier=auto|0|1|2` 可强制），粗层无数据时自动回退到细层；`/api/v1/info.db.tiers` 暴露各层 update_every/保留/序列/块/字节 |
| plugins.d | 外部采集器进程（任意语言）通过 stdout 文本协议接入：`CHART/DIMENSION/CLABEL/BEGIN/SET/END/FLUSH/VARIABLE/DISABLE/EXIT`；自动发现 `plugins.d/*.plugin`，也可在 `plugins.list` 显式声明；崩溃自动重启（1s→60s 指数退避）、无输出看门狗、进程组回收、`DISABLE` 自禁用；状态在 `/api/v1/collectors.plugins` 与 `/api/v1/info.plugins` |
| 应用/服务采集器 | `apps`：进程按应用分组（内置 ssh/database/httpd/containers/browser… 20+ 组，`collectors.modules.apps.groups` 可自定义 glob）→ `apps.cpu/mem/processes/threads/io_read/io_write`；`systemd`：Linux cgroup v2 每服务 CPU/内存/IO；`docker`：Engine API（unix socket / tcp）每容器 CPU/内存/网络/块 IO，容器消失自动移除图表；`nginx`（`stub_status`）；`redis`（内置 RESP 客户端 `INFO`，支持 AUTH/TLS/unix socket）。服务类模块目标不可达时自动禁用并在 `/api/v1/collectors` 标明原因 |
| Functions | 采集器可暴露按需函数：`GET /api/v1/functions` 列出，`GET /api/v1/function?function=<name>&<args>` 执行；内置 `processes`（实时进程表：pid/ppid/name/group/cpu%/rss/threads/cmdline，`sort=cpu|rss|pid`、`group=` 过滤） |
| API | `/api/v1/info` `/charts` `/chart` `/data` `/allmetrics` `/collectors` `/functions` `/function`；`/metrics` Prometheus 格式；`/api/v1/live` WebSocket 每秒推送；可选 `token` 与 `allow_from` CIDR 访问控制 |
| Dashboard | Vue3 + uPlot，按 system/cpu/mem/disk/net 分组，1m/5m/15m/1h 时间窗，WebSocket 实时增量刷新，采集器状态面板，告警面板（实时状态 + 事件流），Functions 面板（实时进程表，2s 刷新、排序/筛选） |

### 已实现能力（M1：健康/告警）

| 模块 | 说明 |
| --- | --- |
| 规则 | YAML 告警规则（`name/on/lookup/calc/warn/crit/every/delay/repeat/to`），语义对齐 Netdata health.d；`on:` 可指定 chart ID 或 context（模板，自动绑定所有匹配图表，支持 `chart_labels` 过滤）；内置 17 条系统规则（CPU/iowait/load/RAM/swap/磁盘空间与 inode/磁盘繁忙/网络错误与丢包/包风暴），`health.d/*.yaml` 或 `health.alarms:` 同名覆盖 |
| 表达式 | `$this` `$status` `$WARNING/$CRITICAL/$CLEAR` 与图表维度、其他告警值、`$cpus/$ram_total`；四则/比较/逻辑/三元、`abs min max isnan isinf`，支持 Netdata 式滞回写法 `$this > (($status >= $WARNING) ? (75) : (85))` |
| 引擎 | 每秒调度、`lookup` 直接查 TSDB（average/min/max/sum/median/last，`percentage`/`absolute` 选项），状态 CLEAR/WARNING/CRITICAL 迁移，`delay up/down multiplier max` 抑制抖动（回到原状态则丢弃通知），`repeat` 周期重复提醒，事件持久化到 `data/health/alarm-log.jsonl` |
| 通知 | Webhook（JSON POST）、Slack 兼容 incoming webhook、SMTP 邮件；`to:` 角色 → 通道路由，`health.silent` 静默 |
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

### 开发

```bash
make test         # go vet + go test -race + 前端 typecheck
./plugins.d/example.plugin 1 | head   # 手工查看示例插件的协议输出
make lint         # gofmt + vet
./scripts/smoke.sh    # 启动 monitord 并验证 API / metrics / dashboard
cd web && npm run dev # 前端热更新，API 代理到 127.0.0.1:19999
```

### 客户端（Flutter，M3 起步）

`app/` 是 macOS / Windows / Linux / Android / iOS 五端客户端：填入 Agent 或 Hub 地址（可选 Bearer token）即可连接，Hub 模式下可切换节点；图表页按 family 分组，历史数据走 `/api/v1/data`，实时点走 `/api/v1/live` WebSocket（断线 3s 自动重连）；告警页显示当前告警与最近状态变化。

```bash
cd app && flutter pub get
flutter run -d macos      # 或 linux / windows / <android-device> / <ios-device>
flutter analyze && flutter test
```

### Android 服务端（M4 起步）

`android/` 是原生 Kotlin 壳：前台服务拉起随包分发的静态 `monitord`（`jniLibs/arm64-v8a/libmonitord.so`），可设置监听端口、可选上报到 Hub、开机自启，并直接打开内嵌 Dashboard。Android 沙箱限制 `/proc/net` 等接口，网络类图表可能缺失。

```bash
./scripts/build-android-server.sh assembleRelease   # 需要 Go、JDK 17、ANDROID_HOME（SDK 35）
```

### 发布（GitHub Release）

每次推送到 `main` 会自动打 tag（在最新 `v*` 的基础上 patch +1，如 `v0.1.3` → `v0.1.4`）并构建、发布全部安装包；需要升 minor/major 时手动推 tag 即可，也可手动运行 Release 工作流重建某个已有 tag：

| | 产物 |
|---|---|
| 服务端 `monitord` | macOS arm64（Apple 芯片）/ amd64（Intel）、Windows amd64、Linux amd64 / arm64、Android arm64 APK |
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

M0 骨架 → M1 单机 Agent → M2 Hub 集中 → M3 Flutter 客户端 → M4 Android 服务端 → M5 ML/日志/导出/集群 → M6 生态。详见架构文档 §11。
