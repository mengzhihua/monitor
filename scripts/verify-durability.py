#!/usr/bin/env python3
"""Verify real process crash recovery, exclusive backup and restore; isolated temp data."""
import argparse, json, pathlib, signal, socket, subprocess, tempfile, time, urllib.request

p = argparse.ArgumentParser()
p.add_argument('--bin', default='core/bin/monitord')
args = p.parse_args()
binary = str(pathlib.Path(args.bin).resolve())

with tempfile.TemporaryDirectory(prefix='monitor-durability-') as tmp:
    root = pathlib.Path(tmp)
    config = root / 'test.yaml'
    config.write_text('collectors:\n  enabled: [cpu, mem]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\ndb:\n  checkpoint: 1s\n')
    child = None
    log = (root / 'server.log').open('w')
    def start(data):
        global child, base, token
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
        base = f'http://127.0.0.1:{port}'
        child = subprocess.Popen([binary, '-config', str(config), '-listen', f'127.0.0.1:{port}', '-data-dir', str(data)], stdout=log, stderr=log)
        for _ in range(100):
            if child.poll() is not None: raise RuntimeError('server exited: ' + (root / 'server.log').read_text())
            try:
                urllib.request.urlopen(base+'/healthz', timeout=1).close()
                token = (data/'web-password').read_text().strip(); return
            except OSError: time.sleep(.1)
        raise RuntimeError('server never became ready')
    def stop(sig=signal.SIGINT):
        global child
        if child and child.poll() is None:
            child.send_signal(sig)
            try: child.wait(timeout=120)
            except subprocess.TimeoutExpired: child.kill(); child.wait(); raise
        child = None
    def data(after, before):
        url=f'{base}/api/v1/data?chart=system.ram&after={after}&before={before}&points=100'
        with urllib.request.urlopen(urllib.request.Request(url,headers={'Authorization':'Bearer '+token}),timeout=5) as r: return json.load(r)['result']['data']
    try:
        source = root/'source'
        start(source); time.sleep(5)
        # Wait for a completed checkpoint newer than the observed samples.
        cutoff=int(time.time())-1
        for _ in range(300):
            with urllib.request.urlopen(urllib.request.Request(base+'/api/v1/info',headers={'Authorization':'Bearer '+token}),timeout=30) as r: info=json.load(r)
            checkpoint=info['db']['persistence']
            if checkpoint['last_checkpoint']>=cutoff: break
            if checkpoint.get('error'): raise RuntimeError(checkpoint['error'])
            time.sleep(.2)
        else: raise RuntimeError('checkpoint did not complete')
        end=cutoff-1; begin=end-4
        expected=data(begin,end)
        assert expected and any(any(v is not None for v in row[1:]) for row in expected), 'no samples'
        # A running server must hold the backup lock.
        active = subprocess.run([binary,'-config',str(config),'-data-dir',str(source),'-backup-dir',str(root/'unsafe')],capture_output=True)
        assert active.returncode != 0, 'accepted a backup of a running server'
        stop(signal.SIGKILL)
        start(source)
        actual=data(begin,end)
        assert actual==expected, f'checkpointed data differs: {expected!r} vs {actual!r}; log: {(root / "server.log").read_text()}'
        stop()
        snap=root/'backup'; restored=root/'restored'
        subprocess.run([binary,'-config',str(config),'-data-dir',str(source),'-backup-dir',str(snap)],check=True)
        subprocess.run([binary,'-config',str(config),'-data-dir',str(restored),'-restore-from',str(snap)],check=True)
        start(restored)
        assert data(begin,end)==expected, 'restored history differs'
        print(json.dumps({'crash_recovery':'passed','active_backup_rejected':True,'backup_restore':'passed','rows_checked':len(expected)}))
    finally:
        stop(); log.close()
