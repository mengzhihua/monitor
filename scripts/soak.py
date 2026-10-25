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

import argparse, datetime, json, math, os, pathlib, re, shutil, signal, socket, statistics, subprocess, sys, tempfile, time, urllib.error, urllib.request
from dataclasses import dataclass

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


@dataclass(frozen=True)
class ClockReading:
    wall: float
    monotonic: float
    boottime: float | None = None


def read_clocks() -> ClockReading:
    boot = None
    if hasattr(time, "CLOCK_BOOTTIME"):
        try:
            boot = time.clock_gettime(time.CLOCK_BOOTTIME)
        except OSError:
            pass
    return ClockReading(time.time(), time.monotonic(), boot)


def utc_stamp(wall: float) -> str:
    return datetime.datetime.fromtimestamp(wall, datetime.timezone.utc).isoformat().replace("+00:00", "Z")


def clock_span(start: ClockReading, end: ClockReading) -> dict:
    return {
        "wall_seconds": end.wall - start.wall,
        "monotonic_seconds": end.monotonic - start.monotonic,
        "boottime_seconds": end.boottime - start.boottime if start.boottime is not None and end.boottime is not None else None,
    }


def elapsed_seconds(start: ClockReading, end: ClockReading) -> float:
    # Monotonic clocks differ by platform in whether they count system sleep.
    # Any clock reaching the deadline ends the run; disagreements fail validation.
    return max(0.0, *(value for value in clock_span(start, end).values() if value is not None))


class SamplingTimeline:
    """Sampling evidence, independent of platform-specific sleep-clock semantics."""
    def __init__(self, started: ClockReading, max_gap: float, sample_every: float):
        self.started = started
        self.max_gap = max_gap
        self.clock_tolerance = max(2.0, min(10.0, sample_every * 0.1))
        self.sampling_started: ClockReading | None = None
        self.sampling_ended: ClockReading | None = None
        self.last_boundary: ClockReading | None = None
        self.samples: list[dict] = []
        self.gaps: list[dict] = []
        self.gap_count = 0
        self.clock_changes: list[dict] = []
        self.clock_change_count = 0

    def start_sampling(self, now: ClockReading) -> None:
        self.sampling_started = self.last_boundary = now

    def observe(self, now: ClockReading, phase: str) -> dict | None:
        if self.last_boundary is None or self.sampling_ended is not None:
            return None
        span = clock_span(self.last_boundary, now)
        values = [v for v in span.values() if v is not None]
        uncertain = min(values) < 0 or max(values) - min(values) > self.clock_tolerance
        entry = {
            "from_utc": utc_stamp(self.last_boundary.wall), "to_utc": utc_stamp(now.wall),
            "phase": phase, **span,
            "classification": "clock_disagreement_unknown_cause" if uncertain else "sampling_gap_unknown_cause",
        }
        if uncertain:
            self.clock_change_count += 1
            self.clock_changes.append(entry)
            del self.clock_changes[:-100]
        if max(values) > self.max_gap:
            self.gap_count += 1
            self.gaps.append(entry)
            del self.gaps[:-100]
        self.last_boundary = now
        return span

    def finish_sampling(self, now: ClockReading) -> None:
        if self.sampling_started is not None and self.sampling_ended is None:
            self.observe(now, "sampling_end")
            self.sampling_ended = now

    def record_sample(self, start: ClockReading, end: ClockReading, interval: dict | None) -> None:
        self.samples.append({
            "index": len(self.samples) + 1,
            "started_at_utc": utc_stamp(start.wall), "finished_at_utc": utc_stamp(end.wall),
            "since_previous_sample": interval, "duration": clock_span(start, end),
        })

    def alerts(self) -> list[str]:
        out = []
        if self.gap_count:
            out.append(f"sampling gaps: {self.gap_count} intervals exceeded {self.max_gap:g}s; continuous coverage not established")
        if self.clock_change_count:
            out.append(f"clock disagreements: {self.clock_change_count}; elapsed time is uncertain (cause unknown)")
        return out

    def report(self, now: ClockReading) -> dict:
        return {
            "started_at_utc": utc_stamp(self.started.wall), "updated_at_utc": utc_stamp(now.wall),
            "elapsed": clock_span(self.started, now),
            "sampling_started_at_utc": utc_stamp(self.sampling_started.wall) if self.sampling_started else None,
            "sampling_ended_at_utc": utc_stamp(self.sampling_ended.wall) if self.sampling_ended else None,
            "sampling_elapsed": clock_span(self.sampling_started, self.sampling_ended or now) if self.sampling_started else None,
            "boottime_available": self.started.boottime is not None,
            "max_sample_gap_seconds": self.max_gap,
            "sample_count": len(self.samples), "sample_timeline": self.samples,
            "gap_count": self.gap_count, "gaps": self.gaps,
            "clock_disagreement_count": self.clock_change_count, "clock_disagreements": self.clock_changes,
            "diagnostic_entries_limit": 100,
        }


def atomic_write_report(path: pathlib.Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    name = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", suffix=".tmp", delete=False) as out:
            name = out.name
            out.write(text + "\n")
            out.flush()
            os.fsync(out.fileno())
        os.replace(name, path)
    finally:
        if name is not None:
            pathlib.Path(name).unlink(missing_ok=True)


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


def heap_marks(seconds: int, every: int) -> list[int]:
    """Elapsed seconds for heap profiles. Always includes the start and the end."""
    marks = [0]
    if every > 0:
        t = every
        while t < seconds:
            marks.append(t)
            t += every
    if marks[-1] != seconds:
        marks.append(seconds)
    return marks


def default_heap_every(seconds: int) -> int:
    """30 minutes below 4 hours, then every 2 hours for the long watch."""
    if seconds >= 4 * 3600:
        return 2 * 3600
    if seconds >= 1800:
        return 1800
    return 0


def heap_label(elapsed: int) -> str:
    hours, rem = divmod(max(elapsed, 0), 3600)
    minutes, seconds = divmod(rem, 60)
    if seconds:
        return f"t{hours:02d}h{minutes:02d}m{seconds:02d}s"
    return f"t{hours:02d}h{minutes:02d}m"


def apply_duration_thresholds(seconds: int, args) -> None:
    """Fill unset memory caps. 24h keeps the looser lab limits; 2h+ uses the hosted limits."""
    if seconds >= 86400:
        caps = (800, 200, 300, 16)
    elif seconds >= 7200:
        caps = (450, 80, 120, 16)
    else:
        return
    if args.max_rss_mib is None:
        args.max_rss_mib = caps[0]
    if args.max_heap_growth_mib is None:
        args.max_heap_growth_mib = caps[1]
    if args.max_rss_growth_mib is None:
        args.max_rss_growth_mib = caps[2]
    if args.max_anomaly_buffer_mib is None:
        args.max_anomaly_buffer_mib = caps[3]


def heap_diff_top(older: pathlib.Path, newer: pathlib.Path) -> str:
    """Text diff of two heap profiles. Empty when go tool pprof cannot run."""
    go = shutil.which("go")
    if not go or not older.is_file() or not newer.is_file():
        return ""
    proc = subprocess.run(
        [go, "tool", "pprof", "-top", "-nodecount=20", f"-diff_base={older}", str(newer)],
        capture_output=True, text=True, timeout=120,
    )
    text = (proc.stdout or "").strip()
    if proc.returncode != 0:
        err = (proc.stderr or "").strip()
        return (text + "\n" + err).strip()[:4000]
    return text[:4000]


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--seconds", type=int, default=120)
    p.add_argument("--bin", default="core/bin/monitord")
    p.add_argument("--output")
    p.add_argument("--sample-every", type=float, default=2.0, help="seconds between samples")
    p.add_argument("--max-sample-gap", type=float, help="fail above this sampling interval in seconds (default max(120, 3 * sample-every))")
    p.add_argument("--report-every", type=float, default=1800.0, help="seconds between atomic intermediate reports (default 1800)")
    p.add_argument("--heap-dir", help="directory for periodic heap.pprof captures (admin API)")
    p.add_argument("--heap-every", type=int, default=None, help="seconds between heap profiles (default 1800 from 30 minutes, 7200 from 4 hours)")
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
    if args.max_sample_gap is None:
        args.max_sample_gap = max(120.0, 3 * args.sample_every)
    for name in ("sample_every", "max_sample_gap", "report_every"):
        value = getattr(args, name)
        if not math.isfinite(value) or value <= 0:
            p.error(f"--{name.replace('_', '-')} must be a finite positive number")

    heap_dir = args.heap_dir
    if args.no_heap:
        heap_dir = None
    elif not heap_dir and args.seconds >= 1800:
        heap_dir = str(pathlib.Path("reports") / f"soak-heaps-{int(time.time())}")
    heap_every = args.heap_every
    if heap_every is None:
        heap_every = default_heap_every(args.seconds)
    if heap_every < 0:
        p.error("--heap-every must be >= 0")
    marks = heap_marks(args.seconds, heap_every)
    apply_duration_thresholds(args.seconds, args)

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
        proc = None
        timeline = SamplingTimeline(read_clocks(), args.max_sample_gap, args.sample_every)
        last_report = timeline.started
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

        def report_body(interrupted: bool, now: ClockReading) -> dict:
            rss_growth = growth(rss)
            heap_growth = growth(heap)
            local_alerts = list(alerts) + timeline.alerts()
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
                **timeline.report(now),
                "report_every_seconds": args.report_every,
                "sample_every_seconds": args.sample_every,
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
                "heap_every_seconds": heap_every,
                "heap_diff_top": state.get("heap_diff_top") or "",
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
            nonlocal last_report
            now = read_clocks()
            body = report_body(interrupted, now)
            text = json.dumps(body, indent=2)
            if echo:
                print(text, flush=True)
            if args.output:
                atomic_write_report(pathlib.Path(args.output), text)
            last_report = now
            return body

        def on_stop(signum, _frame):
            if state["stop"]:
                return
            state["stop"] = True
            state["signum"] = signum
            errors.append(f"interrupted by signal {signum} before {args.seconds}s")
            timeline.finish_sampling(read_clocks())
            write_report(True, True)
            raise SystemExit(1)

        previous_handlers = {sig: signal.signal(sig, on_stop) for sig in (signal.SIGTERM, signal.SIGINT)}
        try:
            write_report(False, False)
            proc = subprocess.Popen(
                [str(pathlib.Path(args.bin).resolve()), "-config", str(cfg), "-data-dir", str(data), "-listen", f"127.0.0.1:{port}"],
                stdout=log, stderr=log,
            )
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

            timeline.start_sampling(read_clocks())
            capture_heap(heap_label(0))
            next_mark = 1
            while not state["stop"]:
                sample_started = read_clocks()
                elapsed = elapsed_seconds(timeline.sampling_started, sample_started)
                interval = timeline.observe(sample_started, "sampling_end" if elapsed >= args.seconds else "sample_start")
                if elapsed >= args.seconds:
                    timeline.finish_sampling(sample_started)
                    break
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
                sample_finished = read_clocks()
                timeline.record_sample(sample_started, sample_finished, interval)
                if elapsed_seconds(last_report, sample_finished) >= args.report_every:
                    write_report(False, False)
                elapsed = elapsed_seconds(timeline.sampling_started, sample_finished)
                while next_mark < len(marks) - 1 and elapsed >= marks[next_mark]:
                    capture_heap(heap_label(marks[next_mark]))
                    next_mark += 1
                sleep_started = read_clocks()
                while not state["stop"]:
                    now = read_clocks()
                    remaining = min(args.sample_every - elapsed_seconds(sleep_started, now),
                                    args.seconds - elapsed_seconds(timeline.sampling_started, now))
                    if remaining <= 0:
                        break
                    until_report = args.report_every - elapsed_seconds(last_report, now)
                    if until_report <= 0:
                        write_report(False, False)
                        continue
                    time.sleep(min(remaining, until_report))
            timeline.finish_sampling(read_clocks())
            capture_heap(heap_label(args.seconds))
            if len(heap_files) >= 2:
                state["heap_diff_top"] = heap_diff_top(pathlib.Path(heap_files[0]), pathlib.Path(heap_files[-1]))
        except Exception as exc:
            errors.append(str(exc))
        finally:
            timeline.finish_sampling(read_clocks())
            try:
                if proc is not None:
                    if proc.poll() is None:
                        proc.send_signal(signal.SIGINT)
                    try:
                        proc.wait(timeout=120)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait()
                        errors.append("shutdown timed out")
            finally:
                log.close()
                try:
                    write_report(state["stop"], False)
                finally:
                    for sig, handler in previous_handlers.items():
                        signal.signal(sig, handler)
        if state["stop"]:
            return 1
        body = write_report(False, True)
        if body["errors"] or body["alerts"]:
            return 1
        return 0


if __name__ == "__main__":
    sys.exit(main())
