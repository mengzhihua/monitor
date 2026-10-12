#!/bin/sh
# Start this package's monitord only. Writes data/monitord.pid and refuses to
# start when that pid is already our instance (matched by config/data path).
set -eu
cd -- "$(dirname -- "$0")"
ROOT=$PWD
DATA=${MONITOR_DATA_DIR:-$ROOT/data}
CONFIG=${MONITOR_CONFIG:-}
PIDFILE=${MONITOR_PIDFILE:-$DATA/monitord.pid}
LISTEN=${MONITOR_LISTEN:-127.0.0.1:20099}
BIN=${MONITOR_BIN:-}
if [ -z "$BIN" ]; then
	if [ -x "$ROOT/monitord" ]; then
		BIN=$ROOT/monitord
	elif [ -x /usr/bin/monitord ]; then
		BIN=/usr/bin/monitord
	else
		printf '%s\n' "monitord binary not found" >&2
		exit 1
	fi
fi
mkdir -p -- "$DATA"

cmdline_of() {
	pid=$1
	if [ -r "/proc/$pid/cmdline" ]; then
		tr '\0' ' ' <"/proc/$pid/cmdline"
		return 0
	fi
	ps -p "$pid" -o args= 2>/dev/null || ps -p "$pid" -o command= 2>/dev/null || true
}

is_our_instance() {
	pid=$1
	kill -0 "$pid" 2>/dev/null || return 1
	cmd=$(cmdline_of "$pid")
	case "$cmd" in
	*"$DATA"*|*"$CONFIG"*) return 0 ;;
	esac
	# Fallback: binary path match when no config was supplied.
	case "$cmd" in
	*"$BIN"*) [ -z "$CONFIG" ] && return 0 ;;
	esac
	return 1
}

if [ -f "$PIDFILE" ]; then
	old=$(cat -- "$PIDFILE")
	if [ -n "$old" ] && is_our_instance "$old"; then
		printf '%s\n' "already running pid $old (pidfile $PIDFILE)" >&2
		exit 1
	fi
	rm -f -- "$PIDFILE"
fi

printf '%s\n' "Open http://${LISTEN}/ ; password: ${DATA}/web-password"
set -- "$BIN" -listen "$LISTEN" -data-dir "$DATA"
if [ -n "$CONFIG" ]; then
	set -- "$@" -config "$CONFIG"
fi
 "$@" &
pid=$!
printf '%s\n' "$pid" >"$PIDFILE"
trap 'rm -f -- "$PIDFILE"' EXIT INT TERM
wait "$pid"
status=$?
rm -f -- "$PIDFILE"
trap - EXIT INT TERM
exit "$status"
