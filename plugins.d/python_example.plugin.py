#!/usr/bin/env python3
"""Example plugins.d collector in Python.

Enable it in monitor.yaml (or rename to *.plugin and chmod +x for auto-discovery):

    plugins:
      list:
        - name: python_example
          command: /usr/bin/python3
          args: [plugins.d/python_example.plugin.py]

Protocol reference: docs/03-plugins-d-protocol.md
"""
import os
import sys
import time

every = int(sys.argv[1]) if len(sys.argv) > 1 else int(os.environ.get("MONITOR_UPDATE_EVERY", "1"))


def out(line: str) -> None:
    sys.stdout.write(line + "\n")
    sys.stdout.flush()  # the agent reads line by line; never leave data buffered


out(f"CHART python.load '' 'Python process load' 'seconds' python python.load line 90010 {every} '' python_example clock")
out("DIMENSION cpu 'cpu time' incremental 1 1")
out("DIMENSION wall 'wall clock' incremental 1 1")

while True:
    out("BEGIN python.load")
    out(f"SET cpu = {time.process_time():.6f}")
    out(f"SET wall = {time.monotonic():.6f}")
    out("END")
    time.sleep(every)
