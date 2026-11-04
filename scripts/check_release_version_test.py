#!/usr/bin/env python3
"""Exercise release checks with real isolated git histories, without remote tags."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("release_check", Path(__file__).with_name("check-release-version.py"))
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class ReleaseVersionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "Release fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        (self.root / "web").mkdir()
        (self.root / "core/cmd/monitord").mkdir(parents=True)
        self.stamp("3.0.17")
        self.commit()

    def git(self, *args):
        return subprocess.run(["git", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", *args],
                              cwd=self.root, capture_output=True, text=True, check=True).stdout.strip()

    def stamp(self, version):
        (self.root / "VERSION").write_text(version + "\n")
        (self.root / "web/package.json").write_text(json.dumps({"version": version}))
        (self.root / "web/package-lock.json").write_text(json.dumps({"version": version, "packages": {"": {"version": version}}}))
        (self.root / "core/cmd/monitord/main.go").write_text(f'package main\nvar version = "{version}"\n')

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")

    def check(self, strict=False):
        with contextlib.redirect_stdout(io.StringIO()):
            mod.check(self.root, strict)

    def test_source_archive_and_strict_missing_tag(self):
        self.check()
        with self.assertRaisesRegex(ValueError, "no stable git tag"):
            self.check(strict=True)

    def test_existing_release_and_later_development_commit(self):
        self.git("tag", "v3.0.17")
        (self.root / "README.md").write_text("development")
        self.commit()
        self.check()
        self.check(strict=True)

    def test_bump_before_release_tag_and_after_tag(self):
        self.git("tag", "v3.0.17")
        self.stamp("3.0.18")
        self.commit()
        self.check()
        with self.assertRaisesRegex(ValueError, "differs from latest stable"):
            self.check(strict=True)
        self.git("tag", "v3.0.18")
        self.check(strict=True)

    def test_numeric_version_order_and_downgrade(self):
        self.stamp("3.0.9")
        self.commit()
        self.git("tag", "v3.0.9")
        self.stamp("3.0.10")
        self.check()
        self.stamp("3.0.8")
        with self.assertRaisesRegex(ValueError, "older than"):
            self.check()

    def test_prerelease_cannot_replace_same_stable(self):
        self.git("tag", "v3.0.17")
        self.stamp("3.0.17-rc.1")
        with self.assertRaisesRegex(ValueError, "older than"):
            self.check()
        self.stamp("3.0.18-rc.1")
        self.check()

    def test_bad_tag_source_is_rejected_even_for_forward_bump(self):
        self.git("tag", "v3.0.17")
        self.git("tag", "v3.0.18")
        self.stamp("3.0.18")
        with self.assertRaisesRegex(ValueError, "points at a commit"):
            self.check()

    def test_unrelated_newer_branch_tag_does_not_block(self):
        self.git("tag", "v3.0.17")
        base = self.git("rev-parse", "HEAD")
        self.stamp("3.0.19")
        self.commit()
        self.git("tag", "v3.0.19")
        self.git("checkout", "-q", "--detach", base)
        self.stamp("3.0.18")
        self.check()

    def test_each_version_surface_must_match(self):
        for path in ("web/package.json", "web/package-lock.json", "core/cmd/monitord/main.go"):
            with self.subTest(path=path):
                self.stamp("3.0.17")
                file = self.root / path
                file.write_text(file.read_text().replace("3.0.17", "3.0.16"))
                with self.assertRaisesRegex(ValueError, "differs from VERSION"):
                    self.check()
        self.stamp("3.0.17")
        file = self.root / "web/package-lock.json"
        data = json.loads(file.read_text())
        data["packages"][""]["version"] = "3.0.16"
        file.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, "packages root"):
            self.check()


if __name__ == "__main__":
    unittest.main()
