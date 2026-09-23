#!/usr/bin/env python3
"""Exercise default authentication against a real, isolated daemon; print no secrets."""
import argparse
import json
import os
import pathlib
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

p = argparse.ArgumentParser()
p.add_argument('--bin', default='core/bin/monitord')
args = p.parse_args()
binary = str(pathlib.Path(args.bin).resolve())

with tempfile.TemporaryDirectory(prefix='monitor-default-auth-') as tmp:
    root = pathlib.Path(tmp)
    config = root / 'config.yaml'
    # No web authentication configuration: production defaults must protect this.
    config.write_text('collectors:\n  enabled: [cpu, mem]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n')
    data = root / 'data'
    password_file = data / 'web-password'
    log_path = root / 'server.log'
    log = log_path.open('w')
    child = None

    def start():
        global child, base
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        base = f'http://127.0.0.1:{port}'
        child = subprocess.Popen([binary, '-config', str(config), '-data-dir', str(data), '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)
        for _ in range(200):
            if child.poll() is not None:
                raise AssertionError('daemon exited before becoming ready')
            try:
                urllib.request.urlopen(base + '/healthz', timeout=1).close()
                return
            except OSError:
                time.sleep(.1)
        raise AssertionError('daemon readiness timed out')

    def stop():
        if child and child.poll() is None:
            child.send_signal(signal.SIGINT)
            try:
                child.wait(timeout=30)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait()
                raise

    def request(path, token=None, upgrade=False):
        headers = {} if token is None else {'Authorization': 'Bearer ' + token}
        if upgrade:
            headers.update({'Connection': 'Upgrade', 'Upgrade': 'websocket', 'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ=='})
        try:
            with urllib.request.urlopen(urllib.request.Request(base + path, headers=headers), timeout=5) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as response:
            return response.code, response.read()

    try:
        start()
        password = password_file.read_text().strip()
        assert len(password) == 43
        if os.name != 'nt':
            assert password_file.stat().st_mode & 0o777 == 0o600
        protected = ['/api/v1/info', '/api/v1/charts', '/api/v1/data?chart=system.ram', '/api/v1/function?function=processes', '/api/v1/logs', '/api/v3/info', '/api/v1/hub/console', '/metrics', '/v1/metrics']
        for path in protected:
            for token in (None, 'incorrect-password'):
                status, body = request(path, token)
                assert status == 401, (path, status)
                assert password.encode() not in body
        assert request('/api/v1/live', upgrade=True)[0] == 401
        assert request('/api/v1/live', 'incorrect-password', upgrade=True)[0] == 401
        for path in ['/api/v1/info', '/api/v1/charts', '/metrics']:
            status, body = request(path, password)
            assert status == 200, (path, status)
            assert password.encode() not in body
        assert request('/')[0] == 200
        assert request('/web-password')[1] != password_file.read_bytes()
        stop()
        assert password not in log_path.read_text(), 'password leaked into logs'
        start()
        assert password_file.read_text().strip() == password
        assert request('/api/v1/info', password)[0] == 200
        stop()
        password_file.unlink()
        start()
        replacement = password_file.read_text().strip()
        assert replacement != password
        assert request('/api/v1/info', password)[0] == 401
        assert request('/api/v1/info', replacement)[0] == 200
        stop()
        password_file.write_text('')
        failed = subprocess.run([binary, '-config', str(config), '-data-dir', str(data), '-listen', '127.0.0.1:0'], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
        assert failed.returncode != 0, 'corrupt credential allowed startup'
        print(json.dumps({'default_auth': 'passed', 'protected_routes': len(protected), 'websocket_unauthorized': 'passed', 'restart_and_reset': 'passed', 'corrupt_password_fails_closed': 'passed'}))
    finally:
        stop()
        log.close()
