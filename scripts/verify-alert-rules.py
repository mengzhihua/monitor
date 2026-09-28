#!/usr/bin/env python3
"""Check alert-rule editing against a real daemon with isolated, silent data."""
import argparse
from concurrent.futures import ThreadPoolExecutor
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
import urllib.parse
import urllib.request


RULES = '/api/v1/alert_config'
MANAGE = '/api/v1/manage/alert-rules'
BASE = 'acceptance_base_memory'
REMOVED_BASE = 'acceptance_deleted_base'
CUSTOM = 'acceptance_dynamic_memory'
DISABLED = 'acceptance_disabled_memory'


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def spec(name, **changes):
    return {'name': name, 'on': 'system.ram', 'calc': '$used',
            'every': '1s', 'warn': '$this < 0', **changes}


class Daemon:
    def __init__(self, binary, root):
        self.binary = binary
        self.root = root
        self.tokens = {role: secrets.token_hex(24) for role in ('admin', 'viewer', 'troubleshooter')}
        self.config = root / 'config.yaml'
        self.data = root / 'data'
        self.store = self.data / 'health' / 'alert-rules.json'
        self.log_path = root / 'daemon.log'
        self.log = self.log_path.open('w')
        self.process = None
        self.base = ''
        # Every credential is generated for this temporary daemon. Silent health
        # and an empty notifier configuration prevent external notification.
        self.config.write_text(f'''web:
  users:
    - name: acceptance-admin
      token: {self.tokens['admin']}
      role: admin
    - name: acceptance-viewer
      token: {self.tokens['viewer']}
      role: viewer
    - name: acceptance-troubleshooter
      token: {self.tokens['troubleshooter']}
      role: troubleshooter
collectors:
  enabled: [mem]
plugins:
  enabled: false
health:
  enabled: true
  builtin: false
  silent: true
  alarms:
    - name: {BASE}
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this < 0'
    - name: {REMOVED_BASE}
      on: system.ram
      calc: '$used'
      every: 1s
      warn: '$this < 0'
''', encoding='utf-8')
        if os.name != 'nt':
            self.config.chmod(0o600)
        self.original_config = self.config.read_bytes()

    def command(self, address):
        return [self.binary, '-config', str(self.config), '-data-dir', str(self.data), '-listen', address]

    def start(self):
        require(self.process is None or self.process.poll() is not None, 'test daemon already running')
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        self.base = f'http://127.0.0.1:{port}'
        self.process = subprocess.Popen(self.command(f'127.0.0.1:{port}'), stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            require(self.process.poll() is None, 'isolated test daemon exited before readiness')
            try:
                return self.snapshot()
            except (OSError, urllib.error.URLError):
                time.sleep(.1)
        raise AssertionError('alert-rule readiness timed out')

    def stop(self):
        if self.process is not None and self.process.poll() is None:
            self.process.send_signal(signal.SIGINT)
            try:
                code = self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
                raise AssertionError('test daemon did not stop cleanly') from None
            require(code == 0, 'test daemon exited unsuccessfully during shutdown')

    def request(self, path, body=None, method=None, role='admin', expected=200, raw=None):
        require(body is None or raw is None, 'request cannot have JSON and raw payloads')
        headers = {}
        if role is not None:
            headers['Authorization'] = 'Bearer ' + self.tokens[role]
        data = raw
        if body is not None:
            data = json.dumps(body).encode()
            headers['Content-Type'] = 'application/json'
        elif raw is not None:
            headers['Content-Type'] = 'application/yaml'
        req = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        # Never route even local test credentials through an environment proxy.
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            with opener.open(req, timeout=5) as response:
                status, content = response.status, response.read()
        except urllib.error.HTTPError as response:
            with response:
                status, content = response.code, response.read()
        allowed = expected if isinstance(expected, tuple) else (expected,)
        if status not in allowed:
            detail = content.decode(errors='replace')[:400]
            for token in self.tokens.values():
                detail = detail.replace(token, '[redacted]')
            raise AssertionError(f'{method or req.get_method()} {path}: got {status}, expected {allowed}: {detail}')
        if status >= 400:
            return status
        return json.loads(content) if content else None

    def snapshot(self, version=1, role='admin'):
        result = self.request(f'/api/v{version}/alert_config', role=role)
        require(result['api'] == version, 'API version not preserved')
        require(isinstance(result['revision'], str) and result['revision'], 'missing opaque rule revision')
        require(result['persistent'] is True, 'real daemon must persist dynamic rules')
        require(result['count'] == len(result['configs']), 'rule count disagrees with snapshot')
        return result

    def saved_bytes(self):
        return self.store.read_bytes() if self.store.exists() else None

    def check_private_store(self):
        require(self.store.is_file(), 'dynamic rule store was not written')
        contents = self.store.read_text(encoding='utf-8')
        require(all(token not in contents for token in self.tokens.values()), 'test credential leaked into rule store')
        if os.name != 'nt':
            require(self.store.stat().st_mode & 0o777 == 0o600, 'rule store must have mode 0600')
        require(self.config.read_bytes() == self.original_config, 'rule editing changed deployment YAML')

    def unchanged(self, before, file_before):
        require(self.snapshot() == before, 'rejected/preview action changed rules or revision')
        require(self.saved_bytes() == file_before, 'rejected/preview action changed persisted rules')

    def mutate(self, action, **fields):
        before = self.snapshot()
        after = self.request(MANAGE, {'revision': before['revision'], 'action': action, **fields})
        require(after == self.snapshot(), 'mutation response does not match saved snapshot')
        require(after['revision'] != before['revision'], 'successful mutation did not advance revision')
        self.check_private_store()
        return after

    def wait_alarm(self, name, status):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            require(self.process.poll() is None, 'daemon exited while waiting for real samples')
            alarms = self.request('/api/v1/alarms?all=true')['alarms'].values()
            for alarm in alarms:
                if alarm['name'] == name and alarm['status'] == status and alarm['value'] is not None and alarm['value'] > 0:
                    require(alarm['chart'] == 'system.ram', 'rule bound to the wrong chart')
                    return
            time.sleep(.2)
        raise AssertionError(f'expected {status} for {name} using real memory samples')


def by_name(snapshot):
    return {entry['name']: entry for entry in snapshot['configs']}


def verify(daemon):
    initial = daemon.start()
    initial_rules = by_name(initial)
    require(set(initial_rules) == {BASE, REMOVED_BASE}, 'unexpected initial rules')
    require(all(r['origin'] == 'base' and r['has_base'] and not r['deleted'] for r in initial_rules.values()), 'base rule metadata missing')
    daemon.wait_alarm(BASE, 'CLEAR')
    version3 = daemon.snapshot(version=3, role='viewer')
    require(version3['configs'] == initial['configs'] and version3['revision'] == initial['revision'], 'v1/v3 snapshots differ')
    daemon.request(RULES, role=None, expected=401)

    candidate = spec(CUSTOM, warn='$this > 0')
    payload = {'revision': initial['revision'], 'action': 'create', 'config': candidate}
    before_file = daemon.saved_bytes()
    for role in ('viewer', 'troubleshooter'):
        for path in (MANAGE, MANAGE + '/preview'):
            daemon.request(path, payload, role=role, expected=403)
    for path in (MANAGE, MANAGE + '/preview'):
        daemon.request(path, {'action': 'create', 'config': candidate}, expected=400)
    preview = daemon.request(MANAGE + '/preview', payload)
    require(preview['valid'] and preview['persistent'] and preview['revision'] == initial['revision'], 'preview metadata incorrect')
    require(preview['action'] == 'create' and preview['name'] == CUSTOM, 'preview action/name incorrect')
    require(preview['matched'] >= 1 and any(c['id'] == 'system.ram' for c in preview['charts']), 'preview did not match existing RAM chart')
    require(preview['limit'] == 50 and not preview['truncated'] and not preview['disabled'] and preview['notice'], 'preview limits/notice incorrect')
    daemon.request(MANAGE + '/preview', {**payload, 'config': spec(CUSTOM, warn='(')}, expected=400)
    daemon.unchanged(initial, before_file)

    daemon.mutate('create', config=candidate)
    daemon.wait_alarm(CUSTOM, 'WARNING')
    daemon.mutate('update', config=spec(BASE, warn='$this > 0'))
    daemon.mutate('delete', name=REMOVED_BASE)
    daemon.mutate('create', config=spec(DISABLED, warn='$this > 0', disabled=True))
    state = daemon.snapshot()
    entries = by_name(state)
    require(entries[BASE]['origin'] == 'override' and entries[BASE]['has_base'], 'base edit did not become an override')
    require(entries[REMOVED_BASE]['origin'] == 'deleted' and entries[REMOVED_BASE]['deleted'] and entries[REMOVED_BASE]['has_base'], 'base delete did not leave a tombstone')
    require(entries[REMOVED_BASE]['config'] == initial_rules[REMOVED_BASE]['config'], 'base tombstone lost reset definition')
    require(entries[CUSTOM]['origin'] == 'custom' and not entries[CUSTOM]['has_base'], 'custom origin metadata incorrect')

    # Competing editors use exactly one observed revision. Neither request is
    # allowed to silently refresh its precondition before sending.
    revision = state['revision']
    barrier = threading.Barrier(2)
    def compete(index):
        barrier.wait(timeout=10)
        result = daemon.request(MANAGE, {'revision': revision, 'action': 'create', 'config': spec(f'acceptance_racer_{index}')}, expected=(200, 409))
        return index, result
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(compete, (0, 1)))
    winners = [index for index, result in results if isinstance(result, dict)]
    require(len(winners) == 1 and sum(result == 409 for _, result in results) == 1, 'concurrent editors did not yield one success and one conflict')
    entries = by_name(daemon.snapshot())
    require(sum(f'acceptance_racer_{i}' in entries for i in (0, 1)) == 1, 'conflicting write leaked a rule')

    # Existing clients retain supported body shapes and no-CAS mutation paths.
    daemon.request(RULES, spec('acceptance_legacy_single'), method='PUT')
    daemon.request('/api/v3/alert_config', {'config': spec('acceptance_legacy_wrapped')}, method='PUT')
    daemon.request(RULES, {'alarms': [spec('acceptance_legacy_batch_a'), spec('acceptance_legacy_batch_b')]}, method='POST')
    daemon.request(RULES, method='PUT', raw=b"alarms:\n  - name: acceptance_legacy_yaml\n    on: system.ram\n    calc: '$used'\n    warn: '$this < 0'\n    every: 1s\n")
    entries = by_name(daemon.snapshot())
    for name in ('acceptance_legacy_single', 'acceptance_legacy_wrapped', 'acceptance_legacy_batch_a', 'acceptance_legacy_batch_b', 'acceptance_legacy_yaml'):
        require(name in entries, f'legacy body shape did not save {name}')
    before, file_before = daemon.snapshot(), daemon.saved_bytes()
    daemon.request(RULES, {'alarms': [spec('acceptance_must_not_partially_save'), spec(BASE)]}, method='POST', expected=409)
    duplicate = spec('acceptance_duplicate_batch')
    daemon.request(RULES, {'alarms': [duplicate, duplicate]}, method='POST', expected=(400, 409))
    daemon.request(MANAGE, {'revision': before['revision'], 'action': 'update', 'config': spec(BASE, warn='(')}, expected=400)
    daemon.unchanged(before, file_before)
    daemon.request(RULES + '?name=acceptance_legacy_single', method='DELETE')
    require('acceptance_legacy_single' not in by_name(daemon.snapshot()), 'legacy delete failed')

    saved = daemon.snapshot()
    saved_file = daemon.saved_bytes()
    daemon.check_private_store()
    daemon.stop()
    restarted = daemon.start()
    require(restarted['configs'] == saved['configs'], 'rules changed after real daemon restart')
    require(restarted['revision'] != saved['revision'], 'restart did not invalidate prior editing epoch')
    require(daemon.saved_bytes() == saved_file, 'reading persisted rules rewrote their contents')
    daemon.wait_alarm(CUSTOM, 'WARNING')
    daemon.wait_alarm(BASE, 'WARNING')
    alarms = daemon.request('/api/v1/alarms?all=true')['alarms'].values()
    require(not any(a['name'] in (DISABLED, REMOVED_BASE) for a in alarms), 'disabled/deleted rule bound an alarm after restart')
    daemon.request(MANAGE, {'revision': saved['revision'], 'action': 'create', 'config': spec('acceptance_stale_restart')}, expected=409)
    daemon.unchanged(restarted, saved_file)

    reset_preview = daemon.request(MANAGE + '/preview', {'revision': restarted['revision'], 'action': 'reset', 'name': BASE})
    require(reset_preview['valid'] and reset_preview['name'] == BASE, 'base reset preview failed')
    daemon.unchanged(restarted, saved_file)
    daemon.mutate('reset', name=BASE)
    daemon.mutate('reset', name=REMOVED_BASE)
    daemon.mutate('delete', name=CUSTOM)
    restored = daemon.snapshot()
    restored_entries = by_name(restored)
    for name in (BASE, REMOVED_BASE):
        require(restored_entries[name] == initial_rules[name], f'reset did not restore the original base definition: {name}')
    require(CUSTOM not in restored_entries, 'deleting custom rule left it visible')
    daemon.stop()
    final = daemon.start()
    require(final['configs'] == restored['configs'], 'reset/deletion did not survive a second restart')
    daemon.wait_alarm(BASE, 'CLEAR')
    daemon.wait_alarm(REMOVED_BASE, 'CLEAR')
    daemon.check_private_store()
    notifications = daemon.request('/api/v1/operations/notifications')
    require(not notifications['recent'] and notifications['total'] == 0, 'isolated acceptance unexpectedly dispatched notifications')
    daemon.stop()
    daemon.log.flush()
    require(all(token not in daemon.log_path.read_text(errors='replace') for token in daemon.tokens.values()), 'test credential leaked into daemon log')

    # A damaged overlay must fail startup without discarding edits or silently
    # falling back to only base rules. This is still the disposable data dir.
    daemon.store.write_bytes(b'{"incomplete":')
    try:
        failed = subprocess.run(daemon.command('127.0.0.1:0'), stdout=daemon.log, stderr=daemon.log, timeout=10)
    except subprocess.TimeoutExpired:
        raise AssertionError('daemon kept running with a corrupt dynamic rule store') from None
    require(failed.returncode != 0, 'corrupt dynamic rule store allowed successful startup')
    require(daemon.store.read_bytes() == b'{"incomplete":', 'startup rewrote corrupt rule storage')
    require(daemon.config.read_bytes() == daemon.original_config, 'acceptance changed deployment YAML')
    print(json.dumps({'alert_rules': 'passed', 'real_restarts': 2, 'real_memory_samples': 'passed',
                      'preview_no_write': 'passed', 'rbac': 'passed', 'concurrent_conflict': 'passed',
                      'legacy_atomic_crud': 'passed', 'override_delete_reset_persistence': 'passed',
                      'restart_epoch': 'passed', 'corrupt_store_fails_closed': 'passed',
                      'external_notifications': 'not configured'}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='core/bin/monitord')
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    require(Path(binary).is_file(), 'monitord binary is missing; build the current server first')
    with tempfile.TemporaryDirectory(prefix='monitor-alert-rules-') as directory:
        daemon = Daemon(binary, Path(directory))
        try:
            verify(daemon)
        finally:
            try:
                daemon.stop()
            finally:
                daemon.log.close()


if __name__ == '__main__':
    main()
