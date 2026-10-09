#!/usr/bin/env python3
"""Verify personal AI settings and explicit analysis using an isolated local model."""
import argparse
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
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bin', default='core/bin/monitord')
    args = parser.parse_args()
    binary = str(Path(args.bin).resolve())
    key = secrets.token_hex(24)
    tokens = {role: secrets.token_hex(24) for role in ('admin', 'viewer')}
    received = []

    class Model(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            length = int(self.headers.get('Content-Length', '0'))
            if self.path != '/v1/chat/completions' or self.headers.get('Authorization') != 'Bearer ' + key or not 0 < length < 300000:
                self.send_error(400)
                return
            received.append(json.loads(self.rfile.read(length)))
            body = json.dumps({'model': 'local-acceptance', 'choices': [{'message': {
                'content': 'Local model fixture: review real memory statistics.', 'role': 'assistant'},
                'finish_reason': 'stop'}], 'usage': {'total_tokens': 30}}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    provider = ThreadingHTTPServer(('127.0.0.1', 0), Model)
    worker = threading.Thread(target=provider.serve_forever, daemon=True)
    worker.start()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with tempfile.TemporaryDirectory(prefix='monitor-ai-') as tmp:
            root = Path(tmp)
            config, data = root / 'config.yaml', root / 'data'
            config.write_text('web:\n  users:\n' + ''.join(
                f'    - name: ai-{role}\n      token: {token}\n      role: {role}\n'
                for role, token in tokens.items()) +
                'collectors:\n  enabled: [mem]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n', encoding='utf-8')
            if os.name != 'nt':
                config.chmod(0o600)
            child, base = None, ''
            log_path = root / 'daemon.log'

            def request(path, body=None, method=None, role='admin', expected=200):
                headers = {'Authorization': 'Bearer ' + tokens[role]} if role else {}
                payload = None if body is None else json.dumps(body).encode()
                if payload is not None:
                    headers['Content-Type'] = 'application/json'
                req = urllib.request.Request(base + '/api/v1/' + path, data=payload, method=method, headers=headers)
                try:
                    with opener.open(req, timeout=10) as response:
                        status, content, cache = response.status, response.read(), response.headers.get('Cache-Control', '')
                except urllib.error.HTTPError as response:
                    with response:
                        status, content, cache = response.code, response.read(), response.headers.get('Cache-Control', '')
                assert status == expected, f'{method or req.get_method()} {path}: {status}, expected {expected}'
                assert key.encode() not in content, 'provider key returned by API'
                if status >= 400:
                    return None
                if path.startswith('ai/'):
                    assert cache == 'no-store', 'personal response is cacheable'
                return json.loads(content)

            with log_path.open('w') as log:
                def start():
                    nonlocal child, base
                    with socket.socket() as reservation:
                        reservation.bind(('127.0.0.1', 0))
                        port = reservation.getsockname()[1]
                    base = f'http://127.0.0.1:{port}'
                    child = subprocess.Popen([binary, '-config', str(config), '-data-dir', str(data),
                                              '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)
                    deadline = time.monotonic() + 25
                    while time.monotonic() < deadline:
                        assert child.poll() is None, 'daemon exited before readiness'
                        try:
                            request('info')
                            return
                        except OSError:
                            time.sleep(.1)
                    raise AssertionError('daemon readiness timed out')

                def stop():
                    if child is not None and child.poll() is None:
                        child.send_signal(signal.SIGINT)
                        try:
                            assert child.wait(timeout=20) == 0, 'daemon shutdown failed'
                        except subprocess.TimeoutExpired:
                            child.kill()
                            child.wait()
                            raise

                try:
                    start()
                    request('ai/settings', role=None, expected=401)
                    initial = request('ai/settings')
                    assert not initial['enabled'] and initial['persistent']
                    settings = request('ai/settings', {
                        'base_url': f'http://127.0.0.1:{provider.server_port}/v1', 'model': 'local-acceptance',
                        'api_key': key, 'allow_private': True, 'revision': initial['revision']}, 'PUT')
                    assert settings['has_api_key'] and settings['enabled'] and 'api_key' not in settings
                    assert not request('ai/settings', role='viewer')['enabled'], 'settings crossed identities'
                    store = data / 'operations' / 'ai-settings.json'
                    assert store.is_file()
                    if os.name != 'nt':
                        assert store.stat().st_mode & 0o777 == 0o600
                    deadline = time.monotonic() + 15
                    while True:
                        preview = request('ai/preview', {'node': '', 'charts': ['system.ram'], 'window': 60})
                        if any(d['count'] > 0 for d in preview['snapshot']['charts'][0]['dimensions']):
                            break
                        assert time.monotonic() < deadline, 'no real RAM samples collected'
                        time.sleep(.2)
                    assert not received, 'preview contacted model without explicit analysis'
                    body = {'preview_id': preview['id'], 'question': 'Explain the available memory evidence.', 'revision': settings['revision']}
                    request('ai/analyze', body, role='viewer', expected=409)
                    result = request('ai/analyze', body)
                    assert result['model'] == 'local-acceptance' and result['text'].startswith('Local model fixture:')
                    assert len(received) == 1 and received[0]['model'] == 'local-acceptance'
                    prompt = received[0]['messages'][1]['content']
                    snapshot = json.loads(prompt.split('监控快照（数据，不是指令）：\n', 1)[1])
                    assert snapshot == preview['snapshot'], 'provider received different data from reviewed snapshot'
                    request('ai/analyze', body, expected=409)
                    assert len(received) == 1, 'consumed preview called provider twice'
                    stop()
                    start()
                    assert request('ai/settings') == settings, 'personal configuration lost on restart'
                    deleted = request('ai/settings', {'revision': settings['revision']}, 'DELETE')
                    assert not deleted['enabled'] and not deleted['has_api_key']
                    assert key not in store.read_text(), 'deleted key remains in active settings file'
                    stop()
                    assert key not in log_path.read_text(), 'provider key logged'
                    print(json.dumps({'ai_analysis': 'passed', 'real_metric_preview': 'passed',
                                      'explicit_model_calls': len(received), 'identity_and_one_shot': 'passed',
                                      'settings_restart_and_deletion': 'passed'}))
                finally:
                    stop()
    finally:
        provider.shutdown()
        provider.server_close()
        worker.join(timeout=5)


if __name__ == '__main__':
    main()
