# 运维聚合看板调研与落地

调研日期：2026-09-23。依据官方产品文档和监控实践归纳常见场景，并非用户访谈或市场偏好排名。

## 观察到的设计重点

| 来源 | 文档中的重点 | 本项目的取舍 |
| --- | --- | --- |
| [Grafana 看板最佳实践](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/best-practices/) | USE 关注利用率、饱和与错误；RED 关注请求、错误与耗时；看板应回答具体问题，减少无目的浏览 | 增加资源等待、TCP 排障，并给运维场景附排查顺序，复用节点选择 |
| [Google SRE 四个黄金信号](https://sre.google/sre-book/monitoring-distributed-systems/) | 延迟、流量、错误、饱和度 | 巡检先看可用性探测和服务，再追查资源；HTTP 探测不冒充业务成功率或 P95 |
| [Datadog Infrastructure](https://docs.datadoghq.com/infrastructure/) | 主机、容器、进程与整体基础设施视图 | 保留已有容器/进程看板，本轮优先补遗漏的故障场景 |
| [Netdata Home](https://learn.netdata.cloud/docs/dashboards-and-charts/tabs) | 基础设施概览与跨节点运行信息 | 当前预设仍按所选节点展示，不将单节点数据宣称为全局聚合 |
| [Zabbix Linux 模板](https://www.zabbix.com/la/integrations/linux) | 文件系统空间、inode 与内存等资源监测 | 增加容量余量，避免只看磁盘字节而遗漏 inode |
| [Zabbix 证书监控](https://www.zabbix.com/documentation/7.4/en/manual/guides/monitor_certificate) | 证书有效期与到期前提醒 | 用已有 HTTP 采集器的证书剩余天数建立入口巡检；本次不新增告警规则 |

## 本轮增加的八个运维场景

以下是结合调研与本地采集能力制定的产品选择；时钟、电源和文件巡检属于基于现有能力补充的运维建议。

| 看板 | 回答的问题 | 自动匹配的核心数据 |
| --- | --- | --- |
| 值班快速巡检 | 哪些服务值得先排查？ | HTTP / TCP / Ping 探测、服务状态、容量、CPU |
| 资源饱和与等待 | CPU 不高为什么仍然卡？ | Linux PSI、负载、换页、磁盘延迟 |
| 容量与耗尽风险 | 哪些资源的余量在缩小？ | 可用内存、磁盘空间、inode、文件句柄 |
| TCP 连接排障 | 为什么连接超时或握手失败？ | TCP、sockstat、conntrack、网卡、探测 |
| 证书与入口巡检 | 是否有证书临期或入口失败？ | HTTP TLS 剩余天数、探测状态、DNS |
| 时钟同步巡检 | 是否有时间漂移或不同步？ | 系统时钟、Chrony、NTP、网络 |
| 硬件与电源巡检 | 温度、电池或存储设备是否异常？ | 温度、UPS、供电、SMART / NVMe / RAID |
| 关键文件更新巡检 | 预期的文件是否存在并更新？ | filecheck 存在状态、修改时间、大小、容量 |

每个看板包含三步排查建议，目录增加「运维值班」分类。26 个预设继续共用自动匹配、筛选、时间范围、节点切换与懒加载；目录覆盖数只计算存在的指标类别，不代表健康状态或数据新鲜度。修补既有存储组对 `smartctl` 和 `md` 的匹配。

不填造缺失数据，不把探测当作业务 SLA，不推算未实现的容量耗尽日期，不把文件更新时间当作备份可恢复证明。Linux PSI、conntrack、UPS 等仍依赖相应系统和采集条件；看板免配置不改变采集前提。

验证：`make all`；`npm run test:e2e -- presets.spec.ts` 覆盖桌面/手机、目录搜索、节点切换、真实图表、八个运维场景、排查提示与 context 匹配。硬件与外部服务未连接的场景只验证匹配逻辑和缺失状态，不声称完成真实 UPS、证书服务器或 Linux PSI 联调。
