#!/usr/bin/env python3
"""Release packaging invariants; fixtures never start a daemon or read real data."""

from contextlib import suppress
import importlib.util
import json
import os
from pathlib import Path
import selectors
import shutil
import signal
import subprocess
import tarfile
import tempfile
import time
import unittest
from unittest.mock import patch
import zipfile


SPEC = importlib.util.spec_from_file_location("package_server_release", Path(__file__).with_name("package-server-release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)
COMMIT = "0123456789abcdef0123456789abcdef01234567"
EPOCH = 1780000000
BUILD_TIME = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(EPOCH))
REPO = Path(__file__).resolve().parent.parent
PLATFORM_DOCUMENTS = {"docs/25-modular-workspace.md", "docs/26-module-boundaries.md", "docs/27-plugin-platform.md"}
HEARTBEAT_FILES = {"examples/plugins/heartbeat/plugin.json": 0o644, "examples/plugins/heartbeat/collector.sh": 0o755}
PRIVATE_FILES = (
    "AGENTS.md", "agent.md", "monitor.yaml", "data/web-password", ".git/config",
    "data/plugins/state.json", "plugins.d/private.plugin", "examples/plugins/private/plugin.json",
    "examples/plugins/heartbeat/.env", "examples/plugins/heartbeat/runtime.log",
    "examples/plugins/heartbeat/state.json", "examples/plugins/heartbeat/monitor.yaml",
)


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
        for name in release.EXAMPLES:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(release.regular_file(REPO, name).read_bytes())
        (root / "core/bin").mkdir(parents=True)
        (root / "core/bin/monitord-linux-amd64").write_bytes(b"linux executable fixture")
        (root / "core/bin/monitord-windows-amd64.exe").write_bytes(b"windows executable fixture")
        (root / "core/bin/release-build.json").write_bytes(release.json_bytes({
            "schema_version": 1, "version": "2.0.0", "source_commit": COMMIT,
            "binaries": {target: {"version": "2.0.0", "source_commit": COMMIT, "sha256": release.sha256(root / release.binary_relative(target))} for target in ("linux-amd64", "windows-amd64")},
        }))
        for name in PRIVATE_FILES:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("PRIVATE DATA MUST NOT ENTER THE PACKAGE")
        (root / "packaging").mkdir(parents=True, exist_ok=True)
        (root / "packaging/start.sh").write_text("#!/bin/sh\n# fixture start\n")
        (root / "packaging/stop.sh").write_text("#!/bin/sh\n# fixture stop\n")

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
                    self.assertTrue(PLATFORM_DOCUMENTS.issubset(expected))
                    self.assertTrue(set(HEARTBEAT_FILES).issubset(expected))
                    if archive.name.endswith(".zip"):
                        with zipfile.ZipFile(archive) as package:
                            names = {name.split("/", 1)[1] for name in package.namelist()}
                            self.assertEqual(names, expected)
                            start = package.read("monitor-2.0.0-windows-amd64/start.cmd")
                            self.assertIn(b"-listen 127.0.0.1:20099 -data-dir", start)
                            self.assertIn("stop.cmd", names)
                            for name, mode in HEARTBEAT_FILES.items():
                                entry = package.getinfo(f"monitor-2.0.0-windows-amd64/{name}")
                                self.assertEqual((entry.external_attr >> 16) & 0o777, mode)
                                self.assertEqual(package.read(entry), (REPO / name).read_bytes())
                    else:
                        with tarfile.open(archive) as package:
                            names = {entry.name.split("/", 1)[1] for entry in package}
                            self.assertEqual(names, expected)
                            executable = package.getmember("monitor-2.0.0-linux-amd64/monitord")
                            self.assertEqual(executable.mode, 0o755)
                            self.assertEqual(executable.uid, 0)
                            self.assertEqual(executable.mtime, EPOCH)
                            self.assertTrue(all(entry.isfile() for entry in package))
                            self.assertIn("stop.sh", names)
                            self.assertIn(b"fixture start", package.extractfile("monitor-2.0.0-linux-amd64/start.sh").read())
                            for name, mode in HEARTBEAT_FILES.items():
                                entry = package.getmember(f"monitor-2.0.0-linux-amd64/{name}")
                                self.assertEqual(entry.mode, mode)
                                self.assertEqual(package.extractfile(entry).read(), (REPO / name).read_bytes())
                    self.assertFalse(names & set(PRIVATE_FILES))
                    self.assertFalse(any(name.startswith("plugins.d/") for name in names))
                self.assertEqual(sums["manifest.json"], release.sha256(output / "manifest.json"))
                before = (output / "manifest.json").read_bytes()
                with self.assertRaises(release.PackageError):
                    release.package(root, root / "dist", ["linux-amd64"], False, "go")
                self.assertEqual((output / "manifest.json").read_bytes(), before)

    def test_example_symlinks_are_rejected_before_packaging(self):
        for name in (*HEARTBEAT_FILES, "examples/plugins/heartbeat", "examples/plugins"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp).resolve()
                self.make_fixture(root)
                original = root / name
                target = root / "outside"
                original.rename(target)
                original.symlink_to(target, target_is_directory=target.is_dir())
                with patch.object(release, "source_metadata", return_value=("2.0.0", COMMIT, EPOCH)), patch.object(release, "inspect_binary", return_value={}):
                    with self.assertRaisesRegex(release.PackageError, "Symbolic links"):
                        release.package(root, root / "dist", ["linux-amd64"], False, "go")
                self.assertFalse((root / "dist").exists())

    @unittest.skipUnless(os.name == "posix", "the heartbeat example requires POSIX shell")
    def test_archived_heartbeat_can_be_installed_and_emits_samples(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.make_fixture(root)
            with patch.object(release, "source_metadata", return_value=("2.0.0", COMMIT, EPOCH)), patch.object(release, "inspect_binary", return_value={}):
                output = release.package(root, root / "dist", ["linux-amd64"], False, "go")
            installed = root / "installed/plugins.d/heartbeat"
            installed.mkdir(parents=True)
            with tarfile.open(output / "monitor-2.0.0-linux-amd64.tar.gz") as package:
                for name in HEARTBEAT_FILES:
                    entry = package.getmember(f"monitor-2.0.0-linux-amd64/{name}")
                    destination = installed / Path(name).name
                    with package.extractfile(entry) as source, destination.open("wb") as dest:
                        shutil.copyfileobj(source, dest)
                    destination.chmod(entry.mode)
            manifest = json.loads((installed / "plugin.json").read_text())
            self.assertEqual(manifest["id"], installed.name)
            self.assertEqual((manifest["api_version"], manifest["protocol"], manifest["capabilities"]), (1, "plugins.d/v1", ["metrics"]))
            command = installed / manifest["command"]
            self.assertTrue(os.access(command, os.X_OK))
            env = {"PATH": os.defpath, "MONITOR_PLUGIN": manifest["id"]}
            process = subprocess.Popen([str(command), str(manifest["update_every"])], cwd=installed,
                                       env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            output = b""
            try:
                deadline = time.monotonic() + 5
                with selectors.DefaultSelector() as selector:
                    selector.register(process.stdout, selectors.EVENT_READ)
                    while output.count(b"\nEND\n") < 2 and time.monotonic() < deadline:
                        if selector.select(max(0, deadline - time.monotonic())):
                            chunk = os.read(process.stdout.fileno(), 4096)
                            if not chunk:
                                break
                            output += chunk
                self.assertIn(b"CHART plugin.heartbeat.pulse ", output)
                self.assertIn(b"DIMENSION count ", output)
                self.assertIn(b"BEGIN plugin.heartbeat.pulse\nSET count = 1\nEND\n", output)
                self.assertIn(b"BEGIN plugin.heartbeat.pulse\nSET count = 2\nEND\n", output)
            finally:
                with suppress(ProcessLookupError):
                    os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    with suppress(ProcessLookupError):
                        os.killpg(process.pid, signal.SIGKILL)
                    process.communicate(timeout=5)

    def test_partial_failure_leaves_no_release_or_staging_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.make_fixture(root)
            with patch.object(release, "source_metadata", return_value=("2.0.0", COMMIT, EPOCH)), patch.object(release, "inspect_binary", return_value={}), patch.object(release, "write_archive", side_effect=OSError("disk full")):
                with self.assertRaises(OSError):
                    release.package(root, root / "dist", ["linux-amd64"], False, "go")
            self.assertEqual(list((root / "dist").iterdir()), [])

    def test_build_receipt_rejects_replaced_binary_and_wrong_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.make_fixture(root)
            release.validate_receipt(root, ["linux-amd64"], "2.0.0", COMMIT)
            with self.assertRaises(release.PackageError):
                release.validate_receipt(root, ["linux-amd64"], "9.9.9", COMMIT)
            (root / release.binary_relative("linux-amd64")).write_bytes(b"replaced by manually rebuilt executable")
            with self.assertRaises(release.PackageError):
                release.validate_receipt(root, ["linux-amd64"], "2.0.0", COMMIT)
            (root / "core/bin/release-build.json").unlink()
            with self.assertRaises(release.PackageError):
                release.validate_receipt(root, ["linux-amd64"], "2.0.0", COMMIT)

    def test_controlled_build_injects_source_version_and_records_binary_hash(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.make_fixture(root)
            with patch.object(release, "source_metadata", return_value=("2.0.0", COMMIT, EPOCH)), patch.object(release, "inspect_binary"), patch.object(release, "command") as run:
                release.build_binaries(root, ["linux-amd64"], "go")
            args, cwd, env = run.call_args.args
            build_time = __import__("time").strftime("%Y-%m-%dT%H:%M:%SZ", __import__("time").gmtime(EPOCH))
            self.assertEqual(
                args[args.index("-ldflags") + 1],
                f"-s -w -X main.version=2.0.0 -X main.commit={COMMIT} -X main.releaseBuild=1 -X main.buildTime={build_time} -X main.dirty=false",
            )
            self.assertEqual(cwd, root / "core")
            self.assertEqual((env["GOOS"], env["GOARCH"]), ("linux", "amd64"))
            release.validate_receipt(root, ["linux-amd64"], "2.0.0", COMMIT)

    def test_output_symlink_is_rejected_without_touching_destination(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.make_fixture(root)
            (root / "outside").mkdir()
            (root / "linked").symlink_to(root / "outside", target_is_directory=True)
            with patch.object(release, "source_metadata", return_value=("2.0.0", COMMIT, EPOCH)), patch.object(release, "inspect_binary", return_value={}):
                with self.assertRaises(release.PackageError):
                    release.package(root, root / "linked", ["linux-amd64"], False, "go")
            self.assertEqual(list((root / "outside").iterdir()), [])

    def test_invalid_source_date_epoch_fails_clearly(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            (root / "VERSION").write_text("2.0.0")
            for value in ("tomorrow", "0", "4294967296"):
                with self.subTest(value=value), patch.dict(release.os.environ, {"SOURCE_DATE_EPOCH": value}), patch.object(release, "command", side_effect=[COMMIT, ""]):
                    with self.assertRaises(release.PackageError):
                        release.source_metadata(root)

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
