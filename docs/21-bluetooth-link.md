# 蓝牙设备与音频链路采集

采集本机**已连接**的蓝牙耳机、音箱、键盘、鼠标等设备，报告链接模式、编解码器（SBC / AAC / aptX / LDAC / LC3 / mSBC 等）、协商后的码率与采样格式、信号与链路质量，以及控制器层面的收发量与错误。采集器名 `bluetooth`，覆盖 Linux、macOS、Windows、Android；没有蓝牙控制器的主机上自动禁用。

对应的 `collectors.modules.bluetooth` 配置见 [`monitor.example.yaml`](../monitor.example.yaml)。

## 一分钟上手

本机已连接蓝牙音箱时，直接运行一次性报告：

```bash
./core/bin/monitord -bluetooth-report          # 人读报告
./core/bin/monitord -bluetooth-report-json     # 同样内容的 JSON
```

Linux 上读取链路质量、发射功率和 AFH 信道图需要原始 HCI 权限，否则这几项会显示“未提供”：

```bash
sudo ./core/bin/monitord -bluetooth-report
# 或给二进制授权后以普通用户运行
sudo setcap 'cap_net_raw,cap_net_admin+eip' ./core/bin/monitord
```

常驻采集时无需额外操作：`collectors.enabled` 为空即包含 `bluetooth`，图表出现在仪表盘的「蓝牙设备与音频」分组，设备明细在 `bluetooth-devices` 函数（API：`/api/v1/function?function=bluetooth-devices`）。

## 报告内容

一次报告分三部分：控制器、设备、**本机未能测量的项目**。第三部分是刻意保留的：缺失的指标会写明原因，不会以 0 或“正常”呈现。

```
蓝牙链路报告  平台: linux  采样时间: 2026-10-12T13:39:28Z

控制器:
  hci0 00:1A:7D:DA:71:13  电源: 开  总线: USB  收/发字节: 1509372 / 482019  错误: 收 0 / 发 3  空口速率: 收 12.4 kbps / 发 331.8 kbps

设备 (1):
  [1] JBL Flip 5  AC:BF:71:11:22:33
      类型: Loudspeaker   传输: BR/EDR   链路模式: active   角色: central (master)
      profile: Audio Sink (A2DP), AVRCP Controller, Hands-Free (HFP)
      编解码器: SBC   码率: 328.0 kbps (协商值，编解码器上限 452 kbps)   采样: 44.1 kHz / 16 bit / joint stereo
      信号: -47 dBm   链路质量: 96 %   发射功率: 4 dBm   AFH 可用信道: 79/79   电量: 90 %
      链路评分: 93 / 100 (excellent)
      数据来源: bluez-dbus, hciconfig, hcitool, pulseaudio
```

## 采集来源与各平台能力

每个来源都是可选的，缺哪个就在报告里说明缺什么。

| 平台 | 来源 | 提供的内容 |
| --- | --- | --- |
| Linux / FreeBSD | `dbus-send` 读 BlueZ `ObjectManager` | 设备清单、Class of Device、UUID、连接与配对状态、RSSI、TxPower、电量；`MediaTransport1` 的 **Codec 字节与 Configuration 原始配置**、传输状态与 Delay |
| Linux / FreeBSD | `bluetoothctl devices Connected` + `info`（`dbus-send` 不可用时的回退） | 同上的设备属性，没有编解码器配置 |
| Linux / FreeBSD | `pactl list`（PulseAudio 或 PipeWire 的 pulse 服务） | 音频服务端当前使用的编解码器名、采样规格、活动 profile、设备描述 |
| Linux / FreeBSD | `hciconfig -a` | 控制器收发字节 / ACL / SCO / 事件 / 命令 / 错误计数、HCI 与 LMP 版本、链路策略 |
| Linux / FreeBSD | `hcitool con / lq / tpl / rssi / afh` | 连接句柄与链路类型、角色、加密、链路质量（0–255）、发射功率、RSSI、AFH 信道图 |
| macOS | `system_profiler SPBluetoothDataType`（JSON 优先，同时解析文本报告） | 设备清单与类型、连接与配对、RSSI、角色、连接模式（active / sniff）、间隔、链路类型、发射功率、AFH 图、电量、固件 |
| Windows | PowerShell `Get-PnpDevice -Class Bluetooth` + 电量属性 | 设备清单、连接状态、经典 / LE 传输、电量 |
| Android | `dumpsys bluetooth_manager` | 适配器状态、已配对设备与设备类别、A2DP **当前编解码器配置**（含 LDAC EQMID 档位） |
| 任意 | `snapshot_dir` 录制回放 | 用其他机器录制的命令输出复现报告，见下文 |

平台的硬限制，报告里会逐条列出：

- **macOS 没有查询当前音频编解码器的命令行接口**。系统只在按住 Option 点击菜单栏蓝牙图标时显示 Active Codec，没有公开 API/CLI，本采集器在 macOS 上不报告编解码器，而不是猜一个。macOS 也不提供控制器字节计数。
- **Windows 的即插即用接口不含编解码器、RSSI 和链路质量**，只有设备清单、连接状态和电量。
- **Android 的 dumpsys 不含 RSSI 与链路质量**，评分只基于编解码器配置。
- Linux 上 `hcitool` 的 `lq` / `tpl` / `rssi` / `afh` 走原始 HCI 套接字，无 `CAP_NET_RAW` 时失败；报告会说明是权限问题而不是链路问题。

## 编解码器识别

编解码器名不是从设备名猜的，而是解析实际协商出来的 A2DP 配置：

- **编解码器类型字节**：0x00 SBC、0x01 MPEG-1,2 Audio、0x02 MPEG-2,4 AAC、0x04 ATRAC、0xFF 厂商自定义。
- **厂商自定义**再按 4 字节 Vendor ID + 2 字节 Codec ID 查表：aptX（0x0000004F:0x0001）、aptX HD（0x000000D7:0x0024）、aptX Adaptive（0x000000D7:0x00AD）、aptX Low Latency 与 FastStream（0x0000000A）、LDAC（0x0000012D:0x00AA）、LHDC（0x0000053A）、LC3plus HR（0x000008A9:0x0001）、三星 Scalable / HD（0x00000075）。未登记的组合按 `Vendor 0x........:0x....` 原样上报，不会被丢弃。
- **SBC** 配置解出采样率、声道模式、块长、子带数、分配方式与 bitpool 范围，并按 A2DP 规范 12.9 节的帧长公式算出码率。44.1 kHz / 联合立体声 / 16 块 / 8 子带 / bitpool 53 得到 328 kbps——这正是行业测试软件给出的 SBC 基准值；bitpool 高于 53 时标记为 SBC XQ。
- **AAC** 配置解出对象类型、采样率、声道、VBR 标志和配置里携带的比特率上限。
- **aptX / aptX HD** 按固定 4:1 压缩比从采样率与声道数推算（48 kHz / 24 bit 的 aptX HD 即 576 kbps）。
- **LDAC** 的 A2DP 配置只携带采样率和声道模式，不含码率。Android 的 `mCodecSpecific1` 给出 EQMID 档位，可换算为 990 / 660 / 330 kbps（44.1 kHz 系列为 909 / 606 / 303），自适应（ABR）模式则明确标注“码率随链路变化”。

每个码率都带来源标记：`negotiated`（由本次协商的配置算出）、`nominal`（编解码器标称值，本机拿不到配置时的回退）、`measured`（由控制器字节计数得出）。报告和表格里分别显示为“协商值 / 标称值 / 实测值”，不会把标称值当成实测结果。

### 编解码器参考表

表中的典型值与上限用于对照当前链路使用了多少编解码器能力，取自各编解码器的公开指标。

| 编解码器 | 传输 profile | 典型码率 | 上限 | 说明 |
| --- | --- | --- | --- | --- |
| SBC | A2DP | 328 kbps | 452 kbps | A2DP 强制编解码器，联合立体声 bitpool 53 |
| SBC XQ | A2DP | 452 kbps | 551 kbps | 提高 bitpool 的非标准配置，需双方支持 |
| AAC | A2DP | 256 kbps | 320 kbps | 苹果设备常用 256 kbps VBR |
| aptX | A2DP | 352 kbps | 384 kbps | 固定 4:1 压缩 |
| aptX HD | A2DP | 576 kbps | 576 kbps | 48 kHz / 24 bit |
| aptX Low Latency | A2DP | 352 kbps | 384 kbps | 目标端到端延迟约 40 ms |
| aptX Adaptive | A2DP | 420 kbps | 620 kbps | 279–420 kbps 自适应 |
| LDAC | A2DP | 660 kbps | 990 kbps | 330 / 660 / 990 三档加 ABR |
| LHDC / LHDC V5 | A2DP | 560 / 600 kbps | 900 kbps / 1 Mbps | 档位随版本不同 |
| LC3plus HR | A2DP | 400 kbps | 800 kbps | Fraunhofer 高解析模式 |
| 三星 Scalable / HD | A2DP | 512 / 384 kbps | 512 / 384 kbps | 88–512 kbps 自适应 |
| FastStream | A2DP | 212 kbps | 212 kbps | 双向 SBC 变体 |
| CVSD | HFP | 64 kbps | 64 kbps | 窄带语音，8 kHz |
| mSBC | HFP | 64 kbps | 64 kbps | 宽带语音（WBS），16 kHz |
| LC3-SWB | HFP | 64 kbps | 64 kbps | HFP 1.9 超宽带，32 kHz |
| LC3 | LE Audio | 160 kbps | 320 kbps | LE Audio 强制编解码器 |

## 指标与图表

全局（`family: bluetooth`）：

| 图表 | 单位 | 内容 |
| --- | --- | --- |
| `bluetooth.devices` | devices | 已连接、已配对设备数 |
| `bluetooth.connected_by_type` | devices | 按音频 / 输入 / 手机 / 电脑 / 其他分类的已连接设备数 |
| `bluetooth.audio_streams` | streams | 按编解码器统计的音频流数量 |

每个控制器：

| 图表 | 单位 | 内容 |
| --- | --- | --- |
| `bluetooth.adapter_traffic.<hci>` | B/s | 控制器收发字节速率 |
| `bluetooth.adapter_packets.<hci>` | packets/s | ACL / SCO 收发、HCI 事件与命令 |
| `bluetooth.adapter_errors.<hci>` | errors/s | 控制器收发错误 |

每个已连接设备（图表 ID 后缀为设备地址，标签含名称与类型）：

| 图表 | 单位 | 内容 |
| --- | --- | --- |
| `bluetooth.device_rssi.<addr>` | dBm | 信号强度 |
| `bluetooth.device_link_quality.<addr>` | percentage | 控制器链路质量（0–255 换算） |
| `bluetooth.device_tx_power.<addr>` | dBm | 发射功率 |
| `bluetooth.device_afh_channels.<addr>` | channels | AFH 可用跳频信道数（满值 79） |
| `bluetooth.device_quality_score.<addr>` | points | 链路综合评分 |
| `bluetooth.device_battery.<addr>` | percentage | 设备电量 |
| `bluetooth.device_audio_bitrate.<addr>` | kbps | 当前码率 |
| `bluetooth.device_audio_sample_rate.<addr>` | kHz | 采样率 |
| `bluetooth.device_audio_delay.<addr>` | milliseconds | A2DP 传输延迟（BlueZ `Delay`） |

`bluetooth-devices` 函数返回同一批数据的明细表，外加控制器视图、数据来源和未能测量的项目清单。

## 两个容易误读的量

- **BR/EDR 的 RSSI 不是绝对值**。HCI `Read_RSSI` 在经典链路上返回的是与控制器“黄金接收功率范围”的偏差（常见 0 表示落在范围内），只有 LE 链路返回绝对 dBm。报告会把这两种值区分标注（`rssi_kind`），黄金范围读数不参与评分，也不会被写成 dBm。BlueZ 的 `Device.RSSI` 属性来自广播/查询，是绝对 dBm，优先采用。
- **控制器空口速率是整机口径**。它由 `hciconfig` 的收发字节计数在两次采样间求得，覆盖该控制器上的所有链路；只连接一台音箱时可以用来和编解码器码率互相印证，多设备同时活动时不能归因到某一台。

## 链路评分

评分是本项目自己的加权，**不是蓝牙标准定义的指标**，用于快速排序而非合规判定：信号强度 40 分、控制器链路质量 25 分、编解码器余量 25 分、信道条件 10 分。本机测不到的项直接跳过，并按剩余权重归一化——缺少探针会降低结论的覆盖面，而不是凭空压低分数。等级为 `excellent`（≥85）、`good`（≥70）、`fair`（≥50）、`poor`。

- 信号：≥ −55 dBm 满分，−55 至 −70 dBm 线性降到一半，−85 dBm 为 0。
- 链路质量：控制器返回值除以 255。
- 编解码器：一半看当前码率占该编解码器上限的比例，一半看码率绝对值（以 660 kbps 为满分参照），避免“低码率编解码器跑满自身上限”被判为优秀。
- 信道：AFH 可用信道数除以 79。

评分旁会给出具体提示，例如信号弱、链路质量低于典型值、AFH 可用信道不足（2.4 GHz 干扰重）、码率取自标称值等。

## 与行业蓝牙测试软件的对照

| 能力 | 行业专用工具 | 本采集器 |
| --- | --- | --- |
| 设备清单与设备类别解码（Class of Device 主/次类、服务类） | 支持 | 支持 |
| 链接模式（active / sniff / hold / park）、角色、加密、连接句柄 | 支持 | 支持（Linux `hcitool con`、macOS 连接模式与间隔） |
| 当前编解码器与协商配置解码 | 支持 | 支持（Linux 读 BlueZ 原始配置；Android 读 A2DP codec status） |
| 码率、采样率、位深、声道模式 | 支持 | 支持，并标注协商值 / 标称值 |
| RSSI、发射功率、链路质量、AFH 信道图 | 支持 | 支持（Linux 需 HCI 权限；macOS 文本报告提供） |
| 控制器吞吐与错误计数 | 支持 | 支持（Linux） |
| 长期趋势、阈值告警、多机汇总 | 多数不支持 | 支持（接入 TSDB、告警与看板） |
| **空口抓包与协议解码**（Ellisys、Teledyne LeCroy Sodera、nRF Sniffer 等） | 支持 | **不支持** |
| **逐包重传率、CRC 错误率、PER/BER** | 支持 | **不支持**（只有控制器聚合错误计数） |
| **音质客观评分（PESQ / POLQA）、端到端音频延迟实测** | 部分支持 | **不支持** |
| **强制切换编解码器、发起回环与射频一致性测试** | 支持 | **不支持**，采集器只读，不改动链路 |
| **LE Audio CIS/BIS 的 QoS 参数、LE 连接间隔** | 支持 | **不支持**，主机栈未公开这些参数 |

需要逐包分析时，用 `btmon` / Android HCI snoop log / 专用抓包硬件；本采集器定位是**持续运行的链路状态与音频质量观测**，不是协议分析仪。

## 录制回放（snapshot_dir）

服务器上没有蓝牙硬件，但需要复现另一台机器的现场时，可以把命令输出录下来回放。文件名按“命令名 + 参数”用下划线连接，匹配时从最具体到最宽松，因此 `hcitool_lq.txt` 可以同时服务所有设备。

```bash
mkdir bt-snapshot && cd bt-snapshot
echo linux > platform
dbus-send --system --print-reply --dest=org.bluez / \
  org.freedesktop.DBus.ObjectManager.GetManagedObjects > dbus-send.txt
hciconfig -a            > hciconfig.txt
pactl list              > pactl.txt
sudo hcitool con        > hcitool_con.txt
sudo hcitool lq   AA:BB:CC:DD:EE:FF > hcitool_lq.txt
sudo hcitool tpl  AA:BB:CC:DD:EE:FF > hcitool_tpl.txt
sudo hcitool rssi AA:BB:CC:DD:EE:FF > hcitool_rssi.txt
sudo hcitool afh  AA:BB:CC:DD:EE:FF > hcitool_afh.txt
```

macOS 用 `system_profiler -json SPBluetoothDataType > system_profiler.json`，Android 用 `dumpsys bluetooth_manager > dumpsys.txt`，平台由 `platform` 文件或文件名推断。回放：

```bash
monitord -bluetooth-report -bluetooth-snapshot ./bt-snapshot
```

回放结果始终标注“数据来自 snapshot_dir 录制的命令输出，不是本机实时采集”。录制文件可能包含设备名与地址，分享前自行脱敏。

## 已验证范围与边界

- 单元测试覆盖：SBC / AAC / aptX HD / LDAC 配置解码与码率换算、厂商编解码器查表、Class of Device 解码、AFH 信道计数、`bluetoothctl info`、`hciconfig -a`、`hcitool con`、`pactl list`、BlueZ `GetManagedObjects` 文本回复、macOS JSON 与文本报告、Windows PnP JSON、Android `dumpsys` 编解码器块，以及从录制快照到图表与函数表格的全链路。
- 本轮开发环境没有蓝牙控制器，**未在真实硬件上验证**；真实设备上的核对请用 `-bluetooth-report`，并与系统自带工具（Linux `bluetoothctl info`、macOS 按 Option 点击蓝牙菜单、Android 开发者选项中的蓝牙音频编解码器）对照。
- 采集为只读：不配对、不断开、不修改编解码器或任何链路参数。
- 默认每 5 秒探测一次（`every`），外部命令带超时；探测失败不会影响其他采集器。
