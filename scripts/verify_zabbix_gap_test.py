#!/usr/bin/env python3
import importlib.util
import pathlib
import tempfile
import unittest

path = pathlib.Path(__file__).with_name("verify-zabbix-gap.py")
spec = importlib.util.spec_from_file_location("verify_zabbix_gap", path)
mod = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(mod)


class VerifyZabbixGapTests(unittest.TestCase):
    def test_evidence_paths_carry_directory(self):
        cell = "`collect/ping.go`、`portcheck.go`"
        paths = mod.evidence_paths(cell)
        self.assertEqual(len(paths), 2)
        self.assertTrue(paths[0].name == "ping.go")
        self.assertTrue(paths[1].name == "portcheck.go")
        self.assertEqual(paths[0].parent, paths[1].parent)

    def test_main_passes_on_repo_doc(self):
        self.assertEqual(mod.main(), 0)


if __name__ == "__main__":
    unittest.main()
