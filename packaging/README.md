# Packaging

Cloud VM install, health checks, Hub/Agent, and API-based AI model setup:

```
sudo ./cloud-host.sh install --check
```

Details are in [docs/22-cloud-host.md](../docs/22-cloud-host.md). Release archives ship that file as `CLOUD-HOST.md` next to `cloud-host.sh` and `monitord.service`.

`monitord.service` is a systemd unit for a static `monitord` binary installed at `/usr/bin/monitord`.

```
install -d /etc/monitor /var/lib/monitor
install -m 644 monitor.example.yaml /etc/monitor/monitor.yaml
install -m 755 core/bin/monitord /usr/bin/monitord
install -m 644 packaging/monitord.service /etc/systemd/system/monitord.service
systemctl daemon-reload
systemctl enable --now monitord
```

## Non-systemd hosts (`start.sh` / `stop.sh`)

For containers, developer laptops, or hosts without systemd, use the scripts shipped
in release packages (and in this directory). They keep a pidfile under the data
directory and stop **only** a process that is both:

1. named `monitord` (via `/proc/<pid>/comm`, exe basename, or argv0), and
2. launched with this instance’s `-data-dir` / `-config` path.

A decoy `sleep` (or any other process) that merely mentions the data path in its
arguments is refused. The scripts never call `pkill` / `killall`.

```
install -m 755 packaging/start.sh packaging/stop.sh /opt/monitor/
# Place or point at the binary (release tarballs ship monitord next to the scripts).
```

### Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `MONITOR_BIN` | `$ROOT/monitord` or `/usr/bin/monitord` | Absolute path to the binary |
| `MONITOR_DATA_DIR` | `$ROOT/data` | Data directory (`-data-dir`) |
| `MONITOR_CONFIG` | _(empty)_ | Optional `-config` path |
| `MONITOR_LISTEN` | `127.0.0.1:20099` | Listen address for `start.sh` |
| `MONITOR_PIDFILE` | `$MONITOR_DATA_DIR/monitord.pid` | Pidfile location |
| `MONITOR_LOG` | _(foreground: console; background: `$MONITOR_DATA_DIR/monitord.log`)_ | Log file path |
| `MONITOR_BACKGROUND` | `0` | Set `1` (or pass `--background` / `-d`) to detach |

`$ROOT` is the directory containing the script (after `cd` there). Paths may be
relative to `$ROOT` or absolute; the scripts normalize them before matching.

Example:

```
MONITOR_BIN=/usr/bin/monitord \
MONITOR_CONFIG=/etc/monitor/monitor.yaml \
MONITOR_DATA_DIR=/var/lib/monitor \
MONITOR_LISTEN=127.0.0.1:19999 \
  /opt/monitor/start.sh

MONITOR_BIN=/usr/bin/monitord \
MONITOR_CONFIG=/etc/monitor/monitor.yaml \
MONITOR_DATA_DIR=/var/lib/monitor \
  /opt/monitor/stop.sh
```

On hosts without `/proc` (some containers / macOS), matching falls back to `ps`
argv0 basename + flag paths. Prefer absolute `MONITOR_*` paths so checks stay
stable after chdir.

Verify pidfile safety locally (needs a built binary):

```
scripts/build.sh core
python3 scripts/verify-packaging-pidfile.py
```

The unit does not ship a package build. Release artifacts stay in `.github/workflows/release.yml`.
