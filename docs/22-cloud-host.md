# 云主机部署指引

在一台 Linux 云主机上安装 `monitord` v3.0.17，用本机 `curl` 确认它在采集，并按需把 AI 模型接到这台机器。发布包里的同名文件是 `CLOUD-HOST.md`（含本文的下一个版本才会打进压缩包；v3.0.16 / v3.0.17 的包里还没有）。

云主机只需要发布包里的静态二进制。不安装 Go / Node，不克隆仓库。验收在这台云主机上执行，看命令退出码和下面列出的字段。

## 固定值

| 项 | 值 |
| --- | --- |
| 版本 | `3.0.17`（`VERSION` 文件与标签 `v3.0.17`）。GitHub 页面的 Latest 徽章当前指向后来补发的 `v3.0.16`，下载时用版本号更高的 `v3.0.17` |
| 包 | `x86_64` → `monitord-v3.0.17-linux-amd64.tar.gz`；`aarch64` → `monitord-v3.0.17-linux-arm64.tar.gz` |
| 二进制 | `/usr/bin/monitord` |
| 配置 | `/etc/monitor/monitor.yaml`，属主 `root:monitor`，模式 `0640` |
| 数据目录 | `/var/lib/monitor` |
| 密码文件 | `/var/lib/monitor/web-password`，属主 `monitor`，模式 `0600` |
| systemd 单元 | `/etc/systemd/system/monitord.service` |
| 监听 | `127.0.0.1:19999`（只本机） |
| 运行用户 | `monitor`（系统用户，shell 为 `nologin`） |
| AI 配置文件 | `/var/lib/monitor/operations/ai-settings.json` |

`monitord -version` 打印一行：`monitord 3.0.17 linux/amd64` 或 `monitord 3.0.17 linux/arm64`。`/api/v1/info` 的 `version` 是 `3.0.17`，没有 `monitord` 前缀。

密码文件是 32 字节随机数的 base64（Raw URL，无 `=` 填充），共 43 个字符，文件末尾有一个换行。Bearer 使用去掉首尾空白后的那 43 个字符。启动日志不打印密码，只打印：

```text
level=WARN msg="dashboard login required; read the password file on this server" password_file=/var/lib/monitor/web-password
level=INFO msg="web server listening" addr=127.0.0.1:19999
```

安全组入方向保留 TCP 22（运维网段）。不添加入方向 TCP 19999 来自 `0.0.0.0/0`。人要看页面时，在自己的电脑上执行 `ssh -N -L 19999:127.0.0.1:19999 <用户>@<云主机>`，浏览器打开 `http://127.0.0.1:19999/`。安装是否成功以云主机上的 `curl` 为准。

前提：root、`bash`、`curl`、`tar`、`sha256sum`、`systemctl`、`python3`。缺 python3 时：Debian/Ubuntu 用 `apt-get update && apt-get install -y python3`；RHEL/CentOS/Alibaba Cloud Linux 用 `dnf install -y python3`。

数据目录所在文件系统至少留 4GiB。默认原始块预算 `db.tier0_retention_size` 是 1GiB，另有检查点和降采样。根分区小于这个数时，先把数据目录改到数据盘再启动，见下文「数据盘」。

## 单台安装

整段以 root 执行。配置文件已存在时脚本退出，避免覆盖现有密码策略。

```bash
set -euo pipefail
test "$(id -u)" = 0
command -v curl tar sha256sum systemctl python3 >/dev/null

VER=v3.0.17
case "$(uname -m)" in
  x86_64) ARCH=linux-amd64 ;;
  aarch64|arm64) ARCH=linux-arm64 ;;
  *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac
if [ -f /etc/monitor/monitor.yaml ]; then
  echo "already configured: /etc/monitor/monitor.yaml" >&2
  exit 1
fi

WORKDIR=/tmp/monitord-${VER}
rm -rf "$WORKDIR"
mkdir -p "$WORKDIR"
cd "$WORKDIR"
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/monitord-${VER}-${ARCH}.tar.gz"
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/SHA256SUMS.txt"
grep "monitord-${VER}-${ARCH}.tar.gz" SHA256SUMS.txt | sha256sum -c -
tar xzf "monitord-${VER}-${ARCH}.tar.gz"
cd "monitord-${VER}-${ARCH}"
got=$(./monitord -version)
case "$ARCH" in
  linux-amd64) expect="monitord 3.0.17 linux/amd64" ;;
  linux-arm64) expect="monitord 3.0.17 linux/arm64" ;;
esac
test "$got" = "$expect"

id monitor >/dev/null 2>&1 || useradd --system --home-dir /var/lib/monitor --create-home --shell /usr/sbin/nologin monitor
install -d -o monitor -g monitor -m 750 /var/lib/monitor
install -d -o root -g monitor -m 750 /etc/monitor
install -m 755 monitord /usr/bin/monitord

cat > /etc/systemd/system/monitord.service <<'EOF'
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
EOF

cat > /etc/monitor/monitor.yaml <<'EOF'
mode: agent
web:
  listen: "127.0.0.1:19999"
  token: ""
EOF
chown root:monitor /etc/monitor/monitor.yaml
chmod 640 /etc/monitor/monitor.yaml

systemctl daemon-reload
systemctl enable --now monitord
```

`sha256sum -c -` 成功时打印 `monitord-v3.0.17-<arch>.tar.gz: OK`。

### 验收

启动后等采集器登记图表，最多 20 秒。失败时脚本打印 `journalctl` 最后 40 行并退出非 0。

```bash
set -euo pipefail
systemctl is-active monitord
ok=
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
  if curl -fsS http://127.0.0.1:19999/healthz | grep -qx 'ok'; then ok=1; break; fi
  sleep 1
done
test -n "$ok"
TOKEN=$(tr -d '[:space:]' < /var/lib/monitor/web-password)
test "${#TOKEN}" -eq 43
python3 - "$TOKEN" <<'PY'
import base64, json, sys, time, urllib.request
token = sys.argv[1]
raw = base64.urlsafe_b64decode(token + "=" * ((4 - len(token) % 4) % 4))
assert len(raw) == 32 and base64.urlsafe_b64encode(raw).decode().rstrip("=") == token

def get(path):
    req = urllib.request.Request("http://127.0.0.1:19999" + path, headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 200
        return json.load(resp)

info = get("/api/v1/info")
assert info["version"] == "3.0.17", info["version"]
assert info["mode"] == "agent"
assert info["db"]["dir"] == "/var/lib/monitor"

charts = data = None
for _ in range(15):
    info = get("/api/v1/info")
    charts = get("/api/v1/charts")["charts"]
    if "system.cpu" in charts and "system.ram" in charts and info["charts_count"] >= 2:
        data = get("/api/v1/data?chart=system.cpu&after=-30&points=5")
        if data["id"] == "system.cpu" and data["points"] >= 1 and len(data["result"]["data"]) >= 1:
            break
    time.sleep(1)
else:
    raise SystemExit("system.cpu/system.ram have no samples yet")
print("ok", info["version"], "charts", info["charts_count"], "cpu_points", data["points"])
PY
```

成功时最后一行类似 `ok 3.0.17 charts 80 cpu_points 5`。`charts` 数量随内核和已安装的工具变化，`system.cpu` 与 `system.ram` 在 Linux 上会有。有 `nvidia-smi` 时图表键里还会出现 `nvidia.` 前缀；没有 GPU 工具时该采集器自行停用，`systemctl is-active` 仍是 `active`。

日志：

```bash
journalctl -u monitord -n 40 --no-pager
```

其中应能看到 `password_file=/var/lib/monitor/web-password` 和 `addr=127.0.0.1:19999`。

### 数据盘

根分区可用空间不足 4GiB、且 `/data` 是独立挂载点时，在写入单元和配置之前改这三处，其余命令不变：

- 数据目录用 `/data/monitor`，`install -d -o monitor -g monitor -m 750 /data/monitor`
- 单元里 `ExecStart` 的 `-data-dir`、`ReadWritePaths`、`WorkingDirectory` 都写成 `/data/monitor`
- 验收里 `info["db"]["dir"]` 改为 `/data/monitor`，密码文件改为 `/data/monitor/web-password`

`monitor` 用户必须是该目录的属主。单元的 `ProtectSystem=strict` 只允许写入 `ReadWritePaths` 列出的路径。

## 没有 systemd

在解压目录执行。不设 `MONITOR_LISTEN` 时，脚本监听 `127.0.0.1:20099`，数据目录是 `./data`。下面显式改成与 systemd 相同的端口，避免和文档里的 `19999` 验收不一致。

```bash
cd /tmp/monitord-v3.0.17/monitord-v3.0.17-linux-amd64   # arm64 包改目录名
MONITOR_LISTEN=127.0.0.1:19999 ./start.sh --background
```

成功时标准输出两行：一行 `started pid <pid> (pidfile .../data/monitord.pid, log .../data/monitord.log)`，以及脚本提示的 `Open http://127.0.0.1:19999/ ; password: .../data/web-password`。验收把上一节 `curl` 的密码文件换成该 `password:` 路径。停止用同一目录的 `./stop.sh`（只结束 pidfile 里、且命令行带这个 `-data-dir` 的 `monitord`）。

## 多台：一台 Hub，其余 Agent

示例地址：Hub `10.0.0.10`，一台 Agent `10.0.0.11`。Hub 安全组入方向 TCP `19999` 只放行 Agent 子网（示例 `10.0.0.0/24`）。Agent 主动连出，Agent 的 `19999` 不必对 Hub 开放。两台都先按「单台安装」装好二进制和用户，再换成各自的配置并重启。

在 Hub 上生成一把 48 位十六进制密钥，只写入两台机器的 `monitor.yaml`：

```bash
openssl rand -hex 24
```

Hub `/etc/monitor/monitor.yaml`：

```yaml
mode: hub
web:
  listen: "10.0.0.10:19999"
  allow_from: ["10.0.0.0/24", "127.0.0.1/32"]
  token: ""
hub:
  api_keys: ["这里换成上一行生成的 48 位十六进制"]
```

Agent `/etc/monitor/monitor.yaml`：

```yaml
mode: agent
web:
  listen: "127.0.0.1:19999"
  token: ""
stream:
  enabled: true
  destinations: ["10.0.0.10:19999"]
  api_key: "与 Hub hub.api_keys 中这一条相同"
```

两台都执行 `chown root:monitor`、`chmod 640`，然后 `systemctl restart monitord`。每台 Agent 使用自己的 `api_key`；一把密钥注册过某台机器之后，另一台不能用同一把密钥顶替那个节点 ID。

Hub 上确认（`TOKEN` 读 Hub 的 `/var/lib/monitor/web-password`）。Agent 连上通常在数秒内，下面最多等 30 秒：

```bash
set -euo pipefail
TOKEN=$(tr -d '[:space:]' < /var/lib/monitor/web-password)
python3 - "$TOKEN" <<'PY'
import json, sys, time, urllib.request
token = sys.argv[1]
def get(url):
    req = urllib.request.Request(url, headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.load(resp)
found = None
for _ in range(30):
    body = get("http://10.0.0.10:19999/api/v1/nodes")
    remotes = [n for n in body["nodes"] if not n.get("local")]
    live = [n for n in remotes if n.get("status") == "live"]
    if live:
        found = live[0]
        break
    time.sleep(1)
assert found is not None, "no live remote node"
assert found["os"] == "linux"
assert found["charts_count"] >= 2
print("live", found["hostname"], found["id"], "charts", found["charts_count"])
PY
```

Agent 上确认上报（密码是 Agent 自己的 `web-password`）。最多等 30 秒：

```bash
TOKEN=$(tr -d '[:space:]' < /var/lib/monitor/web-password)
python3 - "$TOKEN" <<'PY'
import json, sys, time, urllib.request
token = sys.argv[1]
want = "ws://10.0.0.10:19999/api/v1/stream"
for _ in range(30):
    req = urllib.request.Request("http://127.0.0.1:19999/api/v1/info", headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=10) as resp:
        stream = json.load(resp)["stream"]
    if stream.get("enabled") and stream.get("connected") and stream.get("destination") == want:
        print("connected", stream["destination"], "sent", stream["sent"])
        break
    time.sleep(1)
else:
    raise SystemExit("stream last_error=" + str(stream.get("last_error")))
PY
```

`destination` 固定为 `ws://10.0.0.10:19999/api/v1/stream`（配置里写的是 `10.0.0.10:19999`，状态里会补上 `ws://` 和路径 `/api/v1/stream`）。连接刚建立时 `sent` 可以是 0，几秒后再查应大于 0。`last_error` 在已连接时不出现。

## 配置 AI 模型

模型 HTTP 从这台云主机的 `monitord` 进程发出。`127.0.0.1` 是云主机本机。公网地址用 `https://` 开头的 API 根（到 `/v1` 为止）。本机 Ollama / vLLM 使用 `http://127.0.0.1:<端口>/v1`，并且 `allow_private` 为 `true`（调用身份须是管理员；`web-password` 和空 `token` 生成的密码都是管理员）。

密钥写入 `/var/lib/monitor/operations/ai-settings.json`（模式 `0600`）。`GET /api/v1/ai/settings` 返回 `has_api_key`，不返回密钥原文。

未配置时 GET 的形状：

```json
{"base_url":"","model":"","has_api_key":false,"allow_private":false,"revision":0,"enabled":false,"persistent":true}
```

### 公网模型

在云主机上导出三个变量后再执行（示例是 OpenAI 兼容根地址，模型 ID 换成账号实际可用的）：

```bash
export MODEL_BASE=https://api.openai.com/v1
export MODEL_NAME=gpt-4o-mini
export MODEL_KEY='密钥只放在这个环境变量里'
test -n "$MODEL_BASE" && test -n "$MODEL_NAME" && test -n "$MODEL_KEY"
TOKEN=$(tr -d '[:space:]' < /var/lib/monitor/web-password)
curl -fsS -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -X PUT http://127.0.0.1:19999/api/v1/ai/settings \
  -d '{"base_url":"'"${MODEL_BASE}"'","model":"'"${MODEL_NAME}"'","api_key":"'"${MODEL_KEY}"'","allow_private":false,"revision":0}' \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["enabled"] is True and d["has_api_key"] is True and d["allow_private"] is False and d["revision"]==1 and d["base_url"]==sys.argv[1] and d["model"]==sys.argv[2]; print("saved", d["revision"], d["model"])' "$MODEL_BASE" "$MODEL_NAME"
```

再次修改时先 GET，把返回的 `revision` 放进 PUT。改 `base_url` 时要重新提交 `api_key`，或同时传 `"clear_api_key": true`。

### 本机 Ollama

先确认模型进程就在这台云主机上，再打开私网访问：

```bash
curl -fsS http://127.0.0.1:11434/api/tags | python3 -c 'import json,sys; names=[m["name"] for m in json.load(sys.stdin).get("models",[])]; print("\n".join(names)); assert names'
```

把打印出的一个名字填到 `MODEL_NAME`（例如 `qwen2.5:7b`）。Ollama 的 OpenAI 兼容根是 `http://127.0.0.1:11434/v1`。无密钥时 `api_key` 传空字符串：

```bash
export MODEL_BASE=http://127.0.0.1:11434/v1
export MODEL_NAME=qwen2.5:7b
TOKEN=$(tr -d '[:space:]' < /var/lib/monitor/web-password)
curl -fsS -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -X PUT http://127.0.0.1:19999/api/v1/ai/settings \
  -d '{"base_url":"'"${MODEL_BASE}"'","model":"'"${MODEL_NAME}"'","api_key":"","allow_private":true,"revision":0}' \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["enabled"] and d["allow_private"] and d["base_url"].startswith("http://127.0.0.1:"); print("saved", d["model"], "revision", d["revision"])'
```

### 发一次分析

`window` 只能是 `60`、`300`、`900`、`3600`、`21600`、`86400`。图表 1 到 6 个，ID 必须出现在 `/api/v1/charts` 的键里。预览返回的 `id` 是 48 位十六进制，5 分钟内只能用于一次 `analyze`。模型调用最长 60 秒，`curl` 超时设为 70 秒。

```bash
set -euo pipefail
TOKEN=$(tr -d '[:space:]' < /var/lib/monitor/web-password)
REV=$(curl -fsS -H "Authorization: Bearer ${TOKEN}" http://127.0.0.1:19999/api/v1/ai/settings \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["revision"])')
curl -fsS -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -X POST http://127.0.0.1:19999/api/v1/ai/preview \
  -d '{"node":"local","charts":["system.cpu","system.ram"],"window":300}' \
  > /tmp/monitor-ai-preview.json
python3 - <<'PY'
import json
d=json.load(open("/tmp/monitor-ai-preview.json"))
assert len(d["id"])==48 and len(d["snapshot"]["charts"])==2
print("preview", d["id"], "hostname", d["snapshot"]["hostname"])
PY
PID=$(python3 -c 'import json; print(json.load(open("/tmp/monitor-ai-preview.json"))["id"])')
curl -fsS --max-time 70 -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -X POST http://127.0.0.1:19999/api/v1/ai/analyze \
  -d '{"preview_id":"'"${PID}"'","question":"概括这台云主机最近 5 分钟的 CPU 和内存，指出是否需要处理。","revision":'"${REV}"'}' \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); assert isinstance(d["text"], str) and d["text"].strip(); assert d["model"] and d["generated_at"]>0; print("analyzed", d["model"], "chars", len(d["text"]), "ms", d["duration_ms"])'
```

成功时打印 `analyzed <模型名> chars <正整数> ms <耗时>`。`text` 是模型原文。monitord 不执行其中的命令。同一份预览再 POST analyze 会得到 HTTP 409，正文含 `preview expired or already used`。

字段和限额的其余说明见 [AI 分析指南](17-ai-analysis.md)。

## 升级

保留 `/var/lib/monitor` 和 `/etc/monitor/monitor.yaml`。只替换 `/usr/bin/monitord`。把 `VER` 换成新的 `vMAJOR.MINOR.PATCH`，并把下面两处 `3.0.17` 换成该版本号：

```bash
set -euo pipefail
VER=v3.0.18
case "$(uname -m)" in
  x86_64) ARCH=linux-amd64; EXPECT="monitord ${VER#v} linux/amd64" ;;
  aarch64|arm64) ARCH=linux-arm64; EXPECT="monitord ${VER#v} linux/arm64" ;;
  *) exit 1 ;;
esac
cd /tmp
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/monitord-${VER}-${ARCH}.tar.gz"
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/SHA256SUMS.txt"
grep "monitord-${VER}-${ARCH}.tar.gz" SHA256SUMS.txt | sha256sum -c -
rm -rf "unpack-${VER}" && mkdir "unpack-${VER}" && tar xzf "monitord-${VER}-${ARCH}.tar.gz" -C "unpack-${VER}"
BIN=$(echo unpack-${VER}/monitord-${VER}-${ARCH}/monitord)
test "$($BIN -version)" = "$EXPECT"
systemctl stop monitord
install -m 755 "$BIN" /usr/bin/monitord
systemctl start monitord
curl -fsS http://127.0.0.1:19999/healthz | grep -qx ok
test "$(/usr/bin/monitord -version)" = "$EXPECT"
```

## 排错

| 现象 | 含义与处理 |
| --- | --- |
| `sha256sum: ... FAILED` | 包或 `SHA256SUMS.txt` 不完整。删掉 `/tmp` 里这两个文件后重新 `curl` |
| `healthz` 连接被拒绝 | `systemctl status monitord`。单元若是 `activating` 后失败，看 `journalctl -u monitord -n 80 --no-pager`。配置读不了时日志含 `open /etc/monitor/monitor.yaml`；数据目录不可写时含 `/var/lib/monitor` |
| 日志有 `password_file=` 但 `curl` 401 | Bearer 用 `tr -d '[:space:]'` 之后的 43 字符。配置里写了非空 `web.token` 时不会生成 `web-password`，Bearer 用配置中的 token |
| 从别的机器访问得到 403，正文 `forbidden` | 来源不在 `web.allow_from`。单机配置只监听 `127.0.0.1`，其他机器本来就连不上 |
| Hub 30 秒内没有 `status=live` | 在 Agent 上查 `/api/v1/info` 的 `stream.connected` 和 `stream.last_error`。再在 Agent 上执行 `timeout 3 bash -c 'echo >/dev/tcp/10.0.0.10/19999'`，退出码 0 表示 TCP 到达 Hub 的 19999 |
| AI HTTP 502，正文含 provider / connection | 在云主机上 `curl -m 10 -sv "$MODEL_BASE/models"`（或 Ollama 的 `/api/tags`）。公网模型的 `base_url` 以 `https://` 开头。本机模型须 `allow_private: true` |
| AI HTTP 504 | 模型超过 60 秒。服务不自动重试，重新走 preview 再 analyze |
| AI HTTP 409 | 保存配置时 GET 最新 `revision`。分析时重新 POST preview，使用新的 48 位 `id` |
| 单元每隔约 2 秒重启 | `df -B1 /var/lib/monitor` 看剩余空间，或确认没有把数据写到 `ReadWritePaths` 以外 |

重置自动生成的密码：`systemctl stop monitord`，删除数据目录中的 `web-password`，再 `systemctl start monitord`，读取新的 43 字符。配置了 `web.token`、`web.users`、OIDC 或 LDAP 时，改对应项；删除 `web-password` 不会重置那些凭据。
