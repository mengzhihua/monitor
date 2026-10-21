#!/usr/bin/env python3
"""soak.py must not discover a production config beside the seed data dir."""
from __future__ import annotations

import pathlib
import sys
import tempfile

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import soak  # noqa: E402


PRODUCTION = """
web:
  listen: "0.0.0.0:19999"
  token: "production-fixed-token"
health:
  enabled: true
  notify:
    webhook:
      url: https://hooks.example/prod
  actions:
    - name: restart
commands:
  - name: restart
    argv: ["/bin/true"]
trapper:
  enabled: true
notify:
  roles:
    sysadmin: [webhook]
collectors:
  enabled: [cpu, mem]
"""


def main() -> int:
    stripped = soak.strip_seed_config(PRODUCTION)
    for banned in ("hooks.example", "trapper:", "restart", "notify:"):
        if banned in stripped:
            raise SystemExit(f"seed config still contains {banned!r}:\n{stripped}")
    if 'token: "production-fixed-token"' not in stripped and "production-fixed-token" not in stripped:
        raise SystemExit(f"fixed token was removed:\n{stripped}")
    if soak.web_token(stripped) != "production-fixed-token":
        raise SystemExit(f"token parse failed: {soak.web_token(stripped)!r}")
    nested = """
web:
  users:
    - name: viewer
      token: not-the-admin
  token: "admin-token"
collectors:
  enabled:
    - cpu
    - mem
"""
    if soak.web_token(nested) != "admin-token":
        raise SystemExit(f"nested token wins: {soak.web_token(nested)!r}")
    if soak.expects_ml(nested):
        raise SystemExit("cpu/mem config should not expect ml")
    if not soak.expects_ml("collectors:\n  enabled: [cpu, ml]\n"):
        raise SystemExit("inline ml list not detected")
    if not soak.expects_ml("mode: agent\n"):
        raise SystemExit("default config should expect ml")

    with tempfile.TemporaryDirectory() as tmp:
        root = pathlib.Path(tmp)
        data = root / "home" / "box" / "monitor" / "data"
        data.mkdir(parents=True)
        (data.parent / "monitor.yaml").write_text(PRODUCTION, encoding="utf-8")
        work = root / "work"
        work.mkdir()
        cfg = soak.resolve_soak_config(work, None)
        text = cfg.read_text(encoding="utf-8")
        if "production-fixed-token" in text or "hooks.example" in text:
            raise SystemExit("resolve without --seed-config read a sibling production file")
        src = root / "seed.yaml"
        src.write_text(PRODUCTION, encoding="utf-8")
        work2 = root / "work2"
        work2.mkdir()
        copied = soak.resolve_soak_config(work2, str(src))
        copied_text = copied.read_text(encoding="utf-8")
        if "hooks.example" in copied_text or "trapper:" in copied_text:
            raise SystemExit(copied_text)
        if soak.web_token(copied_text) != "production-fixed-token":
            raise SystemExit("explicit seed config lost its token")
    marks = soak.heap_marks(7200, 1800)
    if marks != [0, 1800, 3600, 5400, 7200]:
        raise SystemExit(f"heap marks {marks}")
    if soak.heap_marks(120, 0) != [0, 120]:
        raise SystemExit("short runs still capture start and end")
    if soak.default_heap_every(7200) != 1800 or soak.default_heap_every(6 * 3600) != 7200 or soak.default_heap_every(120) != 0:
        raise SystemExit(f"heap interval {soak.default_heap_every(6 * 3600)}")
    if soak.heap_marks(8 * 3600, 7200) != [0, 7200, 14400, 21600, 28800]:
        raise SystemExit("2h marks")
    if soak.heap_label(1800) != "t00h30m" or soak.heap_label(0) != "t00h00m" or soak.heap_label(8) != "t00h00m08s":
        raise SystemExit(f"heap label {soak.heap_label(1800)} {soak.heap_label(8)}")

    class Args:
        max_rss_mib = None
        max_heap_growth_mib = None
        max_rss_growth_mib = None
        max_anomaly_buffer_mib = None

    two_hour = Args()
    soak.apply_duration_thresholds(7200, two_hour)
    if (two_hour.max_rss_mib, two_hour.max_heap_growth_mib, two_hour.max_rss_growth_mib, two_hour.max_anomaly_buffer_mib) != (450, 80, 120, 16):
        raise SystemExit("2h caps")
    day = Args()
    soak.apply_duration_thresholds(86400, day)
    if day.max_heap_growth_mib != 200 or day.max_rss_mib != 800:
        raise SystemExit("24h caps")
    short = Args()
    soak.apply_duration_thresholds(120, short)
    if short.max_heap_growth_mib is not None:
        raise SystemExit("short runs stay uncapped unless asked")
    print("soak config isolation ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
