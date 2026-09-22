#!/usr/bin/env python3
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from stamp_app_version import stamp_pubspec, stamp_version_json


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


if __name__ == "__main__":
    unittest.main()
