# 云主机部署指引

在 Linux 云主机上安装 `monitord`、确认它在采集，以及把 AI 模型接到这台机器。发布包里的同名文件是 `CLOUD-HOST.md`。

安装、升级和验收使用本文的命令。发布包内的 `monitord`、`monitor.example.yaml`、`start.sh` / `stop.sh` 已够完成安装。本文合入后的发布包会附带 `CLOUD-HOST.md`；更早的包没有这个文件，以仓库里的本文为准。云主机上不需要安装 Go 或 Node，也不需要克隆仓库。

完成标准：

- `curl http://127.0.0.1:19999/healthz` 返回 `ok`
- `GET /api/v1/info` 里的 `version` 与 `monitord -version` 一致
- `systemctl is-active monitord` 为 `active`（使用 systemd 时）
- 需要模型时，`GET /api/v1/ai/settings` 的 `enabled` 为 `true`，且 `has_api_key` 符合预期

## 选包

到 [GitHub Releases](https://github.com/mengzhihua/monitor/releases) 取**版本号最高**的稳定标签（`vMAJOR.MINOR.PATCH`）。当前仓库 `VERSION` 与该标签是 `v3.0.17`。页面上的 Latest 徽章可能落在后来补发的 `v3.0.16` 上，安装时以更高的版本号为准。服务端包名：

| `uname -m` | 文件 |
| --- | --- |
| `x86_64` | `monitord-<tag>-linux-amd64.tar.gz` |
| `aarch64` | `monitord-<tag>-linux-arm64.tar.gz` |

包内是静态二进制，附带示例配置、`README.md`、`plugins.d/`、`start.sh`、`stop.sh` 和 `CLOUD-HOST.md`。`SHA256SUMS.txt` 覆盖该次发布的全部文件。

## 单台云主机

监听只绑回环。操作员以后若要看页面，在这台机器前面加 HTTPS 反向代理，或经 SSH 隧道访问 `127.0.0.1:19999`。安全组不要对公网放行 `19999`。

```bash
set -euo pipefail
VER=v3.0.17   # 版本号最高的稳定标签；不要用页面上可能指向 v3.0.16 的 Latest 徽章
ARCH=linux-amd64   # aarch64 用 linux-arm64
cd /tmp
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/monitord-${VER}-${ARCH}.tar.gz"
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/SHA256SUMS.txt"
grep "monitord-${VER}-${ARCH}.tar.gz" SHA256SUMS.txt | sha256sum -c -
tar xzf "monitord-${VER}-${ARCH}.tar.gz"
cd "monitord-${VER}-${ARCH}"
./monitord -version

id monitor >/dev/null 2>&1 || useradd --system --home-dir /var/lib/monitor --create-home --shell /usr/sbin/nologin monitor
install -d -o monitor -g monitor -m 750 /var/lib/monitor /etc/monitor
install -m 755 monitord /usr/bin/monitord
```

没有 `useradd` 的发行版，用该系统创建用户的命令，家目录 `/var/lib/monitor`，登录 shell 为 `nologin`。

发布包不含 systemd 单元。把下面内容写入 `/etc/systemd/system/monitord.service`：

```ini
[Unit]
Description=Monitor agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/monitord -config /etc/monitor/monitor.yaml -data-dir /var/lib/monitor
Restart=on-failure
RestartSec=2
User=monitor
Group=monitor
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/monitor
WorkingDirectory=/var/lib/monitor

[Install]
WantedBy=multi-user.target
```

最小配置 `/etc/monitor/monitor.yaml`（`root:monitor`，`0640`）。`token` 留空时，首次启动在数据目录生成随机管理员密码。

```yaml
mode: agent
web:
  listen: "127.0.0.1:19999"
  token: ""
```

默认 TSDB 预算大约是 1GiB 原始块，再加上降采样。系统盘很小的云镜像把数据目录改到数据盘，并保证 `monitor` 用户可写，例如 `-data-dir /data/monitor`，同时把单元里的 `ReadWritePaths` 和 `WorkingDirectory` 改成同一路径。

```bash
install -m 640 -o root -g monitor /etc/monitor/monitor.yaml
systemctl daemon-reload
systemctl enable --now monitord
```

### 验收

```bash
set -euo pipefail
systemctl is-active monitord
curl -fsS http://127.0.0.1:19999/healthz
TOKEN=$(cat /var/lib/monitor/web-password)
curl -fsS -H "Authorization: Bearer ${TOKEN}" http://127.0.0.1:19999/api/v1/info
/usr/bin/monitord -version
```

`/healthz` 不需要密码。`/api/v1/info` 及之后的接口需要 `Authorization: Bearer`。密码文件权限是 `0600`，属主是 `monitor`；上面的 `cat` 用 root 执行。启动日志只打印密码文件路径，不打印密码。

采集起来之后可以列出图表 ID：

```bash
curl -fsS -H "Authorization: Bearer ${TOKEN}" http://127.0.0.1:19999/api/v1/charts \
  | python3 -c 'import json,sys; print("\n".join(sorted(json.load(sys.stdin)["charts"])))'
```

有 `nvidia-smi` 时会出现 `nvidia.*` 图表；没有 GPU 工具时该采集器自行停用，服务保持运行。

日志：

```bash
journalctl -u monitord -n 80 --no-pager
```

## 没有 systemd 的云镜像

在解压目录里后台启动。数据目录默认是脚本旁的 `data/`。

```bash
MONITOR_LISTEN=127.0.0.1:19999 ./start.sh --background
# 停止同一实例（只结束本目录启动的 monitord）
./stop.sh
```

密码在 `./data/web-password`。验收把上面的 `curl` 地址保持为 `127.0.0.1:19999`。

## 多台云主机：一台 Hub，其余 Agent

Hub 只监听 VPC 私网地址，安全组只允许 Agent 所在网段访问该端口。Agent 主动连出，Agent 自己的 `19999` 不必对 Hub 开放。

Hub `/etc/monitor/monitor.yaml`：

```yaml
mode: hub
web:
  listen: "10.0.0.10:19999"    # Hub 的私网地址
  allow_from: ["10.0.0.0/16", "127.0.0.1/32"]
  token: ""
hub:
  api_keys: ["换成每台环境独立的长随机串"]
```

Agent：

```yaml
mode: agent
web:
  listen: "127.0.0.1:19999"
  token: ""
stream:
  enabled: true
  destinations: ["10.0.0.10:19999"]
  api_key: "与 Hub hub.api_keys 中的一条相同"
```

在 Hub 上确认节点（把 `HUB` 换成 Hub 的私网地址，密码读 Hub 的数据目录）：

```bash
curl -fsS -H "Authorization: Bearer ${TOKEN}" "http://${HUB}:19999/api/v1/nodes"
```

`status` 为 `live` 即上报成功。每个 Agent 使用自己的 `api_key`，一台机器的密钥不能冒充另一台已注册节点。

## 在云主机上配置 AI 模型

模型请求由 **这台云主机上的 monitord** 发出。`127.0.0.1` 指云主机自己，不是操作员的笔记本。公网模型使用 `https://` 根地址（不要写成 `/chat/completions`）。本机的 Ollama、vLLM 等私网地址必须由管理员打开 `allow_private`。

密钥经 API 写入 `<data_dir>/operations/ai-settings.json`（`0600`）。响应只返回 `has_api_key`，不回显密钥。不要把密钥写进仓库、镜像或本文件。

```bash
set -euo pipefail
TOKEN=$(cat /var/lib/monitor/web-password)
BASE=http://127.0.0.1:19999
AUTH=(-H "Authorization: Bearer ${TOKEN}" -H "Content-Type: application/json")

# 第一次保存时 revision 为 0。已有配置时先 GET，使用返回的 revision。
curl -fsS "${AUTH[@]}" -X PUT "${BASE}/api/v1/ai/settings" \
  -d '{"base_url":"https://api.example.com/v1","model":"替换为模型 ID","api_key":"'"${MODEL_KEY}"'","allow_private":false,"revision":0}'

# 本机模型示例（仅管理员，且端口在这台云主机上）：
# -d '{"base_url":"http://127.0.0.1:11434/v1","model":"qwen2.5","api_key":"'"${MODEL_KEY}"'","allow_private":true,"revision":0}'

REV=$(curl -fsS -H "Authorization: Bearer ${TOKEN}" "${BASE}/api/v1/ai/settings" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["revision"])')

PREVIEW=$(curl -fsS "${AUTH[@]}" -X POST "${BASE}/api/v1/ai/preview" \
  -d '{"node":"local","charts":["system.cpu","system.ram"],"window":300}')
PID=$(printf '%s' "${PREVIEW}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')

curl -fsS "${AUTH[@]}" -X POST "${BASE}/api/v1/ai/analyze" \
  -d '{"preview_id":"'"${PID}"'","question":"概括这台云主机最近 5 分钟的 CPU 和内存，指出是否需要处理。","revision":'"${REV}"'}'
```

`window` 只能是 `60`、`300`、`900`、`3600`、`21600`、`86400`（秒）。图表 1 到 6 张，ID 来自 `/api/v1/charts`。预览 5 分钟内只能发送一次；`409` 表示预览过期或已用过，重新 `POST /api/v1/ai/preview`。服务不会执行模型给出的命令。

分析接口字段与行为的其余说明见 [AI 分析指南](17-ai-analysis.md)。

## 升级

保留 `/var/lib/monitor`（或实际 `-data-dir`）和 `/etc/monitor/monitor.yaml`。只替换二进制：

```bash
systemctl stop monitord
install -m 755 monitord /usr/bin/monitord
systemctl start monitord
curl -fsS http://127.0.0.1:19999/healthz
/usr/bin/monitord -version
```

新包仍用上一节的校验方式核对 `SHA256SUMS.txt`。

## 排错

| 现象 | 处理 |
| --- | --- |
| `healthz` 连接被拒绝 | `systemctl status monitord` 与 `journalctl -u monitord -n 80`。常见原因是配置 YAML 无效，或 `monitor` 用户不能读 `/etc/monitor/monitor.yaml`、不能写数据目录 |
| API `401` | Bearer 用数据目录里的 `web-password`。配置了 `web.token` 时用该值，此时不会生成密码文件 |
| API `403` 且来自非回环地址 | `web.allow_from` 未包含来源网段。空列表表示不按网段限制 |
| 密码文件找不到 | 路径是 `-data-dir`，systemd 单元是 `/var/lib/monitor/web-password`。`start.sh` 默认是脚本目录下的 `data/web-password` |
| Hub 上看不到 Agent | Agent 要能访问 Hub 的 `destinations` 地址；`api_key` 必须在 Hub 的 `hub.api_keys` 里。在 Agent 上查 `journalctl` 里的 stream 错误 |
| AI `502` / `504` | 在云主机上访问 `base_url`（不是在笔记本上）。公网地址用 HTTPS。私网模型需要管理员的 `allow_private: true` |
| AI `409` | `GET /api/v1/ai/settings` 取最新 `revision` 再保存；预览失败则重新生成预览 |
| 单元反复重启 | 数据目录磁盘满，或 `ProtectSystem` 下写入了 `ReadWritePaths` 以外的路径 |

重置自动生成的密码：`systemctl stop monitord`，删除数据目录中的 `web-password`，再启动并读取新文件。已设置 `web.token`、`web.users`、OIDC 或 LDAP 时，改对应配置，删文件不会重置那些凭据。
