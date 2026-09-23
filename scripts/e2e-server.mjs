import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawn } from 'node:child_process';

const root = mkdtempSync(join(tmpdir(), 'monitor-browser-'));
const config = join(root, 'config.yaml');
writeFileSync(config, `global:\n  hostname: browser-test\nweb:\n  token: browser-test-token\ncollectors:\n  enabled: [cpu, mem, load]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\ndb:\n  checkpoint: 1s\n`);
const child = spawn(resolve('../core/bin/monitord'), ['-config', config, '-data-dir', join(root, 'data'), '-listen', '127.0.0.1:19997'], { stdio: 'inherit' });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill('SIGINT'));
child.on('exit', code => { rmSync(root, {recursive:true,force:true}); process.exit(code ?? 1); });
