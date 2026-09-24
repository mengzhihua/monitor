#!/usr/bin/env python3
"""Run real alarms and admin tests against loopback ntfy/Gotify/Bark fixtures."""
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
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    private, token, viewer = [secrets.token_hex(24) for _ in range(3)]
    topic = 'private-' + secrets.token_hex(10)
    received = []
    rejection = set()

    class Sink(BaseHTTPRequestHandler):
        def do_POST(self):
            kind = self.path.strip('/')
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            valid = kind in ('ntfy', 'gotify', 'bark')
            if kind == 'ntfy':
                valid &= self.headers.get('Authorization') == 'Bearer ' + private and body.get('topic') == topic
                valid &= body.get('priority') in (3, 4)
                result = {'id': 'fixture-id', 'event': 'message', 'topic': topic}
            elif kind == 'gotify':
                valid &= self.headers.get('X-Gotify-Key') == private and body.get('priority') in (2, 5)
                result = {'id': 123}
            else:
                valid &= body.get('device_key') == private and body.get('group') == '运维'
                result = {'code': 200}
            valid &= bool(body.get('body' if kind == 'bark' else 'message')) and bool(body.get('title'))
            received.append((kind, valid, body))
            if kind in rejection:
                result = {'code': 400, 'error': private}  # HTTP 200 alone must not count as accepted.
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(result).encode())

        def log_message(self, *args):
            pass

    sink = ThreadingHTTPServer(('127.0.0.1', 0), Sink)
    worker = threading.Thread(target=sink.serve_forever, daemon=True)
    worker.start()
    process = None
    checked = []
    try:
        with tempfile.TemporaryDirectory(prefix='monitor-push-services-') as directory:
            root = Path(directory)
            for target in ('ntfy', 'gotify', 'bark'):
                rejection.clear()
                received.clear()
                config = root / (target + '.yaml')
                config.write_text(f'''global:
  hostname: push-services-test
web:
  token: {token}
  users:
    - name: fixture-reader
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
    roles:
      oncall: [ntfy, gotify, bark]
    ntfy:
      url: http://127.0.0.1:{sink.server_port}/ntfy
      topic_env: MONITOR_TEST_TOPIC
      token_env: MONITOR_TEST_SECRET
    gotify:
      url: http://127.0.0.1:{sink.server_port}/gotify
      token_env: MONITOR_TEST_SECRET
    bark:
      url: http://127.0.0.1:{sink.server_port}/bark
      device_key_env: MONITOR_TEST_SECRET
      group: 运维
  alarms:
    - name: 内存推送验收
      on: system.ram
      calc: '$used'
      warn: '$this > 0'
      every: 1s
      to: oncall
''')
                config.chmod(0o600)
                with socket.socket() as reservation:
                    reservation.bind(('127.0.0.1', 0))
                    port = reservation.getsockname()[1]
                base = f'http://127.0.0.1:{port}/api/v1'

                def call(path, body=None, auth=token, want=200):
                    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None,
                                                 headers={'Authorization': 'Bearer ' + auth, 'Content-Type': 'application/json'})
                    try:
                        response = urllib.request.urlopen(req, timeout=3)
                    except urllib.error.HTTPError as exc:
                        response = exc
                    with response:
                        assert response.code == want, (path, response.code, want)
                        raw = response.read().decode()
                        assert private not in raw and topic not in raw and token not in raw, 'diagnostic leaked secret'
                        return json.loads(raw) if want < 300 else None

                def results(total):
                    until = time.monotonic() + 20
                    while time.monotonic() < until:
                        assert process.poll() is None, 'daemon exited'
                        try:
                            value = call('/operations/notifications')
                            if value['total'] == total and value['in_flight'] is None:
                                return value
                        except (urllib.error.URLError, TimeoutError):
                            pass
                        time.sleep(.1)
                    raise AssertionError('notification results timed out')

                logpath = root / (target + '.log')
                with logpath.open('w') as log:
                    process = subprocess.Popen([binary, '-config', str(config), '-data-dir', str(root / target),
                                                '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log,
                                               env=dict(os.environ, MONITOR_TEST_TOPIC=topic, MONITOR_TEST_SECRET=private))
                    try:
                        before = results(3)
                        assert before['accepted'] == 3 and before['failed'] == 0
                        assert sorted(c['name'] for c in before['channels']) == ['bark', 'gotify', 'ntfy']
                        assert len(received) == 3 and all(valid for _, valid, _ in received)
                        assert all('内存推送验收' in body['title'] and 'WARNING' in body['title'] for _, _, body in received)
                        call('/operations/notifications/test', {'channel': target}, viewer, 403)
                        call('/operations/notifications/test', {'channel': target, 'url': sink.server_address[0]}, want=400)
                        alarms_before = call('/alarm_log')
                        rejection.add(target)
                        call('/operations/notifications/test', {'channel': target}, want=202)
                        call('/operations/notifications/test', {'channel': target}, want=429)
                        after = results(4)
                        result = after['recent'][0]
                        assert after['accepted'] == 3 and after['failed'] == 1
                        assert result['channel'] == target and result['test'] and result['reason'] == 'provider_error'
                        assert len(received) == 4 and received[-1][0] == target and received[-1][1]
                        assert call('/alarm_log') == alarms_before
                        checked.append(target)
                    finally:
                        process.send_signal(signal.SIGINT)
                        try:
                            process.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
                        assert process.returncode == 0, 'daemon shutdown failed'
                assert private not in logpath.read_text() and topic not in logpath.read_text(), 'log leaked secret'
            print(json.dumps({'passed': True, 'channels': checked, 'local_requests': 12,
                              'real_memory_alarms': True, 'environment_auth': True, 'admin_tests': True,
                              'provider_rejection': True, 'RBAC_and_rate_limit': True, 'privacy': True,
                              'scope': 'loopback fixtures, no real device delivery'}, indent=2))
    finally:
        if process and process.poll() is None:
            process.kill()
            process.wait()
        sink.shutdown()
        sink.server_close()
        worker.join(timeout=3)


if __name__ == '__main__':
    main()
