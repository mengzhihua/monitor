#!/usr/bin/env python3
"""Exercise real service editing, handover, restart and offline restore in isolation."""
from concurrent.futures import ThreadPoolExecutor
import argparse
from http.client import RemoteDisconnected
import json
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bin', default='core/bin/monitord')
    args = parser.parse_args()
    binary = str(Path(args.bin).resolve())
    credentials = {role: secrets.token_hex(24) for role in ['admin', 'admin-b', 'operator', 'viewer']}
    with tempfile.TemporaryDirectory(prefix='monitor-services-') as directory:
        root = Path(directory)
        config = root / 'config.yaml'
        users = ''.join(f'    - name: {name}\n      token: {token}\n      role: '
                        f'{"troubleshooter" if name == "operator" else "admin" if name.startswith("admin") else "viewer"}\n'
                        for name, token in credentials.items())
        config.write_text('global:\n  hostname: service-acceptance\nweb:\n  users:\n' + users + '''collectors:
  enabled: [mem]
plugins:
  enabled: false
health:
  enabled: true
  builtin: false
  silent: true
  alarms:
    - name: service_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
    - name: unrelated_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
db:
  checkpoint: 1s
''')
        process = None
        base = ''

        def request(path, body=None, method=None, role='admin', expected=200):
            req = urllib.request.Request(base + path, method=method,
                                         headers={'Authorization': 'Bearer ' + credentials[role], 'Content-Type': 'application/json'},
                                         data=None if body is None else json.dumps(body).encode())
            try:
                with urllib.request.urlopen(req, timeout=5) as response:
                    status, raw = response.status, response.read()
            except urllib.error.HTTPError as error:
                status, raw = error.code, error.read()
            allowed = (expected,) if isinstance(expected, int) else expected
            assert status in allowed, f'{method or "GET"} {path}: expected {expected}, got {status}'
            if status != 200:
                return None
            for token in credentials.values():
                assert token.encode() not in raw, 'credential appeared in service response'
            return json.loads(raw)

        def stop():
            nonlocal process
            if process and process.poll() is None:
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise RuntimeError('isolated service did not stop cleanly')
                assert process.returncode == 0, 'isolated service exited unsuccessfully'
            process = None

        def start(data, log):
            nonlocal process, base
            with socket.socket() as reservation:
                reservation.bind(('127.0.0.1', 0))
                port = reservation.getsockname()[1]
            base = f'http://127.0.0.1:{port}'
            process = subprocess.Popen([binary, '-config', str(config), '-data-dir', str(data),
                                        '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                assert process.poll() is None, 'isolated service exited during startup'
                try:
                    snapshot = request('/api/v1/operations')
                    if len(snapshot['problems']) == 2:
                        return snapshot
                except (urllib.error.URLError, TimeoutError, RemoteDisconnected):
                    pass
                time.sleep(.1)
            raise RuntimeError('real alarm samples did not become ready')

        with (root / 'daemon.log').open('w') as log:
            try:
                data = root / 'data'
                snapshot = start(data, log)
                initial = request('/api/v1/services/config', role='viewer')
                assert initial['services'] == []
                services = [
                    {'name': 'checkout', 'children': ['database'], 'status_rules': [
                        {'type': 'children_at_least', 'n': 1, 'at_least': 'WARNING', 'status': 'CRITICAL'}]},
                    {'name': 'database', 'alarms': ['service_probe']},
                    {'name': 'unrelated', 'alarms': ['unrelated_probe']},
                    {'name': 'unconfigured'},
                ]
                body = {'revision': initial['revision'], 'services': services}
                preview = request('/api/v1/services/config/preview', body, 'POST')
                assert preview['revision'] == initial['revision']
                assert request('/api/v1/services/config') == initial, 'preview wrote configuration'
                for role in ['viewer', 'operator']:
                    request('/api/v1/services/config', body, 'PUT', role, 403)
                request('/api/v1/services', {'services': services}, 'PUT', expected=428)

                def save(role):
                    return request('/api/v1/services/config', body, 'PUT', role, (200, 409))

                with ThreadPoolExecutor(max_workers=2) as workers:
                    results = list(workers.map(save, ['admin', 'admin-b']))
                assert sum(result is not None for result in results) == 1, 'concurrent edits were not exclusive'
                saved = request('/api/v1/services/config')
                assert saved['revision'] != initial['revision']
                assert saved['services'][0]['status_rules'] == services[0]['status_rules']
                request('/api/v1/services', body, 'PUT', expected=409)
                invalid = {'revision': saved['revision'], 'services': [{'name': 'cycle', 'children': ['cycle']}]}
                request('/api/v1/services/config/preview', invalid, 'POST', expected=400)
                assert request('/api/v1/services/config') == saved

                selected = next(p for p in snapshot['problems'] if p['name'] == 'service_probe')
                unrelated = next(p for p in snapshot['problems'] if p['name'] == 'unrelated_probe')
                for problem, note in [(selected, 'service-acceptance-note'), (unrelated, 'must-not-export')]:
                    request('/api/v1/operations/handling', {'id': problem['id'], 'revision': problem['handling']['revision'],
                                                           'action': 'comment', 'note': note}, 'POST', 'operator')

                def verify_report():
                    report = request('/api/v1/services/report?service=checkout&window=1h', role='operator')
                    assert report['schema'] == 'monitor.service-handover.v1'
                    assert report['config_revision'] == saved['revision']
                    assert {s['name'] for s in report['report']['services']} == {'checkout', 'database'}
                    assert report['report']['services'][0]['status'] == 'CRITICAL'
                    assert all(s['sla'] is None for s in report['report']['services']), 'unknown hour reported as known'
                    assert {a['name'] for a in report['alarms']} == {'service_probe'}
                    assert report['history_persistent'] and report['history_matched'] == 1
                    assert report['handling_history'][0]['id'] == selected['id']
                    assert 'service-acceptance-note' in json.dumps(report)
                    assert 'must-not-export' not in json.dumps(report)
                    empty = request('/api/v1/services/report?service=unconfigured')
                    assert empty['report']['services'][0]['status'] == 'UNKNOWN'
                    assert empty['report']['services'][0]['sla'] is None

                verify_report()
                request('/api/v1/services/report?service=checkout', role='viewer', expected=403)
                request('/api/v1/services/report?service=checkout&node=remote', expected=400)
                stop()
                start(data, log)
                assert request('/api/v1/services/config') == saved, 'configuration changed after restart'
                verify_report()
                stop()
                backup, restored = root / 'backup', root / 'restored'
                for arguments in [('-data-dir', data, '-backup-dir', backup), ('-data-dir', restored, '-restore-from', backup)]:
                    subprocess.run([binary, '-config', str(config), *map(str, arguments)], check=True, capture_output=True)
                start(restored, log)
                assert request('/api/v1/services/config') == saved, 'configuration changed after restore'
                verify_report()
                print(json.dumps({'passed': True, 'real_alarm_samples': True, 'preview_no_write': True,
                                  'concurrent_save_conflict': True, 'roles': True, 'unknown_history': True,
                                  'scoped_handover': True, 'restart': True, 'offline_backup_restore': True}))
            finally:
                stop()


if __name__ == '__main__':
    main()
