#!/usr/bin/env python3
"""Exercise SMTP, signed Feishu delivery and admin tests using loopback only."""
import argparse
import base64
from email import policy
from email.parser import BytesParser
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import socketserver
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
    private = secrets.token_hex(24)
    token, viewer = secrets.token_hex(24), secrets.token_hex(24)
    mails, chats = [], []
    rejected = threading.Event()

    class SMTP(socketserver.StreamRequestHandler):
        def handle(self):
            self.connection.settimeout(10)
            self.wfile.write(b'220 fixture ESMTP\r\n')
            while True:
                line = self.rfile.readline()
                if not line:
                    return
                if line.startswith(b'DATA'):
                    self.wfile.write(b'354 send message\r\n')
                    lines = []
                    while True:
                        data = self.rfile.readline()
                        if not data or data == b'.\r\n':
                            break
                        lines.append(data[1:] if data.startswith(b'..') else data)
                    mails.append(BytesParser(policy=policy.default).parsebytes(b''.join(lines)))
                    self.wfile.write(b'250 queued\r\n')
                elif line.startswith(b'QUIT'):
                    self.wfile.write(b'221 bye\r\n')
                    return
                else:
                    self.wfile.write(b'250 ok\r\n')

    class Chat(BaseHTTPRequestHandler):
        def do_POST(self):
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            ts = body.get('timestamp', '')
            expected = base64.b64encode(hmac.new((ts + '\n' + private).encode(), digestmod=hashlib.sha256).digest()).decode()
            valid = body.get('sign') == expected and abs(int(ts) - time.time()) < 60
            chats.append((valid, body))
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps({'code': 19021 if rejected.is_set() or not valid else 0,
                                         'msg': private if rejected.is_set() else 'success'}).encode())

        def log_message(self, *args):
            pass

    smtp = socketserver.ThreadingTCPServer(('127.0.0.1', 0), SMTP)
    chat = ThreadingHTTPServer(('127.0.0.1', 0), Chat)
    threads = [threading.Thread(target=server.serve_forever, daemon=True) for server in (smtp, chat)]
    for thread in threads:
        thread.start()
    process = None
    try:
        with tempfile.TemporaryDirectory(prefix='monitor-notification-services-') as directory:
            root = Path(directory)
            config = root / 'config.yaml'
            config.write_text(f'''global:
  hostname: notification-services-test
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
      oncall: [email, feishu]
    email:
      server: 127.0.0.1:{smtp.server_address[1]}
      from: monitor@example.invalid
      to: [ops@example.invalid]
    feishu:
      webhook_url_env: MONITOR_TEST_FEISHU_WEBHOOK
      secret_env: MONITOR_TEST_FEISHU_SECRET
  alarms:
    - name: 内存通知验收
      on: system.ram
      calc: '$used'
      warn: '$this > 0'
      every: 1s
      info: 本机测试服务，无真实收件人
      to: oncall
''')
            config.chmod(0o600)
            with socket.socket() as reservation:
                reservation.bind(('127.0.0.1', 0))
                port = reservation.getsockname()[1]
            base = f'http://127.0.0.1:{port}/api/v1'
            env = dict(os.environ, MONITOR_TEST_FEISHU_WEBHOOK=f'http://127.0.0.1:{chat.server_port}/{private}',
                       MONITOR_TEST_FEISHU_SECRET=private)

            def call(path, auth=token, body=None, want=200):
                req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None,
                                             headers={'Authorization': 'Bearer ' + auth, 'Content-Type': 'application/json'})
                try:
                    response = urllib.request.urlopen(req, timeout=3)
                except urllib.error.HTTPError as exc:
                    response = exc
                with response:
                    assert response.code == want, (path, response.code, want)
                    raw = response.read().decode()
                    assert private not in raw and token not in raw and 'ops@example.invalid' not in raw, 'secret exposed'
                    return json.loads(raw) if want < 300 else None

            def results(total):
                until = time.monotonic() + 20
                while time.monotonic() < until:
                    assert process.poll() is None, 'daemon exited'
                    try:
                        result = call('/operations/notifications')
                        if result['total'] == total and not result['in_flight']:
                            return result
                    except (urllib.error.URLError, TimeoutError):
                        pass
                    time.sleep(.1)
                raise AssertionError('notification results timed out')

            with (root / 'daemon.log').open('w') as log:
                process = subprocess.Popen([binary, '-config', str(config), '-data-dir', str(root / 'data'),
                                            '-listen', f'127.0.0.1:{port}'], env=env, stdout=log, stderr=log)
                try:
                    first = results(2)
                    assert first['accepted'] == 2 and first['failed'] == 0 and first['can_test']
                    assert len(mails) == len(chats) == 1 and chats[0][0]
                    assert '内存通知验收' in str(mails[0]['Subject'])
                    assert '本机测试服务' in mails[0].get_content()
                    assert '[WARNING]' in chats[0][1]['content']['text']
                    assert not call('/operations/notifications', viewer)['can_test']
                    call('/operations/notifications/test', viewer, {'channel': 'feishu'}, 403)
                    call('/operations/notifications/test', token, {'channel': 'feishu', 'url': 'http://example.invalid'}, 400)
                    # Explicit tests still run under global silence. Failure must
                    # not change the real alarm log, even with an HTTP 200 response.
                    call('/alarms/silence', token, {'all': True})
                    alarms_before = call('/alarm_log')
                    rejected.set()
                    queued = call('/operations/notifications/test', token, {'channel': 'feishu'}, 202)
                    assert queued['queued']
                    call('/operations/notifications/test', token, {'channel': 'email'}, 429)
                    second = results(3)
                    failure = second['recent'][0]
                    assert second['accepted'] == 2 and second['failed'] == 1
                    assert failure['test'] and failure['outcome'] == 'failed' and failure['reason'] == 'provider_error'
                    assert failure['event_id'] == 0 and len(chats) == 2 and len(mails) == 1
                    assert all(valid for valid, _ in chats)
                    assert call('/alarm_log') == alarms_before
                finally:
                    process.send_signal(signal.SIGINT)
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                    assert process.returncode == 0, 'daemon shutdown failed'
            assert private not in (root / 'daemon.log').read_text(), 'secret leaked to log'
            print(json.dumps({'passed': True, 'smtp_messages': len(mails), 'signed_feishu_requests': len(chats),
                              'real_memory_alarm': True, 'chinese_mime': True, 'http_200_business_failure': True,
                              'admin_test_and_rate_limit': True, 'silence_bypass_without_alarm_mutation': True,
                              'response_and_log_privacy': True, 'scope': 'local fixtures only'}, indent=2))
    finally:
        if process and process.poll() is None:
            process.kill()
            process.wait()
        for server in (smtp, chat):
            server.shutdown()
            server.server_close()
        for thread in threads:
            thread.join(timeout=3)


if __name__ == '__main__':
    main()
