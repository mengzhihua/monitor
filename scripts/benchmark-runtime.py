#!/usr/bin/env python3
"""Measure an isolated monitord on macOS/Linux with a fixed local HTTP workload.

Build the Web assets and binary first. By default results measure only the
daemon. --include-children adds whole-run CPU accounting from wait4 and sampled
direct-child RSS/CPU/launch lower bounds. 100% CPU is one CPU core.
Use --collectors cpu,mem,apps,netstat to compare a fixed collector selection.
Catalog IDs and sanitized collector status are captured outside measurement.
"""

import argparse
import concurrent.futures
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import socket
import statistics
import subprocess
import tempfile
import threading
import time


ROOT = Path(__file__).resolve().parent.parent
WORKLOAD = (
    ("info", "/api/v1/info", 2),
    ("charts", "/api/v1/charts", 2),
    ("data_ram", "/api/v1/data?chart=system.ram&after=-60&points=60", 4),
    ("data_cpu", "/api/v1/data?chart=system.cpu&after=-60&points=60", 4),
)


def seconds(value):
    number = float(value)
    if not math.isfinite(number) or number <= 0:
        raise argparse.ArgumentTypeError("must be a finite number greater than zero")
    return number


def collector_names(value):
    names = [name.strip() for name in value.split(",")]
    if not names or any(not re.fullmatch(r"[a-z][a-z0-9_-]*", name) for name in names):
        raise argparse.ArgumentTypeError("use comma-separated collector names (letters, digits, underscores, hyphens)")
    names = sorted(set(names))
    if not {"cpu", "mem"}.issubset(names):
        raise argparse.ArgumentTypeError("include cpu and mem for the fixed system.cpu/system.ram HTTP workload")
    return names


def collector_config(names):
    # Only a collector allowlist is configurable. Do not accept a deployment
    # config that could enable streaming, exports, credentials or data paths.
    if names is None:
        return ""
    return "collectors:\n  enabled: " + json.dumps(names, separators=(",", ":")) + "\n"


def catalog_snapshot(read, started):
    charts = read("/api/v1/charts")["charts"]
    statuses = read("/api/v1/collectors")["status"]
    catalog = {chart_id: sorted(dimension["id"] for dimension in (chart.get("dimensions") or []))
               for chart_id, chart in sorted(charts.items())}
    collectors = []
    for status in statuses:
        # Error text, plugin commands, labels and arbitrary metadata may carry
        # credentials. Persist only IDs and these explicitly typed counters.
        item = {"name": status["name"], "has_error": bool(status.get("error"))}
        if type(status.get("enabled")) is bool:
            item["enabled"] = status["enabled"]
        for field in ("runs", "failures", "last_run_ms"):
            if type(status.get(field)) is int:
                item[field] = status[field]
        collectors.append(item)
    return {
        "available": True, "elapsed_since_launch_seconds": time.monotonic() - started,
        "chart_count": len(catalog), "series_count": sum(len(dims) for dims in catalog.values()),
        "chart_dimensions": catalog, "collectors": sorted(collectors, key=lambda item: item["name"]),
    }


def validate_collectors(names, snapshot):
    if names is not None:
        actual = {status["name"] for status in snapshot["collectors"]}
        if actual != set(names):
            raise RuntimeError("selected collector status does not match requested names; missing="
                               + ",".join(sorted(set(names) - actual))
                               + "; unexpected=" + ",".join(sorted(actual - set(names))))


def percentile(values, fraction):
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)] if values else None


def cpu_seconds(value):
    days, separator, clock = value.partition("-")
    total = 0.0
    for part in (clock if separator else days).split(":"):
        total = total * 60 + float(part)
    return total + (int(days) * 86400 if separator else 0)


def sample(pid, include_children=False):
    started = time.monotonic()
    if include_children:
        # A single process table snapshot avoids launching ps once per child.
        # comm excludes command arguments, which might contain credentials.
        output = subprocess.check_output(
            ["ps", "-A", "-o", "pid=,ppid=,time=,rss=,comm="],
            text=True, env=dict(os.environ, LC_ALL="C"),
        )
        parent, children = None, []
        for line in output.splitlines():
            fields = line.strip().split(None, 4)
            if len(fields) != 5:
                continue
            child_pid, parent_pid = int(fields[0]), int(fields[1])
            if child_pid != pid and parent_pid != pid:
                continue
            point = {
                "pid": child_pid, "cpu_seconds": cpu_seconds(fields[2]),
                "rss_mib": int(fields[3]) / 1024,
                "command": Path(fields[4]).name,
            }
            if child_pid == pid:
                parent = point
            else:
                children.append(point)
        if parent is None:
            raise RuntimeError("daemon disappeared during process sampling")
        finished = time.monotonic()
        return {
            "elapsed_seconds": finished, "snapshot_duration_ms": (finished - started) * 1000,
            "cpu_seconds": parent["cpu_seconds"], "rss_mib": parent["rss_mib"],
            "children": children,
            "daemon_and_direct_children_rss_mib": parent["rss_mib"] + sum(child["rss_mib"] for child in children),
        }
    # BSD and procps ps both report cumulative CPU time and RSS in KiB here.
    output = subprocess.check_output(
        ["ps", "-p", str(pid), "-o", "time=,rss="],
        text=True, env=dict(os.environ, LC_ALL="C"),
    ).split()
    finished = time.monotonic()
    return {
        "elapsed_seconds": finished, "snapshot_duration_ms": (finished - started) * 1000,
        "cpu_seconds": cpu_seconds(output[0]),
        "rss_mib": int(output[1]) / 1024,
    }


class BenchmarkProcess:
    """Own one child and reap it exclusively through wait4, never Popen.wait.

    wait4 preserves CPU from waited descendants, including short-lived commands
    that can disappear between ps snapshots. It reports the complete lifetime;
    it cannot isolate just the steady-state measurement interval.
    """

    def __init__(self, command, **kwargs):
        self.started = time.monotonic()
        self._process = subprocess.Popen(command, **kwargs)
        self.pid = self._process.pid
        self.returncode = None
        self.usage = None
        self.usage_error = None
        self.wall_seconds = None
        self._ended = False

    def poll(self):
        if not self._ended:
            try:
                pid, status, usage = os.wait4(self.pid, os.WNOHANG)
            except ChildProcessError:
                # Do not invent zero CPU if a signal handler or other code
                # unexpectedly reaped the child before this owner could do so.
                self.usage_error = "child was already reaped; wait4 resource usage unavailable"
                self._ended = True
                self.returncode = self._process.returncode
                self._process.returncode = self.returncode if self.returncode is not None else 0
            else:
                if pid:
                    self._ended = True
                    self.returncode = os.WEXITSTATUS(status) if os.WIFEXITED(status) else -os.WTERMSIG(status)
                    self.usage = usage
                    # Prevent Popen's destructor from trying to reap the PID.
                    self._process.returncode = self.returncode
            if self._ended:
                self.wall_seconds = time.monotonic() - self.started
        return self.returncode if self.returncode is not None else (-1 if self._ended else None)

    def send_signal(self, value):
        if self.poll() is None:
            try:
                os.kill(self.pid, value)
            except ProcessLookupError:
                self.poll()

    def wait(self, timeout=None):
        deadline = None if timeout is None else time.monotonic() + timeout
        while self.poll() is None:
            if deadline is not None and time.monotonic() >= deadline:
                raise subprocess.TimeoutExpired("benchmark daemon", timeout)
            time.sleep(.02)
        return self.returncode

    def kill(self):
        self.send_signal(signal.SIGKILL)

    def lifetime_usage(self):
        scope = "whole lifetime including startup, warmup, measurement and shutdown; daemon plus descendants reaped by their parents"
        if self.usage is None:
            return {"available": False, "scope": scope, "reason": self.usage_error or "daemon has not been reaped"}
        cpu = self.usage.ru_utime + self.usage.ru_stime
        return {
            "available": True, "scope": scope, "wall_seconds": self.wall_seconds,
            "user_cpu_seconds": self.usage.ru_utime, "system_cpu_seconds": self.usage.ru_stime,
            "cpu_seconds": cpu, "cpu_one_core_percent": 100 * cpu / self.wall_seconds,
            "not_steady_state": True,
        }


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGINT)
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def child_summary(samples, elapsed, daemon_cpu):
    # Unique observed PIDs are only a launch lower bound. Short-lived commands
    # may start and finish between snapshots; PID reuse can also undercount.
    initial = {child["pid"]: child["cpu_seconds"] for child in samples[0]["children"]}
    observed = {}
    launches = {}
    for point in samples:
        for child in point["children"]:
            pid = child["pid"]
            if pid not in observed:
                observed[pid] = dict(child)
                if pid not in initial:
                    command = child["command"]
                    launches[command] = launches.get(command, 0) + 1
            else:
                observed[pid]["cpu_seconds"] = max(observed[pid]["cpu_seconds"], child["cpu_seconds"])
    cpu = sum(max(0, child["cpu_seconds"] - initial.get(pid, 0)) for pid, child in observed.items())
    rss = [point["daemon_and_direct_children_rss_mib"] for point in samples]
    return {
        "scope": "daemon and direct children only, during measurement",
        "cpu_and_launches_are_observed_lower_bounds": True,
        "limitations": "sampling can miss short-lived commands, final CPU time and RSS peaks; RSS mean/p95 are point-sampled statistics, not continuous measurements; PID reuse can undercount; summed RSS double-counts shared pages",
        "direct_children_present_at_start": len(initial),
        "distinct_direct_child_pids_observed": len(observed),
        "direct_child_launches_observed_lower_bound": sum(launches.values()),
        "launches_by_command_lower_bound": dict(sorted(launches.items())),
        "direct_child_cpu_seconds_observed_lower_bound": cpu,
        "daemon_and_direct_children_cpu_one_core_percent_lower_bound": 100 * (daemon_cpu + cpu) / elapsed,
        "daemon_and_direct_children_rss_mib_sampled": {
            "mean": statistics.mean(rss), "p95": percentile(rss, .95),
            "max_observed_lower_bound": max(rss), "start": rss[0], "end": rss[-1],
        },
    }


def sampling_summary(samples, interval):
    gaps = [right["elapsed_seconds"] - left["elapsed_seconds"]
            for left, right in zip(samples, samples[1:])]
    costs = [point["snapshot_duration_ms"] for point in samples]
    return {
        "sample_count": len(samples), "nominal_wait_seconds": interval,
        "actual_interval_seconds": {
            "mean": statistics.mean(gaps) if gaps else None,
            "p95": percentile(gaps, .95), "max": max(gaps) if gaps else None,
        },
        "snapshot_duration_ms": {
            "mean": statistics.mean(costs), "p95": percentile(costs, .95), "max": max(costs),
        },
        "scope": "ps invocation plus parsing; actual intervals include this observer overhead and scheduling delays",
    }


def measure(process, port, token, duration, include_children=False, child_interval=.2):
    headers = {"Authorization": "Bearer " + token}
    barrier = threading.Barrier(len(WORKLOAD) + 1)
    cancelled = threading.Event()

    def worker(name, path, frequency):
        latencies, sizes, errors = [], [], {}
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        barrier.wait()
        started = time.monotonic()
        index = 0
        try:
            while not cancelled.is_set() and time.monotonic() < started + duration:
                due = started + index / frequency
                if cancelled.wait(max(0, due - time.monotonic())):
                    break
                if time.monotonic() >= started + duration:
                    break
                tick = time.monotonic()
                try:
                    connection.request("GET", path, headers=headers)
                    response = connection.getresponse()
                    body = response.read()
                    latency = (time.monotonic() - tick) * 1000
                    if response.status == 200:
                        latencies.append(latency)
                        sizes.append(len(body))
                    else:
                        key = "HTTP " + str(response.status)
                        errors[key] = errors.get(key, 0) + 1
                except (OSError, http.client.HTTPException) as error:
                    key = type(error).__name__
                    errors[key] = errors.get(key, 0) + 1
                    connection.close()
                    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
                index += 1
        finally:
            connection.close()
        return name, {
            "requests": len(latencies), "errors": errors,
            "p50_ms": percentile(latencies, .5), "p95_ms": percentile(latencies, .95),
            "p99_ms": percentile(latencies, .99), "max_ms": max(latencies) if latencies else None,
            "mean_ms": statistics.mean(latencies) if latencies else None,
            "mean_response_bytes": statistics.mean(sizes) if sizes else None,
        }

    first = sample(process.pid, include_children)
    interval = child_interval if include_children else 1
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(WORKLOAD)) as pool:
        try:
            workers = [pool.submit(worker, *work) for work in WORKLOAD]
            barrier.wait()
            samples = [first]
            while time.monotonic() - first["elapsed_seconds"] < duration:
                time.sleep(min(interval, max(0, duration - (time.monotonic() - first["elapsed_seconds"]))))
                samples.append(sample(process.pid, include_children))
            endpoints = dict(worker.result() for worker in workers)
        finally:
            cancelled.set()
            barrier.abort()
    last = samples[-1]
    elapsed = last["elapsed_seconds"] - first["elapsed_seconds"]
    cpu = last["cpu_seconds"] - first["cpu_seconds"]
    rss = [point["rss_mib"] for point in samples]
    result = {
        "endpoints": endpoints, "measured_wall_seconds": elapsed,
        "cpu_seconds": cpu, "cpu_one_core_percent": 100 * cpu / elapsed,
        "rss_mib": {"mean": statistics.mean(rss), "p95": percentile(rss, .95),
                    "max": max(rss), "start": rss[0], "end": rss[-1]},
        "process_sampling": sampling_summary(samples, interval),
        "samples": [dict(point, elapsed_seconds=point["elapsed_seconds"] - first["elapsed_seconds"])
                    for point in samples],
    }
    if include_children:
        result["child_process_sampling"] = child_summary(samples, elapsed, cpu)
    return result


def run(args, result, directory):
    config = directory / "benchmark.yaml"
    configuration = collector_config(args.collectors)
    config.write_text(configuration)
    result["method"].update({
        "default_collectors": args.collectors is None,
        "selected_collectors": args.collectors,
        "config_sha256": hashlib.sha256(configuration.encode("utf-8")).hexdigest(),
        "config_scope": "empty defaults" if args.collectors is None else "generated collector allowlist only",
        "catalog_snapshots": "before and after measurement; outside steady-state samples, included in lifetime CPU",
    })
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    with (directory / "server.log").open("w") as log:
        started = time.monotonic()
        process = BenchmarkProcess(
            [str(args.binary), "-config", str(config), "-listen", f"127.0.0.1:{port}",
             "-data-dir", str(directory / "data")], cwd=directory,
            stdout=log, stderr=subprocess.STDOUT,
        )
        headers = {}

        def read(path):
            connection = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
            try:
                connection.request("GET", path, headers=headers)
                response = connection.getresponse()
                body = response.read()
                if response.status != 200:
                    raise RuntimeError("HTTP " + str(response.status))
                return json.loads(body)
            finally:
                connection.close()

        try:
            while time.monotonic() - started < 30:
                if process.poll() is not None:
                    raise RuntimeError("daemon exited before becoming ready")
                credential = directory / "data" / "web-password"
                if credential.exists():
                    token = credential.read_text().strip()
                    headers = {"Authorization": "Bearer " + token}
                    try:
                        read("/api/v1/info")
                        break
                    except (OSError, http.client.HTTPException, RuntimeError):
                        pass
                time.sleep(.1)
            else:
                raise RuntimeError("daemon did not become ready within 30 seconds")
            result["startup_ready_ms"] = (time.monotonic() - started) * 1000
            time.sleep(args.warmup)
            before = catalog_snapshot(read, started)
            result["catalog_snapshots"] = {"before": before}
            result["chart_count"] = before["chart_count"]
            result["series_count"] = before["series_count"]
            validate_collectors(args.collectors, before)
            for chart in ("system.ram", "system.cpu"):
                rows = read(f"/api/v1/data?chart={chart}&after=-60&points=60").get("result", {}).get("data", [])
                if not any(value is not None for row in rows for value in row[1:]):
                    raise RuntimeError("required chart has no populated samples: " + chart)
            result.update(measure(process, port, token, args.duration, args.include_children, args.child_sample_interval))
            try:
                result["catalog_snapshots"]["after"] = catalog_snapshot(read, started)
            except Exception as error:
                result["catalog_snapshots"]["after"] = {"available": False, "error_type": type(error).__name__}
                raise RuntimeError("post-measurement catalog snapshot failed (" + type(error).__name__ + ")") from None
        finally:
            stop(process)
            result["exit_code"] = process.returncode
            if args.include_children:
                result["process_tree_lifetime"] = process.lifetime_usage()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "core/bin/monitord")
    parser.add_argument("--label", default="local")
    parser.add_argument("--output", type=Path, help="JSON path; default: new directory in the system temp directory")
    parser.add_argument("--warmup", type=seconds, default=15, help="warmup seconds (default: 15)")
    parser.add_argument("--duration", type=seconds, default=60, help="measurement seconds (default: 60)")
    parser.add_argument("--collectors", type=collector_names, help="fixed comma-separated collector names; must include cpu,mem; default: all with an empty config")
    parser.add_argument("--include-children", action="store_true", help="add whole-run wait4 CPU and sampled direct-child resource/launch lower bounds")
    parser.add_argument("--child-sample-interval", type=seconds, default=.2, help="seconds between direct-child snapshots when enabled (default: 0.2, plus ps runtime)")
    args = parser.parse_args()
    if platform.system() not in ("Darwin", "Linux"):
        parser.error("only macOS and Linux are supported (BSD/procps ps required)")
    if not shutil.which("ps"):
        parser.error("ps is required")
    args.binary = args.binary.resolve()
    if not args.binary.is_file() or not os.access(args.binary, os.X_OK):
        parser.error("binary must exist and be executable; build core/bin/monitord first")
    if args.output is None:
        args.output = Path(tempfile.mkdtemp(prefix="monitor-runtime-result-")) / "result.json"
    args.output = args.output.resolve()
    args.output.parent.mkdir(parents=True, exist_ok=True)

    def interrupt(_signal, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupt)
    digest = hashlib.sha256()
    with args.binary.open("rb") as binary:
        for chunk in iter(lambda: binary.read(1 << 20), b""):
            digest.update(chunk)
    result = {
        "label": args.label, "binary": str(args.binary), "binary_sha256": digest.hexdigest(),
        "host": {"os": platform.platform(), "arch": platform.machine(), "logical_cpus": os.cpu_count()},
        "method": {
            "warmup_seconds": args.warmup, "measurement_seconds": args.duration,
            "new_empty_data_dir": True, "default_collectors": True,
            "authentication": "generated temporary Bearer credential",
            "http": "one keep-alive connection per worker; scheduled requests catch up after delays",
            "workers_hz": {name: frequency for name, _, frequency in WORKLOAD},
            "cpu_definition": "daemon CPU seconds delta / wall seconds * 100; 100% = one CPU core",
            "rss_definition": "daemon resident memory sampled via ps",
            "sample_interval_seconds": args.child_sample_interval if args.include_children else 1,
        },
    }
    if args.include_children:
        result["method"]["children"] = {
            "lifetime_cpu": "wait4 on only the self-started daemon; includes waited descendants and startup/warmup/shutdown",
            "measurement_samples": "ps direct children only; CPU and command launches are observed lower bounds",
            "sampling_overhead": "process-table snapshots add observer work; use identical intervals for A/B; observer ps is excluded from daemon wait4 accounting",
        }
    status = 0
    with tempfile.TemporaryDirectory(prefix="monitor-runtime-") as temporary:
        directory = Path(temporary)
        try:
            run(args, result, directory)
            if result["exit_code"] != 0 or any(endpoint["errors"] or not endpoint["requests"] for endpoint in result["endpoints"].values()):
                status = 1
        except KeyboardInterrupt:
            result["error"] = "interrupted"
            status = 130
        except Exception as error:
            result["error"] = str(error)
            status = 1
        finally:
            if (directory / "server.log").exists():
                shutil.copyfile(directory / "server.log", args.output.with_suffix(".server.log"))
            args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({key: value for key, value in result.items() if key != "samples"}, indent=2))
    print("Full result:", args.output)
    return status


if __name__ == "__main__":
    raise SystemExit(main())
