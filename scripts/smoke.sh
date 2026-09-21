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

curl -sf "http://127.0.0.1:$PORT/api/v1/contexts" | grep '"contexts"' >/dev/null || fail "contexts"
curl -sf "http://127.0.0.1:$PORT/api/v1/allmetrics?format=csv" | grep 'system.ram' >/dev/null || fail "allmetrics csv"

data=$(curl -sf "http://127.0.0.1:$PORT/api/v1/data?chart=system.ram&after=-5") || fail "data"
echo "$data" | grep '"points":[1-9]' >/dev/null || fail "no data points: $data"

metrics=$(curl -sf "http://127.0.0.1:$PORT/metrics") || fail "metrics"
echo "$metrics" | grep '^monitor_system_ram' >/dev/null || fail "prometheus format"
index=$(curl -sf "http://127.0.0.1:$PORT/") || fail "dashboard"
echo "$index" | grep -i '<title>Monitor</title>' >/dev/null || fail "dashboard html"

echo "SMOKE OK"
