#!/usr/bin/env python3
"""Bounded local agent soak. Outputs measured observations, not capacity promises.

Use --seconds 86400 (24h) for an overnight RSS/heap investigation.
By default heap profiles are captured at start/mid/end under --heap-dir
(or a temporary directory when omitted for runs >= 1h).

Closer-to-production mode: pass --seed-data-dir with a real agent data
directory (copied into an isolated temp tree; the source is never written).
Pair with --seed-config (or a sibling monitor.yaml) so collectors/alerts from
the seed are kept. Runs of --seconds >= 86400 apply default RSS/heap alert
thresholds unless overridden.
"""
from __future__ import annotations

import argparse, json, pathlib, re, shutil, signal, socket, statistics, subprocess, tempfile, time, urllib.error, urllib.request

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--seconds', type=int, default=120)
p.add_argument('--bin', default='core/bin/monitord')
p.add_argument('--output')
p.add_argument('--sample-every', type=float, default=2.0, help='seconds between samples')
p.add_argument('--heap-dir', help='directory for start/mid/end heap.pprof captures (admin API)')
p.add_argument('--no-heap', action='store_true', help='disable heap profile capture')
p.add_argument('--seed-data-dir', help='copy an existing agent data directory as the soak starting state')
p.add_argument('--seed-config', help='config file to use (default: sibling monitor.yaml/config.yaml near --seed-data-dir, else a minimal soak config)')
p.add_argument('--max-rss-mib', type=float, help='fail if RSS max exceeds this MiB')
p.add_argument('--max-heap-growth-mib', type=float, help='fail if heap alloc growth exceeds this MiB')
p.add_argument('--max-rss-growth-mib', type=float, help='fail if RSS growth exceeds this MiB')
a = p.parse_args()
if a.seconds < 10:
    p.error('use at least 10 seconds')

heap_re = re.compile(r'^monitor_process_heap_alloc_bytes\{[^}]*\} (\d+)', re.M)
series_re = re.compile(r'^monitor_tsdb_series\{[^}]*\} (\d+)', re.M)

# Overnight defaults: capture heap unless explicitly disabled; apply alert
# thresholds from docs/05-acceptance.md when the operator did not pass any.
heap_dir = a.heap_dir
if a.no_heap:
    heap_dir = None
elif not heap_dir and a.seconds >= 3600:
    heap_dir = str(pathlib.Path('reports') / f'soak-heaps-{int(time.time())}')
if a.seconds >= 86400:
    if a.max_rss_mib is None:
        a.max_rss_mib = 800
    if a.max_heap_growth_mib is None:
        a.max_heap_growth_mib = 200
    if a.max_rss_growth_mib is None:
        a.max_rss_growth_mib = 300


def resolve_soak_config(root: pathlib.Path, seed_data: pathlib.Path | None, seed_config: str | None) -> pathlib.Path:
    """Prefer a real config next to seeded data; fall back to a minimal soak config."""
    cfg = root / 'config.yaml'
    candidates: list[pathlib.Path] = []
    if seed_config:
        candidates.append(pathlib.Path(seed_config).resolve())
    if seed_data is not None:
        parent = seed_data.parent
        candidates.extend([
            seed_data / 'monitor.yaml',
            seed_data / 'config.yaml',
            parent / 'monitor.yaml',
            parent / 'config.yaml',
        ])
    for src in candidates:
        if src.is_file():
            shutil.copy2(src, cfg)
            return cfg
    cfg.write_text(
        'collectors:\n  enabled: [cpu, mem, load, disk, net]\n'
        'plugins:\n  enabled: false\nhealth:\n  enabled: false\n'
    )
    return cfg


with tempfile.TemporaryDirectory(prefix='monitor-soak-') as tmp:
    root = pathlib.Path(tmp)
    data = root / 'data'
    seed_src = None
    if a.seed_data_dir:
        seed_src = pathlib.Path(a.seed_data_dir).resolve()
        if not seed_src.is_dir():
            raise SystemExit(f'seed data dir not found: {seed_src}')
        shutil.copytree(
            seed_src, data, symlinks=False, ignore_dangling_symlinks=True,
            ignore=shutil.ignore_patterns('monitord.pid', '.monitor.lock', '*.tmp'),
        )
        (data / 'monitord.pid').unlink(missing_ok=True)
        (data / '.monitor.lock').unlink(missing_ok=True)
    else:
        data.mkdir()
    cfg = resolve_soak_config(root, seed_src, a.seed_config)
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    base = f'http://127.0.0.1:{port}'
    log = (root / 'server.log').open('w')
    proc = subprocess.Popen(
        [str(pathlib.Path(a.bin).resolve()), '-config', str(cfg), '-data-dir', str(data), '-listen', f'127.0.0.1:{port}'],
        stdout=log, stderr=log,
    )
    latencies = []
    rss = []
    heap = []
    series = []
    errors = []
    alerts = []
    heap_files = []
    charts = 0
    try:
        for _ in range(200):
            try:
                urllib.request.urlopen(base + '/healthz', timeout=1).close()
                break
            except OSError:
                time.sleep(.1)
        # Prefer the live agent password (seed copies may be stale placeholders).
        token_path = data / 'web-password'
        for _ in range(100):
            if token_path.is_file() and token_path.stat().st_size > 0:
                break
            time.sleep(0.1)
        token = token_path.read_text().strip()
        if not token:
            raise RuntimeError('missing web-password after startup')

        def authed(path):
            return urllib.request.Request(base + path, headers={'Authorization': 'Bearer ' + token})

        def capture_heap(label):
            if not heap_dir:
                return
            dest_dir = pathlib.Path(heap_dir)
            dest_dir.mkdir(parents=True, exist_ok=True)
            dest = dest_dir / f'heap-{label}.pprof'
            try:
                with urllib.request.urlopen(authed('/api/v1/debug/pprof/heap'), timeout=60) as r:
                    blob = r.read()
                if len(blob) < 32:
                    errors.append(f'heap {label}: too small')
                    return
                dest.write_bytes(blob)
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

    rss_growth = growth(rss)
    heap_growth = growth(heap)
    if a.max_rss_mib is not None and rss and max(rss) > a.max_rss_mib:
        alerts.append(f'rss_mib_max {max(rss):.1f} > {a.max_rss_mib}')
    if a.max_rss_growth_mib is not None and rss_growth is not None and rss_growth > a.max_rss_growth_mib:
        alerts.append(f'rss_mib_growth {rss_growth:.1f} > {a.max_rss_growth_mib}')
    if a.max_heap_growth_mib is not None and heap_growth is not None and heap_growth > a.max_heap_growth_mib:
        alerts.append(f'heap_mib_growth {heap_growth:.1f} > {a.max_heap_growth_mib}')

    report = {
        'duration_seconds': a.seconds,
        'seed_data_dir': str(pathlib.Path(a.seed_data_dir).resolve()) if a.seed_data_dir else None,
        'config': str(cfg),
        'requests': len(latencies),
        'charts': charts,
        'query_ms_median': statistics.median(latencies) if latencies else None,
        'query_ms_max': max(latencies, default=0),
        'rss_mib_first': rss[0] if rss else None,
        'rss_mib_last': rss[-1] if rss else None,
        'rss_mib_max': max(rss, default=0),
        'rss_mib_growth': rss_growth,
        'heap_mib_first': heap[0] if heap else None,
        'heap_mib_last': heap[-1] if heap else None,
        'heap_mib_max': max(heap, default=0),
        'heap_mib_growth': heap_growth,
        'tsdb_series_first': series[0] if series else None,
        'tsdb_series_last': series[-1] if series else None,
        'tsdb_series_max': max(series, default=0),
        'disk_bytes': sum(f.stat().st_size for f in data.rglob('*') if f.is_file()),
        'heap_profiles': heap_files,
        'alerts': alerts,
        'errors': sorted(set(errors)),
        'scope': 'local agent; prefer --seed-data-dir with a production-like copy; use --seconds 86400 overnight',
        'thresholds': {
            'max_rss_mib': a.max_rss_mib,
            'max_rss_growth_mib': a.max_rss_growth_mib,
            'max_heap_growth_mib': a.max_heap_growth_mib,
        },
    }
    text = json.dumps(report, indent=2)
    print(text)
    if a.output:
        pathlib.Path(a.output).write_text(text + '\n')
    if errors or alerts:
        raise SystemExit(1)
