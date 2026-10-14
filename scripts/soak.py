#!/usr/bin/env python3
"""Bounded local agent soak. Outputs measured observations, not capacity promises.

Use --seconds 86400 (24h) on a machine that can stay up that long.
GitHub-hosted runners are capped at 6h, so CI uses about 5.5h and passes
thresholds explicitly. A SIGTERM (job timeout) writes the partial report and
exits 1.

--seed-config is the only config source besides the built-in minimal file.
A data directory is copied, never used to discover monitor.yaml next to it.
Notification, trapper, and action sections are removed from the seed config.
A fixed web.token in that config is used for login; otherwise the agent
generates web-password.
"""
from __future__ import annotations

import argparse, json, pathlib, re, shutil, signal, socket, statistics, subprocess, sys, tempfile, time, urllib.error, urllib.request

DROP_PATHS = {
    ("notify",),
    ("trapper",),
    ("actions",),
    ("commands",),
    ("health", "notify"),
    ("health", "actions"),
}

heap_re = re.compile(r'^monitor_process_heap_alloc_bytes\{[^}]*\} (\d+)', re.M)
series_re = re.compile(r'^monitor_tsdb_series\{[^}]*\} (\d+)', re.M)
anomaly_re = re.compile(r'^monitor_ml_train_buffer_bytes\{[^}]*\} (\d+)', re.M)


def strip_seed_config(text: str) -> str:
    """Drop notification, trapper, and action blocks. Keep everything else."""
    out: list[str] = []
    stack: list[tuple[int, str]] = []
    drop_at: int | None = None
    for line in text.splitlines(keepends=True):
        raw = line.rstrip("\n")
        if not raw.strip() or raw.lstrip().startswith("#"):
            if drop_at is None:
                out.append(line)
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        body = raw.lstrip(" ")
        if drop_at is not None and indent <= drop_at:
            drop_at = None
        if body.startswith("- "):
            if drop_at is None:
                out.append(line)
            continue
        if ":" not in body:
            if drop_at is None:
                out.append(line)
            continue
        key = body.split(":", 1)[0].strip().strip('"').strip("'")
        while stack and stack[-1][0] >= indent:
            stack.pop()
        path = tuple(item[1] for item in stack) + (key,)
        if drop_at is None and any(path[:len(drop)] == drop for drop in DROP_PATHS):
            drop_at = indent
            continue
        if drop_at is not None:
            continue
        stack.append((indent, key))
        out.append(line)
    return "".join(out)


def _yaml_value(body: str) -> str:
    value = body.split(":", 1)[1].strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
        value = value[1:-1]
    return value


def web_token(text: str) -> str | None:
    """Return the admin web.token. Nested user tokens are ignored."""
    in_web = False
    web_indent = 0
    child_indent: int | None = None
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        body = raw.lstrip()
        if indent == 0 and body.startswith("web:"):
            in_web = True
            web_indent = 0
            child_indent = None
            continue
        if not in_web:
            continue
        if indent <= web_indent:
            break
        if child_indent is None:
            child_indent = indent
        if indent == child_indent and body.startswith("token:"):
            return _yaml_value(body) or None
    return None


def expects_ml(text: str) -> bool:
    """True when this config starts the ml collector (default is all collectors)."""
    in_collectors = False
    collectors_indent = 0
    child_indent: int | None = None
    enabled: list[str] | None = None
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        body = raw.lstrip()
        if indent == 0 and body.startswith("collectors:"):
            in_collectors = True
            collectors_indent = 0
            child_indent = None
            continue
        if not in_collectors:
            continue
        if indent <= collectors_indent:
            break
        if child_indent is None:
            child_indent = indent
        if indent == child_indent and body.startswith("enabled:"):
            enabled = []
            rest = _yaml_value("enabled:" + body.split(":", 1)[1])
            if rest.startswith("[") and rest.endswith("]"):
                enabled = [part.strip().strip('"').strip("'") for part in rest[1:-1].split(",") if part.strip()]
            elif rest:
                enabled = [rest.strip('"').strip("'")]
            continue
        if enabled is not None and indent > child_indent and body.startswith("- "):
            enabled.append(body[2:].strip().strip('"').strip("'"))
    if not in_collectors or enabled is None:
        return True
    return "ml" in enabled


def resolve_soak_config(root: pathlib.Path, seed_config: str | None) -> pathlib.Path:
    """Copy --seed-config after stripping side effects, or write a minimal config.

    Sibling monitor.yaml files are never read. Pointing --seed-data-dir at a
    production data directory must not pick up the deployment config.
    """
    cfg = root / "config.yaml"
    if seed_config:
        src = pathlib.Path(seed_config).resolve()
        if not src.is_file():
            raise SystemExit(f"seed config not found: {src}")
        cfg.write_text(strip_seed_config(src.read_text(encoding="utf-8")))
        return cfg
    cfg.write_text(
        "collectors:\n  enabled: [cpu, mem, load, disk, net]\n"
        "plugins:\n  enabled: false\nhealth:\n  enabled: false\n"
    )
    return cfg


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--seconds", type=int, default=120)
    p.add_argument("--bin", default="core/bin/monitord")
    p.add_argument("--output")
    p.add_argument("--sample-every", type=float, default=2.0, help="seconds between samples")
    p.add_argument("--heap-dir", help="directory for start/mid/end heap.pprof captures (admin API)")
    p.add_argument("--no-heap", action="store_true", help="disable heap profile capture")
    p.add_argument("--seed-data-dir", help="copy an existing agent data directory as the soak starting state")
    p.add_argument("--seed-config", help="config file to use; sibling configs are ignored")
    p.add_argument("--token", help="bearer token; default is web.token from --seed-config, else web-password")
    p.add_argument("--max-rss-mib", type=float, help="fail if RSS max exceeds this MiB")
    p.add_argument("--max-heap-growth-mib", type=float, help="fail if heap alloc growth exceeds this MiB")
    p.add_argument("--max-rss-growth-mib", type=float, help="fail if RSS growth exceeds this MiB")
    p.add_argument("--max-anomaly-buffer-mib", type=float, help="fail if ML train buffer exceeds this MiB")
    args = p.parse_args(argv)
    if args.seconds < 10:
        p.error("use at least 10 seconds")

    heap_dir = args.heap_dir
    if args.no_heap:
        heap_dir = None
    elif not heap_dir and args.seconds >= 3600:
        heap_dir = str(pathlib.Path("reports") / f"soak-heaps-{int(time.time())}")
    if args.seconds >= 86400:
        if args.max_rss_mib is None:
            args.max_rss_mib = 800
        if args.max_heap_growth_mib is None:
            args.max_heap_growth_mib = 200
        if args.max_rss_growth_mib is None:
            args.max_rss_growth_mib = 300
        if args.max_anomaly_buffer_mib is None:
            args.max_anomaly_buffer_mib = 16

    state: dict = {"stop": False, "signum": 0}

    with tempfile.TemporaryDirectory(prefix="monitor-soak-") as tmp:
        root = pathlib.Path(tmp)
        data = root / "data"
        if args.seed_data_dir:
            seed_src = pathlib.Path(args.seed_data_dir).resolve()
            if not seed_src.is_dir():
                raise SystemExit(f"seed data dir not found: {seed_src}")
            shutil.copytree(
                seed_src, data, symlinks=False, ignore_dangling_symlinks=True,
                ignore=shutil.ignore_patterns("monitord.pid", ".monitor.lock", "*.tmp"),
            )
            (data / "monitord.pid").unlink(missing_ok=True)
            (data / ".monitor.lock").unlink(missing_ok=True)
        else:
            data.mkdir()
        cfg = resolve_soak_config(root, args.seed_config)
        cfg_text = cfg.read_text(encoding="utf-8")
        configured_token = args.token if args.token else web_token(cfg_text)
        token = configured_token
        auth_kind = "token" if configured_token else "web-password"
        want_ml = expects_ml(cfg_text)
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        base = f"http://127.0.0.1:{port}"
        log = (root / "server.log").open("w")
        proc = subprocess.Popen(
            [str(pathlib.Path(args.bin).resolve()), "-config", str(cfg), "-data-dir", str(data), "-listen", f"127.0.0.1:{port}"],
            stdout=log, stderr=log,
        )
        latencies: list[float] = []
        rss: list[float] = []
        heap: list[float] = []
        series: list[int] = []
        anomaly: list[float] = []
        errors: list[str] = []
        alerts: list[str] = []
        heap_files: list[str] = []
        charts = 0

        def growth(samples):
            if len(samples) < 2:
                return None
            return samples[-1] - samples[0]

        def report_body(interrupted: bool) -> dict:
            rss_growth = growth(rss)
            heap_growth = growth(heap)
            local_alerts = list(alerts)
            if args.max_rss_mib is not None and rss and max(rss) > args.max_rss_mib:
                local_alerts.append(f"rss_mib_max {max(rss):.1f} > {args.max_rss_mib}")
            if args.max_rss_growth_mib is not None and rss_growth is not None and rss_growth > args.max_rss_growth_mib:
                local_alerts.append(f"rss_mib_growth {rss_growth:.1f} > {args.max_rss_growth_mib}")
            if args.max_heap_growth_mib is not None and heap_growth is not None and heap_growth > args.max_heap_growth_mib:
                local_alerts.append(f"heap_mib_growth {heap_growth:.1f} > {args.max_heap_growth_mib}")
            if args.max_anomaly_buffer_mib is not None and anomaly and max(anomaly) > args.max_anomaly_buffer_mib:
                local_alerts.append(f"anomaly_buffer_mib {max(anomaly):.1f} > {args.max_anomaly_buffer_mib}")
            if want_ml and args.max_anomaly_buffer_mib is not None and latencies and not anomaly:
                local_alerts.append("anomaly buffer metric missing")
            return {
                "duration_seconds": args.seconds,
                "interrupted": interrupted,
                "seed_data_dir": str(pathlib.Path(args.seed_data_dir).resolve()) if args.seed_data_dir else None,
                "seed_config": str(pathlib.Path(args.seed_config).resolve()) if args.seed_config else None,
                "auth": auth_kind,
                "config": str(cfg),
                "requests": len(latencies),
                "charts": charts,
                "query_ms_median": statistics.median(latencies) if latencies else None,
                "query_ms_max": max(latencies, default=0),
                "rss_mib_first": rss[0] if rss else None,
                "rss_mib_last": rss[-1] if rss else None,
                "rss_mib_max": max(rss, default=0),
                "rss_mib_growth": rss_growth,
                "heap_mib_first": heap[0] if heap else None,
                "heap_mib_last": heap[-1] if heap else None,
                "heap_mib_max": max(heap, default=0),
                "heap_mib_growth": heap_growth,
                "anomaly_buffer_mib_max": max(anomaly, default=0),
                "tsdb_series_first": series[0] if series else None,
                "tsdb_series_last": series[-1] if series else None,
                "tsdb_series_max": max(series, default=0),
                "disk_bytes": sum(f.stat().st_size for f in data.rglob("*") if f.is_file()),
                "heap_profiles": heap_files,
                "alerts": local_alerts,
                "errors": sorted(set(errors)),
                "scope": "local agent; pass --seed-config explicitly; CI hosted runs are ~5.5h, 24h needs a self-hosted runner",
                "thresholds": {
                    "max_rss_mib": args.max_rss_mib,
                    "max_rss_growth_mib": args.max_rss_growth_mib,
                    "max_heap_growth_mib": args.max_heap_growth_mib,
                    "max_anomaly_buffer_mib": args.max_anomaly_buffer_mib,
                },
            }

        def write_report(interrupted: bool, echo: bool) -> dict:
            body = report_body(interrupted)
            text = json.dumps(body, indent=2)
            if echo:
                print(text, flush=True)
            if args.output:
                path = pathlib.Path(args.output)
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text + "\n")
            return body

        def on_stop(signum, _frame):
            state["stop"] = True
            state["signum"] = signum
            errors.append(f"interrupted by signal {signum} before {args.seconds}s")
            write_report(True, True)
            raise SystemExit(1)

        signal.signal(signal.SIGTERM, on_stop)
        signal.signal(signal.SIGINT, on_stop)
        try:
            for _ in range(200):
                if state["stop"]:
                    break
                try:
                    urllib.request.urlopen(base + "/healthz", timeout=1).close()
                    break
                except OSError:
                    time.sleep(.1)
            if not token:
                token_path = data / "web-password"
                for _ in range(100):
                    if token_path.is_file() and token_path.stat().st_size > 0:
                        break
                    time.sleep(0.1)
                if not token_path.is_file():
                    raise RuntimeError("missing web-password after startup (pass --token or web.token in --seed-config)")
                token = token_path.read_text().strip()
            if not token:
                raise RuntimeError("empty bearer token")

            def authed(path):
                return urllib.request.Request(base + path, headers={"Authorization": "Bearer " + token})

            def capture_heap(label):
                if not heap_dir:
                    return
                dest_dir = pathlib.Path(heap_dir)
                dest_dir.mkdir(parents=True, exist_ok=True)
                dest = dest_dir / f"heap-{label}.pprof"
                try:
                    with urllib.request.urlopen(authed("/api/v1/debug/pprof/heap"), timeout=60) as r:
                        blob = r.read()
                    if len(blob) < 32:
                        errors.append(f"heap {label}: too small")
                        return
                    dest.write_bytes(blob)
                    heap_files.append(str(dest))
                except (OSError, urllib.error.URLError) as exc:
                    errors.append(f"heap {label}: {exc}")

            capture_heap("start")
            mid_at = time.monotonic() + args.seconds / 2
            mid_done = False
            deadline = time.monotonic() + args.seconds
            samples = 0
            while time.monotonic() < deadline and not state["stop"]:
                try:
                    started = time.perf_counter()
                    with urllib.request.urlopen(authed("/api/v1/data?chart=system.ram&after=-60&points=60"), timeout=10) as r:
                        json.load(r)
                    latencies.append((time.perf_counter() - started) * 1000)
                    with urllib.request.urlopen(authed("/api/v1/info"), timeout=10) as r:
                        info = json.load(r)
                    charts = info["charts_count"]
                    persistence = info.get("db", {}).get("persistence", {})
                    if persistence.get("error"):
                        errors.append("checkpoint: " + persistence["error"])
                    last = persistence.get("last_checkpoint", 0)
                    if last and time.time() - last > max(120, 2 * persistence.get("interval_seconds", 30)):
                        errors.append("checkpoint overdue")
                    errors.extend(c["name"] + ": " + c["error"] for c in info["collectors"] if c.get("error"))
                    usage = subprocess.check_output(["ps", "-o", "rss=", "-p", str(proc.pid)], text=True).strip()
                    rss.append(int(usage) / 1024)
                    with urllib.request.urlopen(authed("/metrics"), timeout=10) as r:
                        metrics = r.read().decode("utf-8", "replace")
                    hm = heap_re.search(metrics)
                    sm = series_re.search(metrics)
                    am = anomaly_re.search(metrics)
                    if hm:
                        heap.append(int(hm.group(1)) / (1024 * 1024))
                    if sm:
                        series.append(int(sm.group(1)))
                    if am:
                        anomaly.append(int(am.group(1)) / (1024 * 1024))
                    if proc.poll() is not None:
                        raise RuntimeError("agent exited")
                except Exception as e:
                    errors.append(str(e))
                samples += 1
                if samples % 10 == 0:
                    write_report(False, False)
                if not mid_done and time.monotonic() >= mid_at:
                    capture_heap("mid")
                    mid_done = True
                time.sleep(min(args.sample_every, max(0, deadline - time.monotonic())))
            capture_heap("end")
        finally:
            proc.send_signal(signal.SIGINT)
            try:
                proc.wait(timeout=120)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                errors.append("shutdown timed out")
            log.close()
        if state["stop"]:
            return 1
        body = write_report(False, True)
        if body["errors"] or body["alerts"]:
            return 1
        return 0


if __name__ == "__main__":
    sys.exit(main())
