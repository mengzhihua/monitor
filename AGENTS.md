# AGENTS.md

This file provides guidance to AI coding agents when working with code in this repository.

## 项目概述

对标 Netdata 的实时（每秒）监控平台 Monitor。服务端 `monitord` 是 Go 单静态二进制，通过配置 `mode: agent | hub` 切换角色；客户端有内嵌 Web Dashboard（Vue3，go:embed 进二进制）和 Flutter 五端 App。

Monorepo 四大组成部分，容易混淆的两处：

- `app/` 是 Flutter **客户端**（含 `app/android/` 平台工程）；根目录 `android/` 是独立的 Kotlin Android **服务端**壳（打包静态 monitord），二者互不相关。
- `core/internal/api/ui/dist/` 是 `web/` 的构建产物，供 Go embed，Git 只保留 `.gitkeep`。

## 常用命令

工具链：Go 1.25+（CI 固定 1.27.1）、Node 24.19.0（`.node-version`，仅构建 Dashboard 需要）、Flutter 3.35.4。

```bash
make all              # 先 npm 构建前端、再编译 core/bin/monitord（顺序固定，UI 必须先于 Go 编译）
make core             # 仅编译服务端（用于嵌入资源已就绪的场景，如 CI 下载产物后）
make run              # make all 后启动 :19999
make test             # go vet + go test -race ./... + 前端 typecheck
make lint             # gofmt -l 检查 + go vet（CI 在 Linux 上强制 gofmt 为空）
make cross            # 交叉编译 linux/darwin/windows/freebsd/android(arm64)

# 单个 Go 测试
cd core && go test -race -run TestName ./internal/collect/

# 前端
cd web && npm run dev          # Vite 热更新，API 代理到 127.0.0.1:19999
cd web && npm run typecheck    # vue-tsc
cd web && npm run test:e2e     # Playwright（需先安装 chromium）

# Flutter 客户端
cd app && flutter analyze && flutter test

# 运行验收脚本
./scripts/smoke.sh                 # 启动 monitord（端口 19998）验证 API/metrics/dashboard
make verify-durability             # 崩溃恢复 + 备份（scripts/verify-durability.py）
make acceptance                    # 全套：构建+测试+smoke+durability+hub-load+soak+e2e+flutter+cross
```

推送到 `main` 会自动打 tag（patch +1）并发布 Release；升 minor/major 需手动推 `v*` tag。

## 服务端架构（core/）

数据流水线（各环节对应 `core/internal/` 下的同名模块）：

```
Collect（采集）→ Registry（Host→Chart→Dimension 元数据；incremental 算法在此做差分）
  → TSDB（tier0 1s / tier1 60s / tier2 3600s 分层降采样，Gorilla 压缩，追加式块文件）
  → 分支：ML 异常检测 / Health 告警引擎 / Stream（Agent→Hub WSS 上报）/ Export
  → Query Engine → HTTP API + WebSocket（core/internal/api，REST /api/v1、/api/v2、/api/v3，/api/v1/live）
```

- 入口 `core/cmd/monitord/main.go`，Agent 与 Hub 共用。命令行 `-listen` / `-data-dir` / `-log-level` 可覆盖 `monitor.yaml`（模板见根目录 `monitor.example.yaml`）。
- Hub 模式：每个远端节点独立 Registry + TSDB 命名空间（`node:<id>|` 前缀），共用同一存储；节点元数据持久化在 `data/hub/nodes.json`。Agent 出站 WebSocket 连 Hub 的 `/api/v1/stream`，断线从本地 TSDB 回放缺口。
- 认证：默认无认证配置时首次启动生成随机密码写入 `<data_dir>/web-password`，作为 Bearer token 使用；`web.users` 提供 admin/troubleshooter/viewer RBAC。
- 详细的模块设计、数据模型（context 是跨主机聚合键）、部署形态见 [docs/02-architecture.md](docs/02-architecture.md)。

### 新增采集器

`core/internal/collect/` 下每个采集器一个文件，模式固定：

```go
func init() { Register("redis", func() Collector { return &redisCollector{} }) }
```

实现 `Collector` 接口（`Name` / `Configure(decode func(v any) error)` / 采集循环），配置读 `collectors.modules.<name>` 段。目标不可达时应自动禁用（返回特定错误）而不是报错刷屏。平台特定代码用 build tags 拆分：`*_linux.go` / `*_darwin.go` / `*_windows.go` / `*_other.go`。`monitord -list-collectors` 列出已注册采集器。

外部采集器走 plugins.d 文本协议（`CHART/DIMENSION/BEGIN/SET/END`，见 [docs/03-plugins-d-protocol.md](docs/03-plugins-d-protocol.md)），无需改 Go 代码。

## 本地运行时测试约定

完整的运行时测试指引（UI 检查、协议/重启验证、Hub 双进程、token 检查）见 `.agents/skills/monitor-runtime-testing/SKILL.md`。关键点：

- 启动前用 `ss -ltnp` / `pgrep -ax monitord` 检查 19998/19999 端口占用；`smoke.sh` 固定用 19998。
- 测试时创建**空的临时 config 文件**（避免读到仓库根的 `monitor.yaml`）+ 临时 data-dir。
- API 调用需认证：读临时数据目录的 `web-password` 作 Bearer token；不要把凭据打印进测试输出。
- 测试结束只停自己启动的进程（SIGINT），保留既有数据目录。

## 其他

- 与 Netdata 的能力差距清单和移植计划（M0–M26 已完成，后续批次）见 [docs/04-netdata-gap.md](docs/04-netdata-gap.md)；性能数字与验收记录见 [docs/05-acceptance.md](docs/05-acceptance.md)。
- Git 忽略的生成物：`core/bin/`、`data/`、`dist/`、`reports/`、`web/test-results/`、`android/app/src/main/jniLibs/`（Android 服务端打包时生成的 Go 二进制）。
