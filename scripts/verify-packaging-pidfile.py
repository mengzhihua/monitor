#!/usr/bin/env python3
"""Verify packaging start/stop refuse decoy pidfiles and stop only our monitord."""
from __future__ import annotations

import os
import pathlib
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[1]


def free_port() -> int:
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def wait_health(url: str, timeout: float = 15.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            urllib.request.urlopen(url, timeout=1).close()
            return
        except OSError:
            time.sleep(0.1)
    raise RuntimeError('agent did not become healthy')


def main() -> int:
    bin_path = pathlib.Path(os.environ.get('MONITOR_TEST_BIN', ROOT / 'core/bin/monitord')).resolve()
    if not bin_path.is_file():
        print(f'missing binary: {bin_path}; build with scripts/build.sh core', file=sys.stderr)
        return 2
    start_sh = ROOT / 'packaging/start.sh'
    stop_sh = ROOT / 'packaging/stop.sh'

    with tempfile.TemporaryDirectory(prefix='monitor-pidfile-') as tmp:
        root = pathlib.Path(tmp)
        data = root / 'data'
        data.mkdir()
        cfg = root / 'config.yaml'
        cfg.write_text('collectors:\n  enabled: [cpu, mem]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n')
        pidfile = data / 'monitord.pid'
        port = free_port()
        env = os.environ.copy()
        env.update({
            'MONITOR_BIN': str(bin_path),
            'MONITOR_DATA_DIR': str(data),
            'MONITOR_CONFIG': str(cfg),
            'MONITOR_LISTEN': f'127.0.0.1:{port}',
            'MONITOR_PIDFILE': str(pidfile),
        })

        # Decoy 1: non-monitord process whose argv embeds the data path (old bug).
        decoy = subprocess.Popen([sys.executable, '-c', 'import sys,time; time.sleep(120)', str(data)])
        try:
            pidfile.write_text(f'{decoy.pid}\n')
            stop = subprocess.run([str(stop_sh)], cwd=root, env=env, capture_output=True, text=True)
            if stop.returncode == 0:
                print('stop.sh accepted python decoy with data path in argv', file=sys.stderr)
                print(stop.stdout, stop.stderr, file=sys.stderr)
                return 1
            if 'not monitord' not in stop.stderr and 'does not match' not in stop.stderr:
                print(f'unexpected stop refusal: {stop.stderr!r}', file=sys.stderr)
                return 1
            if decoy.poll() is not None:
                print('decoy was killed', file=sys.stderr)
                return 1
        finally:
            decoy.send_signal(signal.SIGTERM)
            decoy.wait(timeout=5)
            pidfile.unlink(missing_ok=True)

        # Decoy 2: binary named monitord but no matching data/config path.
        fake_bin = root / 'monitord'
        shutil.copy2('/bin/sleep', fake_bin)
        fake_bin.chmod(0o755)
        fake = subprocess.Popen([str(fake_bin), '120'])
        try:
            pidfile.write_text(f'{fake.pid}\n')
            stop = subprocess.run([str(stop_sh)], cwd=root, env=env, capture_output=True, text=True)
            if stop.returncode == 0:
                print('stop.sh accepted renamed-sleep monitord without path match', file=sys.stderr)
                return 1
            if fake.poll() is not None:
                print('renamed-sleep decoy was killed', file=sys.stderr)
                return 1
        finally:
            fake.send_signal(signal.SIGTERM)
            fake.wait(timeout=5)
            pidfile.unlink(missing_ok=True)

        # start.sh must clear a decoy pidfile and still launch.
        decoy2 = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(120)'])
        try:
            pidfile.write_text(f'{decoy2.pid}\n')
            starter = subprocess.Popen(
                [str(start_sh)], cwd=root, env=env,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
            )
            try:
                wait_health(f'http://127.0.0.1:{port}/healthz')
                if not pidfile.is_file():
                    print('pidfile missing after start over decoy', file=sys.stderr)
                    return 1
                real_pid = int(pidfile.read_text().strip())
                if real_pid == decoy2.pid:
                    print('start.sh kept decoy pid', file=sys.stderr)
                    return 1
                # Second start against a live matching instance must refuse.
                again = subprocess.run([str(start_sh)], cwd=root, env=env, capture_output=True, text=True)
                if again.returncode == 0:
                    print('start.sh started a second matching instance', file=sys.stderr)
                    return 1
                if 'already running' not in again.stderr and 'already running' not in again.stdout:
                    print(f'unexpected second-start output: {again.stdout!r} {again.stderr!r}', file=sys.stderr)
                    return 1
                stop = subprocess.run([str(stop_sh)], cwd=root, env=env, capture_output=True, text=True)
                if stop.returncode != 0:
                    print(f'stop.sh failed on real instance: {stop.stderr}', file=sys.stderr)
                    return 1
                starter.wait(timeout=15)
                if pidfile.exists():
                    print('pidfile left behind after stop', file=sys.stderr)
                    return 1
            finally:
                if starter.poll() is None:
                    starter.send_signal(signal.SIGTERM)
                    try:
                        starter.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        starter.kill()
                        starter.wait(timeout=5)
        finally:
            decoy2.send_signal(signal.SIGTERM)
            decoy2.wait(timeout=5)
            pidfile.unlink(missing_ok=True)

    print('packaging pidfile checks: decoys refused, start clears decoy, real instance stopped')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
