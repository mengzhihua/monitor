#!/usr/bin/env python3
"""Exercise real monitord with simulated SNMP/Redfish devices in isolated temp data.

This checks daemon integration and protocol fixtures, not physical device
compatibility. Requires Python 3.9+ and a built monitord on macOS or Linux.
"""
from __future__ import annotations

import argparse
import base64
import json
import math
import os
from pathlib import Path
import secrets
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import urllib.error
import urllib.parse
import urllib.request


# Net-SNMP is substituted only inside this script's temporary config. Each
# invocation still traverses the real collector's subprocess/parser path.
SNMP_FIXTURE = r'''
import pathlib, sys
root = pathlib.Path(__file__).parent
address, oid = sys.argv[-2:]
if address == "fixture-printer" and (root / "printer-offline").exists():
    sys.exit(1)
cycle_file = root / ("cycle-" + address)
cycle = int(cycle_file.read_text()) if cycle_file.exists() else 0
if oid == "1.3.6.1.2.1.1.3.0":
    cycle += 1
    cycle_file.write_text(str(cycle))
    print("." + oid + " = Timeticks: (" + str(50000 + cycle * 200) + ") 0:08:20.00")
    sys.exit(0)
def emit(index, kind, value):
    print("." + oid + ("." + index if index else "") + " = " + kind + ": " + str(value))
network = address in ("fixture-switch", "fixture-nas")
if network and oid == "1.3.6.1.2.1.2.2.1.2": emit("1", "STRING", "shared-port")
elif network and oid in ("1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1.16"): emit("1", "Counter32", 17)
elif network and oid == "1.3.6.1.2.1.2.2.1.8": emit("1", "INTEGER", 1)
elif network and oid in ("1.3.6.1.2.1.31.1.1.1.6", "1.3.6.1.2.1.31.1.1.1.10"):
    step = 200000 if address == "fixture-switch" else 100000
    emit("1", "Counter64", (1 << 40) + cycle * step)
elif address == "fixture-ups":
    values = {"1.3.6.1.2.1.33.1.2.1.0": 2, "1.3.6.1.2.1.33.1.2.3.0": 30,
              "1.3.6.1.2.1.33.1.2.4.0": 88, "1.3.6.1.2.1.33.1.2.5.0": 242,
              "1.3.6.1.2.1.33.1.2.7.0": 25}
    if oid in values: emit("", "INTEGER", values[oid])
    elif oid == "1.3.6.1.2.1.33.1.4.4.1.5": emit("1", "INTEGER", 140)
    else: sys.exit(1)
elif address == "fixture-printer":
    if oid == "1.3.6.1.2.1.43.11.1.1.6":
        emit("1.1", "STRING", "black"); emit("2.1", "STRING", "cyan"); emit("2.2", "STRING", "unknown")
    elif oid == "1.3.6.1.2.1.43.11.1.1.8":
        emit("1.1", "INTEGER", 200); emit("2.1", "INTEGER", 100); emit("2.2", "INTEGER", 100)
    elif oid == "1.3.6.1.2.1.43.11.1.1.9":
        emit("1.1", "INTEGER", 100); emit("2.1", "INTEGER", 25); emit("2.2", "INTEGER", -3)
    else: sys.exit(1)
elif address == "fixture-nas":
    if oid == "1.3.6.1.2.1.25.3.3.1.2": emit("7", "INTEGER", 30)
    elif oid == "1.3.6.1.2.1.25.2.3.1.3": emit("4", "STRING", "volume1")
    elif oid == "1.3.6.1.2.1.25.2.3.1.4": emit("4", "INTEGER", 4096)
    elif oid == "1.3.6.1.2.1.25.2.3.1.5": emit("4", "INTEGER", 1000)
    elif oid == "1.3.6.1.2.1.25.2.3.1.6": emit("4", "INTEGER", 250)
    else: sys.exit(1)
else: sys.exit(1)
'''


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bin', type=Path, default=Path(__file__).resolve().parents[1] / 'core/bin/monitord')
    parser.add_argument('--timeout', type=float, default=45, help='total acceptance deadline, seconds (default: 45)')
    args = parser.parse_args()
    if args.timeout <= 0 or not math.isfinite(args.timeout):
        parser.error('--timeout must be positive and finite')
    if os.name != 'posix':
        print(json.dumps({'fixture_simulation': True, 'status': 'skipped', 'reason': 'requires Unix executable fixtures'}))
        return 0
    binary = args.bin.resolve()
    if not binary.is_file():
        parser.error('build monitord with make all before running this check')
    stage = 'setup'
    process = None
    servers = []
    workers = []
    failed_bmc = threading.Event()
    fetched_members = set()
    members_lock = threading.Lock()
    deadline = time.monotonic() + args.timeout
    token = ''
    base = ''
    expected_auth = 'Basic ' + base64.b64encode(b'fixture:fixture-only-password').decode()

    def stop():
        nonlocal process
        if process is not None and process.poll() is None:
            process.send_signal(signal.SIGINT)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise AssertionError('owned daemon required forced cleanup')
        process = None

    class RedfishFixture(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            if self.headers.get('Authorization') != expected_auth:
                self.send_error(401)
                return
            if self.server.fixture_name == 'rack-a' and failed_bmc.is_set():
                self.send_error(503)
                return
            path = urllib.parse.urlsplit(self.path).path
            if path == '/redfish/v1/Systems':
                body = {'Name': 'System Collection', 'Members': [{'@odata.id': '/redfish/v1/Systems/1'}, {'@odata.id': '/redfish/v1/Systems/2'}]}
            elif path in ('/redfish/v1/Systems/1', '/redfish/v1/Systems/2'):
                with members_lock:
                    fetched_members.add((self.server.fixture_name, path))
                body = {'Id': path.rsplit('/', 1)[-1], 'PowerState': 'On'}
                if path.endswith('/1'):
                    body.update({'Status': {'Health': 'OK', 'HealthRollup': 'Critical' if self.server.fixture_name == 'rack-a' else 'OK'},
                                 'ProcessorSummary': {'Count': 2}, 'MemorySummary': {'TotalSystemMemoryGiB': 64}})
            else:
                self.send_error(404)
                return
            raw = json.dumps(body).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

    with tempfile.TemporaryDirectory(prefix='monitor-devices-') as temporary:
        root = Path(temporary)
        config = root / 'config.yaml'
        data = root / 'data'
        fixture = root / 'snmp_fixture.py'
        fixture.write_text(SNMP_FIXTURE, encoding='utf-8')
        command = root / 'fixture-snmpwalk'
        command.write_text('#!/bin/sh\nexec ' + shlex.quote(sys.executable) + ' ' + shlex.quote(str(fixture)) + ' "$@"\n', encoding='utf-8')
        command.chmod(0o700)
        target_profiles = [('switch', 'fixture-switch', 'network'), ('power', 'fixture-ups', 'ups'),
                           ('printer', 'fixture-printer', 'printer'), ('nas', 'fixture-nas', 'host')]
        snmp_targets = [{'name': name, 'address': address, 'profile': profile, 'community': 'fixture-only-community',
                         'version': '2c', 'command': str(command), 'timeout': '2s'} for name, address, profile in target_profiles]
        redfish_targets = []
        for name in ('rack-a', 'rack-b'):
            server = ThreadingHTTPServer(('127.0.0.1', 0), RedfishFixture)
            server.daemon_threads = True
            server.fixture_name = name
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            servers.append(server)
            workers.append(worker)
            redfish_targets.append({'name': name, 'url': f'http://127.0.0.1:{server.server_port}/redfish/v1',
                                    'user': 'fixture', 'password': 'fixture-only-password', 'timeout': '2s'})
        settings = {'global': {'hostname': 'isolated-device-fixture', 'update_every': 2},
                    'collectors': {'enabled': ['snmp', 'redfish'], 'modules': {'snmp': {'targets': snmp_targets}, 'redfish': {'targets': redfish_targets}}},
                    'plugins': {'enabled': False}, 'health': {'enabled': False}}
        config.write_text(json.dumps(settings), encoding='utf-8')
        config.chmod(0o600)
        with (root / 'server.log').open('w', encoding='utf-8') as log:
            def request(path, credential=None, method='GET', body=None):
                auth = token if credential is None else credential
                headers = {'Authorization': 'Bearer ' + auth} if auth else {}
                payload = None
                if body is not None:
                    headers['Content-Type'] = 'application/json'
                    payload = json.dumps(body).encode()
                req = urllib.request.Request(base + path, headers=headers, method=method, data=payload)
                try:
                    with urllib.request.urlopen(req, timeout=2) as response:
                        return response.status, response.read()
                except urllib.error.HTTPError as response:
                    with response:
                        return response.code, response.read()

            def read(path):
                status, body = request(path)
                if status != 200:
                    raise AssertionError('authenticated fixture API request failed')
                return json.loads(body)

            def eventually(check, description):
                nonlocal stage
                stage = description
                while time.monotonic() < deadline:
                    if process is not None and process.poll() is not None:
                        raise AssertionError('owned daemon exited')
                    try:
                        if check():
                            return
                    except (OSError, urllib.error.URLError):
                        pass
                    time.sleep(.15)
                raise AssertionError('acceptance deadline exceeded')

            def start():
                nonlocal process, base, token
                with socket.socket() as probe:
                    probe.bind(('127.0.0.1', 0))
                    port = probe.getsockname()[1]
                base = f'http://127.0.0.1:{port}'
                process = subprocess.Popen([str(binary), '-config', str(config), '-data-dir', str(data), '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)
                def ready():
                    nonlocal token
                    token = (data / 'web-password').read_text().strip()
                    return request('/api/v1/info')[0] == 200
                eventually(ready, 'authenticated readiness')

            def value(metrics, chart, dimension='value'):
                return metrics.get(chart, {}).get('dimensions', {}).get(dimension, {}).get('value')

            def close(actual, expected):
                return isinstance(actual, (int, float)) and abs(actual - expected) < 0.001

            try:
                start()
                if request('/api/v1/charts', credential='')[0] != 401:
                    raise AssertionError('anonymous charts access allowed')
                expected = {'snmp.target.power.ups.charge': 88, 'snmp.target.power.ups.voltage': 24.2,
                            'snmp.target.power.ups.output_load.1': 140, 'snmp.target.printer.printer.supply_remaining.1.1': 50,
                            'snmp.target.printer.printer.supply_remaining.2.1': 25, 'snmp.target.nas.host.cpu_load.7': 30,
                            'snmp.target.nas.host.storage_used.4': 1024000, 'snmp.target.nas.host.storage_utilization.4': 25}
                def complete():
                    metrics = read('/api/v1/allmetrics?format=json')
                    good = all(close(value(metrics, chart), want) for chart, want in expected.items())
                    good = good and all(value(metrics, f'snmp.target.{name}.target_status', 'up') == 1 for name, _, _ in target_profiles)
                    good = good and all(value(metrics, f'redfish.target_status.{name}', 'up') == 1 for name in ('rack-a', 'rack-b'))
                    rates = [value(metrics, f'snmp.target.{name}.device_net.1', 'received') for name in ('switch', 'nas')]
                    return good and all(isinstance(rate, (int, float)) and rate > 1000 for rate in rates)
                eventually(complete, 'four profiles and high-capacity counters')
                charts = read('/api/v1/charts')['charts']
                metrics = read('/api/v1/allmetrics?format=json')
                if 'snmp.target.printer.printer.supply_remaining.2.2' in charts:
                    raise AssertionError('unknown printer supply fabricated as a measurement')
                for name in ('switch', 'nas'):
                    chart = charts[f'snmp.target.{name}.device_net.1']
                    if chart['labels']['target'] != name or chart['labels']['ifName'] != 'shared-port':
                        raise AssertionError('interface target identity lost')
                health_charts = [chart for chart in charts.values() if chart['context'] == 'redfish.system_health_state']
                if len(health_charts) != 4 or len(fetched_members) != 4:
                    raise AssertionError('Redfish member retrieval or target isolation failed')
                for chart in health_charts:
                    labels = chart['labels']
                    dim = 'unknown' if labels['system'] == '2' else 'critical' if labels['device'] == 'rack-a' else 'ok'
                    if value(metrics, chart['id'], dim) != 1:
                        raise AssertionError('Redfish health unknown/rollup semantics failed')
                query = urllib.parse.urlencode({'chart': 'snmp.target.power.ups.charge', 'after': -30, 'points': 30, 'group': 'last'})
                history = read('/api/v1/data?' + query)['result']['data']
                if not any(any(close(v, 88) for v in row[1:]) for row in history):
                    raise AssertionError('device sample missing from historical API')

                (root / 'printer-offline').touch()
                failed_bmc.set()
                def failed_independently():
                    current = read('/api/v1/allmetrics?format=json')
                    return (value(current, 'snmp.target.printer.target_status', 'down') == 1 and
                            value(current, 'redfish.target_status.rack-a', 'down') == 1 and
                            value(current, 'snmp.target.nas.target_status', 'up') == 1 and
                            value(current, 'redfish.target_status.rack-b', 'up') == 1)
                eventually(failed_independently, 'per-target failure isolation')
                (root / 'printer-offline').unlink()
                failed_bmc.clear()
                eventually(complete, 'device recovery without daemon restart')

                # Exercise the actual admin config route, then activate a reader
                # through an owned-process restart. No production auth is reused.
                stage = 'admin configuration save'
                cfg = read('/api/v1/manage/config')
                if len(cfg.get('form', {}).get('snmp_targets', [])) != 4 or len(cfg.get('form', {}).get('redfish_targets', [])) != 2:
                    raise AssertionError('visual config omitted named devices')
                viewer = secrets.token_urlsafe(32)
                settings['web'] = {'token': token, 'users': [{'name': 'fixture-viewer', 'role': 'viewer', 'token': viewer}]}
                status, _ = request('/api/v1/manage/config', method='PUT', body={'yaml': json.dumps(settings), 'if_updated': cfg['updated']})
                if status != 200:
                    raise AssertionError('admin config save rejected')
                stop()
                start()
                stage = 'viewer and admin permissions'
                if request('/api/v1/manage/config', credential=viewer)[0] != 403:
                    raise AssertionError('viewer could read device credentials')
                if request('/api/v1/manage/config', credential=viewer, method='PUT', body={'yaml': 'mode: agent'})[0] != 403:
                    raise AssertionError('viewer could modify device configuration')
                if request('/api/v1/charts', credential=viewer)[0] != 200 or request('/api/v1/manage/config')[0] != 200:
                    raise AssertionError('valid role access rejected')
                stop()
                log.flush()
                if token in (root / 'server.log').read_text() or viewer in (root / 'server.log').read_text():
                    raise AssertionError('fixture credential leaked into daemon logs')
                print(json.dumps({'fixture_simulation': True, 'status': 'passed', 'snmp_profiles': 4, 'redfish_targets': 2,
                                  'checks': ['high_capacity_counters', 'target_isolation', 'standard_mib_units', 'unknown_values',
                                             'redfish_member_links', 'health_rollup', 'historical_samples', 'failure_recovery',
                                             'admin_config_save', 'viewer_config_denied'], 'physical_device_validation': False}))
                return 0
            except Exception:
                # No response bodies, headers, config, logs or secrets in failure output.
                print(json.dumps({'fixture_simulation': True, 'status': 'failed', 'stage': stage}))
                return 1
            finally:
                try:
                    stop()
                finally:
                    for server in servers:
                        server.shutdown()
                        server.server_close()
                    for worker in workers:
                        worker.join(timeout=2)


if __name__ == '__main__':
    raise SystemExit(main())
