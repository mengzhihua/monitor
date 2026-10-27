#!/usr/bin/env python3
"""Ensure docs/15-zabbix-gap.md matrix evidence paths exist in the tree."""
from __future__ import annotations

import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
DOC = ROOT / "docs" / "15-zabbix-gap.md"


def resolve_path(token: str, prev: pathlib.Path | None) -> pathlib.Path | None:
    token = token.strip()
    if not token or "*" in token:
        return None
    if token.startswith("core/"):
        return ROOT / token
    if token.startswith("web/"):
        return ROOT / token
    if token.startswith("api/"):
        for base in (ROOT, ROOT / "core/internal"):
            candidate = base / token
            if candidate.exists():
                return candidate
        return ROOT / "core/internal" / token
    if "/" in token:
        for base in (ROOT, ROOT / "core/internal", ROOT / "core/cmd/monitord"):
            candidate = base / token
            if candidate.exists():
                return candidate
        return ROOT / "core/internal" / token
    if prev is not None:
        return prev.parent / token
    for base in (ROOT / "core/internal", ROOT):
        candidate = base / token
        if candidate.exists():
            return candidate
    return ROOT / "core/internal" / token


def evidence_paths(cell: str) -> list[pathlib.Path]:
    out: list[pathlib.Path] = []
    prev: pathlib.Path | None = None
    for segment in re.findall(r"`([^`]+)`", cell):
        for part in re.split(r"[、,]\s*", segment):
            part = part.strip()
            if not part:
                continue
            resolved = resolve_path(part, prev)
            if resolved is None:
                continue
            out.append(resolved)
            prev = resolved
    return out


def main() -> int:
    text = DOC.read_text(encoding="utf-8")
    missing: list[str] = []
    for line in text.splitlines():
        if not line.startswith("|") or line.startswith("|---"):
            continue
        parts = [p.strip() for p in line.split("|")]
        if len(parts) < 6:
            continue
        domain, capability, status, evidence = parts[1], parts[2], parts[3], parts[4]
        if domain == "领域" or evidence == "证据":
            continue
        for path in evidence_paths(evidence):
            if path.is_dir():
                if not any(path.iterdir()):
                    missing.append(f"{domain} / {capability}: empty dir {path}")
                continue
            if not path.is_file():
                missing.append(f"{domain} / {capability}: missing {path}")
    if missing:
        for item in missing:
            print(item, file=sys.stderr)
        return 1
    print("zabbix gap matrix evidence paths ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
