#!/usr/bin/env python3
"""soak.py must not discover a production config beside the seed data dir."""
from __future__ import annotations

import pathlib
import sys
import tempfile
import contextlib
import io
import json
import signal
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import soak  # noqa: E402


PRODUCTION = """
web:
  listen: "0.0.0.0:19999"
  token: "production-fixed-token"
health:
  enabled: true
  notify:
    webhook:
      url: https://hooks.example/prod
  actions:
    - name: restart
commands:
  - name: restart
    argv: ["/bin/true"]
trapper:
  enabled: true
notify:
  roles:
    sysadmin: [webhook]
collectors:
  enabled: [cpu, mem]
"""


class FakeClock:
    def __init__(self):
        self.value = soak.ClockReading(1_700_000_000, 100, 1000)

    def now(self):
        return self.value

    def advance(self, seconds, *, wall=None, boottime=None):
        old = self.value
        self.value = soak.ClockReading(old.wall + (seconds if wall is None else wall),
                                       old.monotonic + seconds,
                                       old.boottime + (seconds if boottime is None else boottime))


class RssTrendTests(unittest.TestCase):
    def test_header_is_written_once(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = str(pathlib.Path(tmp) / "rss-trend-1026.log")
            soak.append_rss_trend(path, 0, 40.5, 3.25)
            soak.append_rss_trend(path, 30, 41, None)
            lines = pathlib.Path(path).read_text(encoding="utf-8").splitlines()
        self.assertEqual(lines[0], "unix_s\telapsed_s\trss_mib\theap_mib")
        self.assertEqual(len(lines), 3)
        self.assertIn("\t0\t40.50\t3.25", lines[1])
        self.assertTrue(lines[2].endswith("\t30\t41.00\t"))


class SoakTimelineTests(unittest.TestCase):
    def timeline(self):
        clock = FakeClock()
        timeline = soak.SamplingTimeline(clock.now(), 120, 2)
        timeline.start_sampling(clock.now())
        return clock, timeline

    def test_long_intervals_are_bounded_and_fail_continuity(self):
        clock, timeline = self.timeline()
        for _ in range(105):
            clock.advance(121)
            timeline.observe(clock.now(), "sample_start")
        report = timeline.report(clock.now())
        self.assertEqual(report["gap_count"], 105)
        self.assertEqual(len(report["gaps"]), 100)
        self.assertEqual(report["clock_disagreement_count"], 0)
        self.assertIn("continuous coverage not established", timeline.alerts()[0])
        self.assertTrue(all(g["classification"] == "sampling_gap_unknown_cause" for g in report["gaps"]))

    def test_forward_and_backward_wall_changes_are_unknown(self):
        for wall in (3600, -3600):
            with self.subTest(wall=wall):
                clock, timeline = self.timeline()
                clock.advance(2, wall=wall)
                timeline.finish_sampling(clock.now())
                report = timeline.report(clock.now())
                self.assertEqual(report["clock_disagreement_count"], 1)
                self.assertEqual(report["clock_disagreements"][0]["classification"], "clock_disagreement_unknown_cause")
                self.assertTrue(timeline.alerts())
                self.assertNotIn("suspend", " ".join(timeline.alerts()))
                # A backward wall adjustment must not extend the deadline.
                self.assertGreaterEqual(soak.elapsed_seconds(timeline.started, clock.now()), 2)

    def test_final_gap_and_unavailable_boottime(self):
        start = soak.ClockReading(1_700_000_000, 0)
        timeline = soak.SamplingTimeline(start, 120, 2)
        timeline.start_sampling(start)
        timeline.finish_sampling(soak.ClockReading(start.wall + 86400, 2))
        # Repeated cleanup/reporting must not count the same gap twice.
        timeline.finish_sampling(soak.ClockReading(start.wall + 86401, 3))
        report = timeline.report(soak.ClockReading(start.wall + 86401, 3))
        self.assertEqual(report["gap_count"], 1)
        self.assertEqual(report["gaps"][0]["phase"], "sampling_end")
        self.assertFalse(report["boottime_available"])
        self.assertIsNone(report["elapsed"]["boottime_seconds"])

    def run_fake(self, sleep=None, extra_args=(), popen_error=None):
        clock = FakeClock()
        reports = []

        class Process:
            pid = 123456
            returncode = None
            stopped = False

            def poll(self):
                return self.returncode

            def send_signal(self, signum):
                self.stopped = True

            def wait(self, timeout=None):
                self.returncode = 0
                return 0

        proc = Process()

        def response(request, **_kwargs):
            url = request if isinstance(request, str) else request.full_url
            if url.endswith("/api/v1/info"):
                body = json.dumps({"charts_count": 1, "collectors": []}).encode()
            elif url.endswith("/metrics"):
                body = b'monitor_process_heap_alloc_bytes{} 1048576\nmonitor_tsdb_series{} 1\n'
            else:
                body = b"{}"
            return io.BytesIO(body)

        original_write = soak.atomic_write_report
        with tempfile.TemporaryDirectory() as tmp:
            output = pathlib.Path(tmp) / "report.json"

            def publish(path, text):
                original_write(path, text)
                reports.append(json.loads(path.read_text()))

            def launch(*_args, **_kwargs):
                self.assertTrue(output.is_file(), "start report must precede process launch")
                self.assertEqual(json.loads(output.read_text())["requests"], 0)
                if popen_error:
                    raise popen_error
                return proc

            previous_handlers = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
            with mock.patch.object(soak, "read_clocks", clock.now), \
                    mock.patch.object(soak.time, "sleep", lambda seconds: sleep(clock, seconds) if sleep else clock.advance(seconds)), \
                    mock.patch.object(soak.subprocess, "Popen", launch), \
                    mock.patch.object(soak.subprocess, "check_output", return_value="1024"), \
                    mock.patch.object(soak.urllib.request, "urlopen", response), \
                    mock.patch.object(soak, "atomic_write_report", publish), \
                    contextlib.redirect_stdout(io.StringIO()):
                try:
                    code = soak.main(["--seconds", "10", "--sample-every", "2", "--report-every", "3",
                                      "--no-heap", "--token", "test-fixture-token", "--output", str(output), *extra_args])
                except SystemExit as exc:
                    code = exc.code
            self.assertEqual({sig: signal.getsignal(sig) for sig in previous_handlers}, previous_handlers)
            self.assertEqual(json.loads(output.read_text()), reports[-1])
        return code, reports, proc

    def test_main_records_each_sample_and_reports_by_time(self):
        code, reports, proc = self.run_fake()
        self.assertEqual(code, 0)
        self.assertTrue(proc.stopped)
        final = reports[-1]
        self.assertEqual(final["requests"], 5)
        self.assertEqual(final["sample_count"], 5)
        self.assertEqual(final["elapsed"]["wall_seconds"], 10)
        self.assertEqual(final["sampling_elapsed"]["monotonic_seconds"], 10)
        self.assertTrue(final["started_at_utc"].endswith("Z"))
        self.assertEqual(final["max_sample_gap_seconds"], 120)
        self.assertEqual(final["gap_count"], 0)
        self.assertEqual(final["alerts"], [])
        self.assertTrue(any(0 < r["elapsed"]["monotonic_seconds"] < 10 for r in reports), "periodic report must not depend on reaching ten samples")
        self.assertTrue(all(s["since_previous_sample"]["monotonic_seconds"] == 2 for s in final["sample_timeline"][1:]))

    def test_main_pause_crossing_deadline_cannot_pass(self):
        def pause(clock, seconds):
            clock.advance(seconds, wall=86400, boottime=86400)
        code, reports, proc = self.run_fake(pause)
        self.assertEqual(code, 1)
        self.assertTrue(proc.stopped)
        final = reports[-1]
        self.assertEqual(final["sample_count"], 1)
        self.assertEqual(final["gap_count"], 1)
        self.assertEqual(final["gaps"][0]["phase"], "sampling_end")
        self.assertEqual(final["sampling_elapsed"]["wall_seconds"], 86400)
        self.assertEqual(final["sampling_elapsed"]["monotonic_seconds"], 2)
        self.assertEqual(final["sampling_elapsed"]["boottime_seconds"], 86400)
        self.assertTrue(final["alerts"])

    def test_report_cadence_can_be_shorter_than_sample_interval(self):
        code, reports, _ = self.run_fake(extra_args=("--sample-every", "6", "--report-every", "1"))
        self.assertEqual(code, 0)
        self.assertEqual(reports[-1]["sample_count"], 2)
        times = {r["elapsed"]["monotonic_seconds"] for r in reports}
        self.assertTrue(set(range(11)).issubset(times))
        code, reports, _ = self.run_fake(extra_args=("--max-sample-gap", "1"))
        self.assertEqual(code, 1)
        self.assertEqual(reports[-1]["max_sample_gap_seconds"], 1)
        self.assertEqual(reports[-1]["gap_count"], 5)

    def test_main_clock_jump_in_either_direction_fails(self):
        for wall in (3600, -3600):
            with self.subTest(wall=wall):
                jumped = False
                def jump(clock, seconds):
                    nonlocal jumped
                    clock.advance(seconds, wall=wall if not jumped else seconds)
                    jumped = True
                code, reports, _ = self.run_fake(jump)
                self.assertEqual(code, 1)
                self.assertEqual(reports[-1]["clock_disagreement_count"], 1)

    def test_main_signal_and_startup_failure_leave_reports(self):
        def interrupt(clock, seconds):
            clock.advance(125)
            signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
        code, reports, proc = self.run_fake(interrupt)
        self.assertEqual(code, 1)
        self.assertTrue(proc.stopped)
        self.assertTrue(reports[-1]["interrupted"])
        self.assertEqual(reports[-1]["gap_count"], 1)
        code, reports, _ = self.run_fake(popen_error=FileNotFoundError("fixture binary missing"))
        self.assertEqual(code, 1)
        self.assertIn("fixture binary missing", reports[-1]["errors"])
        self.assertEqual(reports[-1]["sample_count"], 0)

    def test_intervals_must_be_finite_positive(self):
        for name in ("sample-every", "max-sample-gap", "report-every"):
            for value in ("0", "-1", "nan", "inf", "-inf"):
                with self.subTest(name=name, value=value), contextlib.redirect_stderr(io.StringIO()), \
                        mock.patch.object(soak.subprocess, "Popen") as launch:
                    with self.assertRaises(SystemExit) as raised:
                        soak.main([f"--{name}={value}"])
                    self.assertEqual(raised.exception.code, 2)
                    launch.assert_not_called()

    def test_atomic_report_preserves_old_file_on_replace_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "report.json"
            soak.atomic_write_report(path, '{"generation":1}')
            with mock.patch.object(soak.os, "replace", side_effect=OSError("fixture replacement failed")):
                with self.assertRaises(OSError):
                    soak.atomic_write_report(path, '{"generation":2}')
            self.assertEqual(json.loads(path.read_text()), {"generation": 1})
            self.assertEqual(list(path.parent.iterdir()), [path])


def main() -> int:
    stripped = soak.strip_seed_config(PRODUCTION)
    for banned in ("hooks.example", "trapper:", "restart", "notify:"):
        if banned in stripped:
            raise SystemExit(f"seed config still contains {banned!r}:\n{stripped}")
    if 'token: "production-fixed-token"' not in stripped and "production-fixed-token" not in stripped:
        raise SystemExit(f"fixed token was removed:\n{stripped}")
    if soak.web_token(stripped) != "production-fixed-token":
        raise SystemExit(f"token parse failed: {soak.web_token(stripped)!r}")
    nested = """
web:
  users:
    - name: viewer
      token: not-the-admin
  token: "admin-token"
collectors:
  enabled:
    - cpu
    - mem
"""
    if soak.web_token(nested) != "admin-token":
        raise SystemExit(f"nested token wins: {soak.web_token(nested)!r}")
    if soak.expects_ml(nested):
        raise SystemExit("cpu/mem config should not expect ml")
    if not soak.expects_ml("collectors:\n  enabled: [cpu, ml]\n"):
        raise SystemExit("inline ml list not detected")
    if not soak.expects_ml("mode: agent\n"):
        raise SystemExit("default config should expect ml")

    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        data = root / "home" / "box" / "monitor" / "data"
        data.mkdir(parents=True)
        (data.parent / "monitor.yaml").write_text(PRODUCTION, encoding="utf-8")
        work = root / "work"
        work.mkdir()
        cfg = soak.resolve_soak_config(work, None)
        text = cfg.read_text(encoding="utf-8")
        if "production-fixed-token" in text or "hooks.example" in text:
            raise SystemExit("resolve without --seed-config read a sibling production file")
        src = root / "seed.yaml"
        src.write_text(PRODUCTION, encoding="utf-8")
        work2 = root / "work2"
        work2.mkdir()
        copied = soak.resolve_soak_config(work2, str(src))
        copied_text = copied.read_text(encoding="utf-8")
        if "hooks.example" in copied_text or "trapper:" in copied_text:
            raise SystemExit(copied_text)
        if soak.web_token(copied_text) != "production-fixed-token":
            raise SystemExit("explicit seed config lost its token")
    marks = soak.heap_marks(7200, 1800)
    if marks != [0, 1800, 3600, 5400, 7200]:
        raise SystemExit(f"heap marks {marks}")
    if soak.heap_marks(120, 0) != [0, 120]:
        raise SystemExit("short runs still capture start and end")
    if soak.default_heap_every(7200) != 1800 or soak.default_heap_every(6 * 3600) != 7200 or soak.default_heap_every(120) != 0:
        raise SystemExit(f"heap interval {soak.default_heap_every(6 * 3600)}")
    if soak.heap_marks(8 * 3600, 7200) != [0, 7200, 14400, 21600, 28800]:
        raise SystemExit("2h marks")
    if soak.heap_label(1800) != "t00h30m" or soak.heap_label(0) != "t00h00m" or soak.heap_label(8) != "t00h00m08s":
        raise SystemExit(f"heap label {soak.heap_label(1800)} {soak.heap_label(8)}")

    class Args:
        max_rss_mib = None
        max_heap_growth_mib = None
        max_rss_growth_mib = None
        max_anomaly_buffer_mib = None

    two_hour = Args()
    soak.apply_duration_thresholds(7200, two_hour)
    if (two_hour.max_rss_mib, two_hour.max_heap_growth_mib, two_hour.max_rss_growth_mib, two_hour.max_anomaly_buffer_mib) != (450, 80, 120, 16):
        raise SystemExit("2h caps")
    day = Args()
    soak.apply_duration_thresholds(86400, day)
    if day.max_heap_growth_mib != 200 or day.max_rss_mib != 800:
        raise SystemExit("24h caps")
    short = Args()
    soak.apply_duration_thresholds(120, short)
    if short.max_heap_growth_mib is not None:
        raise SystemExit("short runs stay uncapped unless asked")
    result = unittest.TextTestRunner().run(unittest.defaultTestLoader.loadTestsFromTestCase(SoakTimelineTests))
    if not result.wasSuccessful():
        return 1
    print("soak config isolation and timeline ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
