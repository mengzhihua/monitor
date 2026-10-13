#!/usr/bin/env python3
"""Stamp VERSION, Dashboard package files, and monitord's version string.

Used by release.yml after an auto-tag so REQUIRE_GIT_TAG_VERSION stays aligned.
Keeping this out of the workflow YAML avoids IndentationError from python -c
bodies nested under shell if-blocks inside run:|.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys

SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
VERSION_DECL = re.compile(r'^var version = "[^"]+"', re.M)


def stamp(root: pathlib.Path, version: str) -> None:
    if not SEMVER.fullmatch(version):
        raise SystemExit(f"version must be stable MAJOR.MINOR.PATCH: {version!r}")
    (root / "VERSION").write_text(version + "\n")
    for file in ("web/package.json", "web/package-lock.json"):
        path = root / file
        data = json.loads(path.read_text())
        data["version"] = version
        if file.endswith("package-lock.json"):
            data["packages"][""]["version"] = version
        path.write_text(json.dumps(data, indent=2) + "\n")
    main = root / "core/cmd/monitord/main.go"
    text, n = VERSION_DECL.subn(f'var version = "{version}"', main.read_text(), count=1)
    if n != 1:
        raise SystemExit("could not stamp core/cmd/monitord/main.go")
    main.write_text(text)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("version", help="stable version without the v prefix")
    p.add_argument("--root", type=pathlib.Path, default=pathlib.Path(__file__).resolve().parents[1])
    args = p.parse_args(argv)
    stamp(args.root.resolve(), args.version)
    print(f"stamped release version {args.version}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
