#!/usr/bin/env python3
"""Fast fixtures for benchmark configuration and reporting; no daemon is run."""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location("benchmark_runtime", Path(__file__).with_name("benchmark-runtime.py"))
benchmark = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(benchmark)


class ConfigurationTests(unittest.TestCase):
    def test_default_is_still_empty(self):
        self.assertEqual(benchmark.collector_config(None), "")

    def test_names_are_canonical_and_only_change_enabled_collectors(self):
        names = benchmark.collector_names("mem,apps,cpu, cpu")
        self.assertEqual(names, ["apps", "cpu", "mem"])
        self.assertEqual(benchmark.collector_config(names), 'collectors:\n  enabled: ["apps","cpu","mem"]\n')

    def test_empty_injected_and_incomplete_selections_are_rejected(self):
        for value in ("", "cpu,mem,", "cpu,mem,apps\nweb: true", "cpu,mem,../file", "apps", "cpu"):
            with self.subTest(value=value), self.assertRaises(argparse.ArgumentTypeError):
                benchmark.collector_names(value)

    def test_missing_or_unexpected_names_do_not_silently_change_workload(self):
        for actual in (["cpu"], ["cpu", "mem", "logs"]):
            with self.subTest(actual=actual), self.assertRaises(RuntimeError):
                benchmark.validate_collectors(["cpu", "mem"], {"collectors": [{"name": x} for x in actual]})
        benchmark.validate_collectors(["cpu", "mem"], {"collectors": [{"name": "mem"}, {"name": "cpu"}]})


class CatalogTests(unittest.TestCase):
    def test_ids_and_typed_status_only(self):
        secret = "fixture-private-value"
        responses = {
            "/api/v1/charts": {"charts": {
                "system.ram": {"dimensions": [{"id": "free", "name": secret}], "title": secret, "labels": {"cmdline": secret}},
                "system.cpu": {"dimensions": [{"id": "user", "name": secret}, {"id": "system", "private": secret}], "token": secret},
            }},
            "/api/v1/collectors": {"status": [{
                "name": "mem", "enabled": True, "error": secret,
                "runs": 5, "failures": 2, "last_run_ms": 123,
                "token": secret, "cmdline": secret,
            }, {"name": "cpu", "enabled": False, "runs": secret, "failures": True}],
                "plugins": [{"command": secret}], "token": secret},
        }
        snapshot = benchmark.catalog_snapshot(responses.__getitem__, benchmark.time.monotonic())
        self.assertNotIn(secret, json.dumps(snapshot))
        self.assertEqual(snapshot["chart_dimensions"], {"system.cpu": ["system", "user"], "system.ram": ["free"]})
        self.assertEqual((snapshot["chart_count"], snapshot["series_count"]), (2, 3))
        self.assertEqual(snapshot["collectors"], [
            {"name": "cpu", "enabled": False, "has_error": False},
            {"name": "mem", "enabled": True, "has_error": True, "runs": 5, "failures": 2, "last_run_ms": 123},
        ])

    def test_empty_and_null_dimensions(self):
        responses = {
            "/api/v1/charts": {"charts": {"empty": {"dimensions": []}, "null": {"dimensions": None}}},
            "/api/v1/collectors": {"status": []},
        }
        snapshot = benchmark.catalog_snapshot(responses.__getitem__, benchmark.time.monotonic())
        self.assertEqual(snapshot["chart_dimensions"], {"empty": [], "null": []})
        self.assertEqual(snapshot["series_count"], 0)

    def test_real_sampling_gaps_and_observer_cost_are_visible(self):
        points = [{"elapsed_seconds": t, "snapshot_duration_ms": cost}
                  for t, cost in [(10, 200), (10.6, 400), (11.4, 600)]]
        summary = benchmark.sampling_summary(points, .2)
        self.assertEqual(summary["sample_count"], 3)
        self.assertAlmostEqual(summary["actual_interval_seconds"]["mean"], .7)
        self.assertEqual(summary["snapshot_duration_ms"]["mean"], 400)

    def test_process_snapshot_uses_comm_and_counts_only_direct_children(self):
        table = "20 1 00:02.00 2048 /tmp/monitord\n21 20 00:00.20 1024 /usr/bin/log\n22 21 00:00.30 1024 /bin/other\n"
        with mock.patch.object(benchmark.subprocess, "check_output", return_value=table) as command, \
                mock.patch.object(benchmark.time, "monotonic", side_effect=[10, 10.4]):
            point = benchmark.sample(20, True)
        self.assertIn("pid=,ppid=,time=,rss=,comm=", command.call_args.args[0])
        self.assertEqual([child["pid"] for child in point["children"]], [21])
        self.assertAlmostEqual(point["snapshot_duration_ms"], 400)
        self.assertEqual(point["daemon_and_direct_children_rss_mib"], 3)


class RunFixtureTests(unittest.TestCase):
    def exercise(self, selection=None, after_failure=False, actual_names=None):
        secret = "fixture-generated-password"
        state = {"phase": "before", "process": None, "requests": []}
        result = {"method": {}}

        class FakeProcess:
            def __init__(self, command, **kwargs):
                state["process"] = self
                self.command, self.returncode, self.stopped = command, None, False
                self.directory = Path(command[command.index("-data-dir") + 1])
                self.directory.mkdir()
                (self.directory / "web-password").write_text(secret)
                state["config"] = Path(command[command.index("-config") + 1]).read_text()

            def poll(self):
                return self.returncode

            def send_signal(self, _value):
                self.stopped = True
                self.returncode = 0

            def wait(self, timeout=None):
                return self.returncode

            def lifetime_usage(self):
                return {"available": True, "not_steady_state": True}

        class FakeConnection:
            def __init__(self, host, port, **kwargs):
                self.host = host

            def request(self, method, path, headers):
                self.path = path
                state["requests"].append((state["phase"], path))
                if headers != {"Authorization": "Bearer " + secret}:
                    raise AssertionError("fixture requires generated authentication")

            def getresponse(self):
                if self.path == "/api/v1/info":
                    payload = {"token": secret}
                elif self.path == "/api/v1/charts":
                    if after_failure and state["phase"] == "after":
                        raise OSError(secret)
                    charts = {"system.cpu": {"dimensions": [{"id": "user"}]}, "system.ram": {"dimensions": [{"id": "free"}]}}
                    if state["phase"] == "after":
                        charts["new.chart"] = {"dimensions": [{"id": "new.dim"}]}
                    payload = {"charts": charts}
                elif self.path == "/api/v1/collectors":
                    names = actual_names if actual_names is not None else selection or ["cpu", "mem"]
                    payload = {"status": [{"name": name, "enabled": True, "error": secret, "runs": 1} for name in names]}
                else:
                    payload = {"result": {"data": [[1, 2]]}}
                return SimpleNamespace(status=200, read=lambda: json.dumps(payload).encode())

            def close(self):
                pass

        def measure(*args):
            self.assertEqual(state["phase"], "before")
            self.assertIn("before", result["catalog_snapshots"])
            state["phase"] = "after"
            return {"endpoints": {"info": {"requests": 1, "errors": {}}}}

        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            args = SimpleNamespace(binary=Path("/fixture/monitord"), collectors=selection,
                                   warmup=1, duration=1, include_children=True, child_sample_interval=.2)
            with mock.patch.object(benchmark, "BenchmarkProcess", FakeProcess), \
                    mock.patch.object(benchmark.http.client, "HTTPConnection", FakeConnection), \
                    mock.patch.object(benchmark.time, "sleep"), mock.patch.object(benchmark, "measure", side_effect=measure):
                if after_failure or actual_names is not None:
                    with self.assertRaises(RuntimeError) as caught:
                        benchmark.run(args, result, directory)
                    self.assertNotIn(secret, str(caught.exception))
                else:
                    benchmark.run(args, result, directory)
            self.assertEqual(state["process"].directory, directory / "data")
            self.assertTrue(state["process"].command[state["process"].command.index("-listen") + 1].startswith("127.0.0.1:"))
            self.assertTrue(state["process"].stopped)
            self.assertEqual(result["exit_code"], 0)
            self.assertNotIn(secret, json.dumps(result))
            self.assertEqual(result["method"]["config_sha256"], hashlib.sha256(state["config"].encode()).hexdigest())
        self.assertFalse(directory.exists())
        return result

    def test_default_retains_empty_config_and_captures_catalog_changes(self):
        result = self.exercise()
        self.assertTrue(result["method"]["default_collectors"])
        self.assertEqual(result["method"]["config_sha256"], hashlib.sha256(b"").hexdigest())
        self.assertEqual(result["chart_count"], 2)
        self.assertEqual(result["catalog_snapshots"]["after"]["chart_count"], 3)

    def test_fixed_selection_preserves_isolation_and_reports_hash(self):
        result = self.exercise(["apps", "cpu", "mem"])
        self.assertFalse(result["method"]["default_collectors"])
        self.assertEqual(result["method"]["selected_collectors"], ["apps", "cpu", "mem"])

    def test_after_failure_preserves_measurement_without_error_text(self):
        result = self.exercise(after_failure=True)
        self.assertIn("endpoints", result)
        self.assertEqual(result["catalog_snapshots"]["after"], {"available": False, "error_type": "OSError"})

    def test_unknown_collector_fails_before_measurement_and_still_cleans_up(self):
        result = self.exercise(["cpu", "mem", "typo"], actual_names=["cpu", "mem"])
        self.assertNotIn("endpoints", result)


if __name__ == "__main__":
    unittest.main()
