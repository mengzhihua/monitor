# 多设备监控与商业产品对标

本文记录 2026-10-15 的设备监控建设方向、接入口径和验收边界。对标选取 PRTG、Datadog Network Device Monitoring（NDM）和 LogicMonitor 的官方文档，侧重可落地的设备采集能力，不代表市场排名、采购建议或同规模性能比较。

## 本次实现

SNMP 增加显式的多目标配置：一个采集器最多配置 64 个目标，最多同时采集 4 个目标。每台设备拥有独立名称、地址、凭据、模板和超时；启动时离线的目标也会进入后续轮询，避免因为启动阶段连接失败而永久停止采集。原来的单 `address` 配置仍可使用，不能与 `targets` 混用。

内置模板为 `network`、`ups`、`printer`、`host`。`network` 与 `host` 采集网络接口，优先使用 64 位输入/输出字节计数器，设备不支持时使用 32 位表；已经采用 64 位计数的方向遇到临时缺值时保留缺口，避免切回较小计数导致假速率。多目标模式用设备名及接口索引区分图表，避免同名端口串数据；设备名也用于 UPS、打印机和主机资源的指标隔离。新增模板按用户指定配置生效，目前不按厂商或 `sysObjectID` 自动选择模板。

32 位切换到 64 位时重建计数基线，该次速率留空，避免跨计数空间计算出虚假峰值。接口改名只刷新标签，不改变命名设备的接口图表身份。

管理员在配置的本机表单中添加设备，填写地址、模板与凭据后保存，再通过现有重启操作生效。手机端使用单列设备卡片。表单保留每个已存设备的自定义 OID、LLD、未知字段和未编辑的凭据；非法目标配置在保存前拒绝，旧版单设备配置需通过 YAML 明确迁移。配置管理沿用管理员权限、并发修改检查和备份机制。

每台设备提供单独的 `target_status` 和 `device_sysuptime` 图表。可达状态依据采集结果，不代表该模板的每个可选指标都存在；不支持的 OID、缺少字段和打印机负数未知值不补成零。自定义 `items` 和 `lld` 仍可与内置模板一起使用，接口表读取失败不再阻断它们。

### SNMP 多设备配置

下面是独立的 SNMP 采集示例；合并到现有配置时保留需要的其他 `collectors.enabled` 项。示例地址属于文档网段，密码均为待替换占位符。本机仍需安装可调用的 Net-SNMP `snmpwalk`。

```yaml
global:
  update_every: 15

collectors:
  enabled: [snmp]
  modules:
    snmp:
      targets:
        - name: core-switch
          address: 192.0.2.10
          profile: network
          version: "3"
          user: monitor
          auth_protocol: SHA
          auth_passphrase: "REPLACE_WITH_DEVICE_AUTH_PASSPHRASE"
          priv_protocol: AES
          priv_passphrase: "REPLACE_WITH_DEVICE_PRIV_PASSPHRASE"
          timeout: 5s
        - name: ups-room-a
          address: 192.0.2.20
          profile: ups
          version: "2c"
          community: "REPLACE_WITH_READ_ONLY_COMMUNITY"
          timeout: 5s
        - name: office-printer
          address: 192.0.2.30
          profile: printer
          version: "2c"
          community: "REPLACE_WITH_READ_ONLY_COMMUNITY"
          timeout: 5s
        - name: nas-main
          address: 192.0.2.40
          profile: host
          version: "2c"
          community: "REPLACE_WITH_READ_ONLY_COMMUNITY"
          timeout: 5s
```

设备名称为 1–48 个英文字母、数字、下划线或短横线，目标内不得再嵌套 `targets`。显式配置 `targets: []` 表示不采集任何目标，不会回退到本机默认地址。凭据不从父级继承，应逐目标明确配置；SNMPv2c community 是明文协议凭据，设备支持时可按第一台设备的格式改用 SNMPv3。设备名称用于图表身份，修改名称会生成新的图表序列。

单目标 `timeout` 覆盖该目标一轮采集，不接受负数或超过 1 分钟的值；未指定或为零时使用模块级超时，再回退至 3 秒默认值。调度器默认将 `global.update_every` 同时作为整轮采集截止时间；目标超过 4 台后需考虑分批执行，增加采集周期以容纳总预算。配置中没有 `collectors.timeout` 字段。达到整轮截止时间后停止剩余工作，不应把未执行的探测误判为设备故障。此例的 15 秒周期是起点，实际应根据接口数量和设备响应调整。

| 模板 | 图表 ID 后缀（多目标前缀为 `snmp.target.<name>.`） | 指标 |
| --- | --- | --- |
| 全部 | `target_status`、`device_sysuptime` | 采集可达状态、SNMP 运行时间 |
| `network` / `host` | `device_net.<ifIndex>`、`device_net_operstatus.<ifIndex>` | 接口收发字节速率、接口运行状态 |
| `ups` | `ups.battery_status`、`ups.runtime`、`ups.charge`、`ups.voltage`、`ups.temperature`、`ups.output_load.<lineIndex>` | 电池状态、续航、电量、电压、温度和各输出线路负载 |
| `printer` | `printer.supply_remaining.<hrDeviceIndex>.<supplyIndex>` | 有效耗材/容器剩余百分比 |
| `host` | `host.cpu_load.<index>`、`host.storage_used.<index>`、`host.storage_total.<index>`、`host.storage_utilization.<index>` | 各 CPU 负载、存储池已用/总字节与利用率 |

设备图表同时附带 `target`、`device`、`profile` 标签。旧版单地址模式保留既有网络图表 ID；在旧配置上增加目标名称或改用 `targets` 会改变图表 ID，应检查依赖精确 chart 名称的告警和自定义视图。

### Redfish 多 BMC 配置

Redfish 新增同样最多 64 个目标、4 个并发的显式目标列表。下面片段与上面的 SNMP 配置合并时，将 `redfish` 加入 `collectors.enabled`，并把 `redfish` 模块放入同一个 `collectors.modules`。

```yaml
redfish:
  targets:
    - name: rack01-server01
      url: https://bmc01.example.net/redfish/v1
      user: monitor
      password: "REPLACE_WITH_BMC_READ_ONLY_PASSWORD"
      timeout: 5s
      tls:
        ca_file: /etc/monitor/bmc-ca.pem
    - name: rack01-server02
      url: https://bmc02.example.net/redfish/v1
      user: monitor
      password: "REPLACE_WITH_BMC_READ_ONLY_PASSWORD"
      timeout: 5s
```

`url` 是 Redfish 服务根地址；不在 URL 中放凭据、查询参数或片段。示例中的 `ca_file` 需指向实际证书文件；使用系统已信任证书时省略 `tls`。每个目标独立指定 TLS 和凭据，不能假定从父级继承。目标 `timeout` 限制完整 Systems 遍历，不接受负数或超过 1 分钟的值；未指定或为零时使用模块级超时，默认 5 秒。`targets: []` 禁用所有目标；不能同时配置旧版顶层 `url` 与目标列表。

采集器读取 Systems 集合后跟随成员链接及分页，读取真实资源状态；只接受同源链接和重定向。每个响应限制 8 MiB，每轮最多遍历 8 页、256 个 Systems。显式目标离线不阻止进程启动，后续采集会重试；部分资源成功时保留有效数据，同时将目标本轮采集标为失败。

健康状态优先使用 `Status.HealthRollup`，缺少有效状态时显示 `unknown`，不再默认正常。提供 `redfish.system_health_state`、`redfish.system_power_state`、`redfish.system_processors`、`redfish.system_memory` 等图表 context；电源状态、CPU 数量和内存仅在目标实际提供时发布。`redfish.target_status.<name>` 表示本轮采集是否完整成功，与服务器硬件是否健康是两种不同含义。命名目标的系统图表包含目标与资源身份，两个 BMC 的同名 `System.1` 不会合并。

本轮 Redfish 为只读 Systems 采集，不执行开关机、重启、固件更新等管理动作，也未扩展所有 Chassis/Sensors/Storage 私有资源。

### 默认告警

以下规则按图表 context 绑定，每台设备、每个耗材或存储池独立求值。采集器生成相应图表后自动启用；可在 `health.d` 使用相同规则名覆盖阈值。

| 规则名 | 警告 | 严重 |
| --- | --- | --- |
| `snmp_target_unreachable` | — | 目标 `down = 1`，SNMP 可达性探测失败 |
| `redfish_target_collection_failed` | — | 目标 `down = 1`，Systems 采集失败或不完整 |
| `snmp_ups_battery_state` | 电池状态 3（电量低） | 电池状态 4（耗尽） |
| `snmp_printer_supply_remaining` | 有效剩余量 ≤ 10% | 有效剩余量 ≤ 5% |
| `snmp_host_storage_utilization` | 使用率 ≥ 85% | 使用率 ≥ 95% |

这些规则每 10 秒求值一次，使用最近 30 秒的最后一个有效样本；保持同类内置规则的恢复通知延迟：`down 2m multiplier 1.5 max 30m`。延迟作用于通知，不将界面中的告警状态强行保持两分钟。若自定义采集周期超过查找窗口，也应同步调整告警的 `lookup` 窗口。

UPS 状态 1（未知）及非法状态返回 `UNDEFINED`。打印机和存储的负数值同样不当作有效阈值输入；查找窗口内完全没有数据时，告警为 `UNDEFINED`，既不合成设备故障也不宣称健康。之前的有效样本在 30 秒窗口内仍可参与求值。UPS 本轮仅按设备报告的电池状态告警，不再叠加一套电量百分比告警，以免同一原因重复通知。

既有 `redfish_system_critical` 保持原告警级别和恢复延迟；当系统明确报告 `unknown` 时改为 `UNDEFINED`，避免把“无法确定健康状态”误记为恢复。目标采集失败告警与硬件健康告警分开表达：管理接口访问失败不等于服务器已经断电或硬件损坏。

## 商业产品提供了哪些参照

| 能力 | 官方文档中的行为 | 本项目需要落实的重点 |
| --- | --- | --- |
| 设备发现和模板 | PRTG 先发现设备，再识别设备类型，最后按内置或自定义模板创建传感器；可按需或定时运行。见 [PRTG Auto-Discovery](https://www.paessler.com/manuals/prtg/auto-discovery)。 | 分开记录“网络可达”“身份识别”“采集就绪”，避免把扫描到端口直接当成完整监控。 |
| 通用与厂商配置 | Datadog 用 `sysObjectID` 匹配配置模板，未匹配设备可使用通用模板；模板支持设备类型、指标和标签。见 [NDM Profiles](https://docs.datadoghq.com/network_monitoring/devices/profiles/) 和 [Supported Devices](https://docs.datadoghq.com/network_monitoring/devices/supported_devices/)。 | 先覆盖 IF-MIB、UPS-MIB、Printer-MIB、HOST-RESOURCES-MIB，再逐步加入经过验证的厂商私有 MIB。 |
| 接口监控 | LogicMonitor 自动发现接口，并提供状态、吞吐、利用率、接口识别和 32/64 位计数器选择。见 [SNMP Network Interface Monitoring](https://www.logicmonitor.com/support/snmp-network-interface-monitoring)。 | 高速接口优先使用 64 位计数器；稳定区分接口；将缺失值与真实的零值分开。 |
| 不同设备的专门指标 | PRTG 提供 [UPS 状态](https://www.paessler.com/manuals/prtg/snmp_ups_status_sensor)、[打印机](https://www.paessler.com/manuals/prtg/snmp_printer_sensor)和 [Synology 健康状态](https://www.paessler.com/manuals/prtg/snmp_synology_system_health_sensor)等传感器。 | 除通用网络流量外，按设备提供电池、耗材、CPU 和存储等指标；不能把支持通用协议等同于覆盖厂商全部能力。 |
| 组件动态发现 | LogicMonitor 通过 SNMP 表发现并过滤接口、CPU、存储等实例。见 [SNMP Active Discovery](https://www.logicmonitor.com/support/snmp-active-discovery)。 | 索引和设备身份必须保留，新增组件可独立生成图表，多设备间不能串数据。 |
| 拓扑和权限 | Datadog 的 [设备拓扑](https://docs.datadoghq.com/network_monitoring/devices/topology/)依赖 SNMP 提供的 LLDP/CDP；LogicMonitor 的 [安全实践](https://www.logicmonitor.com/support/getting-started/advanced-logicmonitor-setup/logicmonitor-security-best-practices)强调按角色限制资源和采集器管理。 | 复用项目已有 Room/RBAC 和拓扑基础，逐步连接设备身份、采集器与告警；设备清单和详情都应遵循同一授权边界。 |

这些产品的参考价值在于完整的接入、识别、采集、告警和维护流程。当前阶段应首先保证返回的数据可信、接入配置可解释，并持续积累设备样本。

## 设备能力边界

| 设备类别 | 采集入口 | 可用前提及边界 |
| --- | --- | --- |
| 路由器、交换机、防火墙、无线设备 | SNMP IF-MIB / IF-MIB 扩展 | 设备启用 SNMP，并开放对应接口表；仅有管理网页或能 Ping 通不代表提供完整接口指标。 |
| UPS | SNMP UPS-MIB | 目标实现 RFC 1628 的相关字段；厂商私有电池、插座、旁路和告警表需要额外模板。 |
| 打印机 | SNMP Printer-MIB | 目标返回耗材表及有效容量；负数哨兵代表未知等状态，不能显示成负百分比或耗材耗尽。 |
| NAS、存储主机、操作系统 | SNMP HOST-RESOURCES-MIB | 目标提供 CPU/存储表；存储池可包含内存、磁盘等，不应将每行都标成物理硬盘。RAID、SMART 和存储池告警仍依赖具体采集器或厂商接口。 |
| 带管理控制器的服务器 | Redfish | BMC 启用对应 Redfish 资源并授予只读访问；不同固件、机型和资源版本仍需现场验证。 |
| 安装 Agent 的主机和移动设备 | 既有系统采集器、Agent/Hub 和客户端 | 采集能力受系统 API、运行权限及后台限制影响；一个统一页面不能使未开放的系统指标变得可用。 |

“支持”按协议与指标定义，不按厂商品牌笼统承诺。本轮的自动化测试使用可控 SNMP 输出和本机 Redfish 响应；它们能验证解析、隔离、错误处理和展示逻辑，不能替代 Cisco/Huawei/H3C、Synology/QNAP、APC/Eaton、HP/Canon 或各代 iDRAC/iLO 的逐型号硬件验收。本项目尚未发布这些厂商的全面认证清单。

## 标准指标的换算和未知值

### UPS

以下字段来自 [RFC 1628：UPS-MIB](https://www.rfc-editor.org/rfc/rfc1628.html)。标量 OID 带末尾 `.0`；输出负载表使用线路索引。

| 指标 | OID | 口径 |
| --- | --- | --- |
| 电池状态 | `1.3.6.1.2.1.33.1.2.1.0` | 1 未知、2 正常、3 电量低、4 电量耗尽；未知不能当正常。 |
| 预计续航 | `1.3.6.1.2.1.33.1.2.3.0` | 分钟，设备按当前负载估计。 |
| 剩余电量 | `1.3.6.1.2.1.33.1.2.4.0` | 0–100%。 |
| 电池电压 | `1.3.6.1.2.1.33.1.2.5.0` | 原始值 × 0.1 V。 |
| 电池温度 | `1.3.6.1.2.1.33.1.2.7.0` | 电池附近环境温度，摄氏度。 |
| 输出负载 | `1.3.6.1.2.1.33.1.4.4.1.5.<lineIndex>` | 0–200%，可能高于 100%，不能截断成正常满载。 |

### 打印机耗材

[RFC 3805：Printer-MIB v2](https://www.rfc-editor.org/rfc/rfc3805.html) 的耗材表索引为 `hrDeviceIndex.prtMarkerSuppliesIndex`。必须保留完整复合索引，否则同一 SNMP Agent 下多个打印设备的耗材会发生碰撞。

| 字段 | OID 列前缀 | 处理方式 |
| --- | --- | --- |
| 描述 | `1.3.6.1.2.1.43.11.1.1.6` | 使用设备报告的名称。 |
| 最大容量 | `1.3.6.1.2.1.43.11.1.1.8` | -1 表示其他情况，-2 表示未知。 |
| 剩余量 | `1.3.6.1.2.1.43.11.1.1.9` | -1 表示其他情况，-2 表示未知，-3 表示仍有余量但不知道数值。 |

仅在最大容量大于零、剩余量非负且数据有效时计算百分比。该表也可以描述废料容器剩余空间；统一文案使用“耗材/容器剩余量”，不要把所有行都解释为墨水。缺失和未知应保持无有效样本，不能用零值补齐。

### 主机 CPU 和存储

[RFC 2790：HOST-RESOURCES-MIB](https://www.rfc-editor.org/rfc/rfc2790.html) 定义的 CPU 负载为过去一分钟的非空闲平均百分比，OID 为 `1.3.6.1.2.1.25.3.3.1.2.<index>`。

存储表列前缀为 `1.3.6.1.2.1.25.2.3.1`：`.3` 是描述，`.4` 是每个分配单元的字节数，`.5` 是总分配单元数，`.6` 是已用单元数。容量和已用字节分别乘以分配单元大小；利用率使用 `used / size × 100`。分母缺失或为零时，不发布一个看似正常的利用率。

## 接入与现场验收

本轮本地验证：Go vet / 全量竞态测试、前端类型检查及构建通过；设备与配置相关桌面/手机浏览器测试共 36 项通过。macOS 服务端构建和实际进程模拟验收通过，Linux amd64/arm64 与 Windows amd64 交叉构建通过；交叉构建不等于相应操作系统上的实机运行测试。

`scripts/verify-device-monitoring.py` 使用临时目录、动态本地端口、四类模拟 SNMP 设备和两个模拟 BMC，检查高容量计数、设备隔离、单位、未知值、真实成员读取、历史查询、失败恢复，以及管理员保存/重启和 viewer 配置权限。输出显式区分 `fixture_simulation` 与 `physical_device_validation`。该脚本已加入 `make acceptance` 和 Linux/macOS CI：

```sh
make all
python3 scripts/verify-device-monitoring.py
```

以下步骤仍需在用户实际设备上执行：

1. 在采集器所在主机确认能访问设备管理地址；记录型号、固件、启用的协议和可用 MIB/Redfish 资源。管理地址与业务地址可以不同。
2. 用设备只读凭据配置采集。SNMPv3 的认证与加密算法需要同时被设备和本机 SNMP 工具支持；不要把 community、认证密码或私密地址写入公共问题单和测试样本。
3. 首次观察至少两个采集周期。累计网络计数器需要前后两次有效采样才能得到速率。
4. 对照设备原生管理界面检查状态、单位和数值，记录无法提供的项目。设备未实现 OID、认证失败和实际状态异常应分别排查。
5. 使用第二台设备验证隔离，并覆盖接口重命名、缺失字段、设备重启、超时和恢复。H5 页面同时检查筛选、详情以及无数据提示。
6. 发布兼容清单时列出具体型号、固件、协议、验证过的指标和测试时间；脱敏后的协议样本进入回归测试。

## 后续路线及完成标准

| 阶段 | 工作 | 完成标准 |
| --- | --- | --- |
| 基础接入 | 多设备隔离、通用 SNMP 模板、标准表换算、Redfish 真实状态、设备查看入口。 | 离线样本、异常值、部分失败和跨设备隔离可自动复现；接入示例与实际配置一致。 |
| 发现编排 | 在现有受限地址扫描基础上增加设备识别、待接入列表、去重、模板选择和采集状态。 | 扫描有范围、并发和超时上限；已发现但未成功采集的设备明确标记，不自动假定正常。 |
| 厂商适配 | 按实际设备优先加入交换机、防火墙、NAS、UPS、打印机和 BMC 的私有指标。 | 每个模板附官方 MIB/协议依据、脱敏样本、模型和固件验证记录；通用支持与专项支持分别标识。 |
| 运维闭环 | 设备详情联动拓扑、维护窗口、告警抑制、服务影响和 AI 人工修复方案。 | 端口、设备、服务之间有稳定身份关系；AI 方案引用可访问的真实指标，操作继续由授权用户处理和复核。 |
| 规模和治理 | 调度并发、指标基数预算、批量配置、凭据管理及探针容量规划。 | 给出固定硬件、设备数、接口数、采集周期下的 CPU/RSS、P95 延迟、丢样与恢复结果；不以微基准代替整机容量承诺。 |

后续阶段属于待开发和验收的工作，不应从对标表推断本项目已经具备商业产品全部功能。
