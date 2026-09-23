import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { once } from 'node:events';

// Local-only notification sink: exercise transport success and rejection.
const sink = createServer((req, res) => {
  req.resume();
  res.writeHead(req.url.startsWith('/failed') ? 503 : 204);
  res.end(req.url.startsWith('/failed') ? 'private-test-response' : undefined);
});
sink.listen(0, '127.0.0.1');
await once(sink, 'listening');
const sinkURL = `http://127.0.0.1:${sink.address().port}`;
const root = mkdtempSync(join(tmpdir(), 'monitor-browser-'));
const config = join(root, 'config.yaml');
const port = process.env.MONITOR_E2E_PORT || '19997';
// Isolated history fixtures exercise pagination beyond the overview's 100 rows.
// They are operator records, not fabricated live host samples or active alarms.
const historyDir = join(root, 'data', 'operations');
mkdirSync(historyDir, { recursive: true });
const records = {};
for (let i = 0; i < 130; i++) {
  const suffix = String(i).padStart(3, '0');
  const id = createHash('sha256').update(`history-fixture-${suffix}`).digest('hex');
  const at = Math.floor(Date.now() / 1000) - 500 - i;
  const history = Array.from({ length: i === 129 ? 20 : 1 }, (_, j) => ({
    at: at - (i === 129 ? 19 - j : 0), actor: 'fixture-oncall', action: 'comment',
    note: i === 129 && j === 19 ? '=SUM(1,2)\nhandover-129' : `fixture handover ${suffix}`,
  }));
  records[id] = { id, problem: { node: 'history-fixture-node', hostname: 'history-fixture-host', chart: 'system.ram', name: `history_case_${suffix}`, severity: 'WARNING', since: at - 60 },
    acknowledged: true, assignee: 'fixture-owner', status: 'watching', revision: i === 129 ? 25 : 1, history };
}
writeFileSync(join(historyDir, 'acknowledgements.json'), JSON.stringify({ version: 2, records }), { mode: 0o600 });
writeFileSync(config, `global:\n  hostname: browser-test\nweb:\n  token: browser-test-token\n  users:\n    - name: browser-reader\n      token: browser-viewer-token\n      role: viewer\n    - name: browser-oncall\n      token: browser-operator-token\n      role: troubleshooter\ncollectors:\n  enabled: [cpu, mem, load]\nplugins:\n  enabled: false\nhealth:\n  enabled: true\n  builtin: false\n  silent: false\n  notify:\n    webhook:\n      url: ${sinkURL}/accepted?token=private-test-token\n    slack:\n      webhook_url: ${sinkURL}/failed?token=private-test-token\n  alarms:\n    - name: browser_ram_notice\n      on: system.ram\n      calc: '$used'\n      every: 1s\n      warn: '$this > 0'\n    - name: browser_ram_secondary\n      on: system.ram\n      calc: '$used'\n      every: 1s\n      warn: '$this > 0'\ndb:\n  checkpoint: 1s\n`);
const child = spawn(resolve('../core/bin/monitord'), ['-config', config, '-data-dir', join(root, 'data'), '-listen', `127.0.0.1:${port}`], { stdio: 'inherit' });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill('SIGINT'));
child.on('exit', code => { sink.close(); rmSync(root, {recursive:true,force:true}); process.exit(code ?? 1); });
