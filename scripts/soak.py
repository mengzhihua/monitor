#!/usr/bin/env python3
"""Bounded local agent soak. Outputs measured observations, not capacity promises.

Use --seconds 86400 (24h) to investigate RSS / heap growth over a full day.
Process metrics come from /metrics (monitor_process_heap_* / monitor_tsdb_series).
Optional --heap-dir captures admin heap profiles at start/mid/end for go tool pprof.
"""
import argparse, json, pathlib, re, signal, socket, statistics, subprocess, tempfile, time, urllib.error, urllib.request

p = argparse.ArgumentParser()
p.add_argument('--seconds', type=int, default=120)
p.add_argument('--bin', default='core/bin/monitord')
p.add_argument('--output')
p.add_argument('--sample-every', type=float, default=2.0, help='seconds between samples')
p.add_argument('--heap-dir', help='directory for start/mid/end heap.pprof captures (admin API)')
a = p.parse_args()
if a.seconds < 10:
    p.error('use at least 10 seconds')

heap_re = re.compile(r'^monitor_process_heap_alloc_bytes\{[^}]*\} (\d+)', re.M)
series_re = re.compile(r'^monitor_tsdb_series\{[^}]*\} (\d+)', re.M)

with tempfile.TemporaryDirectory(prefix='monitor-soak-') as tmp:
    root = pathlib.Path(tmp)
    cfg = root / 'config.yaml'
    cfg.write_text('collectors:\n  enabled: [cpu, mem, load, disk, net]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n')
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    log = (root / 'server.log').open('w')
    proc = subprocess.Popen(
        [str(pathlib.Path(a.bin).resolve()), '-config', str(cfg), '-data-dir', str(root / 'data'), '-listen', f'127.0.0.1:{port}'],
        stdout=log, stderr=log,
    )
    latencies = []
    rss = []
    heap = []
    series = []
    errors = []
    heap_files = []
    charts = 0
    try:
        for _ in range(200):
            try:
                urllib.request.urlopen(base + '/healthz', timeout=1).close()
                break
            except OSError:
                time.sleep(.1)
        token = (root / 'data' / 'web-password').read_text().strip()

        def authed(path):
            return urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + token})

        def capture_heap(label):
            if not a.heap_dir:
                return
            dest_dir = pathlib.Path(a.heap_dir)
            dest_dir.mkdir(parents=True, exist_ok=True)
            dest = dest_dir / f'heap-{label}.pprof'
            try:
                with urllib.request.urlopen(authed('/api/v1/debug/pprof/heap'), timeout=60) as r:
                    data = r.read()
                if len(data) < 32:
                    errors.append(f'heap {label}: too small')
                    return
                dest.write_bytes(data)
                heap_files.append(str(dest))
            except (OSError, urllib.error.URLError) as exc:
                errors.append(f'heap {label}: {exc}')

        capture_heap('start')
        mid_at = time.monotonic() + a.seconds / 2
        mid_done = False
        deadline = time.monotonic() + a.seconds
        while time.monotonic() < deadline:
            try:
                started = time.perf_counter()
                with urllib.request.urlopen(authed('/api/v1/data?chart=system.ram&after=-60&points=60'), timeout=10) as r:
                    json.load(r)
                latencies.append((time.perf_counter() - started) * 1000)
                with urllib.request.urlopen(authed('/api/v1/info'), timeout=10) as r:
                    info = json.load(r)
                charts = info['charts_count']
                persistence = info.get('db', {}).get('persistence', {})
                if persistence.get('error'):
                    errors.append('checkpoint: ' + persistence['error'])
                last = persistence.get('last_checkpoint', 0)
                if last and time.time() - last > max(120, 2 * persistence.get('interval_seconds', 30)):
                    errors.append('checkpoint overdue')
                errors.extend(c['name'] + ': ' + c['error'] for c in info['collectors'] if c.get('error'))
                usage = subprocess.check_output(['ps', '-o', 'rss=', '-p', str(proc.pid)], text=True).strip()
                rss.append(int(usage) / 1024)
                with urllib.request.urlopen(authed('/metrics'), timeout=10) as r:
                    metrics = r.read().decode('utf-8', 'replace')
                hm = heap_re.search(metrics)
                sm = series_re.search(metrics)
                if hm:
                    heap.append(int(hm.group(1)) / (1024 * 1024))
                if sm:
                    series.append(int(sm.group(1)))
                if proc.poll() is not None:
                    raise RuntimeError('agent exited')
            except Exception as e:
                errors.append(str(e))
            if not mid_done and time.monotonic() >= mid_at:
                capture_heap('mid')
                mid_done = True
            time.sleep(min(a.sample_every, max(0, deadline - time.monotonic())))
        capture_heap('end')
    finally:
        proc.send_signal(signal.SIGINT)
        try:
            proc.wait(timeout=120)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
            errors.append('shutdown timed out')
        log.close()

    def growth(samples):
        if len(samples) < 2:
            return None
        return samples[-1] - samples[0]

    report = {
        'duration_seconds': a.seconds,
        'requests': len(latencies),
        'charts': charts,
        'query_ms_median': statistics.median(latencies) if latencies else None,
        'query_ms_max': max(latencies, default=0),
        'rss_mib_first': rss[0] if rss else None,
        'rss_mib_last': rss[-1] if rss else None,
        'rss_mib_max': max(rss, default=0),
        'rss_mib_growth': growth(rss),
        'heap_mib_first': heap[0] if heap else None,
        'heap_mib_last': heap[-1] if heap else None,
        'heap_mib_max': max(heap, default=0),
        'heap_mib_growth': growth(heap),
        'tsdb_series_first': series[0] if series else None,
        'tsdb_series_last': series[-1] if series else None,
        'tsdb_series_max': max(series, default=0),
        'disk_bytes': sum(f.stat().st_size for f in (root / 'data').rglob('*') if f.is_file()),
        'heap_profiles': heap_files,
        'errors': sorted(set(errors)),
        'scope': 'local agent; use --seconds 86400 for a 24h soak; not a distributed Hub load test',
    }
    text = json.dumps(report, indent=2)
    print(text)
    if a.output:
        pathlib.Path(a.output).write_text(text + '\n')
    if errors:
        raise SystemExit(1)
