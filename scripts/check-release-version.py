#!/usr/bin/env python3
"""Fail when server/Dashboard source versions drift, or when VERSION disagrees with git tags."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

STABLE_TAG = re.compile(r"^v((0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*))$")
SEMVER = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?")


def run_git(root: Path, *args: str) -> str | None:
    try:
        return subprocess.run(
            ["git", *args],
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
        ).stdout
    except (OSError, subprocess.CalledProcessError):
        return None


def latest_stable_tag_reachable(root: Path) -> str | None:
    """Highest stable tag reachable from HEAD (not tags pushed for newer commits).

    Using reachability avoids racing CI against release.yml, which may push the
    next tag before the matching VERSION commit lands on main.
    """
    out = run_git(root, "tag", "-l", "v[0-9]*", "--merged", "HEAD")
    if out is None:
        return None
    versions = []
    for tag in out.splitlines():
        match = STABLE_TAG.fullmatch(tag.strip())
        if match:
            versions.append(match.group(1))
    if not versions:
        return None
    versions.sort(key=lambda v: [int(p) for p in v.split(".")])
    return versions[-1]


def main():
    root = Path(__file__).resolve().parents[1]
    version = (root / "VERSION").read_text().strip()
    if not SEMVER.fullmatch(version):
        raise ValueError("VERSION must be a semantic version without the v prefix")
    package = json.loads((root / "web/package.json").read_text())
    lock = json.loads((root / "web/package-lock.json").read_text())
    source = (root / "core/cmd/monitord/main.go").read_text()
    match = re.search(r'^var version = "([^"]+)"', source, re.MULTILINE)
    versions = {
        "web/package.json": package.get("version"),
        "web/package-lock.json": lock.get("version"),
        "web/package-lock.json packages root": lock.get("packages", {}).get("", {}).get("version"),
        "core/cmd/monitord/main.go": match.group(1) if match else None,
    }
    for name, actual in versions.items():
        if actual != version:
            raise ValueError(f"{name}: {actual!r} differs from VERSION {version!r}")

    require_tag = os.environ.get("REQUIRE_GIT_TAG_VERSION", "").lower() in {"1", "true", "yes"}
    tag_version = latest_stable_tag_reachable(root)
    if tag_version is None:
        if require_tag:
            raise ValueError("no stable git tag reachable from HEAD; fetch tags or set VERSION from the release tag")
        print(f"Server and Dashboard source versions match: {version} (no reachable git tag to compare)")
        return

    if version != tag_version:
        raise ValueError(
            f"VERSION {version!r} differs from latest stable git tag reachable from HEAD (v{tag_version}); "
            "bump VERSION/package.json/main.go to match the tag, or create the matching tag"
        )
    print(f"Server and Dashboard source versions match git tag v{tag_version}: {version}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        print(f"version check failed: {error}", file=sys.stderr)
        sys.exit(1)
