#!/usr/bin/env python3
"""Resource sampler fixtures: synthetic process tables, clocks and owned helpers."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("sample_resources", Path(__file__).with_name("sample-resources.py"))
sampler = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(sampler)


def mac_row(pid, start="Sun Oct 25 12:00:00 2026", cpu="00:01.00", binary="/fixture/monitord", rss=2048):
    return f"{pid} S {start} {cpu} {rss} {binary}\n"


def response(text, code=0):
    return SimpleNamespace(returncode=code, stdout=text, stderr="private-command-arguments")


def clocks(value, wall=None, boot=None):
    return {"wall": value + 10000 if wall is None else wall, "monotonic": value,
            "boottime": value if boot is None else boot}


def point(value, identity=None, cpu=1, status="ok"):
    result = {"status": status, "started": clocks(value), "finished": clocks(value)}
    if status == "ok":
        result.update(pid=20, identity=identity or [20, "first"], cpu_seconds=cpu, rss_bytes=1024)
    return result


class DiscoveryTests(unittest.TestCase):
    def test_pidfile_is_reread_and_stale_wrong_binary_is_not_sampled(self):
        with tempfile.TemporaryDirectory() as directory:
            pidfile = Path(directory) / "monitord.pid"
            table = {20: mac_row(20), 21: mac_row(21), 22: mac_row(22, binary="/bin/unrelated")}
            calls = []
            def command(argv, **kwargs):
                calls.append(argv)
                return response(table.get(int(argv[2]), ""))
            probe = sampler.ProcessProbe(pid_file=pidfile, platform="darwin", runner=command)
            self.assertEqual(probe.sample(1)["status"], "pid_file_missing")
            for pid, status in [(20, "ok"), (21, "ok"), (22, "stale_pid_file"), (23, "stale_pid_file")]:
                pidfile.write_text(str(pid))
                sample = probe.sample(1)
                self.assertEqual(sample["status"], status)
                if status == "ok":
                    self.assertEqual(sample["pid"], pid)
            pidfile.write_text("123 --secret")
            self.assertEqual(probe.sample(1)["status"], "invalid_pid_file")
            self.assertTrue(all("comm=" in argv[-1] and "args=" not in argv[-1] for argv in calls))

    def test_multiple_candidates_require_a_selector(self):
        table = mac_row(20) + mac_row(21, binary="/other/monitord")
        runner = mock.Mock(side_effect=[response(table)])
        probe = sampler.ProcessProbe(platform="darwin", runner=runner)
        self.assertEqual(probe.sample(1), {"status": "ambiguous", "candidate_count": 2, "candidate_pids": [20, 21]})
        self.assertEqual(runner.call_count, 1)
        probe = sampler.ProcessProbe(binary=Path("/fixture/monitord"), platform="darwin",
                                     runner=mock.Mock(side_effect=[response(table), response(mac_row(20))]))
        self.assertEqual(probe.sample(1)["pid"], 20)

    def test_disappearance_reuse_and_counter_output_errors_are_explicit(self):
        cases = [(response("", 1), "disappeared"),
                 (response(mac_row(20, start="Sun Oct 25 12:01:00 2026")), "identity_changed"),
                 (response(mac_row(20, binary="/bin/unrelated")), "identity_changed"),
                 (response(mac_row(20, cpu="NaN")), "probe_error")]
        for second, expected in cases:
            with self.subTest(expected=expected):
                probe = sampler.ProcessProbe(platform="darwin", runner=mock.Mock(side_effect=[response(mac_row(20)), second]))
                self.assertEqual(probe.sample(1)["status"], expected)
        for exception, expected in [(subprocess.TimeoutExpired("private", 1), "timeout"),
                                    (PermissionError("private"), "permission_denied")]:
            probe = sampler.ProcessProbe(platform="darwin", runner=mock.Mock(side_effect=exception))
            self.assertEqual(probe.sample(1), {"status": expected})

    def test_linux_start_ticks_detect_reused_pid_and_do_not_read_cmdline(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "monitord"
            binary.write_bytes(b"fixture")
            directory = root / "20"
            directory.mkdir()
            (directory / "comm").write_text("monitord\n")
            (directory / "exe").symlink_to(binary)
            (directory / "cmdline").write_text("not-to-be-read-secret")
            def write_stat(start, cpu=50):
                fields = ["0"] * 22
                fields[0], fields[11], fields[12], fields[19], fields[21] = "S", str(cpu), "25", str(start), "3"
                (directory / "stat").write_text("20 (monitord) " + " ".join(fields))
            write_stat(100)
            probe = sampler.ProcessProbe(platform="linux", proc_root=root)
            first = probe.sample(1)
            write_stat(200)
            second = probe.sample(1)
            self.assertEqual((first["status"], second["status"]), ("ok", "ok"))
            self.assertNotEqual(first["identity"], second["identity"])
            self.assertEqual(first["rss_bytes"], 3 * probe.page_size)
            self.assertNotIn("not-to-be-read-secret", json.dumps([first, second]))
            (directory / "stat").unlink()
            self.assertEqual(probe.sample(1)["status"], "missing")

    def test_pidfile_fifo_and_oversized_values_do_not_block_or_become_pids(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "pid"
            path.write_text("2" * 129)
            with self.assertRaisesRegex(sampler.ProbeError, "invalid_pid_file"):
                sampler.read_pid(path)
            path.unlink()
            os.mkfifo(path)
            with self.assertRaisesRegex(sampler.ProbeError, "invalid_pid_file"):
                sampler.read_pid(path)

    def test_unreadable_linux_candidate_prevents_choosing_another_instance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for pid in (20, 21):
                directory = root / str(pid)
                directory.mkdir()
                (directory / "comm").write_text("monitord\n")
            probe = sampler.ProcessProbe(platform="linux", proc_root=root)
            readable = {"pid": 20, "executable": "/fixture/monitord"}
            def record(pid):
                if pid == 21:
                    raise PermissionError("fixture")
                return readable
            with mock.patch.object(probe, "linux_record", side_effect=record):
                self.assertEqual(probe.sample(1), {"status": "permission_denied"})


class SummaryTests(unittest.TestCase):
    def test_cpu_baselines_do_not_cross_identity_changes_or_monitoring_gaps(self):
        summary = sampler.Summary(clocks(0), 10)
        points = [point(0, cpu=100), point(10, cpu=102), point(20, [21, "second"], 1),
                  point(30, [21, "second"], 3), point(40, status="missing"),
                  point(50, [21, "second"], 20), point(60, [21, "reused"], 1)]
        for value in points:
            summary.add(value)
        self.assertEqual([value["cpu_one_core_percent"] for value in points], [None, 20, None, 20, None, None, None])
        self.assertEqual(summary.identity_changes, 2)
        self.assertEqual(summary.unavailable_seconds, 20)
        self.assertEqual(summary.cpu_seconds, 4)
        self.assertEqual(summary.cpu_span, 20)

    def test_suspend_clock_change_and_missing_boottime_remain_observations(self):
        gap = sampler.clock_gap(clocks(0), clocks(10, wall=10070, boot=70), 10)
        self.assertEqual(gap["observations"], ["sampling_gap", "possible_suspend"])
        self.assertEqual(gap["cause"], "not_determined")
        gap = sampler.clock_gap(clocks(0), clocks(10, wall=10070, boot=10), 10)
        self.assertEqual(gap["observations"], ["wall_clock_discontinuity"])
        before, after = clocks(0), clocks(70)
        before["boottime"] = after["boottime"] = None
        self.assertEqual(sampler.clock_gap(before, after, 10)["observations"], ["sampling_gap"])

    def test_failed_atomic_replace_preserves_previous_valid_json(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "report.json"
            sampler.atomic_json(path, {"old": True})
            def check_before_replace(source, target):
                self.assertEqual(json.loads(path.read_text()), {"old": True})
                self.assertEqual(json.loads(Path(source).read_text()), {"new": True})
                raise OSError("fixture disk error")
            with mock.patch.object(sampler.os, "replace", side_effect=check_before_replace):
                with self.assertRaises(OSError):
                    sampler.atomic_json(path, {"new": True})
            self.assertEqual(json.loads(path.read_text()), {"old": True})
            self.assertEqual(list(Path(temporary).iterdir()), [path])
            sampler.atomic_json(path, {"new": True})
            self.assertEqual(json.loads(path.read_text()), {"new": True})

    def test_half_hour_intermediate_summaries_and_bounded_final_with_fake_clock(self):
        class FakeClock:
            source, value = "fixture_boottime", 0
            def now(self):
                return clocks(self.value)
        class Stop:
            def is_set(self):
                return False
            def wait(self, seconds):
                clock.value += seconds
        clock = FakeClock()
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "report.json"
            args = sampler.parser().parse_args(["--seconds", "3601", "--interval", "600", "--output", str(path)])
            reports = []
            def publish(path, value):
                reports.append(copy.deepcopy(value))
                sampler.atomic_json(path, value)
            probe = mock.Mock(sample=mock.Mock(return_value={"status": "missing"}))
            sampler.run(args, probe=probe, clock=clock, stop=Stop(), publish=publish)
            self.assertEqual([(r["elapsed_seconds"], r["final"]) for r in reports], [(0, False), (1800, False), (3600, False), (3601, True)])
            self.assertEqual(reports[-1]["samples"], 7)
            self.assertEqual(len(path.with_suffix(".samples.jsonl").read_text().splitlines()), 7)
            with self.assertRaises(FileExistsError):
                sampler.run(args, probe=probe, clock=clock, stop=Stop())

    def test_failed_probe_still_publishes_final_with_prior_samples(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "report.json"
            args = sampler.parser().parse_args(["--seconds", ".1", "--interval", ".01", "--output", str(path)])
            probe = mock.Mock(sample=mock.Mock(side_effect=[{"status": "missing"}, RuntimeError("fixture")]))
            with self.assertRaises(RuntimeError):
                sampler.run(args, probe=probe)
            report = json.loads(path.read_text())
            self.assertTrue(report["final"])
            self.assertEqual(report["finish_reason"], "sampler_error")
            self.assertEqual(report["samples"], 1)
            self.assertEqual(len(path.with_suffix(".samples.jsonl").read_text().splitlines()), 1)

    def test_suspension_crossing_deadline_preserves_terminal_gap_without_fake_sample(self):
        class FakeClock:
            source, value = "fixture_boottime", clocks(0)
            def now(self):
                return self.value.copy()
        class Stop:
            def is_set(self):
                return False
            def wait(self, seconds):
                clock.value = clocks(seconds, wall=10100, boot=100)
        clock = FakeClock()
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "report.json"
            args = sampler.parser().parse_args(["--seconds", "30", "--output", str(path)])
            probe = mock.Mock(sample=mock.Mock(return_value={"status": "missing"}))
            sampler.run(args, probe=probe, clock=clock, stop=Stop())
            report = json.loads(path.read_text())
            self.assertEqual(report["samples"], 1)
            self.assertEqual(report["elapsed_seconds"], 100)
            self.assertEqual(report["gap_observations"], {"sampling_gap": 1, "possible_suspend": 1})
            self.assertEqual(report["terminal_gap"]["delta_seconds"]["boottime"], 100)
            self.assertEqual(report["unavailable_observation_span_seconds"], 100)
            self.assertEqual(len(path.with_suffix(".samples.jsonl").read_text().splitlines()), 1)

    def test_first_slow_probe_reports_its_own_clock_gap(self):
        summary = sampler.Summary(clocks(0), 10)
        observed = point(0, status="timeout")
        observed["finished"] = clocks(10, wall=10100, boot=100)
        summary.add(observed)
        self.assertIsNone(observed["gap"])
        self.assertEqual(set(observed["probe_gap"]["observations"]), {"sampling_gap", "possible_suspend"})
        self.assertEqual(summary.gaps, {"sampling_gap": 1, "possible_suspend": 1})


class ProcessFixtures(unittest.TestCase):
    def harness(self, root, delay=0):
        fake_ps = root / "ps"
        fake_ps.write_text(f"#!{sys.executable}\nimport time\ntime.sleep({delay})\nprint({mac_row(4242)!r}, end='')\n")
        fake_ps.chmod(0o700)
        wrapper = root / "wrapper.py"
        wrapper.write_text(f"import importlib.util\nspec=importlib.util.spec_from_file_location('sampler',{str(Path(sampler.__file__).resolve())!r})\nm=importlib.util.module_from_spec(spec)\nspec.loader.exec_module(m)\nm.sys.platform='darwin'\nraise SystemExit(m.main())\n")
        return wrapper, dict(os.environ, PATH=str(root) + os.pathsep + os.environ.get("PATH", ""))

    def test_real_sigterm_preserves_final_and_periodic_json_without_signalling_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            wrapper, environment = self.harness(root)
            output = root / "report.json"
            process = subprocess.Popen([sys.executable, str(wrapper), "--seconds", "5", "--interval", ".03", "--summary-every", ".12", "--output", str(output)], env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                deadline = time.monotonic() + 4
                while time.monotonic() < deadline:
                    if output.exists():
                        report = json.loads(output.read_text())
                        if report["elapsed_seconds"] >= .12 and report["samples"] > 0:
                            break
                    time.sleep(.01)
                else:
                    self.fail("intermediate summary did not arrive")
                self.assertFalse(report["final"])
                process.send_signal(signal.SIGTERM)  # this test owns only the wrapper
                stdout, stderr = process.communicate(timeout=3)
                self.assertEqual(process.returncode, 143, stderr.decode())
                final = json.loads(output.read_text())
                self.assertTrue(final["final"])
                self.assertEqual(final["finish_reason"], "SIGTERM")
                observations = [json.loads(line) for line in output.with_suffix(".samples.jsonl").read_text().splitlines()]
                self.assertEqual(len(observations), final["samples"])
                self.assertTrue(all(row["pid"] == 4242 for row in observations))
            finally:
                if process.poll() is None:
                    process.kill()
                    process.communicate()

    def test_real_probe_timeout_is_recorded_and_run_finishes_on_deadline(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            wrapper, environment = self.harness(root, delay=2)
            output = root / "report.json"
            result = subprocess.run([sys.executable, str(wrapper), "--seconds", ".2", "--interval", ".03", "--probe-timeout", ".04", "--output", str(output)], env=environment, capture_output=True, timeout=3)
            self.assertEqual(result.returncode, 0, result.stderr.decode())
            final = json.loads(output.read_text())
            self.assertTrue(final["final"])
            self.assertGreater(final["statuses"]["timeout"], 0)
            self.assertLess(final["elapsed_seconds"], 1)


if __name__ == "__main__":
    unittest.main()
