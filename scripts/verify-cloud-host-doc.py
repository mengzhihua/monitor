#!/usr/bin/env python3
"""Check that the cloud-host guide's relative links and commands exist."""
from __future__ import annotations

import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
DOC = ROOT / "docs" / "22-cloud-host.md"


def fail(msg: str) -> None:
    print(msg, file=sys.stderr)
    raise SystemExit(1)


def main() -> int:
    text = DOC.read_text(encoding="utf-8")
    for match in re.finditer(r"\[[^\]]+\]\(([^)]+)\)", text):
        target = match.group(1).split("#", 1)[0]
        if not target or target.startswith(("http://", "https://", "mailto:")):
            continue
        path = (DOC.parent / target).resolve()
        if not path.exists():
            fail(f"missing link target {target} -> {path}")
    script = ROOT / "packaging" / "cloud-host.sh"
    help_out = subprocess.run(["bash", str(script), "--help"], capture_output=True, text=True)
    if help_out.returncode != 0:
        fail(help_out.stderr)
    for command in ("install", "check", "ai", "upgrade"):
        if command not in help_out.stdout:
            fail(f"cloud-host.sh --help missing {command}")
        if f"cloud-host.sh {command}" not in text and f"./{command}" not in text and f" {command}" not in text:
            fail(f"guide does not mention cloud-host.sh {command}")
    if "packaging/cloud-host.sh" not in text and "cloud-host.sh" not in text:
        fail("guide does not name cloud-host.sh")
    if not (ROOT / "packaging" / "start.sh").is_file():
        fail("start.sh missing")
    if "start.sh --background" not in text:
        fail("guide lost the non-systemd start command")
    for path in (script, ROOT / "scripts" / "ci-soak.sh", ROOT / "packaging" / "start.sh"):
        syntax = subprocess.run(["bash", "-n", str(path)], capture_output=True, text=True)
        if syntax.returncode != 0:
            fail(f"{path}: {syntax.stderr}")
    print("cloud-host guide links and commands ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
