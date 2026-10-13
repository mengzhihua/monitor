#!/bin/sh
# Stop only this package's monitord. Reads the pidfile, verifies the process
# is monitord and still references our data-dir or config path, then signals it.
# Never uses pkill/killall by name.
set -eu
cd -- "$(dirname -- "$0")"
ROOT=$PWD
DATA=${MONITOR_DATA_DIR:-$ROOT/data}
CONFIG=${MONITOR_CONFIG:-}
PIDFILE=${MONITOR_PIDFILE:-$DATA/monitord.pid}
BIN=${MONITOR_BIN:-$ROOT/monitord}

abspath() {
	target=$1
	case "$target" in
	/*) printf '%s\n' "$target" ;;
	*) printf '%s\n' "$ROOT/$target" ;;
	esac
}
DATA=$(abspath "$DATA")
PIDFILE=$(abspath "$PIDFILE")
if [ -n "$CONFIG" ]; then
	CONFIG=$(abspath "$CONFIG")
fi
if [ -n "$BIN" ]; then
	BIN=$(abspath "$BIN")
fi

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

is_monitord_proc() {
	p=$1
	if [ -r "/proc/$p/comm" ]; then
		comm=$(tr -d '\n' <"/proc/$p/comm")
		case "$comm" in
		monitord|monitord.exe) return 0 ;;
		esac
		return 1
	fi
	if [ -L "/proc/$p/exe" ] || [ -e "/proc/$p/exe" ]; then
		exe=$(readlink "/proc/$p/exe" 2>/dev/null || true)
		base=${exe##*/}
		base=${base%% *}
		case "$base" in
		monitord|monitord.exe) return 0 ;;
		esac
	fi
	cmd=$(cmdline_of "$p")
	argv0=${cmd%% *}
	base=${argv0##*/}
	case "$base" in
	monitord|monitord.exe) return 0 ;;
	esac
	return 1
}

path_matches_instance() {
	cmd=" $1 "
	case "$cmd" in
	*" -data-dir ${DATA} "*|*" -data-dir=${DATA} ") return 0 ;;
	esac
	if [ -n "$CONFIG" ]; then
		case "$cmd" in
		*" -config ${CONFIG} "*|*" -config=${CONFIG} ") return 0 ;;
		esac
	fi
	return 1
}

if ! kill -0 "$pid" 2>/dev/null; then
	printf '%s\n' "process $pid not running; removing stale pidfile" >&2
	rm -f -- "$PIDFILE"
	exit 0
fi

cmd=$(cmdline_of "$pid")
if ! is_monitord_proc "$pid"; then
	printf '%s\n' "pid $pid is not monitord (refusing to signal): $cmd" >&2
	exit 1
fi
if ! path_matches_instance "$cmd"; then
	printf '%s\n' "pid $pid cmdline does not match this instance (data=$DATA config=$CONFIG): $cmd" >&2
	exit 1
fi

kill "$pid"
# Wait briefly for a clean exit; escalate once if needed.
# Whole seconds only — fractional sleep is not portable across all hosts.
i=0
while [ "$i" -lt 10 ]; do
	kill -0 "$pid" 2>/dev/null || break
	i=$((i + 1))
	sleep 1
done
if kill -0 "$pid" 2>/dev/null; then
	kill -KILL "$pid" 2>/dev/null || true
fi
rm -f -- "$PIDFILE"
printf '%s\n' "stopped pid $pid"
