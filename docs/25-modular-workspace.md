# 模块化监控工作台

本轮重构聚焦 Web 监控工作台。服务端采集协议、TSDB 和配置格式保持兼容；前端仍由 `make all` 构建并嵌入同一个 `monitord`，无需部署额外服务。

## 页面组织

桌面左侧按“监控、诊断分析、管理”划分功能。页头只保留主机/节点选择、连接状态、告警数量与构建标识。每次只挂载一个工作区，进入日志、AI、配置等模块后，旧工作区的图表订阅、定时器和请求随组件卸载清理。

手机保留“总览、图表、告警、更多”底部导航；更多功能按相同分组展示。切换模块支持 URL 定位及浏览器前进/后退，配置和规则等编辑模块在离开前检查草稿，终端在导航切换前确认断开连接。

| 模块 | 展示方式 |
| --- | --- |
| 运维总览 | 健康摘要固定，问题中心、主机资源、处理历史、维护计划、通知诊断按分区展示 |
| 指标图表 | 主机资源、网络、存储、设备、应用、数据库与中间件、容器与虚拟化、其他指标分类 |
| 指标搜索 | 匹配标识、标题、family、context、采集模块和设备标签；切换分类或节点后回到第一页 |
| 指标分页 | 每页最多 12 张图表，换页卸载上一页；图表内部仍按可见区域创建画布、订阅实时数据 |
| 聚合看板 | 原有内置/个人看板、导入导出、草稿、统一时间轴继续可用，返回指标时恢复视图偏好 |
| 节点管理 | Hub 模式提供独立入口；管理员可清理离线节点，图表页不再堆放全部节点卡片 |
| 配置 | 沿用原有 YAML/表单和版本冲突检查；手机账号、通知角色表格按卡片排列 |

没有采集到的数据仍表示未知或缺失，分类、分页和无匹配结果不代表设备健康。图表页的采集器状态可以展开查看，远端节点不冒用本机采集状态。

## 代码边界

```text
web/src/
├── App.vue                         会话、导航与当前模块的装配
├── api.ts                          旧调用方兼容入口，不在此新增领域实现
├── api/                            auth、selection、transport 与各领域 client/types
├── features/
│   ├── monitoring/
│   │   └── useMonitorSession.ts     认证、节点、元数据、告警、实时连接与轮询
│   ├── workspace/
│   │   ├── contracts.ts           模块离开检查与图表下钻的公开接口
│   │   ├── modules.ts              唯一模块注册表、菜单、可见条件、异步加载入口
│   │   ├── WorkspaceModuleHost.vue 模块错误边界、离开检查与异步就绪代理
│   │   ├── WorkspaceHeader.vue     身份、节点选择与连接状态
│   │   └── NodeInventory.vue       节点管理
│   ├── metrics/
│   │   ├── MetricsWorkspace.vue    搜索、分类、分页、看板与时间轴装配
│   │   ├── metricCatalog.ts        指标分类和全文索引，无网络请求
│   │   ├── useMetricsTimeline.ts   时间轴状态及浏览器暂存
│   │   └── CollectorStatus.vue     采集器/插件状态
│   ├── operations/
│   │   ├── OperationsNavigation.vue 分区与键盘导航
│   │   ├── OperationsNodes.vue      主机资源卡片
│   │   ├── OperationsProblemCard.vue 单个问题展示与交互事件
│   │   ├── useOperationsSection.ts  分区与 URL 同步
│   │   ├── useOperationsFilters.ts  筛选状态与统一查询参数
│   │   ├── useOperationsOverview.ts 概览轮询、搜索防抖与列表派生
│   │   ├── useProblemHandling.ts    单项/批量处置、草稿与版本
│   │   └── useOperationsExports.ts  导出请求与取消
│   └── configuration/
│       ├── useLocalConfiguration.ts 本机表单/YAML、基线、冲突、保存、回滚
│       ├── useNodeConfiguration.ts  节点读取/应用状态、草稿与下发
│       ├── useConfigurationRequest.ts 可取消读取与响应有效性
│       └── useConfigurationFeedback.ts 跨编辑器变更锁与消息
└── components/                     现有业务模块及可复用的图表、编辑器、工具组件
```

`components/` 中的成熟业务模块继续复用；本次优先拆解入口耦合、看板数据与运行生命周期，没有为改目录而一次性迁移所有文件。

## 新模块接入约定

1. 在 `features/<领域>/` 放领域逻辑、独立组件与状态；不要把领域请求或轮询重新塞回 `App.vue`。
2. 在 `features/workspace/modules.ts` 注册标识、中文名称、分组、说明、可见条件和 `defineAsyncComponent`。桌面导航、手机入口与 URL 标识由同一注册表驱动。
3. 业务状态放到同领域的 `use*.ts`，接收 ref、函数和纯类型接口，不引用 `.vue` 组件。`App.vue` 与领域容器只装配这些状态和视图。在 `App.vue` 的 `moduleProps` / `moduleEvents` 适配模块边界。模块只接受所需 props，通过事件请求导航，不直接修改其他模块的状态。
4. 有草稿或敏感活动的模块暴露同步 `canLeave(): boolean`；写入期间返回 false。配置、规则、模板等编辑器的关闭与切换共用草稿检查，不能绕过现有配置并发校验。AI/终端的主动关闭沿用原有取消/断开行为，导航离开另行检查未保存配置或活动连接。
5. 卸载时终止自身请求、轮询、订阅和事件监听。不要用隐藏 DOM 的方式让整个功能继续后台轮询；跨分区需保留的少量草稿上移到所属领域状态。
6. 新领域代码直接引用 `api/<领域>/client.ts` 与 `types.ts`，HTTP 错误使用 `api/transport.ts`。旧 `api.ts` 仅兼容已有调用方；不要反向从 API 内层或新 features 引用它。
7. 菜单可见条件仅改善交互，服务端 API 权限仍是最终边界。查看者的深链接不能挂载管理员模块或请求其配置。

## 领域状态的职责

| 状态所有者 | 负责内容 | 生命周期边界 |
| --- | --- | --- |
| 运维筛选 | 搜索、级别、责任人、进度、分页和个人视图参数 | 列表和导出使用同一查询定义 |
| 运维概览 | 15 秒轮询、250 毫秒搜索防抖、派生列表 | 筛选变化和卸载取消旧读取 |
| 问题处置 | 单项/批量互斥、选择、备注、责任人、进度和原始 revision | 问题卡片或分区卸载不丢草稿，整个工作区离开才检查草稿 |
| 运维导出 | 快照/诊断包请求、权限和下载状态 | 筛选变化、权限失效和卸载取消不再需要的导出 |
| 本机配置 | YAML/表单、编辑基线、配置冲突、保存/回滚/重启 | 异步变更完成后先确认模块仍有效 |
| 节点配置 | 选中节点、读取、应用状态、草稿、下发版本 | 切换取消旧节点读取；轮询等待正在进行的慢读取结束 |
| 配置协调器 | 两个编辑器共用的变更锁、错误和提示 | 不持有配置内容或 revision；写入期间阻止切换和离开 |

`OperationsProblemCard` 仅接收展示所需数据并发出事件，不发起请求或私自持有处置草稿。模块宿主的公开实例接口统一定义在 `workspace/contracts.ts`；异步加载、错误显示与下钻排队继续由 `WorkspaceModuleHost` 管理。

## 模块依赖检查

`npm run check:architecture` 使用现有 TypeScript 与 Vue 解析器检查源码依赖。`npm run build` 和 `make test` 都执行此检查，不需要新增第三方依赖。

- API 内层不得引用兼容入口、Vue、组件或 features；基础层不感知 UI。
- features 必须显式引用领域客户端或类型，避免继续扩大聚合 API 的耦合。
- 状态 composable 不引用 `.vue` 实例；跨模块能力通过纯类型和参数传入。
- 除工作区装配层外，领域不直接引用另一个领域的内部实现；公共契约下沉到共享层。
- 检查静态 import/export 和字面量动态 import 的运行时循环；纯类型引用不误判为运行时循环。只有 `main.ts` 可以导入 `App.vue`。

检查针对本项目源码中的相对路径依赖，测试覆盖错误分层、动态循环、类型引用、Vue 脚本位置和注释误判。新增路径别名或新的模块加载方式时，需要同步扩展检查器的解析规则。

## 数据与登录生命周期

会话层用节点与请求代次校验响应。切换节点会清空旧数据、停止旧实时连接并取消在途读取；旧节点迟到的成功响应或 401 不得覆盖新节点。任一当前会话的元数据、节点、函数或告警请求返回 401 时，清空监控状态并回到登录。

告警快照和历史继续合并实时事件并去重；只有进入告警工作区才加载事件历史。页面隐藏时沿用前台轮询规则。浏览器禁止会话存储时，凭据只在当前页面内存中使用，刷新后需重新登录；URL 中一次性导入的 token 仍会移除。

## 验证

先构建嵌入式 UI：

```bash
make all
cd web
npm run check:architecture
MONITOR_BROWSER_CHANNEL=chrome npm run test:e2e -- \
  module-navigation.spec.ts navigation-lifecycle.spec.ts monitor-session.spec.ts metrics-workspace.spec.ts \
  operations-sections.spec.ts operations-module-lifecycle.spec.ts config-module-lifecycle.spec.ts \
  api-contract.spec.ts performance.spec.ts mobile-h5.spec.ts
```

重点覆盖：单一工作区挂载、懒加载、100 张指标的分页上限、分类和无结果、历史时间、节点切换、登录失效、草稿与离开确认、取消隐藏模块请求、320/390/430 像素布局，以及原有配置、设备接入、告警、看板、处置、维护和通知回归。并行运行浏览器测试时必须各用独立的 `MONITOR_E2E_PORT` 和 `--output` 目录。

本地 Chrome 的手机尺寸测试不等同于真实 iPhone Safari 或 Android 软键盘验收。没有把代码拆分后的文件体积换算为未经测量的线上响应速度、CPU 或内存收益。
