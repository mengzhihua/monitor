#!/usr/bin/env python3
"""Read-only replacement resource sampler for an existing monitord (Linux/macOS).

Example:
  python3 scripts/sample-resources.py --seconds 86400 --pid-file /path/monitord.pid \
      --output reports/resources.json

Every sample discovers and revalidates the process. Multiple matches produce a
monitoring gap; use --pid-file and/or --binary to restrict the target. The JSON
summary is atomically replaced every 1800 seconds and on SIGINT/SIGTERM/expiry.
Individual observations are flushed to a sibling .samples.jsonl file. Existing
outputs are refused. No monitored process is started, signalled or stopped.
Only executable identity is read, never command arguments or environment.
"""
from __future__ import annotations

import argparse
from collections import Counter
import ctypes
import json
import math
import os
from pathlib import Path
import signal
import stat
import subprocess
import sys
import tempfile
import threading
import time


class ProbeError(Exception):
    pass


def positive(value):
    value = float(value)
    if not math.isfinite(value) or value <= 0:
        raise argparse.ArgumentTypeError("must be finite and greater than zero")
    return value


def boottime_clock():
    if hasattr(time, "CLOCK_BOOTTIME"):
        return lambda: time.clock_gettime(time.CLOCK_BOOTTIME), "CLOCK_BOOTTIME"
    if sys.platform == "darwin":
        try:
            lib = ctypes.CDLL("/usr/lib/libSystem.B.dylib")

            class Timebase(ctypes.Structure):
                _fields_ = [("numer", ctypes.c_uint32), ("denom", ctypes.c_uint32)]

            base = Timebase()
            lib.mach_timebase_info.argtypes = [ctypes.POINTER(Timebase)]
            lib.mach_timebase_info.restype = ctypes.c_int
            lib.mach_continuous_time.argtypes = []
            lib.mach_continuous_time.restype = ctypes.c_uint64
            if lib.mach_timebase_info(ctypes.byref(base)) == 0 and base.denom:
                scale = base.numer / base.denom / 1e9
                return lambda: lib.mach_continuous_time() * scale, "mach_continuous_time"
        except (OSError, AttributeError):
            pass
    return None, None


class Clock:
    def __init__(self):
        self.boot, self.source = boottime_clock()

    def now(self):
        return {"wall": time.time(), "monotonic": time.monotonic(),
                "boottime": self.boot() if self.boot else None}


def elapsed(start, end):
    key = "boottime" if start["boottime"] is not None and end["boottime"] is not None else "monotonic"
    return end[key] - start[key]


def read_pid(path):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK)
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):
                raise ProbeError("invalid_pid_file")
            value = os.read(fd, 129)
        finally:
            os.close(fd)
    except FileNotFoundError:
        raise ProbeError("pid_file_missing") from None
    if len(value) > 128 or not value.strip().isdigit() or int(value) <= 1:
        raise ProbeError("invalid_pid_file")
    return int(value)


def cpu_seconds(value):
    days, separator, clock = value.partition("-")
    result = 0.0
    for part in (clock if separator else days).split(":"):
        result = result * 60 + float(part)
    return result + (int(days) * 86400 if separator else 0)


class ProcessProbe:
    def __init__(self, pid_file=None, binary=None, platform=None, proc_root=Path("/proc"), runner=subprocess.run):
        self.pid_file = pid_file
        self.binary = binary
        self.platform = platform or sys.platform
        self.proc_root = proc_root
        self.runner = runner
        self.ticks = os.sysconf("SC_CLK_TCK")
        self.page_size = os.sysconf("SC_PAGE_SIZE")

    def linux_record(self, pid):
        directory = self.proc_root / str(pid)
        # Read the start tick on both sides of exe lookup to reject PID reuse.
        def fields():
            text = (directory / "stat").read_text()
            return text[text.rfind(")") + 2:].split()
        before = fields()
        executable = os.readlink(directory / "exe")
        executable_stat = os.stat(directory / "exe")
        after = fields()
        if len(before) < 22 or len(after) < 22:
            raise ProbeError("probe_error")
        if before[19] != after[19]:
            raise ProbeError("identity_changed")
        if after[0] == "Z":
            raise ProcessLookupError()
        return {"pid": pid, "identity": [pid, after[19], executable_stat.st_dev, executable_stat.st_ino],
                "identity_source": "proc_start_ticks_and_executable_inode", "executable": executable,
                "cpu_seconds": (int(after[11]) + int(after[12])) / self.ticks,
                "rss_bytes": max(0, int(after[21])) * self.page_size}

    def mac_records(self, pid, timeout):
        columns = "pid=,stat=,lstart=,time=,rss=,comm="
        command = ["ps", "-p", str(pid), "-o", columns] if pid else ["ps", "-axo", columns]
        result = self.runner(command, capture_output=True, text=True, timeout=timeout,
                             env=dict(os.environ, LC_ALL="C"))
        if result.returncode not in (0, 1):
            raise ProbeError("probe_error")
        rows = []
        for line in result.stdout.splitlines():
            fields = line.split(None, 9)
            if len(fields) != 10 or fields[1].startswith("Z"):
                continue
            executable = fields[9]
            rows.append({"pid": int(fields[0]), "identity": [int(fields[0]), " ".join(fields[2:7]), executable],
                         "identity_source": "ps_lstart_seconds_and_executable", "executable": executable,
                         "cpu_seconds": cpu_seconds(fields[7]), "rss_bytes": int(fields[8]) * 1024})
        return rows

    def sample(self, timeout):
        deadline = time.monotonic() + timeout
        def remaining():
            left = deadline - time.monotonic()
            if left <= 0:
                raise ProbeError("timeout")
            return left
        try:
            pid = read_pid(self.pid_file) if self.pid_file else None
            binary = str(Path(self.binary).resolve()) if self.binary else None
            def matches(row):
                path = row["executable"]
                if path.endswith(" (deleted)"):
                    path = path[:-10]
                return os.path.realpath(path) == binary if binary else Path(path).name == "monitord"
            if self.platform.startswith("linux"):
                candidates = []
                inaccessible_candidate = False
                for item in ([str(pid)] if pid else os.listdir(self.proc_root)):
                    remaining()
                    if not item.isdigit():
                        continue
                    candidate = int(item)
                    name = None
                    try:
                        # Do not inspect unrelated executable paths in the common case.
                        name = (self.proc_root / item / "comm").read_text().strip()
                        if not pid and name != (Path(binary).name[:15] if binary else "monitord"):
                            continue
                        row = self.linux_record(candidate)
                        if matches(row):
                            candidates.append(row)
                    except (FileNotFoundError, ProcessLookupError):
                        continue
                    except PermissionError:
                        if pid:
                            raise
                        if name == (Path(binary).name[:15] if binary else "monitord"):
                            inaccessible_candidate = True
                if inaccessible_candidate:
                    # An unreadable matching process might be another candidate;
                    # do not silently choose the one that happened to be readable.
                    return {"status": "permission_denied"}
            elif self.platform == "darwin":
                candidates = [row for row in self.mac_records(pid, remaining()) if matches(row)]
            else:
                raise ProbeError("unsupported_platform")
            if not candidates:
                return {"status": "stale_pid_file" if pid else "missing"}
            if len(candidates) != 1:
                return {"status": "ambiguous", "candidate_count": len(candidates),
                        "candidate_pids": sorted(row["pid"] for row in candidates)[:32]}
            before = candidates[0]
            remaining()
            if self.platform.startswith("linux"):
                after = self.linux_record(before["pid"])
            else:
                rows = self.mac_records(before["pid"], remaining())
                if len(rows) != 1:
                    return {"status": "disappeared"}
                after = rows[0]
            if after["identity"] != before["identity"] or not matches(after):
                return {"status": "identity_changed"}
            if self.pid_file and read_pid(self.pid_file) != after["pid"]:
                return {"status": "identity_changed"}
            if not math.isfinite(after["cpu_seconds"]) or after["cpu_seconds"] < 0 or after["rss_bytes"] < 0:
                raise ProbeError("probe_error")
            # Executable paths are selectors, not resource-report contents.
            after.pop("executable")
            return {"status": "ok", **after}
        except ProbeError as error:
            return {"status": str(error)}
        except subprocess.TimeoutExpired:
            return {"status": "timeout"}
        except (FileNotFoundError, ProcessLookupError):
            return {"status": "disappeared"}
        except PermissionError:
            return {"status": "permission_denied"}
        except (OSError, ValueError, IndexError):
            return {"status": "probe_error"}


def clock_gap(previous, current, interval):
    if previous is None:
        return None
    delta = {key: current[key] - previous[key] if current[key] is not None and previous[key] is not None else None
             for key in ("wall", "monotonic", "boottime")}
    span = delta["boottime"] if delta["boottime"] is not None else delta["monotonic"]
    flags = []
    if span > max(interval * 2, interval + 5):
        flags.append("sampling_gap")
    if abs(delta["wall"] - span) > 2:
        flags.append("wall_clock_discontinuity")
    if delta["boottime"] is not None and delta["boottime"] - delta["monotonic"] > 2:
        flags.append("possible_suspend")
    return {"delta_seconds": delta, "observations": flags,
            "cause": "not_determined" if flags else None}


class Summary:
    def __init__(self, started, interval):
        self.started, self.interval = started, interval
        self.statuses, self.gaps = Counter(), Counter()
        self.previous = None
        self.last = None
        self.rss_sum = self.rss_count = self.rss_max = 0
        self.cpu_seconds = self.cpu_span = 0.0
        self.identity_changes = 0
        self.last_identity = None
        self.unavailable_seconds = 0.0

    def add(self, point):
        self.statuses[point["status"]] += 1
        point["gap"] = clock_gap(self.last["finished"] if self.last else None, point["finished"], self.interval)
        probe_gap = clock_gap(point["started"], point["finished"], self.interval)
        observations = set(probe_gap["observations"])
        if observations:
            point["probe_gap"] = probe_gap
        if point["gap"]:
            observations.update(point["gap"]["observations"])
            if self.last["status"] != "ok" or point["status"] != "ok":
                self.unavailable_seconds += max(0, elapsed(self.last["finished"], point["finished"]))
        self.gaps.update(observations)
        point["cpu_one_core_percent"] = None
        point["cpu_interval_seconds"] = None
        if point["status"] == "ok":
            self.rss_sum += point["rss_bytes"]
            self.rss_count += 1
            self.rss_max = max(self.rss_max, point["rss_bytes"])
            if self.last_identity is not None and self.last_identity != point["identity"]:
                self.identity_changes += 1
            self.last_identity = point["identity"]
            if self.previous and self.previous["identity"] == point["identity"]:
                span = elapsed(self.previous["finished"], point["finished"])
                delta = point["cpu_seconds"] - self.previous["cpu_seconds"]
                if span > 0 and delta >= 0:
                    point["cpu_one_core_percent"] = delta / span * 100
                    point["cpu_interval_seconds"] = span
                    self.cpu_seconds += delta
                    self.cpu_span += span
                else:
                    point["cpu_baseline_reset"] = "counter_or_clock_regressed"
            else:
                point["cpu_baseline_reset"] = "new_identity_or_monitoring_gap"
            self.previous = point
        else:
            self.previous = None
        self.last = point

    def report(self, now, final, reason, source, args):
        # Suspension can cross the run deadline without another process sample.
        # Keep this last unobserved interval separate from real resource points.
        last_time = self.last["finished"] if self.last else self.started
        terminal_gap = clock_gap(last_time, now, self.interval) if final else None
        gaps = self.gaps.copy()
        if terminal_gap:
            gaps.update(terminal_gap["observations"])
        trailing_span = max(0, elapsed(last_time, now)) if final else 0
        unavailable = self.unavailable_seconds
        if final and (self.last is None or self.last["status"] != "ok"):
            unavailable += trailing_span
        return {"schema_version": 1, "sampler": "replacement_read_only_resource_sampler", "final": final,
                "finish_reason": reason, "started": self.started, "updated": now,
                "elapsed_seconds": elapsed(self.started, now), "boottime_source": source,
                "settings": {"seconds": args.seconds, "interval_seconds": args.interval,
                             "summary_every_seconds": args.summary_every, "probe_timeout_seconds": args.probe_timeout},
                "samples": sum(self.statuses.values()), "statuses": dict(self.statuses),
                "gap_observations": dict(gaps), "terminal_gap": terminal_gap,
                "trailing_observation_span_seconds": trailing_span,
                "process_identity_changes": self.identity_changes,
                "unavailable_observation_span_seconds": unavailable,
                "rss_bytes": {"sample_mean": self.rss_sum / self.rss_count if self.rss_count else None,
                              "sample_max": self.rss_max if self.rss_count else None},
                "cpu": {"observed_seconds": self.cpu_seconds, "covered_interval_seconds": self.cpu_span,
                        "one_core_percent": self.cpu_seconds / self.cpu_span * 100 if self.cpu_span else None},
                "last_observation": self.last,
                "notes": ["CPU is the selected process only; 100 percent is one core.",
                          "Intervals with missing or changed identity do not produce CPU deltas.",
                          "Gap flags are observations, not proof of sleep, crash or a specific cause.",
                          "Unavailable spans describe monitoring coverage, not proven daemon downtime.",
                          "macOS ps start identity has one-second precision; no command arguments are collected.",
                          "SIGKILL cannot write a final report; the last summary and flushed JSONL remain."]}


def atomic_json(path, value):
    descriptor, temporary = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as file:
            json.dump(value, file, ensure_ascii=False, allow_nan=False, separators=(",", ":"))
            file.write("\n")
            file.flush()
            os.fsync(file.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def run(args, probe=None, clock=None, stop=None, reason=None, publish=atomic_json):
    clock = clock or Clock()
    stop = stop or threading.Event()
    reason = reason if reason is not None else []
    probe = probe or ProcessProbe(args.pid_file, args.binary)
    path = Path(args.output)
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists() or path.is_symlink():
        raise FileExistsError("output already exists")
    samples_path = path.with_suffix(".samples.jsonl")
    # Exclusive creation also prevents two samplers from sharing one output.
    with samples_path.open("x", encoding="utf-8") as samples:
        os.chmod(samples_path, 0o600)
        started = clock.now()
        summary = Summary(started, args.interval)
        next_sample, next_summary = 0.0, args.summary_every
        finish_reason = "duration"
        try:
            publish(path, summary.report(started, False, None, clock.source, args))
            while not stop.is_set():
                now = clock.now()
                age = elapsed(started, now)
                if age >= args.seconds:
                    break
                if age >= next_sample:
                    point = {"started": now, **probe.sample(min(args.probe_timeout, args.seconds - age))}
                    point["finished"] = clock.now()
                    point["probe_duration_seconds"] = elapsed(now, point["finished"])
                    summary.add(point)
                    samples.write(json.dumps(point, allow_nan=False, separators=(",", ":")) + "\n")
                    samples.flush()
                    age = elapsed(started, point["finished"])
                    next_sample = (math.floor(age / args.interval) + 1) * args.interval
                if age >= next_summary:
                    samples.flush()
                    os.fsync(samples.fileno())
                    publish(path, summary.report(clock.now(), False, None, clock.source, args))
                    next_summary = (math.floor(age / args.summary_every) + 1) * args.summary_every
                wait = min(next_sample, next_summary, args.seconds) - elapsed(started, clock.now())
                if wait > 0:
                    stop.wait(wait)
            if stop.is_set():
                finish_reason = reason[0] if reason else "cancelled"
        except BaseException:
            finish_reason = "sampler_error"
            raise
        finally:
            samples.flush()
            os.fsync(samples.fileno())
            publish(path, summary.report(clock.now(), True, finish_reason, clock.source, args))
    return 0


def parser():
    result = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    result.add_argument("--seconds", required=True, type=positive, help="bounded observation duration, including sleep when boottime is available")
    result.add_argument("--interval", type=positive, default=10, help="sampling cadence in seconds (default 10)")
    result.add_argument("--summary-every", type=positive, default=1800, help="atomic intermediate-summary cadence (default 1800)")
    result.add_argument("--probe-timeout", type=positive, default=5)
    result.add_argument("--pid-file", type=Path, help="reread this pidfile on every sample; stale/non-monitord PIDs are rejected")
    result.add_argument("--binary", type=Path, help="exact executable path; may be combined with --pid-file")
    result.add_argument("--output", type=Path, required=True, help="new JSON summary path; sibling JSONL preserves individual samples")
    return result


def main(argv=None):
    args = parser().parse_args(argv)
    if sys.platform not in ("linux", "darwin"):
        parser().error("only Linux and macOS are supported")
    stop, reason = threading.Event(), []
    def interrupt(number, _frame):
        if not reason:
            reason.append(signal.Signals(number).name)
        stop.set()
    old = {number: signal.signal(number, interrupt) for number in (signal.SIGINT, signal.SIGTERM)}
    try:
        run(args, stop=stop, reason=reason)
        return 128 + signal.Signals[reason[0]].value if reason else 0
    except (OSError, ValueError, ProbeError) as error:
        print("resource sampler failed: " + type(error).__name__, file=sys.stderr)
        return 1
    finally:
        for number, handler in old.items():
            signal.signal(number, handler)


if __name__ == "__main__":
    raise SystemExit(main())
