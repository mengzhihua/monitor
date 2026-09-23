# 构建与验收脚本

下列命令均从**仓库根目录**执行。`make all` 按顺序构建 Web 和 Go 服务端；`make core` 只编译 Go，依赖已经生成的内嵌 Web 资源。

## 构建和打包

| 脚本 | 用途 | 输出 |
| --- | --- | --- |
| `build-android-server.sh` | 编译 Android Go 二进制并调用独立 Kotlin 工程的 Gradle | `android/app/src/main/jniLibs/`、`android/app/build/outputs/apk/` |
| `package-macos-app.sh` | 将已有 Universal `.app` 拆分架构并打包 | 调用者指定的目录，发布流程使用 `dist/` |
| `package-server-release.py` | 受控构建 8 个服务端目标并生成白名单归档、macOS Universal、提交清单和 SHA-256 | `dist/monitor-<版本>-<提交>/` |
| `package-server-release_test.py` | 校验错版本/旧构建、符号链接、归档白名单和原目录保护 | 终端测试结果 |
| `check-release-version.py` | 检查 VERSION、Go 默认版本、Web 包和锁文件一致 | 不一致时返回失败 |
| `stamp_app_version.py` | 发布构建前同步 Flutter 应用版本 | 修改调用者指定的 `pubspec.yaml` 或构建产物 `version.json` |
| `stamp_app_version_test.py` | 验证版本写入规则 | 终端测试结果 |

前两个脚本的参数见文件头注释。Android 打包前需 `make web`，macOS 打包前需完成 Flutter Universal 构建。

服务端正式打包使用 `make package-server`：先构建内嵌 Web，再由脚本以根目录 `VERSION` 编译二进制并记录 SHA-256 构建收据，随后打包。需要已提交且干净的工作树；拒绝错平台、非当前提交、脏源码或缺失/不匹配构建收据的二进制。单独运行 `python3 scripts/package-server-release.py` 只打包已具备匹配收据的构建，`--build-only` 可只编译，`--target linux-amd64 --no-universal` 可限制目标。不会覆盖已有发布目录，可用 `--output-root` 指定新的父目录。

## 验收

| 脚本 | 检查范围 | 运行方式 |
| --- | --- | --- |
| `smoke.sh` | Agent / Hub API、默认密码、claim 和 ring | `bash scripts/smoke.sh` |
| `verify-default-auth.py` | 默认认证、错误密码、WebSocket 拒绝、重启和重置 | `python3 scripts/verify-default-auth.py` |
| `verify-operations.py` | 2.0 真实采样、处置/个人视图持久化、服务重启、单条/批量版本冲突及历史查询导出 | `python3 scripts/verify-operations.py` |
| `verify-notifications.py` | 本机真实内存触发通知、HTTP 成功/503、静默/路由、凭据隐藏、只读权限及重启计数边界 | `python3 scripts/verify-notifications.py` |
| `verify-maintenance.py` | 预约开始、精确告警范围、活动计划重启、到期恢复新提醒、重叠取消、并发与只读权限、第二次重启保留取消记录 | `python3 scripts/verify-maintenance.py` |
| `verify-apps-identity.py` | macOS 进程身份、完整快照、历史计数、认证与退出清理 | `python3 scripts/verify-apps-identity.py --require-clean-startup` |
| `verify-durability.py` | 强杀恢复、离线备份和恢复 | `python3 scripts/verify-durability.py` |
| `load-hub.py` | 模拟节点阶梯负载、Hub 重启历史校验 | `python3 scripts/load-hub.py` |
| `soak.py` | 指定时长的持续采集和查询测量 | `python3 scripts/soak.py --seconds 120` |
| `benchmark-runtime.py` | macOS/Linux 默认采集器下的固定12请求/秒负载、接口延迟及主进程 CPU/RSS | `python3 scripts/benchmark-runtime.py --output reports/runtime.json` |
| `benchmark-browser.mjs` | 遍历100张模拟图表后的画布、DOM、事件监听器和 GC 后 JS 堆 | `node scripts/benchmark-browser.mjs --output reports/browser.json` |
| `e2e-server.mjs` | 为 Playwright 启动临时服务 | 由 `web/playwright.config.ts` 调用，无需手动启动 |

运行验收前先 `make all`。`make acceptance` 串联 Go/Web、默认认证、恢复、运维记录重启持久化、macOS 进程身份、Hub 负载、持续采集、浏览器、Flutter 和跨平台编译，所需环境见 [验收记录](../docs/05-acceptance.md)。进程身份脚本只在 macOS 上运行，`--require-clean-startup` 同时要求启动和稳定阶段的采集失败数为零；其他平台由 Go 测试覆盖通用采集逻辑。

命令行进程测试使用临时配置和数据，读取测试目录生成的密码，不使用部署密码。冒烟占用 `19998` / `18999`，浏览器测试占用 `19997`；其余 Python 进程测试选择本机空闲端口。

通知验收和浏览器测试的通知接收器仅绑定 `127.0.0.1` 随机端口，分别返回 HTTP 204 / 503，不连接真实第三方通知服务。`verify-notifications.py --binary <路径>` 可直接验证包内二进制。它重启同一临时数据目录，验证通知诊断不会把上次启动的发送结果算作当前进程结果；原有告警日志仍按既有方式保存。

`verify-maintenance.py` 同样支持 `--binary`，使用两条基于真实内存的测试告警和本机 HTTP 接收器。它为其中一条安排 12 秒维护窗口，期间重启，验证另一条仍发送、问题仍显示，维护结束后按 `repeat` 周期产生新提醒；随后验证重叠计划、取消和第二次重启。配置、凭据、采样和发送记录仅存在于隔离测试目录或内存。

保存报告时统一放入被 Git 忽略的 `reports/`：

```sh
mkdir -p reports
python3 scripts/load-hub.py --output reports/hub-load.json
python3 scripts/soak.py --seconds 120 --output reports/agent-soak.json
```

浏览器截图和 trace 保留 Playwright 的原生位置 `web/test-results/`；Flutter、Gradle 产物保留各工程自己的 `build/`。不要把产物移进源码目录，也不要提交真实配置、密码、备份或日志。

两个 `benchmark-*` 脚本都支持 `--binary` 和 `--label`，可对不同版本构建重复同一场景。运行对比时避免同时编译或运行其它压测；运行基准默认先预热15秒再采样60秒，CPU百分比以单个逻辑核为100%，只计 `monitord` 主进程，不含外部采集命令或浏览器。浏览器基准需要先在 `web/` 安装依赖，默认使用 Chrome；`--channel chromium` 使用已安装的 Playwright Chromium。模拟历史数据只有3行，测量的是保留资源，不代表生产查询吞吐或系统总内存。两者均使用自建临时服务并在结束后删除临时凭据和数据，JSON旁保留测试服务日志。

运行基准可加 `--include-children --child-sample-interval 0.2`，补充外部采集命令的开销：`process_tree_lifetime` 使用 `wait4` 统计本次启动的服务及已回收后代的 CPU，包含启动、预热、测量和退出全程，不能当作60秒稳定区间的 CPU。`child_process_sampling` 按进程表快照统计服务及直接子进程的 RSS，可能重复计算共享页；短命令和瞬时峰值可能漏采，因此子进程 CPU、启动数量和最大 RSS 只是观测下限，平均值/p95 是采样统计。对比两版时保持相同采样间隔；采样工具本身的开销不计入服务 CPU。
