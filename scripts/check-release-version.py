#!/usr/bin/env python3
"""Check source versions and reject release regressions; allow an untagged version bump."""
from __future__ import annotations

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

    Using reachability keeps tags published on unrelated/newer branches from
    changing the version floor of the checked-out history.
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


def check(root: Path, require_tag: bool = False) -> None:
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

    tag_version = latest_stable_tag_reachable(root)
    # Inspect a matching tag even when it is not reachable from this branch.
    # A tag attached to the wrong source must not silently bless a future bump.
    tagged = run_git(root, "show", f"v{version}:VERSION")
    if tagged is not None and tagged.strip() != version:
        raise ValueError(
            f"tag v{version} points at a commit whose VERSION is {tagged.strip()!r}; "
            "use a new version on the intended source commit (see docs/23-release.md)"
        )
    if tag_version is None:
        if require_tag:
            raise ValueError("no stable git tag reachable from HEAD; fetch tags or set VERSION from the release tag")
        print(f"Server and Dashboard source versions match: {version} (no reachable git tag to compare)")
        return

    source_base = tuple(int(p) for p in version.split("-", 1)[0].split("."))
    tag_base = tuple(int(p) for p in tag_version.split("."))
    if source_base < tag_base or (source_base == tag_base and "-" in version):
        raise ValueError(f"VERSION {version!r} is older than reachable stable tag v{tag_version}")
    if require_tag and version != tag_version:
        raise ValueError(
            f"VERSION {version!r} differs from latest stable git tag reachable from HEAD (v{tag_version}); "
            "bump VERSION/package.json/main.go to match the tag, or create the matching tag"
        )
    if version != tag_version:
        print(f"Source versions match: {version} (unreleased; latest reachable stable tag v{tag_version})")
        return
    print(f"Server and Dashboard source versions match git tag v{tag_version}: {version}")


if __name__ == "__main__":
    try:
        check(
            Path(__file__).resolve().parents[1],
            os.environ.get("REQUIRE_GIT_TAG_VERSION", "").lower() in {"1", "true", "yes"},
        )
    except (OSError, ValueError) as error:
        print(f"version check failed: {error}", file=sys.stderr)
        sys.exit(1)
