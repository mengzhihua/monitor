#!/usr/bin/env python3
"""Real daemon restart acceptance for the operations store, in isolated temp data."""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', default='core/bin/monitord')
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    with tempfile.TemporaryDirectory(prefix='monitor-operations-') as root:
        root = Path(root)
        token = secrets.token_hex(24)
        config = root / 'config.yaml'
        config.write_text(f'''web:
  token: {token}
collectors:
  enabled: [cpu, mem]
plugins:
  enabled: false
health:
  enabled: true
  builtin: false
  silent: true
  alarms:
    - name: restart_memory_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
''')
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        base = f'http://127.0.0.1:{port}/api/v1/operations'
        process = None

        def request(path='', body=None):
            req = urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + token},
                                         data=None if body is None else json.dumps(body).encode())
            if body is not None:
                req.add_header('Content-Type', 'application/json')
            with urllib.request.urlopen(req, timeout=3) as response:
                return json.load(response)

        def wait_ready():
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError('isolated test daemon exited before readiness')
                try:
                    data = request()
                    if data['problems'] and data['nodes'][0]['memory']['value'] is not None:
                        return data
                except (urllib.error.URLError, TimeoutError):
                    pass
                time.sleep(.2)
            raise RuntimeError('operations readiness timed out')

        def stop():
            if process and process.poll() is None:
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise RuntimeError('daemon did not shut down cleanly')

        with (root / 'daemon.log').open('w') as log:
            def start():
                return subprocess.Popen([binary, '-config', str(config), '-data-dir', str(root / 'data'),
                                         '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)
            try:
                process = start()
                before = wait_ready()
                old = before['problems'][0]
                request('/acknowledgements', {'id': old['id'], 'action': 'acknowledge',
                        'revision': 0, 'note': 'Restart acceptance: record must survive.'})
                request('/handling', {'id': old['id'], 'action': 'assign', 'assignee': 'admin', 'revision': 1})
                request('/handling', {'id': old['id'], 'action': 'progress', 'status': 'investigating',
                        'revision': 2, 'note': 'Restart acceptance: owner and progress must survive.'})
                assert request()['problems'][0]['handling']['acknowledged']
                history_query = urllib.parse.urlencode({'q': 'Restart acceptance', 'assignee': 'admin', 'status': 'investigating'})
                history_before = request('/history?' + history_query)
                assert history_before['total'] == 1
                store = root / 'data/operations/acknowledgements.json'
                assert store.is_file()
                if os.name != 'nt':
                    assert store.stat().st_mode & 0o777 == 0o600
                stop()
                process = start()
                after = wait_ready()
                record = next(r for r in after['activity'] if r['id'] == old['id'])
                assert record['acknowledged'] and record['history'][-1]['note'].startswith('Restart acceptance')
                assert record['assignee'] == 'admin' and record['status'] == 'investigating'
                assert record['history'][-1]['previous_status'] == 'open'
                assert record['revision'] == 3
                # Health intentionally evaluates afresh after restart: never suppress a new episode.
                assert after['problems'][0]['id'] != old['id']
                assert not after['problems'][0]['handling']['acknowledged']
                assert after['problems'][0]['handling']['assignee'] == ''
                assert after['problems'][0]['handling']['status'] == 'open'
                history_after = request('/history?' + history_query)
                assert history_after['total'] == 1 and history_after['records'][0]['id'] == old['id']
                exported = request('/history/export?' + history_query + '&format=json&snapshot=' + history_after['snapshot'])
                assert exported['records'] == history_after['records'] and exported['next_cursor'] == ''
                try:
                    request('/history/export?format=json&snapshot=' + history_before['snapshot'])
                    raise AssertionError('snapshot from before restart was accepted')
                except urllib.error.HTTPError as error:
                    assert error.code == 409
                print('PASS: real samples, durable workflow, restart, history search/export, expired snapshot rejection')
            finally:
                stop()


if __name__ == '__main__':
    main()
