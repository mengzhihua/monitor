#!/usr/bin/env python3
"""Action audit acceptance against an isolated daemon.

This script starts its own monitord, alarm, and whitelisted command. It does
not read data directories or audit logs left by other acceptance scripts.
"""
import json
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


def main() -> None:
    binary = Path('core/bin/monitord').resolve()
    if not binary.is_file():
        raise SystemExit(f'missing {binary}; build monitord before this check')
    python = shutil.which('python3') or shutil.which('python')
    if not python or not Path(python).is_absolute():
        raise SystemExit('python3 must resolve to an absolute path for commands.argv')
    token = 'action-audit-token'
    with tempfile.TemporaryDirectory(prefix='monitor-actions-') as tmp:
        root = Path(tmp)
        config = root / 'config.yaml'
        config.write_text(f'''web:
  token: {token}
collectors:
  enabled: [cpu, mem]
plugins:
  enabled: false
commands:
  - name: mark
    argv: [{json.dumps(python)}, "-c", "print('action-mark')"]
health:
  enabled: true
  builtin: false
  silent: false
  alarms:
    - name: action_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
  actions:
    - name: mark-action
      alarms: [action_probe]
      severities: [warning]
      steps:
        - command: mark
''')
        data = root / 'data'
        log_path = root / 'server.log'
        log = log_path.open('w')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        base = f'http://127.0.0.1:{port}'
        child = subprocess.Popen(
            [str(binary), '-config', str(config), '-data-dir', str(data), '-listen', f'127.0.0.1:{port}'],
            stdout=log, stderr=log,
        )
        try:
            wait_ready(child, base, log_path)
            entry = wait_action(base, token, log_path)
        finally:
            if child.poll() is None:
                child.send_signal(signal.SIGINT)
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=5)
            log.close()
        if entry.get('action') != 'action:mark':
            raise SystemExit(f'unexpected audit action: {entry}')
        if 'duration_ms' not in entry or not isinstance(entry['duration_ms'], int):
            raise SystemExit(f'duration_ms missing: {entry}')
        if entry.get('exit_code') != 0 or 'action-mark' not in (entry.get('output') or ''):
            raise SystemExit(f'command result missing: {entry}')
        print('action audit ok', f"duration_ms={entry['duration_ms']}")


def wait_ready(child: subprocess.Popen, base: str, log_path: Path) -> None:
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if child.poll() is not None:
            raise SystemExit('daemon exited before ready\n' + log_path.read_text(errors='replace'))
        try:
            with urllib.request.urlopen(base + '/healthz', timeout=1) as resp:
                if resp.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError):
            pass
        time.sleep(0.1)
    raise SystemExit('healthz timed out\n' + log_path.read_text(errors='replace'))


def wait_action(base: str, token: str, log_path: Path) -> dict:
    deadline = time.monotonic() + 25
    req = urllib.request.Request(base + '/api/v1/audit?user=system&limit=20', headers={'Authorization': 'Bearer ' + token})
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(req, timeout=2) as resp:
                payload = json.load(resp)
        except urllib.error.URLError:
            time.sleep(0.2)
            continue
        for entry in payload.get('entries') or []:
            if entry.get('action') == 'action:mark':
                return entry
        time.sleep(0.3)
    raise SystemExit('action audit entry did not appear\n' + log_path.read_text(errors='replace'))


if __name__ == '__main__':
    main()
