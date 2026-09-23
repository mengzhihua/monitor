import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawn } from 'node:child_process';

const root = mkdtempSync(join(tmpdir(), 'monitor-browser-'));
const config = join(root, 'config.yaml');
const port = process.env.MONITOR_E2E_PORT || '19997';
writeFileSync(config, `global:\n  hostname: browser-test\nweb:\n  token: browser-test-token\n  users:\n    - name: browser-reader\n      token: browser-viewer-token\n      role: viewer\n    - name: browser-oncall\n      token: browser-operator-token\n      role: troubleshooter\ncollectors:\n  enabled: [cpu, mem, load]\nplugins:\n  enabled: false\nhealth:\n  enabled: true\n  builtin: false\n  silent: true\n  alarms:\n    - name: browser_ram_notice\n      on: system.ram\n      calc: '$used'\n      every: 1s\n      warn: '$this > 0'\ndb:\n  checkpoint: 1s\n`);
const child = spawn(resolve('../core/bin/monitord'), ['-config', config, '-data-dir', join(root, 'data'), '-listen', `127.0.0.1:${port}`], { stdio: 'inherit' });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill('SIGINT'));
child.on('exit', code => { rmSync(root, {recursive:true,force:true}); process.exit(code ?? 1); });
