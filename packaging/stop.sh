#!/bin/sh
# Stop only this package's monitord. Reads the pidfile, verifies the process
# command line still references our data-dir or config path, then signals it.
# Never uses pkill/killall by name.
set -eu
cd -- "$(dirname -- "$0")"
ROOT=$PWD
DATA=${MONITOR_DATA_DIR:-$ROOT/data}
CONFIG=${MONITOR_CONFIG:-}
PIDFILE=${MONITOR_PIDFILE:-$DATA/monitord.pid}
BIN=${MONITOR_BIN:-$ROOT/monitord}

if [ ! -f "$PIDFILE" ]; then
	printf '%s\n' "pidfile not found: $PIDFILE" >&2
	exit 1
fi
pid=$(cat -- "$PIDFILE")
if [ -z "$pid" ]; then
	printf '%s\n' "empty pidfile: $PIDFILE" >&2
	exit 1
fi

cmdline_of() {
	p=$1
	if [ -r "/proc/$p/cmdline" ]; then
		tr '\0' ' ' <"/proc/$p/cmdline"
		return 0
	fi
	ps -p "$p" -o args= 2>/dev/null || ps -p "$p" -o command= 2>/dev/null || true
}

if ! kill -0 "$pid" 2>/dev/null; then
	printf '%s\n' "process $pid not running; removing stale pidfile" >&2
	rm -f -- "$PIDFILE"
	exit 0
fi

cmd=$(cmdline_of "$pid")
ok=0
case "$cmd" in
*"$DATA"*|*"$CONFIG"*) ok=1 ;;
esac
if [ "$ok" -eq 0 ] && [ -n "$BIN" ]; then
	case "$cmd" in
	*"$BIN"*) [ -z "$CONFIG" ] && ok=1 ;;
	esac
fi
if [ "$ok" -eq 0 ]; then
	printf '%s\n' "pid $pid cmdline does not match this instance (data=$DATA config=$CONFIG): $cmd" >&2
	exit 1
fi

kill "$pid"
# Wait briefly for a clean exit; escalate once if needed.
i=0
while [ "$i" -lt 20 ]; do
	kill -0 "$pid" 2>/dev/null || break
	i=$((i + 1))
	sleep 0.1
done
if kill -0 "$pid" 2>/dev/null; then
	kill -KILL "$pid" 2>/dev/null || true
fi
rm -f -- "$PIDFILE"
printf '%s\n' "stopped pid $pid"
