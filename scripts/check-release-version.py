#!/usr/bin/env python3
"""Fail a release build when server and embedded Dashboard source versions drift."""
import json
from pathlib import Path
import re
import sys


def main():
    root = Path(__file__).resolve().parents[1]
    version = (root / "VERSION").read_text().strip()
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?", version):
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
    print(f"Server and Dashboard source versions match: {version}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        print(f"version check failed: {error}", file=sys.stderr)
        sys.exit(1)
