#!/usr/bin/env python3
"""Real local package discovery/control/restart acceptance; prints no credentials."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', default='core/bin/monitord')
    args = parser.parse_args()
    if os.name == 'nt':
        raise SystemExit('This process fixture uses a POSIX shell; run it on macOS or Linux.')
    binary = str(Path(args.binary).resolve())
    example = Path(__file__).resolve().parent.parent / 'examples/plugins/heartbeat'
    with tempfile.TemporaryDirectory(prefix='monitor-plugin-platform-') as temporary:
        root = Path(temporary)
        installed = root / 'plugins'
        installed.mkdir()
        admin, reader = secrets.token_hex(24), secrets.token_hex(24)
        config = root / 'monitor.yaml'
        config.write_text(f'''web:
  token: {admin}
  users:
    - name: fixture-reader
      token: {reader}
      role: viewer
collectors:
  enabled: [none]
plugins:
  dir: plugins
health:
  enabled: false
db:
  checkpoint: 1s
''')
        data = root / 'data'
        process = None
        base = ''
        log_path = root / 'daemon.log'
        with log_path.open('w') as log:
            def request(path, body=None, token=admin, expected=200):
                headers = {} if token is None else {'Authorization': 'Bearer ' + token}
                if body is not None:
                    headers['Content-Type'] = 'application/json'
                req = urllib.request.Request(base + path, headers=headers,
                    data=None if body is None else json.dumps(body).encode())
                try:
                    with urllib.request.urlopen(req, timeout=15) as response:
                        status, payload = response.status, response.read()
                except urllib.error.HTTPError as response:
                    status, payload = response.code, response.read()
                assert status == expected, f'{path}: expected {expected}, received {status}'
                assert admin.encode() not in payload and reader.encode() not in payload
                assert b'private-package-argument' not in payload, 'execution args leaked'
                return json.loads(payload) if status == 200 else None

            def wait_for(check, description):
                deadline = time.monotonic() + 20
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise AssertionError('test daemon exited during ' + description)
                    try:
                        value = check()
                        if value:
                            return value
                    except (OSError, urllib.error.URLError):
                        pass
                    time.sleep(.1)
                raise AssertionError('timed out: ' + description)

            def catalog():
                return request('/api/v1/plugins')

            def heartbeat():
                return next((p for p in catalog()['plugins'] if p['name'] == 'heartbeat'), {})

            def sampled():
                item = heartbeat()
                return item if item.get('state') == 'running' and item['stats']['samples'] >= 2 else None

            def action(name, verb, revision=None, expected=200):
                if revision is None:
                    revision = catalog()['revision']
                return request('/api/v1/plugins/actions',
                    {'name': name, 'action': verb, 'if_revision': revision}, expected=expected)

            def start():
                nonlocal process, base
                with socket.socket() as sock:
                    sock.bind(('127.0.0.1', 0))
                    port = sock.getsockname()[1]
                base = f'http://127.0.0.1:{port}'
                process = subprocess.Popen([binary, '-config', str(config), '-data-dir', str(data),
                    '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)
                wait_for(lambda: urllib.request.urlopen(base + '/healthz', timeout=1).close() is None, 'HTTP readiness')
                wait_for(lambda: catalog()['running'], 'plugin runtime readiness')

            def stop():
                if process and process.poll() is None:
                    process.send_signal(signal.SIGINT)
                    try:
                        assert process.wait(timeout=15) == 0, 'daemon failed to stop'
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                        raise AssertionError('plugin shutdown timed out')

            def require_process_gone(pid):
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    return
                raise AssertionError('stopped plugin process survived')

            try:
                start()
                platform = request('/api/v1/platform')
                assert 'plugins.control.v1' in platform['capabilities']
                assert catalog()['plugins'] == []
                request('/api/v1/plugins', token=None, expected=401)
                request('/api/v1/plugins', token=reader, expected=403)
                request('/api/v1/plugins/actions', {'action': 'rescan', 'if_revision': 1}, token=reader, expected=403)
                request('/api/v1/plugins?node=remote', expected=400)

                # Installing a local package and rescanning needs no daemon rebuild/restart.
                package = installed / 'heartbeat'
                shutil.copytree(example, package)
                (package / 'collector.sh').chmod(0o755)
                manifest = json.loads((package / 'plugin.json').read_text())
                manifest['args'] = ['private-package-argument']
                (package / 'plugin.json').write_text(json.dumps(manifest))
                action('', 'rescan')
                first = wait_for(sampled, 'first real plugin samples')
                assert first['source'] == 'manifest' and first['manifest']['protocol'] == 'plugins.d/v1'
                history = request('/api/v1/data?chart=plugin.heartbeat.pulse&after=-60&points=60')
                assert any(row[1] is not None for row in history['result']['data'])
                stale = catalog()['revision']
                action('heartbeat', 'disable')
                require_process_gone(first['pid'])
                action('heartbeat', 'enable', stale, expected=409)
                assert heartbeat()['state'] == 'disabled'
                stop()
                start()
                assert heartbeat()['state'] == 'disabled' and not heartbeat()['enabled']
                action('heartbeat', 'enable')
                second = wait_for(sampled, 'enable after daemon restart')
                action('heartbeat', 'restart')
                third = wait_for(lambda: (p if p.get('pid') != second['pid'] and p.get('state') == 'running' else None)
                    if (p := heartbeat()) else None, 'independent restart')
                require_process_gone(second['pid'])

                # An invalid sibling is diagnosed without stopping the valid package.
                broken = installed / 'broken'
                shutil.copytree(package, broken)
                invalid = dict(manifest, id='broken', command='../heartbeat/collector.sh')
                (broken / 'plugin.json').write_text(json.dumps(invalid))
                scanned = action('', 'rescan')
                assert any(d['path'] == 'broken/plugin.json' for d in scanned['diagnostics'])
                assert heartbeat()['pid'] == third['pid']

                # A running package may emit invalid protocol, not just an invalid
                # manifest. Its rejected writes must not affect its healthy sibling.
                violator = installed / 'violator'
                violator.mkdir()
                (violator / 'plugin.json').write_text(json.dumps(dict(manifest, id='violator', args=[])))
                (violator / 'collector.sh').write_text('''#!/bin/sh
echo 'CHART system.injected'
echo 'DIMENSION value'
echo 'HOST_LABEL injected forbidden'
echo 'CHART plugin.violator.safe'
echo 'DIMENSION value'
while :; do
  echo 'OVERWRITE plugin.violator.safe'
  echo 'BEGIN plugin.violator.safe'
  echo 'SET value = NaN'
  echo 'SET value = 3'
  echo 'END'
  sleep 1
done
''')
                (violator / 'collector.sh').chmod(0o755)
                before_samples = heartbeat()['stats']['samples']
                action('', 'rescan')
                invalid_runtime = wait_for(lambda: next((p for p in catalog()['plugins']
                    if p['name'] == 'violator' and p['stats']['errors'] >= 4 and p['stats']['samples'] >= 2), None),
                    'invalid protocol isolation')
                wait_for(lambda: heartbeat()['stats']['samples'] > before_samples, 'healthy sibling continues sampling')
                assert 'system.injected' not in request('/api/v1/charts')['charts']
                assert 'injected' not in (request('/api/v1/info')['host'].get('labels') or {})
                action('violator', 'disable')
                require_process_gone(invalid_runtime['pid'])
                # File content changes are detected even at the same path/version.
                entry = package / 'collector.sh'
                entry.write_text(entry.read_text() + '\n# installed update\n')
                action('', 'rescan')
                fourth = wait_for(lambda: (p if p.get('pid') != third['pid'] and p.get('state') == 'running' else None)
                    if (p := heartbeat()) else None, 'updated package handoff')
                require_process_gone(third['pid'])
                shutil.rmtree(package)
                action('', 'rescan')
                assert not heartbeat()
                require_process_gone(fourth['pid'])
                audit = request('/api/v1/audit?limit=100')
                assert any(e.get('target') == 'heartbeat' and e['status'] == 200 for e in audit['entries'])
                stop()
                assert admin not in log_path.read_text() and reader not in log_path.read_text()
                print(json.dumps({'plugin_platform': 'passed', 'live_install_and_rescan': 'passed',
                    'real_samples_and_history': 'passed', 'independent_lifecycle': 'passed',
                    'persistent_disable': 'passed', 'stale_revision_and_rbac': 'passed',
                    'invalid_package_and_protocol_isolation': 'passed', 'content_update_and_removal': 'passed',
                    'process_cleanup_and_audit': 'passed'}))
            finally:
                stop()


if __name__ == '__main__':
    main()
