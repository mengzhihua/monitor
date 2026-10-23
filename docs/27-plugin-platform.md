# 平台能力与插件接入

服务端通过版本化能力目录、插件清单和管理接口接入本地扩展。外部采集器独立运行，输出进入现有 Registry、TSDB、实时图表和告警链路；安装或更新插件无需重新编译、重启 `monitord`。前端插件管理作为工作区模块注册，管理对象始终是当前服务本机。

当前扩展点是 **metrics 采集进程**，支持现有 plugins.d/v1 文本协议。插件函数调用、第三方前端代码加载、在线商店和远程下载安装尚未提供。

## 安装第一个插件

仓库提供 [`examples/plugins/heartbeat`](../examples/plugins/heartbeat/)：

```text
plugins.d/
  heartbeat/
    plugin.json
    collector.sh
```

把示例整个目录复制到配置的 `plugins.dir` 下，确保 `collector.sh` 可执行，再使用已认证管理员登录“插件管理”，点击“重新扫描”。也可以重启服务自动发现。示例使用 POSIX shell，适用于 macOS/Linux；Windows 包应提供对应平台可执行文件。

`plugins.dir` 相对 `monitor.yaml` 所在目录解析，默认 `plugins.d`。示例清单：

```json
{
  "api_version": 1,
  "id": "heartbeat",
  "name": "Heartbeat example",
  "version": "1.0.0",
  "description": "Publishes a pulse counter",
  "protocol": "plugins.d/v1",
  "capabilities": ["metrics"],
  "command": "collector.sh",
  "update_every": 1,
  "timeout": 10
}
```

- `id` 必须与目录名相同，格式为小写字母开头，后续允许小写字母、数字、下划线和连字符，最多 64 字节。
- `api_version` 当前为 `1`，`protocol` 为 `plugins.d/v1`，能力必须恰好为 `["metrics"]`。未知字段、版本、协议或能力会产生目录诊断，该包不会执行。
- `name`、`version` 必填；`description` 可省略。`version` 是插件作者提供的版本标签，不会触发网络下载或依赖解析。
- `command` 必须是包内相对文件，不能越过目录或符号链接；包目录和清单也不能是符号链接。可选 `args` 会追加在采样间隔参数之后，管理响应不返回执行参数。
- 清单最大 64 KiB，最多 64 个参数，每个参数最多 4096 字节。`update_every`、`timeout` 接受 0～86400 秒，0 采用原有默认值。
- 新清单包只继承必要的系统运行环境以及 `MONITOR_*` / `NETDATA_UPDATE_EVERY`。已有显式配置和旧脚本保持环境兼容；不要把凭据硬编码到示例或提交到仓库。

清单包输出的图表 ID 必须以 `plugin.<id>.` 开头，例如 `plugin.heartbeat.pulse`。每个包最多保留 128 张图表，每张最多 64 个维度、128 个标签，最多 128 个变量；已注册定义跨插件进程重启计入预算。重复声明仍保留历史；清单包禁止 `OVERWRITE`，避免替换定义后绕过累积维度预算。已有其他采集器占用的同名图表不会被接管。这些预算约束当前服务进程保留的定义，不是磁盘配额。

`HOST_LABEL` 和 `FUNCTION` 不属于 metrics 能力，清单包使用时记入协议错误。非有限数值不会进入采样/变量状态。其他文本协议见 [plugins.d 协议](03-plugins-d-protocol.md)。

## 发现、更新与兼容

同名优先级为：`plugins.list` 显式配置 → 清单包 → 顶层旧 `*.plugin`。包目录预留自己的名称；坏清单不会退回执行同名旧脚本。旧协议的 ID 和主机标签行为保持兼容，新清单约束不会冒充对旧脚本的隔离。

重新扫描会比较执行设置、元数据和入口文件内容：未变化的实例继续运行；新增实例启动；改变或删除的实例先停止，再交接新实例。更新入口依赖的其他文件时，请显式“重启”插件或修改清单版本。无效包显示诊断，其他合法包继续采集。禁用或卸载不会删除历史数据。

插件是管理员安装的本机程序，与服务使用同一系统身份运行。命名空间、预算和环境过滤是协议边界，不是操作系统沙盒；包仍可访问该系统身份有权访问的文件和网络。目录只应由可信管理员维护。

Unix 停止先发送进程组 TERM，超期后 KILL；退出时清理残留后代。Windows 先挂起创建进程，加入 Job Object 后才恢复初始线程，避免加入前派生后代；启动任一步失败都会清理并等待回收。Windows 专项父子进程回归已加入测试，但本轮本机只有交叉编译证据，未在 Windows 实机执行。

## 管理 API

`GET /api/v1/platform` 和 `/api/v1/info.platform` 返回相同能力契约：

```json
{
  "api_version": 1,
  "scope": "local",
  "capabilities": ["plugins.catalog.v1", "plugins.control.v1"],
  "plugin_protocols": ["plugins.d/v1"]
}
```

能力列表根据插件运行时是否配置、当前用户是否为已认证管理员生成。不能通过插件数量或匿名管理员角色推断操作权限。

| 接口 | 行为 |
| --- | --- |
| `GET /api/v1/plugins` | 返回 `schema_version`、`revision`、`running`、`plugins`、`diagnostics` |
| `POST /api/v1/plugins/actions` | 接收 `{ "name": "heartbeat", "action": "disable", "if_revision": 7 }`，返回最新目录 |
| 同一 POST，`action=rescan` | `name` 为空，重新读取本机目录 |

动作支持 `enable`、`disable`、`restart`、`rescan`。`if_revision` 必须来自最近一次目录快照，陈旧版本返回 409；普通采样和状态轮询不改变管理版本。操作成功表示管理变更已提交，实际采集是否运行仍需看状态与最新样本。

接口禁止未认证用户、viewer/troubleshooter 和远端 `node` 参数；不能从 API 传入任意命令、参数或环境变量。动作进入现有审计日志，记录插件名和动作，不记录执行凭据。前端读取失败时标记快照过期并禁用写入，版本冲突要求刷新，后台与卸载停止轮询。

管理员启停状态保存在 `<data-dir>/plugins/state.json`，原子写入成功后才改变进程；配置里的 `plugins.disabled` / `list[].disabled` 是更高优先级的锁定，API 不能绕过。损坏的控制状态会阻止启动，不会静默恢复执行插件。动作在提交前可取消，提交后完成进程交接；客户端超时应刷新状态，不能假定操作未发生。

## 开发边界

- HTTP 层只依赖 `PluginRuntime` 接口；插件运行时负责发现、控制、持久化和进程监督，各自独立文件实现。
- 内置 Collector 注册先复制工厂快照，再执行工厂/初始化，避免扩展初始化占用全局注册锁。
- `Scheduler.Close` 是终态、幂等的资源清理入口，未调用 Run 或后续启动失败也释放初始化时打开的端口。Init 仍必须自行返回，Collect 必须遵守 context 取消。
- 工作区注册描述包含 `local`、`node`、`global` 范围，标题与组件重建遵循该范围，避免选中远端节点后误标本机操作。

## 验证入口

```sh
make all
make test-fast
make test
python3 scripts/verify-plugin-platform.py
cd web && npx playwright test e2e/plugins.spec.ts e2e/plugin-runtime.spec.ts e2e/api-contract.spec.ts
```

真实插件验收使用隔离临时目录、随机端口和临时凭据，覆盖在线安装、真实样本与历史、权限、版本冲突、独立进程启停、停用状态重启保留、入口内容更新、坏包隔离、移除与审计。测试结束清理服务和插件进程，不使用部署目录。

## 本轮验收（2026-10-23）

- `make test-fast`、`make test` 均通过：Go vet、24 个包的竞态测试、30 项 Python 单元测试及 soak 配置隔离、21 项前端模型/策略测试、14 项架构测试和类型检查。
- `verify-plugin-platform.py` 通过真实进程全链路；运行中的违规插件输出越界 ID、主机标签、OVERWRITE 和 NaN 被拒绝，合法插件仍持续采样。
- 插件管理、API 契约、模块导航/生命周期、监控会话和图表时间线 6 个浏览器 spec 共 74 项通过，桌面/手机各 37 项；另抽查两种尺寸的插件面板截图。独立真实服务的浏览器用例再通过 2 项，验证真实清单、样本查询、界面启停/重启、版本和 PID 变化，以及退出后没有残留插件进程。
- Collector 生命周期用例竞态重复 30 轮通过，真实主进程后续启动失败时释放 StatsD/OTLP 端口的用例重复 5 轮通过。Unix 插件组忽略 TERM、正常退出后遗留后代和 TERM 收尾用例通过。
- Web、本机服务端和 8 个服务端目标交叉编译通过；Windows amd64/arm64 的新增父子进程测试也已交叉编译，尚未在 Windows 上运行。
