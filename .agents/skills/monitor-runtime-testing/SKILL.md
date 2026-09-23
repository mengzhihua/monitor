---
name: monitor-runtime-testing
description: Exercise embedded monitord dashboards, agent-to-Hub streaming, RBAC, live protocol, and TSDB restart persistence locally.
---

# Monitor runtime testing

## Devin Secrets Needed
None for local default mode. Use an explicitly agreed temporary token for auth tests; never reuse a production token.

## Setup
- Add `/usr/local/go/bin` to PATH if Go is not found.
- Run `make all` from the repo root: UI assets must be built before the Go binary to embed the current dashboard. Building only Go can leave stale or absent UI assets.
- Inspect `ss -ltnp` and `pgrep -ax monitord` before using port 19999. Stop only the identified test service with SIGINT; preserve existing data directories.
- Create an empty temporary config file (so a developer `monitor.yaml` in the repo root is not picked up), then run `./core/bin/monitord -config <temporary-config-file> -listen 127.0.0.1:19999 -data-dir <temporary-data-directory>`, capturing its log.
- `scripts/smoke.sh` starts its own process on 19998. Keep that port free.

## UI checks
- Open `/`; check header identity/counts, green live dot, and sidebar collector statuses.
- Header filter matches chart id/title/family. `system.ram` isolates a stable chart; clear to restore all groups.
- Range labels are Chinese: `1 分钟`, `5 分钟`, `15 分钟`, `1 小时`.
- uPlot legends display `-` until hovered: hover the data region to distinguish missing values from normal idle legends.
- Verify sparse-cadence charts such as `system.load` separately: compare populated API rows against visible plot geometry. Five-second sampling on a one-second grid can produce invisible isolated samples when points and gap-spanning are disabled.
- Check both normal desktop and narrow browser widths; the header/grid can overflow at small widths.
- Counts depend on host cores, disks, interfaces and mounts; compare UI with API instead of assuming fixed totals.

## Protocol and restart checks
- Default deployments require authentication. Read the isolated test data directory’s `web-password` and send it as a Bearer token; do not print credentials into test output.
- An independent native browser WebSocket can capture `/api/v1/live?charts=system.cpu,system.ram`; validate `{chart,t,v}`, finite values and one-second timestamp increments.
- Save non-null RAM timestamp/value rows immediately before SIGINT. Restart using the same data directory; require positive `tsdb: loaded series=N blocks=M` and compare overlapping rows exactly.
- Keep the before/after queries within their history window. Use fixed absolute bounds when repeatability is important.
- Allow the UI reconnect backoff (up to 15 seconds); a visible gap for missing live samples is expected. Check restored history separately via API.

## Token checks
- A config with `web.token` guards API/metrics but not root/static assets.
- Test absent/wrong token -> 401, query/Bearer token -> 200.
- Distinguish public UI-shell availability from a functioning authenticated dashboard. Test `/?token=...` explicitly; the UI may not forward it to API/WebSocket.
- Stop the isolated test processes after testing. Keep the default password protection enabled; never restore an anonymous service.

## Hub runtime setup and checks
- Use separate configs/data directories for Hub and agent. Configure `mode: hub`, `hub.api_keys`, and `web.users` on the Hub; configure `stream.enabled`, `stream.destinations`, and `stream.api_key` on the agent. Use distinct web ports (e.g. 19999/19998); do not run smoke.sh on an occupied agent port.
- Same-machine agents share machine-id with the Hub. Discover the non-local node ID from `/api/v1/nodes`; use empty node or `local` for Hub views rather than the host ID.
- Enable `apps` on the agent for a meaningful remote Processes Function. Different enabled collector sets distinguish scoped registries even when both processes observe the same physical host.
- In the M2 UI, `?token=` is imported into **sessionStorage** (`monitor.token`), then removed from the URL. Node selection uses `monitor.node` in sessionStorage too. Reload must retain both.
- Native browser WebSocket auth uses subprotocols `monitor` and `bearer.<base64url-token>`. Pass `node=<remote-id>` and require matching frame `node`; unknown-node testing requires a real upgrade, not plain HTTP GET.
- Exercise viewer chart access plus Function 403, troubleshooter Function 200 plus DELETE 403, admin live DELETE 409 and offline DELETE 204. Use explicit test credentials, not extracted browser cookies.
- Add a silent test-only alarm based on real memory use if deterministic remote alarm visibility is needed. Verify both current rules and recent events, then compare direct-agent and Hub snapshots across a Hub restart.
- For outage tests, automate stop/wait/restart using known PIDs and record actual timestamps. Reconnect backoff can extend the disconnected interval beyond the Hub downtime. Use a five-minute or longer chart window and fixed absolute API bounds; compare populated agent CPU rows with Hub rows exactly.
- Connected-but-silent streams become stale after more than max(10 seconds, 3*update_every). Use SIGSTOP for that state. Resume before SIGINT to test a graceful disconnect, which can become offline immediately. The UI polls nodes every 30 seconds.
- Test wrong keys on a third port and isolated data directory. Require HTTP401 logs with increasing retry delays while the process/local API remain alive; clean up only that known PID afterward.
