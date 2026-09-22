#!/usr/bin/env bash
# Starts monitord, waits a few seconds, checks the API answers with real data.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=${BIN:-core/bin/monitord}
[[ -x "$BIN" || -x "$BIN.exe" ]] || { echo "binary $BIN not found (run make core)"; exit 1; }
[[ -x "$BIN" ]] || BIN="$BIN.exe"

PORT=${PORT:-19998}
DATA=$(mktemp -d)
"$BIN" -listen "127.0.0.1:$PORT" -data-dir "$DATA" -log-level warn &
PID=$!
trap 'kill $PID 2>/dev/null || true; wait $PID 2>/dev/null || true; rm -rf "$DATA"' EXIT

for i in $(seq 1 30); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
sleep 4

fail() { echo "SMOKE FAIL: $*"; exit 1; }

info=$(curl -sf "http://127.0.0.1:$PORT/api/v1/info") || fail "info"
echo "$info" | grep '"charts_count":[1-9]' >/dev/null || fail "no charts: $info"

charts=$(curl -sf "http://127.0.0.1:$PORT/api/v1/charts") || fail "charts"
echo "$charts" | grep '"system.cpu"' >/dev/null || fail "system.cpu missing"
echo "$charts" | grep '"system.ram"' >/dev/null || fail "system.ram missing"
curl -sf "http://127.0.0.1:$PORT/api/v1/charts" | grep '"system.idlejitter"' >/dev/null || fail "system.idlejitter missing"

curl -sf "http://127.0.0.1:$PORT/api/v1/contexts" | grep '"contexts"' >/dev/null || fail "contexts"
curl -sf "http://127.0.0.1:$PORT/api/v1/allmetrics?format=csv" | grep 'system.ram' >/dev/null || fail "allmetrics csv"
curl -sf "http://127.0.0.1:$PORT/api/v1/data?context=system.ram&after=-5" | grep '"points":[1-9]' >/dev/null || fail "data context"
curl -sf "http://127.0.0.1:$PORT/api/v1/data?chart=system.ram&after=-5&format=csv" | grep 'time' >/dev/null || fail "data csv"
curl -sf "http://127.0.0.1:$PORT/api/v2/contexts" | grep '"api":2' >/dev/null || fail "v2 contexts"
curl -sf "http://127.0.0.1:$PORT/api/v2/nodes" | grep '"api":2' >/dev/null || fail "v2 nodes"
curl -sf "http://127.0.0.1:$PORT/api/v1/alarm_count" | grep '"count"' >/dev/null || fail "alarm_count"
curl -sf "http://127.0.0.1:$PORT/api/v1/alarm_summary" | grep '"status"' >/dev/null || fail "alarm_summary"
curl -sf "http://127.0.0.1:$PORT/api/v1/manage/health" | grep '"enabled"' >/dev/null || fail "manage health"
curl -sf "http://127.0.0.1:$PORT/api/v1/info" | grep '"aclk"' >/dev/null || fail "info aclk"
curl -sf "http://127.0.0.1:$PORT/api/v1/info" | python3 -c "import json,sys; a=json.load(sys.stdin)['aclk']; assert 'protocol' in a and 'capabilities' in a, a" || fail "info aclk fields"
curl -sf "http://127.0.0.1:$PORT/api/v1/data?chart=system.ram&after=-5" | grep '"anomaly"' >/dev/null || fail "data anomaly"
curl -sf "http://127.0.0.1:$PORT/api/v1/weights?method=volume&group=chart&top=5&after=-10&before=0&baseline_after=-40&baseline_before=-10" | grep '"group"' >/dev/null || fail "weights group"
curl -sf "http://127.0.0.1:$PORT/api/v2/nodes?contexts=true" | grep '"contexts"' >/dev/null || fail "v2 nodes contexts"
curl -sf "http://127.0.0.1:$PORT/api/v2/q?chart=system.ram&after=-5" | grep '"points"' >/dev/null || fail "v2 q"
curl -sf "http://127.0.0.1:$PORT/api/v2/alert_transitions" | grep '"transitions"' >/dev/null || fail "alert_transitions"
curl -sf "http://127.0.0.1:$PORT/api/v1/badge.svg?chart=system.ram" | grep '<svg' >/dev/null || fail "badge.svg"
curl -sf "http://127.0.0.1:$PORT/api/v1/weights?method=anomaly-rate" | grep '"weights"' >/dev/null || fail "weights"
curl -sf "http://127.0.0.1:$PORT/api/v1/functions" | grep -E 'processes|mounts|disks|network-interfaces' >/dev/null || fail "functions"
curl -sf "http://127.0.0.1:$PORT/api/v1/prometheus/catalog" | grep '"etcd"' >/dev/null || fail "prometheus catalog"

data=$(curl -sf "http://127.0.0.1:$PORT/api/v1/data?chart=system.ram&after=-5") || fail "data"
echo "$data" | grep '"points":[1-9]' >/dev/null || fail "no data points: $data"

metrics=$(curl -sf "http://127.0.0.1:$PORT/metrics") || fail "metrics"
echo "$metrics" | grep '^monitor_system_ram' >/dev/null || fail "prometheus format"
index=$(curl -sf "http://127.0.0.1:$PORT/") || fail "dashboard"
echo "$index" | grep -i '<title>Monitor</title>' >/dev/null || fail "dashboard html"

# Agent has no org store → hub cloud routes 404
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/v1/hub/spaces")
[[ "$code" == "404" ]] || fail "hub spaces on agent: $code"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/api/v1/hub/console")
[[ "$code" == "404" ]] || fail "hub console on agent: $code"

# Hub claim / config / ring (second process). Avoid PORT+1: default agent
# smoke uses 19998 so +1 collides with the stock :19999 listener.
HUBPORT=${HUBPORT:-18999}
HUBDATA=$(mktemp -d)
cat > "$HUBDATA/monitor.yaml" <<EOF
mode: hub
global:
  hostname: smoke-hub
  data_dir: $HUBDATA/data
hub:
  space: smoke
  room: edge
EOF
"$BIN" -config "$HUBDATA/monitor.yaml" -listen "127.0.0.1:$HUBPORT" -log-level warn &
HPID=$!
trap 'kill $PID $HPID 2>/dev/null || true; wait $PID 2>/dev/null || true; wait $HPID 2>/dev/null || true; rm -rf "$DATA" "$HUBDATA"' EXIT
for i in $(seq 1 30); do
  curl -sf "http://127.0.0.1:$HUBPORT/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
curl -sf "http://127.0.0.1:$HUBPORT/healthz" >/dev/null || fail "hub healthz"

eval "$(python3 - "$HUBPORT" <<'PY'
import json, sys, urllib.request, urllib.error
base = f"http://127.0.0.1:{sys.argv[1]}"
def get(path):
    return json.load(urllib.request.urlopen(base + path))
def req(method, path, body=None, headers=None):
    data = None if body is None else json.dumps(body).encode()
    r = urllib.request.Request(base + path, data=data, method=method, headers=headers or {"Content-Type": "application/json"})
    with urllib.request.urlopen(r) as resp:
        raw = resp.read()
        return json.loads(raw) if raw else {}
spaces = get("/api/v1/hub/spaces")["spaces"]
assert spaces and spaces[0]["name"] == "smoke", spaces
rooms = get("/api/v1/hub/rooms?space_id=" + spaces[0]["id"])["rooms"]
assert rooms and rooms[0]["name"] == "edge", rooms
cl = req("POST", "/api/v1/hub/claim-tokens", {"space_id": spaces[0]["id"], "room_id": rooms[0]["id"], "ttl": "1h"})
assert cl.get("token"), cl
redeemed = req("POST", "/api/v1/claim", {"token": cl["token"], "node_id": "box-1"})
assert redeemed.get("api_key") and redeemed.get("node_id") == "box-1", redeemed
cfg = req("PUT", "/api/v1/hub/config?node=box-1", {"node_id": "box-1", "disabled": ["nvidia"]})
assert cfg.get("disabled") == ["nvidia"], cfg
got = req("GET", "/api/v1/agent/config", headers={"Authorization": "Bearer " + redeemed["api_key"]})
assert got.get("disabled") == ["nvidia"], got
ring = req("POST", "/api/v1/hub/ring", {
    "host": {"id": "peer-agent", "hostname": "peer-agent", "os": "linux", "update_every": 1},
    "charts": [{"id": "system.ram", "context": "system.ram", "units": "MiB", "dimensions": [{"id": "used"}, {"id": "free"}]}],
    "samples": [{"chart": "system.ram", "t": 1, "v": {"used": 11, "free": 22}}],
})
assert ring.get("replica") is True, ring
nodes = get("/api/v1/nodes")["nodes"]
assert any(n.get("id") == "peer-agent" and n.get("replica") for n in nodes), nodes
cons = get("/api/v1/hub/console")
assert cons.get("spaces") and cons["spaces"][0]["name"] == "smoke", cons
assert cons.get("aclk", {}).get("protocol") == "stream+mqtt", cons
print("HUB_SMOKE=1")
PY
)" || fail "hub claim/config/ring"
[[ "${HUB_SMOKE:-}" == "1" ]] || fail "hub python smoke"

echo "SMOKE OK"
