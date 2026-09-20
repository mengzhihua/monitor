# Monitor 实时监控平台

对标 [Netdata](https://www.netdata.cloud/) 的实时（每秒）、零配置、边缘优先的基础设施监控平台。

- **服务端** `monitord`（Go 单二进制，`--mode agent|hub`）：macOS / Linux / Windows / Android
- **客户端**：内嵌 Web Dashboard（Vue3）+ Monitor App（Flutter）：macOS / Linux / Windows / Android / iOS

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/01-netdata-capability-study.md](docs/01-netdata-capability-study.md) | Netdata 能力调研：组件、数据流水线、平台覆盖、部署拓扑、安全模型 |
| [docs/02-architecture.md](docs/02-architecture.md) | Monitor 架构设计：总体架构、技术选型、服务端模块（采集/TSDB/健康/ML/流式/API/Functions）、Hub、Android 服务端专项、客户端、仓库结构、路线图、能力对照表 |

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
| API | `/api/v1/info` `/charts` `/chart` `/data` `/allmetrics` `/collectors`；`/metrics` Prometheus 格式；`/api/v1/live` WebSocket 每秒推送；可选 `token` 与 `allow_from` CIDR 访问控制 |
| Dashboard | Vue3 + uPlot，按 system/cpu/mem/disk/net 分组，1m/5m/15m/1h 时间窗，WebSocket 实时增量刷新，采集器状态面板 |

### 开发

```bash
make test         # go vet + go test -race + 前端 typecheck
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
