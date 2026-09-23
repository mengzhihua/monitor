#!/usr/bin/env python3
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from stamp_app_version import find_macos_version_json_targets, stamp_pubspec, stamp_version_json


class StampTests(unittest.TestCase):
    def test_pubspec(self):
        src = "name: monitor_app\nversion: 0.1.0\nenvironment:\n  sdk: ^3.9.2\n"
        out = stamp_pubspec(src, "0.1.20", "10020")
        self.assertIn("version: 0.1.20+10020\n", out)
        self.assertNotIn("version: 0.1.0\n", out)

    def test_version_json(self):
        src = '{"app_name":"monitor_app","version":"0.1.0","build_number":"0","package_name":"monitor_app"}'
        out = stamp_version_json(src, "0.1.20", "10020")
        self.assertIn('"version": "0.1.20"', out)
        self.assertIn('"build_number": "10020"', out)
        self.assertIn('"app_name": "monitor_app"', out)

    def test_find_macos_version_json_existing_assets(self):
        import tempfile

        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            assets = root / "Monitor.app/Contents/Frameworks/App.framework/Resources/flutter_assets"
            assets.mkdir(parents=True)
            (assets / "AssetManifest.bin").write_bytes(b"")
            got = find_macos_version_json_targets(root)
            self.assertEqual(got, [assets / "version.json"])

    def test_find_macos_version_json_fallback(self):
        import tempfile

        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            (root / "Monitor.app/Contents/MacOS").mkdir(parents=True)
            got = find_macos_version_json_targets(root)
            self.assertEqual(
                got,
                [root / "Monitor.app/Contents/Frameworks/App.framework/Resources/flutter_assets/version.json"],
            )


if __name__ == "__main__":
    unittest.main()
