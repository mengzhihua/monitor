#!/usr/bin/env python3
import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest

path = pathlib.Path(__file__).with_name("sync-release-version.py")
spec = importlib.util.spec_from_file_location("sync_release_version", path)
mod = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(mod)
stamp = mod.stamp


class SyncReleaseVersionTests(unittest.TestCase):
    def test_stamps_all_version_surfaces(self):
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            (root / "web").mkdir()
            (root / "core/cmd/monitord").mkdir(parents=True)
            (root / "VERSION").write_text("3.0.16\n")
            (root / "web/package.json").write_text('{"name":"monitor-dashboard","version":"3.0.16"}\n')
            (root / "web/package-lock.json").write_text(
                json.dumps(
                    {"name": "monitor-dashboard", "version": "3.0.16", "packages": {"": {"version": "3.0.16"}}},
                    indent=2,
                )
                + "\n"
            )
            (root / "core/cmd/monitord/main.go").write_text('package main\n\nvar version = "3.0.16"\n')
            stamp(root, "3.0.17")
            self.assertEqual((root / "VERSION").read_text(), "3.0.17\n")
            self.assertEqual(json.loads((root / "web/package.json").read_text())["version"], "3.0.17")
            lock = json.loads((root / "web/package-lock.json").read_text())
            self.assertEqual(lock["version"], "3.0.17")
            self.assertEqual(lock["packages"][""]["version"], "3.0.17")
            self.assertIn('var version = "3.0.17"', (root / "core/cmd/monitord/main.go").read_text())

    def test_rejects_non_stable(self):
        with tempfile.TemporaryDirectory() as td:
            with self.assertRaises(SystemExit):
                stamp(pathlib.Path(td), "v3.0.17")


if __name__ == "__main__":
    unittest.main()
