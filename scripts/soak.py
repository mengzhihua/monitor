#!/usr/bin/env python3
"""Bounded local agent soak. Outputs measured observations, not capacity promises."""
import argparse, json, pathlib, signal, socket, statistics, subprocess, tempfile, time, urllib.request
p=argparse.ArgumentParser()
p.add_argument('--seconds',type=int,default=120)
p.add_argument('--bin',default='core/bin/monitord')
p.add_argument('--output')
a=p.parse_args()
if a.seconds<10: p.error('use at least 10 seconds')
with tempfile.TemporaryDirectory(prefix='monitor-soak-') as tmp:
 root=pathlib.Path(tmp);cfg=root/'config.yaml'
 cfg.write_text('collectors:\n  enabled: [cpu, mem, load, disk, net]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n')
 with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
 base=f'http://127.0.0.1:{port}'
 log=(root/'server.log').open('w')
 proc=subprocess.Popen([str(pathlib.Path(a.bin).resolve()),'-config',str(cfg),'-data-dir',str(root/'data'),'-listen',f'127.0.0.1:{port}'],stdout=log,stderr=log)
 latencies=[];rss=[];errors=[];charts=0
 try:
  for _ in range(200):
   try:urllib.request.urlopen(base+'/healthz',timeout=1).close();break
   except OSError:time.sleep(.1)
  token=(root/'data'/'web-password').read_text().strip()
  def authed(path):return urllib.request.Request(base+path,headers={'Authorization':'Bearer '+token})
  deadline=time.monotonic()+a.seconds
  while time.monotonic()<deadline:
   try:
    started=time.perf_counter()
    with urllib.request.urlopen(authed('/api/v1/data?chart=system.ram&after=-60&points=60'),timeout=10) as r:body=json.load(r)
    latencies.append((time.perf_counter()-started)*1000)
    with urllib.request.urlopen(authed('/api/v1/info'),timeout=10) as r:info=json.load(r)
    charts=info['charts_count']
    persistence=info.get('db',{}).get('persistence',{})
    if persistence.get('error'): errors.append('checkpoint: '+persistence['error'])
    last=persistence.get('last_checkpoint',0)
    if last and time.time()-last>max(120,2*persistence.get('interval_seconds',30)): errors.append('checkpoint overdue')
    errors.extend(c['name']+': '+c['error'] for c in info['collectors'] if c.get('error'))
    usage=subprocess.check_output(['ps','-o','rss=','-p',str(proc.pid)],text=True).strip()
    rss.append(int(usage)/1024)
    if proc.poll() is not None:raise RuntimeError('agent exited')
   except Exception as e:errors.append(str(e))
   time.sleep(min(2,max(0,deadline-time.monotonic())))
 finally:
  proc.send_signal(signal.SIGINT)
  try:proc.wait(timeout=120)
  except subprocess.TimeoutExpired:proc.kill();proc.wait();errors.append('shutdown timed out')
  log.close()
 report={'duration_seconds':a.seconds,'requests':len(latencies),'charts':charts,'query_ms_median':statistics.median(latencies) if latencies else None,'query_ms_max':max(latencies,default=0),'rss_mib_max':max(rss,default=0),'disk_bytes':sum(f.stat().st_size for f in (root/'data').rglob('*') if f.is_file()),'errors':sorted(set(errors)),'scope':'local agent; not a distributed Hub load test'}
 text=json.dumps(report,indent=2);print(text)
 if a.output:pathlib.Path(a.output).write_text(text+'\n')
 if errors:raise SystemExit(1)
