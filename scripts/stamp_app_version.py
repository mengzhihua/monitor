#!/usr/bin/env python3
"""Stamp Flutter app version so release artifacts match the Git tag.

Flutter Linux/Windows/macOS embed `pubspec.yaml` into
`data/flutter_assets/version.json`. `--build-name` / `--build-number` do not
always rewrite that file on desktop, so CI stamps pubspec before `flutter
build` and rewrites version.json afterwards.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys


def find_macos_version_json_targets(root: pathlib.Path) -> list[pathlib.Path]:
    """Locate (or invent) version.json paths under a Flutter macOS Release dir."""
    apps = sorted(root.glob("*.app"))
    if not apps:
        raise FileNotFoundError(f"no .app under {root}")
    out: list[pathlib.Path] = []
    for app in apps:
        dirs = [p for p in app.rglob("flutter_assets") if p.is_dir()]
        if not dirs:
            # Prefer the standard App.framework location when the tree is empty
            # in tests; live CI fails earlier if flutter_assets is missing.
            dirs = [app / "Contents/Frameworks/App.framework/Resources/flutter_assets"]
        for d in dirs:
            out.append(d / "version.json")
    # stable unique
    seen: set[pathlib.Path] = set()
    uniq: list[pathlib.Path] = []
    for p in out:
        if p in seen:
            continue
        seen.add(p)
        uniq.append(p)
    return uniq


def stamp_pubspec(text: str, version: str, build: str) -> str:
    new, n = re.subn(r"(?m)^version:\s*.*$", f"version: {version}+{build}", text, count=1)
    if n != 1:
        raise ValueError("pubspec.yaml has no version: line")
    return new


def stamp_version_json(text: str, version: str, build: str) -> str:
    data = json.loads(text) if text.strip() else {}
    if not isinstance(data, dict):
        raise ValueError("version.json is not an object")
    data["version"] = version
    data["build_number"] = str(build)
    return json.dumps(data, indent=2) + "\n"


def _write(path: pathlib.Path, body: str) -> None:
    path.write_text(body, encoding="utf-8")
    print(f"stamped {path}")


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("version", help="semver without v prefix, e.g. 0.1.20")
    p.add_argument("build", help="numeric build number")
    p.add_argument("--pubspec", type=pathlib.Path, help="app/pubspec.yaml")
    p.add_argument("--json", type=pathlib.Path, dest="json_path", help="flutter_assets/version.json")
    args = p.parse_args(argv)
    if args.pubspec is None and args.json_path is None:
        p.error("pass --pubspec and/or --json")
    if args.pubspec is not None:
        _write(args.pubspec, stamp_pubspec(args.pubspec.read_text(encoding="utf-8"), args.version, args.build))
    if args.json_path is not None:
        raw = args.json_path.read_text(encoding="utf-8") if args.json_path.exists() else "{}"
        _write(args.json_path, stamp_version_json(raw, args.version, args.build))
    return 0


if __name__ == "__main__":
    sys.exit(main())
