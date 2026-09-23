#!/usr/bin/env python3
"""Package committed server builds without copying configuration or runtime data.

Run ``make package-server`` after committing the release. A controlled build
writes a receipt binding the injected VERSION and commit to each binary SHA256.
Packaging existing binaries requires that receipt; filename labels alone do not
establish the embedded version of a cross-compiled executable.
Requires Python 3.9+, Git and Go; macOS Universal packaging also requires Apple's
lipo/codesign. The Python implementation has no third-party dependencies.

    python3 scripts/package-server-release.py --build
    python3 scripts/package-server-release.py  # reuse a matching build receipt
    python3 scripts/package-server-release.py --target linux-amd64 --no-universal

Output: dist/monitor-<VERSION>-<12-character-commit>/ (never overwritten).
"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import zipfile


TARGETS = (
    "linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64",
    "windows-amd64", "freebsd-amd64", "freebsd-arm64", "android-arm64",
)
DOCUMENTS = (
    "README.md", "monitor.example.yaml", "docs/01-netdata-capability-study.md",
    "docs/02-architecture.md", "docs/03-plugins-d-protocol.md", "docs/04-netdata-gap.md",
    "docs/05-acceptance.md", "docs/06-monitor-2.0.md",
    "docs/07-preset-dashboards.md", "docs/08-release-2.0.md",
    "docs/08-operations-dashboard-research.md", "docs/09-dashboard-final-update.md",
    "docs/10-final-integration.md", "scripts/README.md",
)
VERSION_RE = re.compile(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?")
COMMIT_RE = re.compile(r"[0-9a-f]{40}(?:[0-9a-f]{24})?")


class PackageError(Exception):
    pass


def command(args: list[str], cwd: Path | None = None, env: dict | None = None) -> str:
    try:
        result = subprocess.run(args, cwd=cwd, env=env, check=False, capture_output=True, text=True)
    except OSError as exc:
        raise PackageError(f"Cannot run {args[0]}: {exc}") from exc
    if result.returncode:
        # Do not echo arbitrary tool output (e.g. compiler flags containing secrets).
        raise PackageError(f"{Path(args[0]).name} failed (exit {result.returncode})")
    return result.stdout.strip()


def regular_file(root: Path, relative: str) -> Path:
    """Reject traversal and symlinks in every component, including parent dirs."""
    rel = Path(relative)
    if rel.is_absolute() or not rel.parts or any(part in (".", "..") for part in rel.parts):
        raise PackageError(f"Unsafe package input: {relative}")
    current = root
    for part in rel.parts:
        current = current / part
        if current.is_symlink():
            raise PackageError(f"Symbolic links are not package inputs: {relative}")
    try:
        if not stat.S_ISREG(current.stat().st_mode):
            raise PackageError(f"Not a regular file: {relative}")
    except FileNotFoundError as exc:
        raise PackageError(f"Missing package input: {relative}") from exc
    return current


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def json_bytes(value: object) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")


def source_metadata(root: Path) -> tuple[str, str, int]:
    version = regular_file(root, "VERSION").read_text(encoding="utf-8").strip()
    if not VERSION_RE.fullmatch(version):
        raise PackageError("VERSION must contain a safe semantic version")
    commit = command(["git", "rev-parse", "HEAD"], root)
    if not COMMIT_RE.fullmatch(commit):
        raise PackageError("Git did not return a full commit SHA")
    if command(["git", "status", "--porcelain", "--untracked-files=no"], root):
        raise PackageError("Commit tracked source changes before building release packages")
    epoch_text = os.environ.get("SOURCE_DATE_EPOCH")
    if epoch_text is None:
        epoch_text = command(["git", "show", "-s", "--format=%ct", "HEAD"], root)
    try:
        epoch = int(epoch_text)
        if not 315532800 <= epoch <= 4294967295:  # shared ZIP/gzip range, 1980..2106
            raise ValueError()
    except ValueError as exc:
        raise PackageError("Release timestamp must be within the ZIP/gzip 1980..2106 range") from exc
    return version, commit, epoch


def inspect_binary(path: Path, target: str, version: str, commit: str, go: str) -> dict:
    try:
        info = json.loads(command([go, "version", "-m", "-json", str(path)]))
        settings = {item["Key"]: item["Value"] for item in info["Settings"]}
        go_version = info["GoVersion"]
        executable_path = info["Path"]
    except (ValueError, TypeError, KeyError) as exc:
        raise PackageError(f"Missing Go build metadata: {path.name}") from exc
    os_name, arch = target.split("-")
    if executable_path != "github.com/mengzhihua/monitor/core/cmd/monitord":
        raise PackageError(f"Unexpected Go executable: {path.name}")
    if (settings.get("GOOS"), settings.get("GOARCH")) != (os_name, arch):
        raise PackageError(f"Binary platform does not match {target}: {path.name}")
    if settings.get("vcs.revision") != commit or settings.get("vcs.modified") != "false":
        raise PackageError(f"Rebuild {path.name} from the clean current commit before packaging")
    host_os = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows", "FreeBSD": "freebsd"}.get(platform.system())
    host_arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine())
    version_verified = (os_name, arch) == (host_os, host_arch)
    if version_verified and command([str(path), "-version"]) != f"monitord {version} {os_name}/{arch}":
        raise PackageError(f"Runtime version does not match VERSION: {path.name}")
    return {
        "go_version": go_version,
        "source_commit": settings["vcs.revision"],
        "source_modified": False,
        "os": os_name,
        "architecture": arch,
        "cgo_enabled": settings.get("CGO_ENABLED"),
        "runtime_version_verified_on_build_host": version_verified,
    }


def binary_relative(target: str) -> str:
    return f"core/bin/monitord-{target}" + (".exe" if target.startswith("windows-") else "")


def build_binaries(root: Path, targets: list[str], go: str) -> None:
    """Own the version-injecting command; a subsequent SHA binds its exact output."""
    version, commit, _ = source_metadata(root)
    bin_dir = root / "core/bin"
    if (root / "core").is_symlink() or bin_dir.is_symlink():
        raise PackageError("Binary output path must not be a symbolic link")
    bin_dir.mkdir(parents=True, exist_ok=True)
    receipt_path = bin_dir / "release-build.json"
    if receipt_path.is_symlink():
        raise PackageError("Build receipt must not be a symbolic link")
    binaries = {}
    for target in targets:
        if target not in TARGETS:
            raise PackageError("Unsupported build target")
        os_name, arch = target.split("-")
        binary = root / binary_relative(target)
        if binary.is_symlink():
            raise PackageError(f"Binary output must not be a symbolic link: {target}")
        env = os.environ.copy()
        env.update({"GOOS": os_name, "GOARCH": arch})
        if os_name == "android":
            env["CGO_ENABLED"] = "0"
        print(f"Building {target} for Monitor {version}", flush=True)
        command([go, "build", "-trimpath", "-ldflags", f"-s -w -X main.version={version}", "-o", str(binary), "./cmd/monitord"], root / "core", env)
        inspect_binary(binary, target, version, commit, go)
        binaries[target] = {"version": version, "source_commit": commit, "sha256": sha256(binary)}
    if source_metadata(root)[:2] != (version, commit):
        raise PackageError("Source changed while building")
    receipt = json_bytes({"schema_version": 1, "version": version, "source_commit": commit, "binaries": binaries})
    with tempfile.NamedTemporaryFile(prefix=".release-build-", dir=bin_dir, delete=False) as dest:
        temporary = Path(dest.name)
        try:
            dest.write(receipt)
            dest.flush()
            os.fsync(dest.fileno())
        except BaseException:
            temporary.unlink(missing_ok=True)
            raise
    try:
        temporary.replace(receipt_path)
    finally:
        temporary.unlink(missing_ok=True)


def validate_receipt(root: Path, targets: list[str], version: str, commit: str) -> None:
    try:
        receipt = json.loads(regular_file(root, "core/bin/release-build.json").read_text(encoding="utf-8"))
        if (receipt["schema_version"], receipt["version"], receipt["source_commit"]) != (1, version, commit):
            raise ValueError()
        for target in targets:
            expected = {"version": version, "source_commit": commit, "sha256": sha256(regular_file(root, binary_relative(target)))}
            if receipt["binaries"][target] != expected:
                raise ValueError()
    except (ValueError, KeyError, TypeError, PackageError) as exc:
        raise PackageError("Missing or mismatched controlled-build receipt; run with --build") from exc


def launcher(os_name: str) -> tuple[str, bytes]:
    if os_name == "windows":
        return "start.cmd", (
            '@echo off\r\ncd /d "%~dp0"\r\n'
            'echo Open http://127.0.0.1:20099/ ; password: data\\web-password\r\n'
            'monitord.exe -listen 127.0.0.1:20099 -data-dir .\\data\r\n'
            'if errorlevel 1 pause\r\n'
        ).encode("ascii")
    return ("运行.command" if os_name == "darwin" else "start.sh"), (
        '#!/bin/sh\nset -eu\ncd -- "$(dirname -- "$0")"\n'
        'printf "%s\\n" "Open http://127.0.0.1:20099/ ; password: data/web-password"\n'
        'exec ./monitord -listen 127.0.0.1:20099 -data-dir ./data\n'
    ).encode("utf-8")


def quickstart(version: str, os_name: str, architecture: str, launch_name: str) -> bytes:
    signing = "macOS 包没有 Developer ID 签名或公证；Universal 包使用临时签名（ad-hoc）。\n" if os_name == "darwin" else ""
    android = "Android 包是命令行服务端，需要可执行原生二进制的设备环境；不是 APK。\n" if os_name == "android" else ""
    return (
        f"Monitor {version} 服务端 + 内嵌 Web\n目标：{os_name}/{architecture}\n\n"
        f"启动：{launch_name}（Unix 可执行 ./{launch_name}）。\n"
        "浏览器：http://127.0.0.1:20099/\n"
        "首次启动会在本包目录的 data/web-password 创建随机登录密码。\n"
        "启动器只监听本机，使用本包目录独立的 data；不要与已有运行实例共用数据。\n"
        "若端口占用，请手动指定其他 -listen 地址。按 Ctrl+C 正常停止。\n\n"
        "包内 monitor.example.yaml 仅是示例，不会自动加载；按需复制为 monitor.yaml。\n"
        "已有安装升级前先停止旧服务并备份原始配置和完整数据，阅读 docs/08-release-2.0.md。\n"
        "升级原安装应保留原有配置/数据路径，勿用空白示例覆盖。\n"
        "不要在运行中替换二进制、移动数据或并发启动多个实例写同一数据目录。\n\n"
        f"{signing}{android}"
        "各平台运行验证范围见发布说明，交叉编译成功不代表真实设备验证。\n"
        "BUILD.json 记录版本、源提交和目标平台；外层 manifest.json 与 SHA256SUMS 记录包校验值。\n"
    ).encode("utf-8")


def write_archive(path: Path, folder: str, entries: dict[str, tuple[Path | bytes, int]], epoch: int) -> None:
    """Only explicitly supplied regular files are copied; no recursive walking."""
    if path.suffix == ".zip":
        date = time.gmtime(epoch)[:6]
        with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
            for name, (content, mode) in sorted(entries.items()):
                entry = zipfile.ZipInfo(f"{folder}/{name}", date_time=date)
                entry.create_system = 3
                entry.external_attr = (stat.S_IFREG | mode) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                with archive.open(entry, "w", force_zip64=True) as dest:
                    if isinstance(content, Path):
                        with content.open("rb") as source:
                            shutil.copyfileobj(source, dest, length=1024 * 1024)
                    else:
                        dest.write(content)
        return
    with path.open("wb") as raw:
        with gzip.GzipFile(fileobj=raw, filename="", mode="wb", mtime=epoch, compresslevel=6) as compressed:
            with tarfile.open(fileobj=compressed, mode="w|", format=tarfile.PAX_FORMAT) as archive:
                for name, (content, mode) in sorted(entries.items()):
                    entry = tarfile.TarInfo(f"{folder}/{name}")
                    entry.mode, entry.mtime = mode, epoch
                    entry.uid = entry.gid = 0
                    entry.uname = entry.gname = ""
                    if isinstance(content, Path):
                        entry.size = content.stat().st_size
                        with content.open("rb") as source:
                            archive.addfile(entry, source)
                    else:
                        entry.size = len(content)
                        archive.addfile(entry, io.BytesIO(content))


def make_universal(sources: dict[str, Path], dest: Path) -> None:
    command(["lipo", "-create", str(sources["darwin-amd64"]), str(sources["darwin-arm64"]), "-output", str(dest)])
    if set(command(["lipo", "-archs", str(dest)]).split()) != {"x86_64", "arm64"}:
        raise PackageError("Universal binary must contain exactly x86_64 and arm64")
    command(["codesign", "--force", "--sign", "-", "--timestamp=none", str(dest)])
    command(["codesign", "--verify", "--strict", "--all-architectures", str(dest)])


def package(root: Path, output_root: Path, targets: list[str], universal: bool, go: str) -> Path:
    version, commit, epoch = source_metadata(root)
    if len(set(targets)) != len(targets) or any(target not in TARGETS for target in targets):
        raise PackageError("Targets must be distinct supported platform names")
    if universal and platform.system() != "Darwin":
        raise PackageError("macOS Universal packages must be made on macOS")
    inputs = list(dict.fromkeys(targets + (["darwin-amd64", "darwin-arm64"] if universal else [])))
    if not inputs:
        raise PackageError("Select at least one target")
    validate_receipt(root, inputs, version, commit)
    documents = {name: regular_file(root, name) for name in DOCUMENTS}
    sources: dict[str, Path] = {}
    builds: dict[str, dict] = {}
    for target in inputs:
        binary = regular_file(root, binary_relative(target))
        sources[target] = binary
        builds[target] = inspect_binary(binary, target, version, commit, go)

    # Do not follow output symlinks, including when a parent directory is linked.
    output_root = output_root.absolute()
    if any(path.is_symlink() for path in (output_root, *output_root.parents)):
        raise PackageError("Output path must not contain symbolic links")
    output_root.mkdir(parents=True, exist_ok=True)
    release_name = f"monitor-{version}-{commit[:12]}"
    final = output_root / release_name
    if final.exists() or final.is_symlink():
        raise PackageError(f"Output already exists; use a different --output-root: {final}")
    with tempfile.TemporaryDirectory(prefix=".monitor-package-", dir=output_root) as temporary:
        staging = Path(temporary)
        all_targets = list(targets)
        if universal:
            universal_binary = staging / "monitord-universal"
            make_universal(sources, universal_binary)
            sources["darwin-universal"] = universal_binary
            builds["darwin-universal"] = {
                "os": "darwin", "architecture": "universal",
                "architectures": ["amd64", "arm64"],
                "slices": [builds["darwin-amd64"], builds["darwin-arm64"]],
                "signature": "ad-hoc", "notarized": False,
            }
            expected_arch = {"x86_64": "amd64", "arm64": "arm64"}.get(platform.machine())
            if command([str(universal_binary), "-version"]) != f"monitord {version} darwin/{expected_arch}":
                raise PackageError("Universal executable runtime version does not match VERSION")
            all_targets.append("darwin-universal")
        artifacts = []
        for target in all_targets:
            os_name, arch = target.split("-")
            binary_name = "monitord.exe" if os_name == "windows" else "monitord"
            binary_hash = sha256(sources[target])
            build = {
                "schema_version": 1, "version": version, "source_commit": commit,
                "source_modified": False, "source_date_epoch": epoch,
                "version_source": "controlled go build receipt binds injected VERSION to binary SHA256",
                "target": target, "binary_sha256": binary_hash, "build": builds[target],
            }
            if os_name == "darwin":
                build["notarized"] = False
            launch_name, launch_data = launcher(os_name)
            entries: dict[str, tuple[Path | bytes, int]] = {name: (path, 0o644) for name, path in documents.items()}
            entries.update({
                binary_name: (sources[target], 0o755), launch_name: (launch_data, 0o755),
                "使用说明.txt": (quickstart(version, os_name, arch, launch_name), 0o644),
                "BUILD.json": (json_bytes(build), 0o644),
            })
            label = target.replace("darwin-", "macos-")
            folder = f"monitor-{version}-{label}"
            archive = staging / (folder + (".zip" if os_name == "windows" else ".tar.gz"))
            write_archive(archive, folder, entries, epoch)
            if sha256(sources[target]) != binary_hash:
                raise PackageError(f"Binary changed while packaging: {target}")
            artifacts.append({
                "filename": archive.name, "target": target, "sha256": sha256(archive),
                "size_bytes": archive.stat().st_size, "binary_sha256": binary_hash,
                "files": sorted(entries), "build": builds[target],
            })
            print(f"Packaged {archive.name}", flush=True)
        if universal:
            sources["darwin-universal"].unlink()  # only our temporary intermediate
        manifest = staging / "manifest.json"
        manifest.write_bytes(json_bytes({
            "schema_version": 1, "version": version, "source_commit": commit,
            "source_modified": False, "source_date_epoch": epoch,
            "artifacts": artifacts,
        }))
        checksums = [f"{item['sha256']}  {item['filename']}\n" for item in artifacts]
        checksums.append(f"{sha256(manifest)}  manifest.json\n")
        (staging / "SHA256SUMS").write_text("".join(checksums), encoding="utf-8")
        # Recheck tracked source after packaging; source edits invalidate the release.
        if source_metadata(root)[:2] != (version, commit):
            raise PackageError("Source changed while packaging")
        if final.exists() or final.is_symlink():
            raise PackageError("Output appeared while packaging; refusing to overwrite it")
        staging.rename(final)
    return final


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--target", action="append", choices=TARGETS, help="Repeat to select targets; default: all eight")
    parser.add_argument("--no-universal", action="store_true", help="Skip the additional macOS Universal archive")
    parser.add_argument("--output-root", type=Path, help="Parent output directory (default: repo/dist)")
    parser.add_argument("--go", default="go", help="Go executable used to inspect build metadata")
    build_mode = parser.add_mutually_exclusive_group()
    build_mode.add_argument("--build", action="store_true", help="Build targets with the committed VERSION and create a receipt before packaging; build Web first")
    build_mode.add_argument("--build-only", action="store_true", help="Build targets and create a receipt without packaging; build Web first")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    targets = args.target or list(TARGETS)
    universal = not args.no_universal and platform.system() == "Darwin" and (args.target is None or any(target.startswith("darwin-") for target in targets))
    try:
        if args.build or args.build_only:
            inputs = list(dict.fromkeys(targets + (["darwin-amd64", "darwin-arm64"] if universal else [])))
            build_binaries(root, inputs, args.go)
        if args.build_only:
            print(root / "core/bin/release-build.json")
            return 0
        result = package(root, args.output_root or root / "dist", targets, universal, args.go)
    except (PackageError, OSError) as exc:
        print(f"Release packaging failed: {exc}", file=sys.stderr)
        return 1
    print(result)
    return 0


if __name__ == "__main__":
    sys.exit(main())
