# Monitor 实时监控平台

对标 [Netdata](https://www.netdata.cloud/) 的实时（每秒）、零配置、边缘优先的基础设施监控平台。

- **服务端** `monitord`（Go 单二进制，`--mode agent|hub`）：macOS / Linux / Windows / Android
- **客户端**：内嵌 Web Dashboard（Vue3）+ Monitor App（Flutter）：macOS / Linux / Windows / Android / iOS

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/01-netdata-capability-study.md](docs/01-netdata-capability-study.md) | Netdata 能力调研：组件、数据流水线、平台覆盖、部署拓扑、安全模型 |
| [docs/02-architecture.md](docs/02-architecture.md) | Monitor 架构设计：总体架构、技术选型、服务端模块（采集/TSDB/健康/ML/流式/API/Functions）、Hub、Android 服务端专项、客户端、仓库结构、路线图、能力对照表 |

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
