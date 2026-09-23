#!/usr/bin/env python3
"""Maintenance acceptance using real samples, loopback delivery and two restarts."""
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', default='core/bin/monitord')
    binary = str(Path(parser.parse_args().binary).resolve())
    received = []

    class Sink(BaseHTTPRequestHandler):
        def do_POST(self):
            received.append(json.loads(self.rfile.read(int(self.headers['Content-Length']))))
            self.send_response(204)
            self.end_headers()

        def log_message(self, *args):
            pass

    sink = ThreadingHTTPServer(('127.0.0.1', 0), Sink)
    thread = threading.Thread(target=sink.serve_forever, daemon=True)
    thread.start()
    process = None

    def stop():
        if process and process.poll() is None:
            process.send_signal(signal.SIGINT)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError('isolated daemon did not exit cleanly')
            assert process.returncode == 0

    try:
        with tempfile.TemporaryDirectory(prefix='monitor-maintenance-') as temporary:
            root = Path(temporary)
            token, viewer = secrets.token_hex(24), secrets.token_hex(24)
            config = root / 'config.yaml'
            config.write_text(f'''global:
  hostname: maintenance-runtime-test
web:
  token: {token}
  users:
    - name: reader
      token: {viewer}
      role: viewer
collectors:
  enabled: [mem]
plugins:
  enabled: false
health:
  enabled: true
  builtin: false
  notify:
    webhook:
      url: http://127.0.0.1:{sink.server_port}/notify
  alarms:
    - name: maintenance_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
      repeat: warning 1s
    - name: unrelated_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
      repeat: warning 1s
''')
            with socket.socket() as reservation:
                reservation.bind(('127.0.0.1', 0))
                port = reservation.getsockname()[1]
            base = f'http://127.0.0.1:{port}/api/v1'

            def request(path='/operations/maintenance', body=None, auth=None):
                req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(),
                                             headers={'Authorization': 'Bearer ' + (auth or token), 'Content-Type': 'application/json'})
                with urllib.request.urlopen(req, timeout=3) as response:
                    return json.load(response)

            def wait(check, seconds=20):
                deadline = time.monotonic() + seconds
                while time.monotonic() < deadline:
                    assert process.poll() is None, 'isolated daemon stopped unexpectedly'
                    try:
                        value = check()
                        if value:
                            return value
                    except (urllib.error.URLError, TimeoutError):
                        pass
                    time.sleep(.15)
                raise RuntimeError('maintenance acceptance condition timed out')

            def spec(title, scope='all', start=0, duration=120):
                return {'title': title, 'reason': 'isolated local runtime acceptance', 'scope': scope,
                        'chart': 'system.ram' if scope == 'alarm' else '', 'alarm': 'maintenance_probe' if scope == 'alarm' else '',
                        'starts_at': start, 'duration_seconds': duration}

            def create(plan):
                data = request()
                return request(body={'action': 'create', 'revision': data['revision'], 'plan': plan})

            def cancel(plan_id):
                data = request()
                return request(body={'action': 'cancel', 'revision': data['revision'], 'id': plan_id, 'reason': 'work completed'})

            with (root / 'daemon.log').open('w') as log:
                def start():
                    return subprocess.Popen([binary, '-config', str(config), '-listen', f'127.0.0.1:{port}', '-data-dir', str(root / 'data')], stdout=log, stderr=log)

                try:
                    process = start()
                    wait(lambda: len(request()['targets']) == 2 and len(received) >= 2)
                    starts = int(time.time()) + 3
                    first = create(spec('scheduled exact alarm', 'alarm', starts, 12))
                    plan = first['plans'][0]
                    assert plan['state'] == 'scheduled' and plan['starts_at'] == starts
                    assert plan['created_by'] == 'admin' and plan['ends_at'] == starts + 12
                    for role in [viewer]:
                        try:
                            request(body={'action': 'cancel', 'revision': 1, 'id': plan['id'], 'reason': 'forbidden'}, auth=role)
                            raise AssertionError('viewer canceled maintenance')
                        except urllib.error.HTTPError as exc:
                            assert exc.code == 403
                    wait(lambda: any(r['reason'] == 'planned_maintenance' for r in request('/operations/notifications')['recent']))
                    path = root / 'data/health/maintenance-plans.json'
                    saved = json.loads(path.read_text())
                    assert token not in path.read_text() and viewer not in path.read_text()
                    if os.name != 'nt':
                        assert path.stat().st_mode & 0o777 == 0o600
                    stop()
                    process = start()
                    wait(lambda: request()['plans'][0]['state'] == 'active' and len(request()['targets']) == 2)
                    after = request()
                    assert after['revision'] == first['revision'] and after['plans'][0]['id'] == plan['id']
                    assert json.loads(path.read_text()) == saved
                    wait(lambda: any(r['reason'] == 'planned_maintenance' for r in request('/operations/notifications')['recent']))
                    problems = request('/operations')['problems']
                    assert len(problems) == 2 and all(p['value'] > 0 and p['severity'] == 'WARNING' for p in problems)
                    wait(lambda: any(e['name'] == 'unrelated_probe' and e['when'] >= starts + 2 for e in received))
                    wait(lambda: request()['plans'][0]['state'] == 'ended')
                    wait(lambda: any(e['name'] == 'maintenance_probe' and e['when'] >= plan['ends_at'] for e in received))
                    # Leave a one-second margin here for scheduler timestamps;
                    # exact inclusive/exclusive boundaries have deterministic Go tests.
                    assert not any(e['name'] == 'maintenance_probe' and starts+1 <= e['when'] < plan['ends_at']-1 for e in received)
                    a = create(spec('global A'))
                    aid = next(p['id'] for p in a['plans'] if p['title'] == 'global A')
                    b = create(spec('global B'))
                    bid = next(p['id'] for p in b['plans'] if p['title'] == 'global B')
                    try:
                        request(body={'action': 'cancel', 'revision': a['revision'], 'id': aid, 'reason': 'stale'})
                        raise AssertionError('stale revision accepted')
                    except urllib.error.HTTPError as exc:
                        assert exc.code == 409
                    cancel(aid)
                    before_suppressed = request('/operations/notifications')['suppressed']
                    wait(lambda: request('/operations/notifications')['suppressed'] >= before_suppressed + 2)
                    canceled = cancel(bid)
                    assert all(p['state'] in ['ended', 'canceled'] for p in canceled['plans'])
                    stop()
                    count = len(received)
                    process = start()
                    wait(lambda: len(received) >= count+2 and len(request()['targets']) == 2)
                    final = request()
                    assert final['revision'] == canceled['revision']
                    assert all(p['state'] in ['ended', 'canceled'] for p in final['plans'])
                    assert {p['canceled_by'] for p in final['plans'] if p['state'] == 'canceled'} == {'admin'}
                    print(json.dumps({'passed': True, 'real_samples': True, 'scheduled_exact_scope': True,
                                      'unrelated_alarm_continues': True, 'active_plan_survives_restart': True,
                                      'expiry_resumes_new_notifications': True, 'overlap_and_cancel': True,
                                      'stale_revision_rejected': True, 'viewer_denied': True,
                                      'cancellation_survives_second_restart': True, 'private_file_0600': os.name != 'nt'}, indent=2))
                finally:
                    stop()
    finally:
        sink.shutdown()
        sink.server_close()
        thread.join(timeout=3)


if __name__ == '__main__':
    main()
