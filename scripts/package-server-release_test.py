#!/usr/bin/env python3
"""Release packaging invariants; fixtures never start a daemon or read real data."""

import importlib.util
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import zipfile


SPEC = importlib.util.spec_from_file_location("package_server_release", Path(__file__).with_name("package-server-release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)
COMMIT = "0123456789abcdef0123456789abcdef01234567"
EPOCH = 1780000000


def build_info(target="linux-amd64", commit=COMMIT, modified="false"):
    os_name, arch = target.split("-")
    return json.dumps({
        "Path": "github.com/mengzhihua/monitor/core/cmd/monitord",
        "GoVersion": "go1.27.1",
        "Settings": [{"Key": key, "Value": value} for key, value in {
            "GOOS": os_name, "GOARCH": arch, "vcs.revision": commit,
            "vcs.modified": modified, "CGO_ENABLED": "0",
        }.items()],
    })


class ReleaseTests(unittest.TestCase):
    def test_source_paths_reject_symlinks_and_traversal(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root / "real").mkdir()
            (root / "real/file").write_text("source")
            (root / "linked").symlink_to(root / "real", target_is_directory=True)
            (root / "link").symlink_to(root / "real/file")
            for name in ("../outside", str(root / "real/file"), "link", "linked/file", "real"):
                with self.subTest(name=name), self.assertRaises(release.PackageError):
                    release.regular_file(root, name)
            self.assertEqual(release.regular_file(root, "real/file"), root / "real/file")

    def test_binary_metadata_rejects_wrong_target_or_stale_source(self):
        for info in (build_info("linux-arm64"), build_info(commit="f" * 40), build_info(modified="true"), "{}"):
            with self.subTest(info=info), patch.object(release, "command", return_value=info):
                with self.assertRaises(release.PackageError):
                    release.inspect_binary(Path("binary"), "linux-amd64", "2.0.0", COMMIT, "go")

    def test_native_version_is_actually_executed(self):
        with patch.object(release.platform, "system", return_value="Linux"), patch.object(release.platform, "machine", return_value="x86_64"):
            with patch.object(release, "command", side_effect=[build_info(), "monitord 2.0.0-dev linux/amd64"]):
                with self.assertRaises(release.PackageError):
                    release.inspect_binary(Path("binary"), "linux-amd64", "2.0.0", COMMIT, "go")
            with patch.object(release, "command", side_effect=[build_info(), "monitord 2.0.0 linux/amd64"]):
                info = release.inspect_binary(Path("binary"), "linux-amd64", "2.0.0", COMMIT, "go")
                self.assertTrue(info["runtime_version_verified_on_build_host"])

    def test_source_version_and_dirty_state_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root / "VERSION").write_text("2.0.0/../../bad")
            with self.assertRaises(release.PackageError):
                release.source_metadata(root)
            (root / "VERSION").write_text("2.0.0")
            with patch.object(release, "command", side_effect=[COMMIT, " M tracked-file"]):
                with self.assertRaises(release.PackageError):
                    release.source_metadata(root)

    def make_fixture(self, root):
        for name in release.DOCUMENTS:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"Document {name}")
        (root / "core/bin").mkdir(parents=True)
        (root / "core/bin/monitord-linux-amd64").write_bytes(b"linux executable fixture")
        (root / "core/bin/monitord-windows-amd64.exe").write_bytes(b"windows executable fixture")
        (root / "core/bin/release-build.json").write_bytes(release.json_bytes({
            "schema_version": 1, "version": "2.0.0", "source_commit": COMMIT,
            "binaries": {target: {"version": "2.0.0", "source_commit": COMMIT, "sha256": release.sha256(root / release.binary_relative(target))} for target in ("linux-amd64", "windows-amd64")},
        }))
        for name in ("AGENTS.md", "agent.md", "monitor.yaml", "data/web-password", ".git/config"):
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("PRIVATE DATA MUST NOT ENTER THE PACKAGE")

    def test_archives_whitelist_permissions_checksums_and_collision(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.make_fixture(root)
            with patch.object(release, "source_metadata", return_value=("2.0.0", COMMIT, EPOCH)), patch.object(release, "inspect_binary", return_value={"checked": True}):
                output = release.package(root, root / "dist", ["linux-amd64", "windows-amd64"], False, "go")
                self.assertEqual(output.name, "monitor-2.0.0-0123456789ab")
                manifest = json.loads((output / "manifest.json").read_text())
                self.assertEqual(manifest["source_commit"], COMMIT)
                sums = dict(line.split("  ", 1)[::-1] for line in (output / "SHA256SUMS").read_text().splitlines())
                for artifact in manifest["artifacts"]:
                    archive = output / artifact["filename"]
                    self.assertEqual(artifact["sha256"], release.sha256(archive))
                    self.assertEqual(sums[archive.name], artifact["sha256"])
                    expected = set(artifact["files"])
                    if archive.name.endswith(".zip"):
                        with zipfile.ZipFile(archive) as package:
                            names = {name.split("/", 1)[1] for name in package.namelist()}
                            self.assertEqual(names, expected)
                            start = package.read("monitor-2.0.0-windows-amd64/start.cmd")
                            self.assertIn(b"-listen 127.0.0.1:20099 -data-dir", start)
                    else:
                        with tarfile.open(archive) as package:
                            names = {entry.name.split("/", 1)[1] for entry in package}
                            self.assertEqual(names, expected)
                            executable = package.getmember("monitor-2.0.0-linux-amd64/monitord")
                            self.assertEqual(executable.mode, 0o755)
                            self.assertEqual(executable.uid, 0)
                            self.assertEqual(executable.mtime, EPOCH)
                            self.assertTrue(all(entry.isfile() for entry in package))
                    self.assertFalse(names & {"AGENTS.md", "agent.md", "monitor.yaml", "data/web-password", ".git/config"})
                self.assertEqual(sums["manifest.json"], release.sha256(output / "manifest.json"))
                before = (output / "manifest.json").read_bytes()
                with self.assertRaises(release.PackageError):
                    release.package(root, root / "dist", ["linux-amd64"], False, "go")
                self.assertEqual((output / "manifest.json").read_bytes(), before)

    def test_partial_failure_leaves_no_release_or_staging_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.make_fixture(root)
            with patch.object(release, "source_metadata", return_value=("2.0.0", COMMIT, EPOCH)), patch.object(release, "inspect_binary", return_value={}), patch.object(release, "write_archive", side_effect=OSError("disk full")):
                with self.assertRaises(OSError):
                    release.package(root, root / "dist", ["linux-amd64"], False, "go")
            self.assertEqual(list((root / "dist").iterdir()), [])

    def test_universal_sign_failure_is_not_ignored(self):
        sources = {"darwin-amd64": Path("intel"), "darwin-arm64": Path("arm")}
        for results in (["", "arm64"], ["", "x86_64 arm64", release.PackageError("sign failed")], ["", "x86_64 arm64", "", release.PackageError("verify failed")]):
            with self.subTest(results=results), patch.object(release, "command", side_effect=results):
                with self.assertRaises(release.PackageError):
                    release.make_universal(sources, Path("universal"))

    def test_fixed_inputs_produce_identical_archives(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            entries = {"monitord": (b"fixture", 0o755), "docs/readme.md": (b"document", 0o644)}
            for suffix in (".tar.gz", ".zip"):
                first, second = root / ("first" + suffix), root / ("second" + suffix)
                release.write_archive(first, "monitor-2.0.0-linux-amd64", entries, EPOCH)
                release.write_archive(second, "monitor-2.0.0-linux-amd64", entries, EPOCH)
                self.assertEqual(first.read_bytes(), second.read_bytes())


if __name__ == "__main__":
    unittest.main()
