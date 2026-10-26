#!/bin/bash
# Seed a throwaway data dir and run soak.py. Profile is "hosted" (5.5h) or "lab" (24h).
# Runs of 30 minutes to 4 hours capture a heap profile every 30 minutes. Longer runs
# capture one every 2 hours, then diff the first profile against the last. Runs of at
# least 2 hours also fail when memory growth exceeds the hosted caps, unless the flags
# below set a different limit.
set -euo pipefail
profile=${1:-hosted}
mkdir -p reports/seed-data
cat > reports/seed-config.yaml <<'YAML'
collectors:
  enabled: [cpu, mem, load, disk, net, ml]
plugins:
  enabled: false
health:
  enabled: false
YAML
python3 - <<'PY'
import pathlib, shutil, socket, subprocess, tempfile, time, urllib.request
root = pathlib.Path('reports')
cfg = root / 'seed-config.yaml'
seed = root / 'seed-data'
with tempfile.TemporaryDirectory(prefix='seed-warm-') as tmp:
    data = pathlib.Path(tmp) / 'data'
    data.mkdir()
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    log = open(pathlib.Path(tmp) / 'warm.log', 'w')
    proc = subprocess.Popen(
        ['core/bin/monitord', '-config', str(cfg), '-data-dir', str(data),
         '-listen', f'127.0.0.1:{port}'],
        stdout=log, stderr=log,
    )
    try:
        for _ in range(100):
            try:
                urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz', timeout=1).close()
                break
            except OSError:
                time.sleep(0.1)
        time.sleep(20)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=30)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        log.close()
    if seed.exists():
        shutil.rmtree(seed)
    shutil.copytree(data, seed, ignore=shutil.ignore_patterns('monitord.pid', '.monitor.lock'))
print('seed ready', seed)
PY
if [ "$profile" = "lab" ]; then
  seconds=86400
  rss=800
  heap_growth=200
  rss_growth=300
else
  seconds=19800
  rss=450
  heap_growth=80
  rss_growth=120
fi
rss_log="reports/rss-trend-$(TZ=Asia/Shanghai date +%m%d).log"
python3 scripts/soak.py \
  --seconds "$seconds" \
  --sample-every 30 \
  --rss-log "$rss_log" \
  --bin core/bin/monitord \
  --seed-data-dir reports/seed-data \
  --seed-config reports/seed-config.yaml \
  --heap-dir reports/soak-heaps \
  --max-rss-mib "$rss" \
  --max-heap-growth-mib "$heap_growth" \
  --max-rss-growth-mib "$rss_growth" \
  --max-anomaly-buffer-mib 16 \
  --output reports/soak-nightly.json
