# Netdata 能力调研

> 资料来源：https://www.netdata.cloud/ 、https://learn.netdata.cloud/ 、https://github.com/netdata/netdata（调研日期 2026-09）。
> 目的：明确 Netdata "具备什么"，作为本项目（Monitor）能力对标与架构设计的输入。

## 1. Netdata 是什么

Netdata 是一个开源（Agent 为 GPLv3）、实时（每秒采集）、零配置、边缘计算（数据留在被监控机器上）的基础设施监控平台。核心口号：

- **Per-second truth, no sampling**：所有指标每秒采集，不采样、不聚合后再存。
- **Zero configuration**：装上即监控，自动发现主机上的一切（系统、容器、常见应用）。
- **Edge-native**：采集、存储、ML、告警、查询都在被监控节点上完成，"分发代码而不是集中数据"。
- **Distribute, don't centralize**：Netdata Cloud 不集中存储指标，只做统一视图、用户/权限、告警路由，实时反查各 Agent。

## 2. 三大组件

| 组件 | 职责 | 备注 |
| --- | --- | --- |
| **Netdata Agent** | 采集 / 存储 / ML / 告警 / 导出 / 流式上报 / 查询 API / 内嵌 Dashboard 的单进程守护程序 | 运行在服务器、云主机、K8s、IoT；默认端口 19999 |
| **Netdata Cloud** | 用户管理、RBAC、Space / Room 组织、跨节点统一 Dashboard、集中告警通知、移动端推送 | 不存指标；通过 ACLK（Agent-Cloud Link，MQTT over WebSocket）实时查询 Agent |
| **Netdata UI** | Dashboard 与可视化（本地内嵌 + CDN 最新版） | 无查询语言，点击即切片/分组 |

Netdata 官方还提供 **Netdata Mobile App**（iOS / Android），用于接收告警推送、查看节点与告警状态。

## 3. Agent 数据流水线（Collect → Store → Learn → Detect → Check → Stream → Archive → Query → Score）

| 步骤 | 能力 | 关键实现点 |
| --- | --- | --- |
| Collect | 800+ 集成；系统/容器/VM/硬件传感器/应用/日志/合成检查（Ping、TCP、HTTP、证书） | 内部采集器（C 线程，零依赖）+ 外部采集器（独立进程，经 stdin/stdout 管道用文本协议与 daemon 通信，`plugins.d` 管理；go.d.plugin / python.d.plugin / charts.d.plugin 等） |
| Store | 高效分层时序库 dbengine，约 0.5 byte/sample | Tier0 每秒、Tier1 每分钟、Tier2 每小时；各 tier 独立 retention（时间/空间双限制）；`update every` 1~3600s |
| Learn | 每个指标在边缘训练多个无监督 ML 模型（k-means） | 训练基于最近历史；低资源 |
| Detect | 每个采样点打异常标记（anomaly bit），生成 anomaly rate 指标 | 用于图表高亮与"异常顾问"(Anomaly Advisor) |
| Check | 健康引擎：数百条预置告警，可完全自定义 | `alarm`/`template` 定义：`lookup`（时间窗口 + 聚合函数）、`calc`、`warn`/`crit` 表达式、`every`、`delay`、hysteresis、`to` 收件人角色 |
| Stream | Child → Parent 实时流 + 重连补传（replication） | Parent 可再上报形成层级；Parent 集群做 HA；Child 可 thin 模式（仅采集转发） |
| Archive | 导出到 Prometheus remote write、InfluxDB、OpenTSDB、Graphite、JSON、MongoDB 等 | 导出引擎 |
| Query | REST API（/api/v1、/api/v2）：info、charts、data、alarms、alarm_log、weights、functions… | OpenAPI 文档；支持多种聚合、分组、格式（json/csv/ssv/…） |
| Score | 打分引擎：Metric Correlations（关联分析）、weights、anomaly rate 排序 | 快速定位"什么和这次故障相关" |

### 3.1 Functions（实时诊断函数）

Agent 暴露可远程调用的"函数"，返回实时表格而非时序：processes（类 top）、network-connections（每进程 TCP/UDP 套接字）、systemd-journal / Windows Event Log（日志浏览）、streaming 状态等。Parent 会将请求转发给在线的 Child。

### 3.2 日志

- Linux：直接读取 systemd-journald，边缘处理、无需集中；提供 `log2journal` 把文本日志结构化进 journal。
- Windows：Windows Event Log / ETW。
- Cloud 提供统一日志视图（同样实时反查 Agent）。

### 3.3 平台覆盖（官方表）

| 能力 | Linux | FreeBSD | macOS | Windows |
| --- | --- | --- | --- | --- |
| CPU / 内存 / 系统资源 | Full | Yes | Yes | Yes |
| 磁盘 / 挂载点 / 文件系统 / RAID | Full | Yes | Yes | Yes |
| 网络接口 / 协议 / 防火墙 | Full | Yes | Yes | Yes |
| 硬件传感器（风扇/温度/GPU…） | Full | Some | Some | Some |
| OS 服务（systemd） | Yes | - | - | - |
| 进程 | Yes | Yes | Yes | Yes |
| 系统与应用日志 | journald | - | - | Event Log / ETW |
| 每 PID 的实时 TCP/UDP 连接 | Yes | - | - | - |
| 容器（Docker/containerd/LXC/K8s） | Yes | - | - | - |
| 虚拟机（宿主视角） | cgroups | - | - | Hyper-V |
| 合成检查（HTTP/TCP/Ping/证书） | Yes | Yes | Yes | Yes |
| 打包应用（nginx/pg/redis/mongo…） | Yes | Yes | Yes | Yes |
| 云厂商 | Yes | Yes | Yes | Yes |
| 自定义应用（OpenMetrics / StatsD / OTel） | Yes | Yes | Yes | Yes |

> 注意：Netdata **没有 Android / iOS 上的 Agent**，移动端只有查看/告警 App。本项目要求服务端支持安卓，是相对 Netdata 的扩展点（见架构文档）。

## 4. 部署拓扑

1. **单 Agent 独立**：装完访问 `http://host:19999`，各节点各自看、各自告警。
2. **Parent-Child 集中**：Child 流式上报到 Parent，Parent 保存全量历史、统一 Dashboard、统一告警；Parent 可级联（Proxy）、可组集群（HA）。
3. **接入 Cloud**：Agent 主动外连 Cloud（claim token 认领），Cloud 提供 Space/Room、用户、RBAC、跨节点视图、集中通知、移动端推送；数据仍留在本地。

## 5. 安全模型

- 采集器按需最小特权：Linux capabilities（CAP_DAC_READ_SEARCH、CAP_SYS_PTRACE、CAP_NET_ADMIN…）或 setuid；`ndsudo` 只允许执行硬编码的白名单命令。
- 文件权限 `0750`/`4750`，属主 `root:netdata`，daemon 以非 root 用户运行。
- Agent 与 Cloud 之间仅 Agent 主动出站（TLS），无入站端口暴露需求；claim token 一次性认领。
- Cloud API 使用 Bearer token，带 scope。

## 6. 非功能特征（官方宣称）

- 资源占用：约同类工具的 15%；"最节能的监控工具"（阿姆斯特丹大学研究）。
- 存储：约 0.5 byte/sample（分层 + 压缩），40x 存储效率。
- 查询：1 秒延迟的实时可视化，22x 响应速度。
- 扩展：Parent-Child 原生水平扩展，百万级 samples/s。

## 7. 对本项目的启示（能力清单）

必须对标的"核心能力"（P0）：

1. 每秒采集、零配置自动发现（系统级采集器全平台可用）。
2. 内嵌分层时序库（1s / 1m / 1h 三层，时间+空间双 retention）。
3. 内嵌 HTTP API + Web Dashboard，单二进制即完整服务端。
4. 健康引擎（预置规则 + 自定义规则 + hysteresis/delay）与多渠道通知。
5. Child → Parent 流式上报 + 断线补传；Parent 级联与集群。
6. 中心平台：认领节点、Space/Room、用户与 RBAC、统一视图、集中通知、移动端推送。
7. 外部采集器插件协议（任意语言）+ OpenMetrics / StatsD 摄入。

增强能力（P1）：

8. 边缘 ML 异常检测（anomaly bit / anomaly rate）与关联分析（metric correlations）。
9. Functions（实时进程表、连接表、日志浏览）。
10. 导出（Prometheus remote write / InfluxDB / OpenTSDB）。
11. 日志（journald / Windows Event Log / 文件日志）边缘检索。

差异化（Netdata 没有，本项目要做）：

12. **Android 服务端**（手机/平板/安卓工控盒作为被监控节点）。
13. 全平台原生客户端（macOS / Linux / Windows / Android / iOS 一套代码）。
