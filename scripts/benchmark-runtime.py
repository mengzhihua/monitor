#!/usr/bin/env python3
"""Measure an isolated monitord on macOS/Linux with a fixed local HTTP workload.

Build the Web assets and binary first. Results measure only the daemon process,
not helper processes, browsers, or system-wide load. 100% CPU is one CPU core.
"""

import argparse
import concurrent.futures
import http.client
import json
import math
import os
from pathlib import Path
import platform
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


def percentile(values, fraction):
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)] if values else None


def cpu_seconds(value):
    days, separator, clock = value.partition("-")
    total = 0.0
    for part in (clock if separator else days).split(":"):
        total = total * 60 + float(part)
    return total + (int(days) * 86400 if separator else 0)


def sample(pid):
    # BSD and procps ps both report cumulative CPU time and RSS in KiB here.
    output = subprocess.check_output(
        ["ps", "-p", str(pid), "-o", "time=,rss="],
        text=True, env=dict(os.environ, LC_ALL="C"),
    ).split()
    return {
        "elapsed_seconds": time.monotonic(),
        "cpu_seconds": cpu_seconds(output[0]),
        "rss_mib": int(output[1]) / 1024,
    }


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGINT)
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def measure(process, port, token, duration):
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

    first = sample(process.pid)
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(WORKLOAD)) as pool:
        try:
            workers = [pool.submit(worker, *work) for work in WORKLOAD]
            barrier.wait()
            samples = [first]
            while time.monotonic() - first["elapsed_seconds"] < duration:
                time.sleep(min(1, max(0, duration - (time.monotonic() - first["elapsed_seconds"]))))
                samples.append(sample(process.pid))
            endpoints = dict(worker.result() for worker in workers)
        finally:
            cancelled.set()
            barrier.abort()
    last = samples[-1]
    elapsed = last["elapsed_seconds"] - first["elapsed_seconds"]
    cpu = last["cpu_seconds"] - first["cpu_seconds"]
    rss = [point["rss_mib"] for point in samples]
    return {
        "endpoints": endpoints, "measured_wall_seconds": elapsed,
        "cpu_seconds": cpu, "cpu_one_core_percent": 100 * cpu / elapsed,
        "rss_mib": {"mean": statistics.mean(rss), "p95": percentile(rss, .95),
                    "max": max(rss), "start": rss[0], "end": rss[-1]},
        "samples": [dict(point, elapsed_seconds=point["elapsed_seconds"] - first["elapsed_seconds"])
                    for point in samples],
    }


def run(args, result, directory):
    config = directory / "empty.yaml"
    config.write_text("")
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    with (directory / "server.log").open("w") as log:
        started = time.monotonic()
        process = subprocess.Popen(
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
            charts = read("/api/v1/charts").get("charts", {})
            result["chart_count"] = len(charts)
            result["series_count"] = sum(len(chart.get("dimensions", {})) for chart in charts.values())
            for chart in ("system.ram", "system.cpu"):
                rows = read(f"/api/v1/data?chart={chart}&after=-60&points=60").get("result", {}).get("data", [])
                if not any(value is not None for row in rows for value in row[1:]):
                    raise RuntimeError("required chart has no populated samples: " + chart)
            result.update(measure(process, port, token, args.duration))
        finally:
            stop(process)
            result["exit_code"] = process.returncode


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "core/bin/monitord")
    parser.add_argument("--label", default="local")
    parser.add_argument("--output", type=Path, help="JSON path; default: new directory in the system temp directory")
    parser.add_argument("--warmup", type=seconds, default=15, help="warmup seconds (default: 15)")
    parser.add_argument("--duration", type=seconds, default=60, help="measurement seconds (default: 60)")
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
    result = {
        "label": args.label, "binary": str(args.binary),
        "host": {"os": platform.platform(), "arch": platform.machine(), "logical_cpus": os.cpu_count()},
        "method": {
            "warmup_seconds": args.warmup, "measurement_seconds": args.duration,
            "new_empty_data_dir": True, "default_collectors": True,
            "authentication": "generated temporary Bearer credential",
            "http": "one keep-alive connection per worker; scheduled requests catch up after delays",
            "workers_hz": {name: frequency for name, _, frequency in WORKLOAD},
            "cpu_definition": "daemon CPU seconds delta / wall seconds * 100; 100% = one CPU core",
            "rss_definition": "daemon resident memory sampled every second via ps",
        },
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
