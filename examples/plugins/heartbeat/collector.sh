#!/bin/sh
set -eu
every="${1:-1}"
chart="plugin.${MONITOR_PLUGIN:-heartbeat}.pulse"
printf 'CHART %s "" "Plugin heartbeat" "pulses" plugins plugin.heartbeat line 90000 %s\n' "$chart" "$every"
printf 'DIMENSION count "Pulse count" absolute 1 1\n'
count=0
while :; do
  count=$((count + 1))
  printf 'BEGIN %s\nSET count = %s\nEND\n' "$chart" "$count"
  sleep "$every"
done
