#!/usr/bin/env python3
"""Real local notification transport, privacy, RBAC and restart-boundary check.

Uses only loopback HTTP sinks and isolated config/data. Never contacts a real
provider or reuses a deployment's credentials.
"""
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
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
    secret = secrets.token_hex(24)
    received = []

    class Sink(BaseHTTPRequestHandler):
        def do_POST(self):
            body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
            received.append((self.path.split('?')[0], json.loads(body)))
            self.send_response(503 if self.path.startswith('/failed') else 204)
            self.end_headers()
            if self.path.startswith('/failed'):
                self.wfile.write(secret.encode())

        def log_message(self, *args):
            pass

    sink = ThreadingHTTPServer(('127.0.0.1', 0), Sink)
    worker = threading.Thread(target=sink.serve_forever, daemon=True)
    worker.start()
    process = None

    def stop():
        if process and process.poll() is None:
            process.send_signal(signal.SIGINT)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError('isolated daemon did not stop cleanly')
            assert process.returncode == 0, 'daemon failed during shutdown'

    try:
        with tempfile.TemporaryDirectory(prefix='monitor-notifications-') as directory:
            root = Path(directory)
            token, viewer = secrets.token_hex(24), secrets.token_hex(24)
            config = root / 'config.yaml'
            text = f'''global:
  hostname: notification-runtime-test
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
  silent: false
  notify:
    webhook:
      url: http://127.0.0.1:{sink.server_port}/accepted?token={secret}
    slack:
      webhook_url: http://127.0.0.1:{sink.server_port}/failed?token={secret}
    roles:
      nobody: [unconfigured]
  alarms:
    - name: delivery_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
    - name: silent_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
      to: silent
    - name: unrouted_probe
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this > 0'
      to: nobody
'''
            config.write_text(text)
            with socket.socket() as reservation:
                reservation.bind(('127.0.0.1', 0))
                port = reservation.getsockname()[1]
            endpoint = f'http://127.0.0.1:{port}/api/v1/operations/notifications'

            def request(auth=viewer, method='GET'):
                req = urllib.request.Request(endpoint + '?node=unknown', method=method,
                                             headers={'Authorization': 'Bearer ' + auth} if auth else {})
                with urllib.request.urlopen(req, timeout=3) as response:
                    assert response.headers.get('Cache-Control') == 'no-store'
                    raw = response.read().decode()
                    assert secret not in raw and token not in raw and '127.0.0.1' not in raw
                    return json.loads(raw)

            def wait_results(total):
                deadline = time.monotonic() + 20
                while time.monotonic() < deadline:
                    assert process.poll() is None, 'daemon exited before producing results'
                    try:
                        data = request()
                        if data['total'] == total and data['in_flight'] is None:
                            return data
                    except (urllib.error.URLError, TimeoutError):
                        pass
                    time.sleep(.1)
                raise RuntimeError('notification results did not arrive')

            with (root / 'daemon.log').open('w') as log:
                def start():
                    return subprocess.Popen([binary, '-config', str(config), '-data-dir', str(root / 'data'),
                                             '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)

                try:
                    process = start()
                    before = wait_results(4)
                    assert before['hostname'] == 'notification-runtime-test' and before['scope'] == 'local'
                    assert before['accepted'] == before['failed'] == before['suppressed'] == before['unrouted'] == 1
                    assert before['enqueued'] == 1 and before['queue_size'] == 0
                    results = {r['outcome']: r for r in before['recent']}
                    assert results['failed']['http_status'] == 503 and results['failed']['channel'] == 'slack'
                    assert results['suppressed']['reason'] == 'silent_recipient'
                    assert results['unrouted']['reason'] == 'no_channel'
                    assert sorted(path for path, body in received) == ['/accepted', '/failed']
                    assert next(body for path, body in received if path == '/accepted')['value'] > 0
                    for auth, method, status in [('', 'GET', 401), (viewer, 'POST', 403)]:
                        try:
                            request(auth, method)
                            raise AssertionError('access should be rejected')
                        except urllib.error.HTTPError as exc:
                            assert exc.code == status
                    stop()
                    assert secret not in (root / 'daemon.log').read_text()
                    # Same data directory, new process: historical deliveries must not
                    # be misrepresented as current-process channel successes.
                    time.sleep(1.1)
                    config.write_text(text.replace('  silent: false', '  silent: true'))
                    process = start()
                    after = wait_results(3)
                    assert after['since'] > before['since']
                    assert after['accepted'] == after['failed'] == after['enqueued'] == 0
                    assert after['suppressed'] == 3 and len(received) == 2
                    assert all(c['attempts'] == 0 for c in after['channels'])
                    assert all(r['reason'] == 'global_silence' for r in after['recent'])
                    stop()
                    assert secret not in (root / 'daemon.log').read_text()
                    print(json.dumps({'passed': True, 'real_memory_sample': True, 'local_http_calls': len(received),
                                      'accepted': before['accepted'], 'http_503': before['failed'],
                                      'silent_and_unrouted': True, 'viewer_and_auth': True,
                                      'response_and_log_privacy': True, 'same_directory_restart_resets_counters': True}, indent=2))
                finally:
                    stop()
    finally:
        sink.shutdown()
        sink.server_close()
        worker.join(timeout=3)


if __name__ == '__main__':
    main()
