---
name: monitor-runtime-testing
description: Exercise the embedded monitord dashboard, real collectors, live protocol, token guard, and TSDB restart persistence locally.
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
- Direct unauthenticated local API calls are suitable when no browser identity is in use.
- An independent native browser WebSocket can capture `/api/v1/live?charts=system.cpu,system.ram`; validate `{chart,t,v}`, finite values and one-second timestamp increments.
- Save non-null RAM timestamp/value rows immediately before SIGINT. Restart using the same data directory; require positive `tsdb: loaded series=N blocks=M` and compare overlapping rows exactly.
- Keep the before/after queries within their history window. Use fixed absolute bounds when repeatability is important.
- Allow the UI reconnect backoff (up to 15 seconds); a visible gap for missing live samples is expected. Check restored history separately via API.

## Token checks
- A config with `web.token` guards API/metrics but not root/static assets.
- Test absent/wrong token -> 401, query/Bearer token -> 200.
- Distinguish public UI-shell availability from a functioning authenticated dashboard. Test `/?token=...` explicitly; the UI may not forward it to API/WebSocket.
- Restore the default no-token service after testing.
