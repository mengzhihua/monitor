# Packaging

`monitord.service` is a systemd unit for a static `monitord` binary installed at `/usr/bin/monitord`.

```
install -d /etc/monitor /var/lib/monitor
install -m 644 monitor.example.yaml /etc/monitor/monitor.yaml
install -m 755 core/bin/monitord /usr/bin/monitord
install -m 644 packaging/monitord.service /etc/systemd/system/monitord.service
systemctl daemon-reload
systemctl enable --now monitord
```

For non-systemd installs, use `start.sh` / `stop.sh`. They keep a pidfile under the data
directory and stop only the pid whose command line still references that data dir or
config path — never `pkill -x monitord`.

```
install -m 755 packaging/start.sh packaging/stop.sh /opt/monitor/
# optional overrides: MONITOR_BIN MONITOR_DATA_DIR MONITOR_CONFIG MONITOR_LISTEN MONITOR_PIDFILE
MONITOR_BIN=/usr/bin/monitord MONITOR_CONFIG=/etc/monitor/monitor.yaml \
  MONITOR_DATA_DIR=/var/lib/monitor /opt/monitor/start.sh
/opt/monitor/stop.sh
```

The unit does not ship a package build. Release artifacts stay in `.github/workflows/release.yml`.
