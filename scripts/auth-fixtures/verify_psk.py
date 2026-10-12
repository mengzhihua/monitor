#!/usr/bin/env python3
"""Spin a tiny Hub+agent pair and confirm X-Monitor-PSK is enforced."""
import argparse, pathlib, signal, socket, subprocess, tempfile, time, urllib.error, urllib.request

p = argparse.ArgumentParser()
p.add_argument('--hub-bin', default='core/bin/monitord')
a = p.parse_args()
bin_path = pathlib.Path(a.hub_bin).resolve()

def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

with tempfile.TemporaryDirectory(prefix='monitor-psk-') as tmp:
    root = pathlib.Path(tmp)
    hub_port, agent_port = free_port(), free_port()
    (root / 'hub.yaml').write_text(f"""mode: hub
global:
  hostname: psk-hub
hub:
  api_keys: [hub-fixture-key]
  psk: fixture-psk-shared-secret
web:
  token: hub-admin
collectors:
  enabled: [cpu]
plugins:
  enabled: false
health:
  enabled: false
""")
    (root / 'agent.yaml').write_text(f"""global:
  hostname: psk-agent
web:
  token: agent-admin
stream:
  enabled: true
  destinations: ["http://127.0.0.1:{hub_port}"]
  api_key: hub-fixture-key
  psk: fixture-psk-shared-secret
collectors:
  enabled: [cpu]
plugins:
  enabled: false
health:
  enabled: false
""")
    hub = subprocess.Popen([str(bin_path), '-config', str(root / 'hub.yaml'), '-data-dir', str(root / 'hub-data'), '-listen', f'127.0.0.1:{hub_port}'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    agent = subprocess.Popen([str(bin_path), '-config', str(root / 'agent.yaml'), '-data-dir', str(root / 'agent-data'), '-listen', f'127.0.0.1:{agent_port}'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(50):
            try:
                urllib.request.urlopen(f'http://127.0.0.1:{hub_port}/healthz', timeout=1).close()
                break
            except OSError:
                time.sleep(0.1)
        # Wrong PSK on a raw stream probe should be rejected.
        req = urllib.request.Request(
            f'http://127.0.0.1:{hub_port}/api/v1/stream',
            headers={'Authorization': 'Bearer hub-fixture-key', 'X-Monitor-PSK': 'wrong-psk'},
        )
        try:
            urllib.request.urlopen(req, timeout=2)
            raise SystemExit('wrong PSK was accepted')
        except urllib.error.HTTPError as e:
            if e.code not in (401, 403):
                raise SystemExit(f'unexpected status for wrong PSK: {e.code}')
        # Give the correctly configured agent time to appear.
        ok = False
        for _ in range(30):
            nodes = urllib.request.urlopen(
                urllib.request.Request(f'http://127.0.0.1:{hub_port}/api/v1/nodes', headers={'Authorization': 'Bearer hub-admin'}),
                timeout=2,
            )
            data = nodes.read().decode()
            if 'psk-agent' in data or '"status":"live"' in data:
                ok = True
                break
            time.sleep(0.5)
        if not ok:
            raise SystemExit('agent with matching PSK did not register')
        print('psk fixture ok')
    finally:
        for proc in (agent, hub):
            proc.send_signal(signal.SIGINT)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
