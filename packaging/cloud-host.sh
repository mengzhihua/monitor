#!/bin/bash
# Install, check, and point AI at monitord on a Linux cloud VM.
# Run from an unpacked server release (monitord sits next to this script):
#   sudo ./cloud-host.sh install --check
set -euo pipefail

die() { printf '%s\n' "cloud-host: $*" >&2; exit 1; }

usage() {
	cat <<'EOF'
Usage:
  cloud-host.sh install [--check]
  cloud-host.sh check
  cloud-host.sh ai [--analyze]
  cloud-host.sh upgrade --binary PATH

Paths (override the prefix with MONITOR_ROOT in tests):
  /usr/bin/monitord
  /etc/monitor/monitor.yaml          root:monitor 0640
  /var/lib/monitor                    password: web-password
  /etc/systemd/system/monitord.service

Environment:
  MONITOR_BIN            binary to copy (default: ./monitord, else /usr/bin/monitord)
  MONITOR_MODE           agent (default) or hub
  MONITOR_LISTEN         default 127.0.0.1:19999
  MONITOR_DATA_DIR       default /var/lib/monitor
  MONITOR_ALLOW_FROM     comma-separated CIDRs; required for hub
  MONITOR_STREAM_KEY     48 hex chars from: openssl rand -hex 24
  MONITOR_STREAM_DEST    agent upstream host:port, e.g. 10.0.0.10:19999
  MONITOR_REPLACE_CONFIG 1 overwrites an existing monitor.yaml
  MONITOR_ROOT           write under this prefix and skip useradd/systemctl
  MODEL_BASE MODEL_NAME MODEL_KEY
  MONITOR_ALLOW_PRIVATE  true for an on-host http://127.0.0.1 model
EOF
}

ROOT_PREFIX=${MONITOR_ROOT:-}
MODE=${MONITOR_MODE:-agent}
LISTEN=${MONITOR_LISTEN:-127.0.0.1:19999}
STREAM_KEY=${MONITOR_STREAM_KEY:-}
STREAM_DEST=${MONITOR_STREAM_DEST:-}
ALLOW_FROM=${MONITOR_ALLOW_FROM:-}
REPLACE=${MONITOR_REPLACE_CONFIG:-0}
DO_CHECK=0
DO_ANALYZE=0
BINARY_ARG=

case "${1:-}" in
install | check | ai | upgrade | -h | --help) cmd=$1; shift ;;
*) usage >&2; die "unknown command: ${1:-}" ;;
esac
if [ "$cmd" = "-h" ] || [ "$cmd" = "--help" ]; then
	usage
	exit 0
fi

while [ $# -gt 0 ]; do
	case "$1" in
	--check) DO_CHECK=1 ;;
	--analyze) DO_ANALYZE=1 ;;
	--binary)
		[ $# -ge 2 ] || die "--binary needs a path"
		BINARY_ARG=$2
		shift
		;;
	--url)
		[ $# -ge 2 ] || die "--url needs a value"
		MONITOR_URL=$2
		shift
		;;
	--password-file)
		[ $# -ge 2 ] || die "--password-file needs a path"
		MONITOR_PASSWORD_FILE=$2
		shift
		;;
	--data-dir)
		[ $# -ge 2 ] || die "--data-dir needs a path"
		MONITOR_DATA_DIR=$2
		shift
		;;
	--mode)
		[ $# -ge 2 ] || die "--mode needs agent or hub"
		MODE=$2
		shift
		;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
	shift
done

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if [ -n "${MONITOR_DATA_DIR:-}" ]; then
	DATA=$MONITOR_DATA_DIR
elif [ -n "$ROOT_PREFIX" ]; then
	DATA=$ROOT_PREFIX/var/lib/monitor
else
	DATA=/var/lib/monitor
fi

pfx() { printf '%s%s\n' "$ROOT_PREFIX" "$1"; }
BIN_DEST=$(pfx /usr/bin/monitord)
CFG=$(pfx /etc/monitor/monitor.yaml)
UNIT=$(pfx /etc/systemd/system/monitord.service)
CFG_DIR=$(dirname -- "$CFG")
UNIT_DIR=$(dirname -- "$UNIT")

valid_listen() {
	printf '%s' "$1" | grep -Eq '^[A-Za-z0-9._-]+:[0-9]{1,5}$'
}
valid_path() {
	printf '%s' "$1" | grep -Eq '^/[A-Za-z0-9._/-]+$'
}
valid_key() {
	printf '%s' "$1" | grep -Eq '^[0-9a-f]{48}$'
}

resolve_source_bin() {
	if [ -n "$BINARY_ARG" ]; then
		printf '%s\n' "$BINARY_ARG"
		return
	fi
	if [ -n "${MONITOR_BIN:-}" ]; then
		printf '%s\n' "$MONITOR_BIN"
		return
	fi
	if [ -x "$SCRIPT_DIR/monitord" ]; then
		printf '%s\n' "$SCRIPT_DIR/monitord"
		return
	fi
	if [ -x /usr/bin/monitord ]; then
		printf '%s\n' /usr/bin/monitord
		return
	fi
	die "monitord binary not found (set MONITOR_BIN or unpack the release next to this script)"
}

write_config() {
	valid_listen "$LISTEN" || die "bad MONITOR_LISTEN: $LISTEN"
	case "$MODE" in
	agent | hub) ;;
	*) die "MONITOR_MODE must be agent or hub" ;;
	esac
	local allow_yaml="" cidr
	if [ "$MODE" = "hub" ]; then
		[ -n "$ALLOW_FROM" ] || die "hub requires MONITOR_ALLOW_FROM (example: 10.0.0.0/24,127.0.0.1/32)"
		valid_key "$STREAM_KEY" || die "hub requires MONITOR_STREAM_KEY: 48 hex chars from openssl rand -hex 24"
		local IFS=','
		for cidr in $ALLOW_FROM; do
			cidr=${cidr// /}
			printf '%s' "$cidr" | grep -Eq '^[0-9A-Fa-f.:/]+$' || die "bad CIDR: $cidr"
			allow_yaml+="    - \"$cidr\"
"
		done
		cat >"$CFG" <<EOF
mode: hub
web:
  listen: "$LISTEN"
  allow_from:
$allow_yaml  token: ""
hub:
  api_keys: ["$STREAM_KEY"]
EOF
		return
	fi
	if [ -n "$STREAM_DEST" ]; then
		valid_listen "$STREAM_DEST" || die "bad MONITOR_STREAM_DEST: $STREAM_DEST"
		valid_key "$STREAM_KEY" || die "agent stream requires MONITOR_STREAM_KEY (48 hex chars)"
		cat >"$CFG" <<EOF
mode: agent
web:
  listen: "$LISTEN"
  token: ""
stream:
  enabled: true
  destinations: ["$STREAM_DEST"]
  api_key: "$STREAM_KEY"
EOF
		return
	fi
	cat >"$CFG" <<EOF
mode: agent
web:
  listen: "$LISTEN"
  token: ""
EOF
}

write_unit() {
	valid_path "$DATA" || die "bad data dir: $DATA"
	cat >"$UNIT" <<EOF
[Unit]
Description=Monitor agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/monitord -config /etc/monitor/monitor.yaml -data-dir $DATA
Restart=on-failure
RestartSec=2
User=monitor
Group=monitor
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$DATA
WorkingDirectory=$DATA

[Install]
WantedBy=multi-user.target
EOF
}

ensure_user() {
	if [ -n "$ROOT_PREFIX" ]; then
		return
	fi
	[ "$(id -u)" = 0 ] || die "install as root"
	local nologin=/usr/sbin/nologin
	[ -x "$nologin" ] || nologin=/sbin/nologin
	getent group monitor >/dev/null 2>&1 || groupadd --system monitor
	if ! id monitor >/dev/null 2>&1; then
		useradd --system --gid monitor --home-dir "$DATA" --shell "$nologin" monitor
	fi
	install -d -o monitor -g monitor -m 750 "$DATA"
	install -d -o root -g monitor -m 750 "$CFG_DIR"
}

install_files() {
	local src
	src=$(resolve_source_bin)
	[ -f "$src" ] || die "binary missing: $src"
	if [ -z "$ROOT_PREFIX" ] && [ -f "$CFG" ] && [ "$REPLACE" != 1 ]; then
		die "already configured: $CFG (set MONITOR_REPLACE_CONFIG=1 to overwrite)"
	fi
	if [ -n "$ROOT_PREFIX" ] && [ -f "$CFG" ] && [ "$REPLACE" != 1 ]; then
		die "already configured: $CFG (set MONITOR_REPLACE_CONFIG=1 to overwrite)"
	fi
	mkdir -p -- "$(dirname -- "$BIN_DEST")" "$CFG_DIR" "$UNIT_DIR"
	if [ -z "$ROOT_PREFIX" ]; then
		ensure_user
		install -m 755 "$src" "$BIN_DEST"
	else
		mkdir -p -- "$DATA"
		install -m 755 "$src" "$BIN_DEST"
	fi
	write_unit
	write_config
	if [ -z "$ROOT_PREFIX" ]; then
		chown root:monitor "$CFG"
		chmod 640 "$CFG"
		command -v systemctl >/dev/null 2>&1 || die "systemctl not found; start with: MONITOR_LISTEN=$LISTEN ./start.sh --background"
		systemctl daemon-reload
		systemctl enable --now monitord
	else
		chmod 640 "$CFG" || true
	fi
	printf '%s\n' "installed mode=$MODE binary=$BIN_DEST config=$CFG data=$DATA listen=$LISTEN"
}

yaml_field() {
	# $1 file, $2 key on its own line as: key: "value" or key: value
	awk -v key="$2" '
		$1 == key ":" {
			v = $2
			gsub(/"/, "", v)
			gsub(/,/, "", v)
			print v
			exit
		}
	' "$1"
}

load_runtime() {
	if [ -z "${MONITOR_URL:-}" ] && [ -f "$CFG" ]; then
		local listen
		listen=$(yaml_field "$CFG" listen)
		[ -n "$listen" ] && MONITOR_URL=http://$listen
		if [ -z "${MONITOR_MODE_EXPLICIT:-}" ]; then
			local m
			m=$(awk '$1 == "mode:" { print $2; exit }' "$CFG")
			[ -n "$m" ] && MODE=$m
		fi
		if [ -z "$STREAM_DEST" ]; then
			STREAM_DEST=$(awk '
				/destinations:/ {
					if (match($0, /"([^"]+)"/)) { print substr($0, RSTART + 1, RLENGTH - 2); exit }
				}
			' "$CFG")
		fi
	fi
	: "${MONITOR_URL:=http://$LISTEN}"
	: "${MONITOR_PASSWORD_FILE:=$DATA/web-password}"
}

run_check() {
	load_runtime
	local expect_stream="" version_bin
	if [ -n "$STREAM_DEST" ]; then
		expect_stream=ws://$STREAM_DEST/api/v1/stream
	fi
	version_bin=${BINARY_ARG:-${MONITOR_BIN:-}}
	if [ -z "$version_bin" ] && [ -x "$BIN_DEST" ]; then
		version_bin=$BIN_DEST
	elif [ -z "$version_bin" ] && [ -x "$SCRIPT_DIR/monitord" ]; then
		version_bin=$SCRIPT_DIR/monitord
	fi
	MONITOR_CHECK_URL="$MONITOR_URL" \
		MONITOR_PASSWORD_FILE="$MONITOR_PASSWORD_FILE" \
		MONITOR_EXPECT_DIR="$DATA/db" \
		MONITOR_EXPECT_MODE="$MODE" \
		MONITOR_EXPECT_STREAM="$expect_stream" \
		MONITOR_VERSION_BIN="$version_bin" \
		python3 - <<'PY'
import base64, json, os, subprocess, sys, time, urllib.error, urllib.request

url = os.environ["MONITOR_CHECK_URL"].rstrip("/")
password_file = os.environ["MONITOR_PASSWORD_FILE"]
expect_dir = os.environ["MONITOR_EXPECT_DIR"]
expect_mode = os.environ["MONITOR_EXPECT_MODE"]
expect_stream = os.environ.get("MONITOR_EXPECT_STREAM", "")
version_bin = os.environ.get("MONITOR_VERSION_BIN", "")

def fail(msg):
    sys.stderr.write("cloud-host: " + msg + "\n")
    raise SystemExit(1)

deadline = time.monotonic() + 20
body = ""
while time.monotonic() < deadline:
    try:
        with urllib.request.urlopen(url + "/healthz", timeout=2) as resp:
            body = resp.read().decode()
        if body.strip() == "ok":
            break
    except OSError:
        time.sleep(1)
else:
    fail("healthz did not return ok from " + url + "/healthz")

token = ""
for _ in range(20):
    if os.path.isfile(password_file):
        token = open(password_file, encoding="utf-8").read().strip()
        if token:
            break
    time.sleep(0.25)
if not token:
    fail("password file missing: " + password_file)
if len(token) != 43:
    fail("password length is %d, want 43 (generated web-password)" % len(token))
raw = base64.urlsafe_b64decode(token + "=" * ((4 - len(token) % 4) % 4))
if len(raw) != 32 or base64.urlsafe_b64encode(raw).decode().rstrip("=") != token:
    fail("password is not 32-byte raw-url base64")

expect_version = ""
if version_bin and os.path.isfile(version_bin):
    line = subprocess.check_output([version_bin, "-version"], text=True).strip()
    parts = line.split()
    if len(parts) != 3 or parts[0] != "monitord":
        fail("unexpected -version line: " + line)
    expect_version = parts[1]

def get(path):
    req = urllib.request.Request(url + path, headers={"Authorization": "Bearer " + token})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            if resp.status != 200:
                fail(path + " status " + str(resp.status))
            return json.load(resp)
    except urllib.error.HTTPError as exc:
        fail(path + " HTTP " + str(exc.code) + " " + exc.read(200).decode("utf-8", "replace"))

info = None
data = None
for _ in range(20):
    info = get("/api/v1/info")
    charts = get("/api/v1/charts")["charts"]
    if expect_version and info.get("version") != expect_version:
        fail("info.version %s != %s" % (info.get("version"), expect_version))
    if info.get("mode") == expect_mode and info.get("db", {}).get("dir") == expect_dir and "system.cpu" in charts and "system.ram" in charts:
        data = get("/api/v1/data?chart=system.cpu&after=-30&points=5")
        if data.get("id") == "system.cpu" and data.get("points", 0) >= 1 and len(data.get("result", {}).get("data") or []) >= 1:
            break
    time.sleep(1)
else:
    fail("system.cpu/system.ram have no samples (mode=%s dir=%s)" % (info.get("mode") if info else "", info.get("db") if info else ""))

stream_note = ""
if expect_stream:
    stream = None
    for _ in range(30):
        stream = get("/api/v1/info").get("stream") or {}
        if stream.get("enabled") and stream.get("connected") and stream.get("destination") == expect_stream:
            stream_note = " stream=%s sent=%s" % (stream["destination"], stream.get("sent"))
            break
        time.sleep(1)
    else:
        fail("stream want %s got %s last_error=%s" % (expect_stream, (stream or {}).get("destination"), (stream or {}).get("last_error")))

print("ok version=%s mode=%s dir=%s charts=%s cpu_points=%s%s" % (
    info["version"], info["mode"], info["db"]["dir"], info["charts_count"], data["points"], stream_note))
PY
}

run_ai() {
	load_runtime
	[ -n "${MODEL_BASE:-}" ] && [ -n "${MODEL_NAME:-}" ] || die "set MODEL_BASE and MODEL_NAME"
	MONITOR_CHECK_URL="$MONITOR_URL" \
		MONITOR_PASSWORD_FILE="$MONITOR_PASSWORD_FILE" \
		MODEL_BASE="$MODEL_BASE" \
		MODEL_NAME="$MODEL_NAME" \
		MODEL_KEY="${MODEL_KEY:-}" \
		MONITOR_ALLOW_PRIVATE="${MONITOR_ALLOW_PRIVATE:-false}" \
		MONITOR_AI_ANALYZE="$DO_ANALYZE" \
		python3 - <<'PY'
import json, os, urllib.error, urllib.request

url = os.environ["MONITOR_CHECK_URL"].rstrip("/")
password_file = os.environ["MONITOR_PASSWORD_FILE"]
base = os.environ["MODEL_BASE"].rstrip("/")
model = os.environ["MODEL_NAME"]
key = os.environ.get("MODEL_KEY", "")
private = os.environ.get("MONITOR_ALLOW_PRIVATE", "false").lower() == "true"
analyze = os.environ.get("MONITOR_AI_ANALYZE", "0") == "1"

def fail(msg):
    raise SystemExit("cloud-host: " + msg)

if not os.path.isfile(password_file):
    fail("password file missing: " + password_file)
token = open(password_file, encoding="utf-8").read().strip()

if base.endswith("/chat/completions"):
    fail("MODEL_BASE must be the API root, not /chat/completions")
if base.startswith("http://") and not private:
    fail("http MODEL_BASE requires MONITOR_ALLOW_PRIVATE=true (on-host model only)")
if not private and not base.startswith("https://"):
    fail("public MODEL_BASE must start with https://")

def call(method, path, payload=None):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(
        url + path, data=data, method=method,
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=70) as resp:
            return json.load(resp)
    except urllib.error.HTTPError as exc:
        fail(path + " HTTP " + str(exc.code) + " " + exc.read(300).decode("utf-8", "replace"))

current = call("GET", "/api/v1/ai/settings")
saved = call("PUT", "/api/v1/ai/settings", {
    "base_url": base,
    "model": model,
    "api_key": key,
    "allow_private": private,
    "revision": current["revision"],
})
if not saved.get("enabled") or saved.get("base_url") != base or saved.get("model") != model:
    fail("settings not saved: " + json.dumps(saved))
if key and not saved.get("has_api_key"):
    fail("has_api_key is false after saving a key")
if bool(saved.get("allow_private")) != private:
    fail("allow_private mismatch")
print("saved revision=%s model=%s base=%s private=%s" % (saved["revision"], saved["model"], saved["base_url"], str(saved["allow_private"]).lower()))

if not analyze:
    raise SystemExit(0)
preview = call("POST", "/api/v1/ai/preview", {"node": "local", "charts": ["system.cpu", "system.ram"], "window": 300})
pid = preview.get("id") or ""
charts = (preview.get("snapshot") or {}).get("charts") or []
if len(pid) != 48 or len(charts) != 2:
    fail("preview id/charts unexpected")
result = call("POST", "/api/v1/ai/analyze", {
    "preview_id": pid,
    "question": "概括这台云主机最近 5 分钟的 CPU 和内存，指出是否需要处理。",
    "revision": saved["revision"],
})
text = result.get("text") or ""
if not text.strip() or not result.get("model") or not result.get("generated_at"):
    fail("analyze response missing text")
print("analyzed model=%s chars=%d ms=%s" % (result["model"], len(text), result.get("duration_ms")))
PY
}

case "$cmd" in
install)
	install_files
	if [ "$DO_CHECK" = 1 ]; then
		[ -z "$ROOT_PREFIX" ] || die "--check starts against the running service; omit MONITOR_ROOT"
		run_check
	fi
	;;
check) run_check ;;
ai) run_ai ;;
upgrade)
	[ -n "$BINARY_ARG" ] || die "upgrade --binary PATH"
	[ -f "$BINARY_ARG" ] || die "binary missing: $BINARY_ARG"
	valid_path "$DATA" || die "bad data dir: $DATA"
	if [ -z "$ROOT_PREFIX" ]; then
		[ "$(id -u)" = 0 ] || die "upgrade as root"
		systemctl stop monitord
		install -m 755 "$BINARY_ARG" "$BIN_DEST"
		systemctl start monitord
	else
		mkdir -p -- "$(dirname -- "$BIN_DEST")"
		install -m 755 "$BINARY_ARG" "$BIN_DEST"
	fi
	printf '%s\n' "upgraded binary=$BIN_DEST"
	;;
esac
