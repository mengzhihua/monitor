#!/usr/bin/env python3
"""Capture one or two admin-auth heap profiles for offline go tool pprof diffs.

Examples:
  python3 scripts/capture-heap.py --url http://127.0.0.1:19999 --token "$TOKEN" \\
      --output reports/heap-a.pb
  python3 scripts/capture-heap.py --url http://127.0.0.1:19999 --token "$TOKEN" \\
      --output reports/heap-a.pb --second reports/heap-b.pb --sleep 60
"""
from __future__ import annotations

import argparse
import pathlib
import sys
import time
import urllib.error
import urllib.request


def fetch_heap(base: str, token: str, dest: pathlib.Path) -> int:
    req = urllib.request.Request(
        base.rstrip('/') + '/api/v1/debug/pprof/heap',
        headers={'Authorization': 'Bearer ' + token},
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = resp.read()
    if len(data) < 32:
        raise RuntimeError(f'heap profile too small ({len(data)} bytes)')
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_bytes(data)
    return len(data)


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--url', required=True, help='monitord base URL, e.g. http://127.0.0.1:19999')
    p.add_argument('--token', required=True, help='admin bearer token')
    p.add_argument('--output', required=True, help='path for the first heap profile')
    p.add_argument('--second', help='optional path for a second profile after --sleep')
    p.add_argument('--sleep', type=float, default=60.0, help='seconds between profiles when --second is set')
    a = p.parse_args()
    try:
        n = fetch_heap(a.url, a.token, pathlib.Path(a.output))
        print(f'wrote {a.output} ({n} bytes)')
        if a.second:
            time.sleep(max(0.0, a.sleep))
            n2 = fetch_heap(a.url, a.token, pathlib.Path(a.second))
            print(f'wrote {a.second} ({n2} bytes)')
            print('compare with: go tool pprof -base', a.output, a.second)
    except (OSError, urllib.error.URLError, RuntimeError) as exc:
        print(f'capture-heap failed: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
