# 云主机部署指引

在 Linux 云主机上安装 `monitord`、确认它在采集，并把 AI 模型接到这台机器。发布包里与本文对应的文件是 `CLOUD-HOST.md`，旁边还有可执行的 `cloud-host.sh` 和 `monitord.service`。仓库里的脚本是 [packaging/cloud-host.sh](../packaging/cloud-host.sh)。

按本机架构下载并校验后，进入解压目录，以 root 执行安装：

```bash
case "$(uname -m)" in
  x86_64) ARCH=linux-amd64 ;;
  aarch64|arm64) ARCH=linux-arm64 ;;
  *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac
VER=v3.0.17
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/monitord-${VER}-${ARCH}.tar.gz"
curl -fsSL -O "https://github.com/mengzhihua/monitor/releases/download/${VER}/SHA256SUMS.txt"
grep "monitord-${VER}-${ARCH}.tar.gz" SHA256SUMS.txt | sha256sum -c -
tar xzf "monitord-${VER}-${ARCH}.tar.gz"
cd "monitord-${VER}-${ARCH}"
sudo ./cloud-host.sh install --check
```

成功时先打印一行 `installed`，再打印一行：

```text
ok version=3.0.17 mode=agent dir=/var/lib/monitor/db charts=<N> cpu_points=<M>
```

`version` 与 `./monitord -version` 的第二段相同（发布包是 `3.0.17`）。`cpu_points` 至少为 1。验收只看这两行和退出码 0。`sha256sum -c -` 成功时打印 `monitord-v3.0.17-linux-amd64.tar.gz: OK`（arm64 包名对应替换）。

当前仓库 `VERSION` 与应下载的标签是 `v3.0.17`。GitHub 页面的 Latest 徽章可能仍指向后来补发的 `v3.0.16`，下载版本号更高的 `v3.0.17`。

## 固定路径

| 项 | 值 |
| --- | --- |
| 二进制 | `/usr/bin/monitord` |
| 配置 | `/etc/monitor/monitor.yaml`，`root:monitor`，`0640` |
| 数据目录 | `/var/lib/monitor` |
| 密码 | `/var/lib/monitor/web-password`，`monitor:monitor`，`0600` |
| 单元 | `/etc/systemd/system/monitord.service` |
| 监听 | `127.0.0.1:19999` |
| AI 配置 | `/var/lib/monitor/operations/ai-settings.json`，`0600` |

密码是 32 字节的 base64（Raw URL，无 `=`），共 43 个字符，文件末尾一个换行。Bearer 使用去掉空白后的 43 个字符。日志里有 `password_file=/var/lib/monitor/web-password` 和 `addr=127.0.0.1:19999`，没有密码正文。

安全组入方向保留 TCP 22。不放行来自 `0.0.0.0/0` 的 TCP 19999。数据目录所在文件系统至少留 4GiB（原始块预算 1GiB，另有检查点和降采样）。

配置已存在时 `install` 退出码为 1，stderr 含 `already configured: /etc/monitor/monitor.yaml`。覆盖时加 `MONITOR_REPLACE_CONFIG=1`。

## 数据盘

根分区可用空间不足 4GiB 时，把数据目录指到数据盘。单元里的 `-data-dir`、`ReadWritePaths`、`WorkingDirectory` 会写成同一路径。

```bash
sudo MONITOR_DATA_DIR=/data/monitor ./cloud-host.sh install --check
```

成功行中的 `dir=/data/monitor/db`。密码仍在 `/data/monitor/web-password`。

## 没有 systemd

`start.sh` 默认监听 `127.0.0.1:20099`。与上面的验收端口对齐时显式指定：

```bash
MONITOR_LISTEN=127.0.0.1:19999 ./start.sh --background
```

标准输出包含：

```text
health: curl -fsS http://127.0.0.1:19999/healthz
password: /解压目录/data/web-password
started pid <pid> (pidfile /解压目录/data/monitord.pid, log /解压目录/data/monitord.log)
```

然后：

```bash
./cloud-host.sh check --url http://127.0.0.1:19999 --password-file "$PWD/data/web-password" --data-dir "$PWD/data"
```

停止用同一目录的 `./stop.sh`。

## 多台：Hub 与 Agent

示例：Hub `10.0.0.10`，Agent `10.0.0.11`。Hub 安全组入方向 TCP 19999 只放行 `10.0.0.0/24`。在 Hub 上生成密钥（48 位十六进制），两台使用同一把；每台 Agent 应使用自己的密钥，一把密钥注册过节点后不能被另一台顶替。

```bash
openssl rand -hex 24
```

Hub：

```bash
sudo \
  MONITOR_MODE=hub \
  MONITOR_LISTEN=10.0.0.10:19999 \
  MONITOR_ALLOW_FROM=10.0.0.0/24,127.0.0.1/32 \
  MONITOR_STREAM_KEY=<48位十六进制> \
  ./cloud-host.sh install --check
```

`install` 写成的 `monitor.yaml` 含 `mode: hub`、`listen: "10.0.0.10:19999"`、两条 `allow_from` 和 `hub.api_keys`。密钥不是 48 位小写十六进制时退出码为 1，stderr 含 `48 hex`。

Agent：

```bash
sudo \
  MONITOR_MODE=agent \
  MONITOR_LISTEN=127.0.0.1:19999 \
  MONITOR_STREAM_DEST=10.0.0.10:19999 \
  MONITOR_STREAM_KEY=<与 Hub 的这一条相同> \
  ./cloud-host.sh install --check
```

Agent 的成功行额外包含：

```text
stream=ws://10.0.0.10:19999/api/v1/stream sent=<N>
```

配置里写的是 `10.0.0.10:19999`，状态里补成 `ws://` 加路径 `/api/v1/stream`。30 秒内未连上时 stderr 含 `stream want` 和 `last_error=`。在 Agent 上用 `timeout 3 bash -c 'echo >/dev/tcp/10.0.0.10/19999'` 看 TCP，退出码 0 表示端口可达。

Hub 上查看远端节点（密码读 Hub 的 `web-password`）：

```bash
TOKEN=$(tr -d '[:space:]' < /var/lib/monitor/web-password)
curl -fsS -H "Authorization: Bearer ${TOKEN}" http://10.0.0.10:19999/api/v1/nodes
```

远端对象的 `local` 为 false、`status` 为 `live`、`charts_count` 至少为 2。

## AI 模型

请求从云主机上的 `monitord` 发出。`127.0.0.1` 是这台云主机。

公网模型（`MODEL_BASE` 必须是 `https://` 根，不能写成 `/chat/completions`）：

```bash
sudo \
  MODEL_BASE=https://api.openai.com/v1 \
  MODEL_NAME=gpt-4o-mini \
  MODEL_KEY='密钥只放在这个环境变量里' \
  ./cloud-host.sh ai
```

成功行：

```text
saved revision=1 model=gpt-4o-mini base=https://api.openai.com/v1 private=false
```

本机 Ollama 先确认 `curl -fsS http://127.0.0.1:11434/api/tags` 返回的 `models[].name`，再：

```bash
sudo \
  MODEL_BASE=http://127.0.0.1:11434/v1 \
  MODEL_NAME=qwen2.5:7b \
  MODEL_KEY= \
  MONITOR_ALLOW_PRIVATE=true \
  ./cloud-host.sh ai --analyze
```

`http://` 且未设置 `MONITOR_ALLOW_PRIVATE=true` 时退出码为 1，stderr 含 `MONITOR_ALLOW_PRIVATE=true`。`--analyze` 再多一行 `analyzed model=<名称> chars=<正整数> ms=<耗时>`。预览 `id` 为 48 位十六进制，图表是 `system.cpu` 和 `system.ram`，`window` 为 300 秒。同一预览不能发第二次。

未配置时 `GET /api/v1/ai/settings` 为 `revision=0`、`enabled=false`、`has_api_key=false`、`persistent=true`。保存后响应不回显密钥，只给出 `has_api_key=true`。

## 升级

保留数据目录和 `monitor.yaml`，只换二进制：

```bash
sudo ./cloud-host.sh upgrade --binary ./monitord
./cloud-host.sh check
```

`upgraded binary=/usr/bin/monitord` 之后，`check` 的 `version=` 应等于新二进制 `monitord -version` 的第二段。

## 排错

| 退出信息 | 处理 |
| --- | --- |
| `sha256sum` 行不是 `OK` | 重新下载包和 `SHA256SUMS.txt` |
| `already configured` | 沿用现有配置，或确认后加 `MONITOR_REPLACE_CONFIG=1` |
| `healthz did not return ok` | `systemctl status monitord` 与 `journalctl -u monitord -n 40 --no-pager` |
| `password length` 不是 43 | 这台机器配置了自定义 `web.token`，Bearer 用配置里的值；自动密码才是 43 字符 |
| `info.version` 与 `-version` 不一致 | `upgrade` 之后没有重启到新二进制 |
| `system.cpu/system.ram have no samples` | 再执行一次 `./cloud-host.sh check`。仍失败时看采集器是否被配置成空列表 |
| `stream want ... last_error=` | 核对 `MONITOR_STREAM_KEY` 与 Hub 的 `hub.api_keys`，以及 Agent 到 Hub 的 TCP 19999 |
| `MODEL_BASE must be the API root` | 去掉末尾的 `/chat/completions` |
| AI HTTP 502 / 504 | 在云主机上访问 `MODEL_BASE`。504 表示模型超过 60 秒，重新执行 `ai --analyze` |
| AI HTTP 409 | 再执行一次 `./cloud-host.sh ai`，脚本会先 GET 当前 `revision` |
