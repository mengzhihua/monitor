#!/bin/sh
# Start this package's monitord only. Writes data/monitord.pid and refuses to
# start when that pid is already our monitord instance (binary name + data/config).
set -eu
cd -- "$(dirname -- "$0")"
ROOT=$PWD
DATA=${MONITOR_DATA_DIR:-$ROOT/data}
CONFIG=${MONITOR_CONFIG:-}
PIDFILE=${MONITOR_PIDFILE:-$DATA/monitord.pid}
LISTEN=${MONITOR_LISTEN:-127.0.0.1:20099}
BIN=${MONITOR_BIN:-}
LOG=${MONITOR_LOG:-}
BACKGROUND=${MONITOR_BACKGROUND:-0}
# Accept start.sh --background / -d for hosts without systemd.
for arg in "$@"; do
	case "$arg" in
	--background|-d|--daemon) BACKGROUND=1 ;;
	--log=*) LOG=${arg#--log=} ;;
	--help|-h)
		printf '%s\n' "Usage: start.sh [--background|-d] [--log=PATH]" \
			"Env: MONITOR_BIN MONITOR_DATA_DIR MONITOR_CONFIG MONITOR_LISTEN MONITOR_PIDFILE MONITOR_LOG MONITOR_BACKGROUND=1" >&2
		exit 0
		;;
	esac
done

# Resolve to absolute paths so pidfile checks stay stable after chdir.
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

if [ -z "$BIN" ]; then
	if [ -x "$ROOT/monitord" ]; then
		BIN=$ROOT/monitord
	elif [ -x /usr/bin/monitord ]; then
		BIN=/usr/bin/monitord
	else
		printf '%s\n' "monitord binary not found (set MONITOR_BIN)" >&2
		exit 1
	fi
fi
BIN=$(abspath "$BIN")
mkdir -p -- "$DATA"

cmdline_of() {
	pid=$1
	if [ -r "/proc/$pid/cmdline" ]; then
		tr '\0' ' ' <"/proc/$pid/cmdline"
		return 0
	fi
	ps -p "$pid" -o args= 2>/dev/null || ps -p "$pid" -o command= 2>/dev/null || true
}

# Require the process to look like monitord (comm or argv0), not a decoy that
# merely mentions our data path in its arguments.
is_monitord_proc() {
	pid=$1
	if [ -r "/proc/$pid/comm" ]; then
		comm=$(tr -d '\n' <"/proc/$pid/comm")
		case "$comm" in
		monitord|monitord.exe) return 0 ;;
		esac
		return 1
	fi
	if [ -L "/proc/$pid/exe" ] || [ -e "/proc/$pid/exe" ]; then
		exe=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
		base=${exe##*/}
		base=${base%% *}
		case "$base" in
		monitord|monitord.exe) return 0 ;;
		esac
	fi
	cmd=$(cmdline_of "$pid")
	argv0=${cmd%% *}
	base=${argv0##*/}
	case "$base" in
	monitord|monitord.exe) return 0 ;;
	esac
	return 1
}

path_matches_instance() {
	# Require an explicit -data-dir / -config flag pointing at this instance.
	# Padding spaces avoid matching a longer sibling path prefix.
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

is_our_instance() {
	pid=$1
	kill -0 "$pid" 2>/dev/null || return 1
	is_monitord_proc "$pid" || return 1
	cmd=$(cmdline_of "$pid")
	path_matches_instance "$cmd"
}

if [ -f "$PIDFILE" ]; then
	old=$(cat -- "$PIDFILE")
	if [ -n "$old" ] && is_our_instance "$old"; then
		printf '%s\n' "already running pid $old (pidfile $PIDFILE)" >&2
		exit 1
	fi
	rm -f -- "$PIDFILE"
fi

printf '%s\n' "health: curl -fsS http://${LISTEN}/healthz"
printf '%s\n' "password: ${DATA}/web-password"
set -- "$BIN" -listen "$LISTEN" -data-dir "$DATA"
if [ -n "$CONFIG" ]; then
	set -- "$@" -config "$CONFIG"
fi
if [ "$BACKGROUND" = "1" ] || [ "$BACKGROUND" = "true" ] || [ "$BACKGROUND" = "yes" ]; then
	if [ -z "$LOG" ]; then
		LOG=$DATA/monitord.log
	fi
	LOG=$(abspath "$LOG")
	mkdir -p -- "$(dirname -- "$LOG")"
	# Detach: keep pidfile, redirect logs, do not wait.
	nohup "$@" >>"$LOG" 2>&1 &
	pid=$!
	printf '%s\n' "$pid" >"$PIDFILE"
	printf '%s\n' "started pid $pid (pidfile $PIDFILE, log $LOG)"
	exit 0
fi
if [ -n "$LOG" ]; then
	LOG=$(abspath "$LOG")
	mkdir -p -- "$(dirname -- "$LOG")"
	 "$@" >>"$LOG" 2>&1 &
else
	 "$@" &
fi
pid=$!
printf '%s\n' "$pid" >"$PIDFILE"
trap 'rm -f -- "$PIDFILE"' EXIT INT TERM
wait "$pid"
status=$?
rm -f -- "$PIDFILE"
trap - EXIT INT TERM
exit "$status"
