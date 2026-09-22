# 构建与验收脚本

下列命令均从**仓库根目录**执行。`make all` 按顺序构建 Web 和 Go 服务端；`make core` 只编译 Go，依赖已经生成的内嵌 Web 资源。

## 构建和打包

| 脚本 | 用途 | 输出 |
| --- | --- | --- |
| `build-android-server.sh` | 编译 Android Go 二进制并调用独立 Kotlin 工程的 Gradle | `android/app/src/main/jniLibs/`、`android/app/build/outputs/apk/` |
| `package-macos-app.sh` | 将已有 Universal `.app` 拆分架构并打包 | 调用者指定的目录，发布流程使用 `dist/` |
| `stamp_app_version.py` | 发布构建前同步 Flutter 应用版本 | 修改调用者指定的 `pubspec.yaml` 或构建产物 `version.json` |
| `stamp_app_version_test.py` | 验证版本写入规则 | 终端测试结果 |

前两个脚本的参数见文件头注释。Android 打包前需 `make web`，macOS 打包前需完成 Flutter Universal 构建。

## 验收

| 脚本 | 检查范围 | 运行方式 |
| --- | --- | --- |
| `smoke.sh` | Agent / Hub API、默认密码、claim 和 ring | `bash scripts/smoke.sh` |
| `verify-default-auth.py` | 默认认证、错误密码、WebSocket 拒绝、重启和重置 | `python3 scripts/verify-default-auth.py` |
| `verify-durability.py` | 强杀恢复、离线备份和恢复 | `python3 scripts/verify-durability.py` |
| `load-hub.py` | 模拟节点阶梯负载、Hub 重启历史校验 | `python3 scripts/load-hub.py` |
| `soak.py` | 指定时长的持续采集和查询测量 | `python3 scripts/soak.py --seconds 120` |
| `e2e-server.mjs` | 为 Playwright 启动临时服务 | 由 `web/playwright.config.ts` 调用，无需手动启动 |

运行验收前先 `make all`。`make acceptance` 串联 Go/Web、默认认证、恢复、Hub 负载、持续采集、浏览器、Flutter 和跨平台编译，所需环境见 [验收记录](../docs/05-acceptance.md)。

命令行进程测试使用临时配置和数据，读取测试目录生成的密码，不使用部署密码。冒烟占用 `19998` / `18999`，浏览器测试占用 `19997`；其余 Python 进程测试选择本机空闲端口。

保存报告时统一放入被 Git 忽略的 `reports/`：

```sh
mkdir -p reports
python3 scripts/load-hub.py --output reports/hub-load.json
python3 scripts/soak.py --seconds 120 --output reports/agent-soak.json
```

浏览器截图和 trace 保留 Playwright 的原生位置 `web/test-results/`；Flutter、Gradle 产物保留各工程自己的 `build/`。不要把产物移进源码目录，也不要提交真实配置、密码、备份或日志。
