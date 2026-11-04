#!/usr/bin/env python3
"""Check local inline Markdown links in public docs; no network/dependencies.

Checks README.md, scripts/README.md and docs/*.md. Supports angle destinations,
balanced parentheses, optional titles, UTF-8 fragments and ATX heading anchors.
Fenced/inline code is ignored. Reference-style links and HTML hrefs are outside
this small guard's scope; it is not a complete Markdown renderer.
"""
from __future__ import annotations

import html
from pathlib import Path
import re
import sys
import unicodedata
from urllib.parse import unquote, urlsplit


def prose_lines(text: str):
    fence = None
    for number, line in enumerate(text.splitlines(), 1):
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", line)
        if fence:
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip():
                fence = None
            continue
        if marker:
            fence = marker[1]
            continue
        yield number, line


def anchors(text: str) -> set[str]:
    used: set[str] = set()
    for _, line in prose_lines(text):
        for explicit in re.finditer(r'<(?:a|h[1-6])\b[^>]*\b(?:id|name)=["\']([^"\']+)["\']', line):
            used.add(explicit[1])
        heading = re.match(r"^ {0,3}#{1,6}\s+(.+?)\s*$", line)
        if not heading:
            continue
        title = re.sub(r"\s+#+$", "", heading[1])
        title = re.sub(r"\[([^\]]+)\]\([^)]*\)", r"\1", title)
        title = html.unescape(re.sub(r"<[^>]+>", "", title)).lower()
        slug = "".join(c for c in title if c in " -_" or unicodedata.category(c)[0] in "LNM").replace(" ", "-")
        candidate, suffix = slug, 0
        while candidate in used:
            suffix += 1
            candidate = f"{slug}-{suffix}"
        used.add(candidate)
    return used


def links(text: str):
    for number, line in prose_lines(text):
        # Code-span backticks must close with a matching-length run.
        line = re.sub(r"(?<!`)(`+)(?!`)(.*?)\1(?!`)", "", line)
        for match in re.finditer(r"!?\[[^\]\n]*\]\(\s*", line):
            start = match.end()
            if line[start:start + 1] == "<":
                end = line.find(">", start + 1)
                if end >= 0:
                    yield number, line[start + 1:end]
                continue
            depth, end = 0, start
            while end < len(line):
                char = line[end]
                if char == "\\" and end + 1 < len(line):
                    end += 2
                    continue
                if char == "(":
                    depth += 1
                elif char == ")":
                    if depth == 0:
                        break
                    depth -= 1
                elif char.isspace() and depth == 0:
                    break
                end += 1
            if end > start:
                yield number, re.sub(r"\\([()])", r"\1", line[start:end])


def check(root: Path, sources: list[Path]) -> list[str]:
    errors = []
    cache = {}
    for source in sources:
        for line, target in links(source.read_text(encoding="utf-8")):
            url = urlsplit(target)
            if url.scheme or url.netloc:
                continue
            path = unquote(url.path)
            destination = (root / path.lstrip("/") if path.startswith("/") else source.parent / path) if path else source
            label = f"{source.relative_to(root)}:{line}: {target}"
            if not destination.exists():
                errors.append(f"{label}: missing local target")
            elif url.fragment and destination.suffix.lower() == ".md":
                if destination not in cache:
                    cache[destination] = anchors(destination.read_text(encoding="utf-8"))
                if unquote(url.fragment) not in cache[destination]:
                    errors.append(f"{label}: missing heading anchor")
    return errors


def main() -> int:
    root = Path(__file__).resolve().parents[1]
    sources = [root / "README.md", root / "scripts/README.md", *sorted((root / "docs").glob("*.md"))]
    errors = check(root, sources)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"Local inline documentation links OK ({len(sources)} files)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
