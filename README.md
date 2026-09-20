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
| API | `/api/v1/info` `/charts` `/chart` `/data` `/allmetrics` `/collectors`；`/metrics` Prometheus 格式；`/api/v1/live` WebSocket 每秒推送；可选 `token` 与 `allow_from` CIDR 访问控制 |
| Dashboard | Vue3 + uPlot，按 system/cpu/mem/disk/net 分组，1m/5m/15m/1h 时间窗，WebSocket 实时增量刷新，采集器状态面板，告警面板（实时状态 + 事件流） |

### 已实现能力（M1：健康/告警）

| 模块 | 说明 |
| --- | --- |
| 规则 | YAML 告警规则（`name/on/lookup/calc/warn/crit/every/delay/repeat/to`），语义对齐 Netdata health.d；`on:` 可指定 chart ID 或 context（模板，自动绑定所有匹配图表，支持 `chart_labels` 过滤）；内置 17 条系统规则（CPU/iowait/load/RAM/swap/磁盘空间与 inode/磁盘繁忙/网络错误与丢包/包风暴），`health.d/*.yaml` 或 `health.alarms:` 同名覆盖 |
| 表达式 | `$this` `$status` `$WARNING/$CRITICAL/$CLEAR` 与图表维度、其他告警值、`$cpus/$ram_total`；四则/比较/逻辑/三元、`abs min max isnan isinf`，支持 Netdata 式滞回写法 `$this > (($status >= $WARNING) ? (75) : (85))` |
| 引擎 | 每秒调度、`lookup` 直接查 TSDB（average/min/max/sum/median/last，`percentage`/`absolute` 选项），状态 CLEAR/WARNING/CRITICAL 迁移，`delay up/down multiplier max` 抑制抖动（回到原状态则丢弃通知），`repeat` 周期重复提醒，事件持久化到 `data/health/alarm-log.jsonl` |
| 通知 | Webhook（JSON POST）、Slack 兼容 incoming webhook、SMTP 邮件；`to:` 角色 → 通道路由，`health.silent` 静默 |
| API | `/api/v1/alarms`（`?all=true` 含 CLEAR）、`/api/v1/alarm_log?after=<id>`、`/api/v1/alarm_rules`；`/api/v1/info.alarms` 汇总；`/api/v1/live` 推送 `{"alarm":{...}}` 事件 |

### 开发

```bash
make test         # go vet + go test -race + 前端 typecheck
./plugins.d/example.plugin 1 | head   # 手工查看示例插件的协议输出
make lint         # gofmt + vet
./scripts/smoke.sh    # 启动 monitord 并验证 API / metrics / dashboard
cd web && npm run dev # 前端热更新，API 代理到 127.0.0.1:19999
```

## 仓库规划

```
core/      Go：monitord（agent/hub）、monitorctl、gomobile 绑定
web/       Vue3 Dashboard（embed 进 monitord）
app/       Flutter 五端客户端
android/   Android 服务端壳（Kotlin 前台服务 + 采集桥）
plugins/   外部采集器（plugins.d 文本协议）
proto/     节点↔Hub 流协议
api/       OpenAPI 定义
packaging/ 安装包与安装脚本
```

## 路线图

M0 骨架 → M1 单机 Agent → M2 Hub 集中 → M3 Flutter 客户端 → M4 Android 服务端 → M5 ML/日志/导出/集群 → M6 生态。详见架构文档 §11。
