#!/usr/bin/env python3
"""Local HTTP Hub ring ingestion staircase; synthetic nodes, not a fleet claim."""
import argparse
import concurrent.futures
import json
import pathlib
import signal
import socket
import statistics
import subprocess
import tempfile
import time
import urllib.request

p = argparse.ArgumentParser(description=__doc__)
p.add_argument('--bin', default='core/bin/monitord')
p.add_argument('--nodes', default='10,100,1000')
p.add_argument('--dimensions', type=int, default=20)
p.add_argument('--samples', type=int, default=10)
p.add_argument('--output')
a = p.parse_args()
levels = sorted(set(map(int, a.nodes.split(','))))
if not levels or min(levels) < 1 or max(levels) > 1000 or not 1 <= a.dimensions <= 100 or not 2 <= a.samples <= 600:
    p.error('nodes 1..1000, dimensions 1..100, samples 2..600')
binary = str(pathlib.Path(a.bin).resolve())
with tempfile.TemporaryDirectory(prefix='monitor-hub-load-') as tmp:
    root = pathlib.Path(tmp)
    config = root/'config.yaml'
    config.write_text('mode: hub\nweb:\n  token: load-api\nhub:\n  peer_token: load-peer\ncollectors:\n  enabled: [none]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\ndb:\n  checkpoint: 1s\n')
    log = (root/'server.log').open('w')
    proc = None
    def start():
        global proc, base
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
        base = f'http://127.0.0.1:{port}'
        proc = subprocess.Popen([binary, '-config', str(config), '-data-dir', str(root/'data'), '-listen', f'127.0.0.1:{port}'], stdout=log, stderr=log)
        for _ in range(200):
            if proc.poll() is not None: raise RuntimeError('Hub exited; '+(root/'server.log').read_text()[-2000:])
            try:
                urllib.request.urlopen(base+'/healthz', timeout=1).close(); return
            except OSError: time.sleep(.1)
        raise RuntimeError('Hub startup timed out')
    def stop():
        global proc
        if proc and proc.poll() is None:
            proc.send_signal(signal.SIGINT)
            try: proc.wait(timeout=120)
            except subprocess.TimeoutExpired: proc.kill(); proc.wait(); raise
            if proc.returncode: raise RuntimeError(f'Hub exit {proc.returncode}')
        proc = None
    def request(path, data=None, token='load-api'):
        req = urllib.request.Request(base+path, data=None if data is None else json.dumps(data).encode(), headers={'Authorization':'Bearer '+token, 'Content-Type':'application/json'})
        with urllib.request.urlopen(req, timeout=60) as r: return json.load(r)
    stamp = int(time.time())-a.samples-1
    chart = {'id':'capacity.metric', 'context':'capacity.metric', 'family':'capacity', 'title':'Synthetic capacity', 'units':'value', 'update_every':1, 'dimensions':[{'id':f'd{i}'} for i in range(a.dimensions)]}
    def ingest(i):
        body = {'host':{'id':f'load-{i}', 'hostname':f'load-{i}', 'update_every':1}, 'charts':[chart], 'samples':[{'chart':chart['id'], 't':stamp+j, 'v':{f'd{k}':float(i+j+k) for k in range(a.dimensions)}} for j in range(a.samples)]}
        begin = time.perf_counter(); result = request('/api/v1/hub/ring', body, 'load-peer')
        if result.get('samples') != a.samples: raise RuntimeError('sample acceptance mismatch')
        return (time.perf_counter()-begin)*1000
    def query(i):
        return request(f'/api/v1/data?node=load-{i}&chart=capacity.metric&after={stamp-1}&before={stamp+a.samples-1}&points={a.samples}')['result']['data']
    try:
        start(); results=[]; previous=0
        for count in levels:
            begin=time.perf_counter()
            with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
                latencies=list(pool.map(ingest,range(previous,count)))
            elapsed=time.perf_counter()-begin
            nodes=request('/api/v1/nodes')['nodes']
            assert len([n for n in nodes if not n.get('local')])==count
            samples=(count-previous)*a.dimensions*a.samples
            results.append({'nodes':count,'added_samples':samples,'elapsed_seconds':elapsed,'ingest_samples_per_second':samples/elapsed,'request_ms_median':statistics.median(latencies),'request_ms_max':max(latencies)})
            previous=count
        checked=sorted(set([0, max(levels)//2, max(levels)-1]))
        before={i:query(i) for i in checked}
        for i,rows in before.items():
            values=[row[1] for row in rows if row[1] is not None]
            assert values==[float(i+j) for j in range(a.samples)], (i,values)
        checkpoint=request('/api/v1/info')['db']['persistence']
        assert not checkpoint.get('error'), checkpoint
        stop(); start()
        for i in checked: assert query(i)==before[i], 'history changed after restart'
        nodes=request('/api/v1/nodes')['nodes']
        assert len([n for n in nodes if not n.get('local')])==max(levels)
        assert all(n.get('replica') for n in nodes if not n.get('local')), 'replica identity lost'
        report={'scope':'local HTTP ring ingestion; synthetic nodes, one chart each; not 1000 connected agents','dimensions_per_node':a.dimensions,'samples_per_dimension':a.samples,'levels':results,'restart_history':'passed','replica_identity':'passed','checked_nodes':checked}
        text=json.dumps(report,indent=2);print(text)
        if a.output: pathlib.Path(a.output).write_text(text+'\n')
    finally:
        stop();log.close()
