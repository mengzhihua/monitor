#!/usr/bin/env python3
"""Exercise real Agent/Hub processes, streaming, outage backfill and restart."""
import argparse
from http.client import RemoteDisconnected
import json
import math
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bin', default='core/bin/monitord')
    parser.add_argument('--protocol', choices=['stream', 'mqtt'], default='stream')
    parser.add_argument('--output')
    args = parser.parse_args()
    binary = str(Path(args.bin).resolve())
    tokens = {name: secrets.token_hex(24) for name in ['admin', 'viewer', 'operator', 'agent', 'key', 'wrong']}
    with tempfile.TemporaryDirectory(prefix='monitor-agent-hub-') as directory:
        root = Path(directory)
        processes, logs, ports = {}, {}, {}
        reservations = []
        for name in ['hub', 'agent', 'rejected']:
            sock = socket.socket()
            sock.bind(('127.0.0.1', 0))
            ports[name] = sock.getsockname()[1]
            reservations.append(sock)
        for sock in reservations:
            sock.close()
        users = ''.join(f'    - name: {name}\n      token: {tokens[name]}\n      role: {role}\n'
                        for name, role in [('admin', 'admin'), ('viewer', 'viewer'), ('operator', 'troubleshooter')])
        common = 'plugins:\n  enabled: false\ndb:\n  checkpoint: 1s\n'
        (root / 'hub.yaml').write_text('mode: hub\nglobal:\n  hostname: acceptance-hub\nweb:\n  users:\n' + users
                                      + f'hub:\n  api_keys: [{tokens["key"]}]\n'
                                      + 'collectors:\n  enabled: [mem]\nhealth:\n  enabled: false\n' + common)
        for name, key in [('agent', 'key'), ('rejected', 'wrong')]:
            (root / f'{name}.yaml').write_text(
                f'global:\n  hostname: acceptance-{name}\nweb:\n  token: {tokens["agent"]}\n'
                f'stream:\n  enabled: true\n  protocol: {args.protocol}\n'
                f'  destinations: ["127.0.0.1:{ports["hub"]}"]\n  api_key: {tokens[key]}\n  replicate: 1h\n'
                'collectors:\n  enabled: [mem, apps]\n  timeout: 5s\nhealth:\n  enabled: true\n'
                '  builtin: false\n  silent: true\n  alarms:\n    - name: stream_memory_probe\n'
                "      on: system.ram\n      calc: '$used'\n      every: 1s\n      warn: '$this > 0'\n" + common)

        def request(name, path, role='admin', method='GET', expected=200):
            token = tokens[role] if name == 'hub' else tokens['agent']
            req = urllib.request.Request(f'http://127.0.0.1:{ports[name]}' + path, method=method,
                                         headers={'Authorization': 'Bearer ' + token})
            try:
                with urllib.request.urlopen(req, timeout=10) as response:
                    status, raw = response.status, response.read()
            except urllib.error.HTTPError as error:
                status, raw = error.code, error.read()
            assert status == expected, f'{name} {method} {path}: expected {expected}, got {status}'
            # Responses may describe stream destinations, but must never expose credentials.
            assert all(token.encode() not in raw for token in tokens.values()), 'credential in API response'
            return json.loads(raw) if status == 200 else None

        def wait_for(label, check, seconds=60):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                assert all(p.poll() is None for p in processes.values()), f'process exited while waiting for {label}'
                try:
                    result = check()
                    if result:
                        return result
                except (urllib.error.URLError, TimeoutError, RemoteDisconnected, ConnectionResetError):
                    pass  # Retry bounded readiness reads only; never retry a mutation.
                time.sleep(.2)
            raise AssertionError(f'timed out waiting for {label}')

        def start(name):
            if name not in logs:
                logs[name] = (root / f'{name}.log').open('a')
            processes[name] = subprocess.Popen([binary, '-config', str(root / f'{name}.yaml'),
                                                '-data-dir', str(root / name), '-listen', f'127.0.0.1:{ports[name]}'],
                                               stdout=logs[name], stderr=logs[name])
            wait_for(name + ' authenticated API', lambda: request(name, '/api/v1/info'))

        def stop(name):
            process = processes.pop(name, None)
            if process is not None and process.poll() is None:
                process.send_signal(signal.SIGCONT)
                process.send_signal(signal.SIGINT)
                try:
                    process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
                    raise AssertionError(name + ' did not stop cleanly')
                assert process.returncode == 0, name + ' stopped unsuccessfully'

        def remote():
            return next((n for n in request('hub', '/api/v1/nodes')['nodes'] if not n.get('local')), None)

        def live_remote():
            node = remote()
            return node if node and node['status'] == 'live' else None

        def rows(name, begin, end, node=''):
            query = urllib.parse.urlencode({'chart': 'system.ram', 'node': node, 'after': begin,
                                            'before': end, 'points': end - begin, 'group': 'average'})
            result = request(name, '/api/v1/data?' + query)['result']
            return result['labels'], [row for row in result['data'] if any(v is not None for v in row[1:])]

        try:
            start('hub')
            start('agent')
            node = wait_for('live remote node', live_remote)
            node_id = node['id']
            suffix = urllib.parse.urlencode({'node': node_id})
            assert node['hostname'] == 'acceptance-agent'
            wait_for('stream connected', lambda: request('agent', '/api/v1/info')['stream']['connected'])
            request('hub', '/api/v1/charts?' + suffix, role='viewer')
            request('hub', '/api/v1/function?function=processes&' + suffix, role='viewer', expected=403)
            wait_for('remote function catalog', lambda: 'processes' in (remote().get('functions') or []))
            def process_snapshot():
                result = request('hub', '/api/v1/function?function=processes&' + suffix, role='operator')
                return result if result.get('result', {}).get('rows') else None

            result = wait_for('remote complete process snapshot', process_snapshot)
            assert result['node'] == node_id and result['result']['collected_at'] > 0
            request('hub', '/api/v1/nodes?' + suffix, role='operator', method='DELETE', expected=403)
            request('hub', '/api/v1/nodes?' + suffix, method='DELETE', expected=409)
            wait_for('forwarded real alarm', lambda: request('hub', '/api/v1/alarms?' + suffix)['summary']['warning'] > 0)

            # Native WebSocket client, same bearer subprotocol as the browser. Credentials only enter stdin.
            websocket = subprocess.run(['node', '--input-type=module', '-e', '''
import { readFileSync } from 'node:fs';
const input = JSON.parse(readFileSync(0, 'utf8'));
const ws = new WebSocket(input.url, ['monitor', 'bearer.' + Buffer.from(input.token).toString('base64url')]);
const frames = [];
let advanced = 0, latest = -Infinity, done = false;
const timer = setTimeout(() => { console.error('live samples timed out'); process.exit(1); }, 30000);
ws.onerror = () => { console.error('live connection failed'); process.exit(1); };
ws.onmessage = event => {
  if (done) return;
  const frame = JSON.parse(event.data);
  if (frame.chart !== 'system.ram') return;
  frames.push(frame);
  if (frame.t > latest) { latest = frame.t; advanced++; }
  if (advanced === 3) { done = true; clearTimeout(timer); ws.close(); console.log(JSON.stringify(frames)); }
};
'''], input=json.dumps({'url': f'ws://127.0.0.1:{ports["hub"]}/api/v1/live?charts=system.ram&{suffix}',
                       'token': tokens['viewer']}), capture_output=True, text=True, timeout=40, check=True)
            frames = json.loads(websocket.stdout)
            assert all(f['node'] == node_id and f['v'] and all(math.isfinite(v) for v in f['v'].values()) for f in frames)
            # Ingest broadcasts replay/duplicate timestamps too; both shipped
            # chart clients ignore non-advancing frames. Require three actual
            # advances, retain and report every duplicate rather than claiming
            # exactly-once delivery from this feed.
            latest, advances, non_advancing = -math.inf, 0, 0
            for frame in frames:
                if frame['t'] > latest:
                    latest, advances = frame['t'], advances + 1
                else:
                    non_advancing += 1
            assert advances == 3 and latest >= int(time.time()) - 5, 'live samples did not advance to the present'

            processes['agent'].send_signal(signal.SIGSTOP)
            wait_for('connected but stale', lambda: remote()['status'] == 'stale', seconds=30)
            request('hub', '/api/v1/nodes?' + suffix, method='DELETE', expected=409)
            processes['agent'].send_signal(signal.SIGCONT)
            wait_for('resumed stream', lambda: remote()['status'] == 'live')

            # Keep the agent collecting during a real Hub outage, then compare fixed history bounds exactly.
            stop('hub')
            begin = int(time.time()) + 1
            wait_for('agent collected during outage', lambda: int(time.time()) >= begin + 8, seconds=15)
            end = int(time.time()) - 1
            expected = rows('agent', begin, end)
            assert len(expected[1]) >= 5, 'insufficient actual samples during outage'
            start('hub')
            wait_for('outage history backfilled', lambda: rows('hub', begin, end, node_id) == expected)
            wait_for('alarm snapshot after reconnect', lambda: request('hub', '/api/v1/alarms?' + suffix)['summary']['warning'] > 0)
            stop('agent')
            wait_for('disconnected node offline', lambda: remote()['status'] == 'offline')
            stop('hub')
            start('hub')
            assert remote()['status'] == 'offline', 'offline node identity changed after restart'
            assert rows('hub', begin, end, node_id) == expected, 'persisted remote history changed'

            start('rejected')
            wait_for('wrong stream key rejected with retries', lambda: request('rejected', '/api/v1/info')['stream']['reconnects'] >= 2)
            rejected = request('rejected', '/api/v1/info')['stream']
            rejection = 'mqtt: connect refused' if args.protocol == 'mqtt' else '401'
            assert not rejected['connected'] and rejection in rejected.get('last_error', ''), 'wrong key was not rejected'
            assert remote()['status'] == 'offline', 'rejected sender took over offline node'
            stop('rejected')
            request('hub', '/api/v1/nodes?' + suffix, method='DELETE', expected=204)
            assert remote() is None, 'offline node was not forgotten'
            stop('hub')
            for name, log in logs.items():
                log.flush()
                text = (root / f'{name}.log').read_text()
                assert 'panic' not in text.lower(), name + ' panicked'
                assert all(token not in text for token in tokens.values()), 'credential in daemon log'
            report = {'passed': True, 'protocol': args.protocol, 'scope': 'real local Agent and Hub processes; loopback transport',
                      'remote_function_rbac': True, 'live_websocket_frames': len(frames), 'live_timestamp_advances': advances,
                      'live_non_advancing_frames': non_advancing, 'alarm_forwarding': True,
                      'stale_resume_offline': True, 'outage_backfill_rows': len(expected[1]),
                      'restart_history_exact': True, 'wrong_key_rejected': True, 'offline_forget': True}
            print(json.dumps(report))
            if args.output:
                Path(args.output).write_text(json.dumps(report, indent=2) + '\n')
        finally:
            cleanup_errors = []
            for name in list(processes):
                try:
                    stop(name)
                except Exception as error:
                    cleanup_errors.append(str(error))
            for log in logs.values():
                log.close()
            if args.output:
                diagnostics = {}
                for name in logs:
                    text = (root / f'{name}.log').read_text()
                    for token in tokens.values():
                        text = text.replace(token, '<redacted>')
                    diagnostics[name] = text
                Path(args.output + '.logs.json').write_text(json.dumps(diagnostics, indent=2) + '\n')
            if cleanup_errors:
                raise RuntimeError('; '.join(cleanup_errors))


if __name__ == '__main__':
    main()
