import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, connect } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const helperURL = new URL('./e2e-process.mjs', import.meta.url).href;
const scriptPath = fileURLToPath(new URL('./e2e-server.mjs', import.meta.url));

async function waitUntil(predicate) {
  const deadline = Date.now() + 6000;
  while (!predicate()) {
    assert.ok(Date.now() < deadline, 'fixture did not settle before the deadline');
    await delay(20);
  }
}

function running(pid) {
  try { process.kill(pid, 0); return true; }
  catch (error) { if (error.code === 'ESRCH') return false; throw error; }
}

function temporary(t) {
  const root = mkdtempSync(join(tmpdir(), 'monitor-e2e-lifecycle-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  return root;
}

function start(t, script, options = {}) {
  const child = spawn(process.execPath, [script], { stdio: ['ignore', 'pipe', 'pipe'], ...options });
  const output = { stdout: '', stderr: '' };
  child.stdout.on('data', chunk => { output.stdout += chunk; });
  child.stderr.on('data', chunk => { output.stderr += chunk; });
  const done = once(child, 'exit');
  t.after(() => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); });
  return { child, output, done };
}

async function assertPortReleased(port) {
  const server = createServer();
  try { server.listen(port, '127.0.0.1'); await once(server, 'listening'); }
  finally { await new Promise(resolve => server.close(resolve)); }
}

function makeHarness(t, mode) {
  const root = temporary(t);
  const data = join(root, 'data');
  mkdirSync(data);
  const ready = join(root, 'ready.json');
  const pidfile = join(root, 'child.pid');
  const harness = join(root, 'harness.mjs');
  const childScript = join(root, 'child.mjs');
  writeFileSync(childScript, `import { writeFileSync } from 'node:fs';
process.on('SIGINT', () => {}); process.on('SIGTERM', () => {});
writeFileSync(${JSON.stringify(pidfile)}, String(process.pid));
${mode === 'normal' ? 'process.exit(7);' : 'setInterval(() => {}, 1000);'}
`);
  writeFileSync(harness, `import { createServer } from 'node:http';
import { once, EventEmitter } from 'node:events';
import { writeFileSync } from 'node:fs';
import { runFixtureProcess } from ${JSON.stringify(helperURL)};
const sink = createServer((req, res) => { req.resume(); res.end(); });
process.exitCode = await runFixtureProcess({
  root: ${JSON.stringify(data)}, sink, command: process.execPath, args: [${JSON.stringify(childScript)}],
  graceMs: 120, killMs: 1000, parentCheckMs: 20,
  prepare: async signal => {
    sink.listen(0, '127.0.0.1'); await once(sink, 'listening', { signal });
    writeFileSync(${JSON.stringify(ready)}, JSON.stringify({ port: sink.address().port }));
    ${mode === 'prepare-error' ? "throw new Error('fixture preparation failed');" : ''}
    ${mode === 'prepare-signal' ? "await once(new EventEmitter(), 'never', { signal });" : ''}
  },
});
`);
  t.after(() => {
    if (existsSync(pidfile)) {
      const pid = Number(readFileSync(pidfile, 'utf8'));
      try { if (running(pid)) process.kill(process.platform === 'win32' ? pid : -pid, 'SIGKILL'); }
      catch (error) { if (error.code !== 'ESRCH') throw error; }
    }
  });
  return { root, data, ready, pidfile, harness };
}

test('real e2e entry point cleans its directory when the binary is missing', { timeout: 8000 }, async t => {
  const root = temporary(t);
  const cwd = join(root, 'web'); mkdirSync(cwd);
  const fixture = start(t, scriptPath, { cwd, env: { ...process.env, TMPDIR: root, TMP: root, TEMP: root } });
  assert.deepEqual(await fixture.done, [1, null]);
  assert.match(fixture.output.stderr, /Browser fixture failed \(ENOENT\)/);
  assert.doesNotMatch(fixture.output.stderr, /Unhandled 'error' event/);
  assert.deepEqual(readdirSync(root), ['web']);
});

test('normal child exit keeps its exit code and releases the sink and directory', { timeout: 8000 }, async t => {
  const fixture = makeHarness(t, 'normal');
  const runner = start(t, fixture.harness);
  assert.deepEqual(await runner.done, [7, null]);
  assert.equal(existsSync(fixture.data), false);
  await assertPortReleased(JSON.parse(readFileSync(fixture.ready)).port);
});

test('preparation failure also closes an already listening sink', { timeout: 8000 }, async t => {
  const fixture = makeHarness(t, 'prepare-error');
  assert.deepEqual(await start(t, fixture.harness).done, [1, null]);
  assert.equal(existsSync(fixture.pidfile), false);
  assert.equal(existsSync(fixture.data), false);
  await assertPortReleased(JSON.parse(readFileSync(fixture.ready)).port);
});

test('SIGTERM during preparation cancels setup and cleans resources', { timeout: 8000, skip: process.platform === 'win32' }, async t => {
  const fixture = makeHarness(t, 'prepare-signal');
  const runner = start(t, fixture.harness);
  await waitUntil(() => existsSync(fixture.ready));
  runner.child.kill('SIGTERM');
  assert.deepEqual(await runner.done, [143, null]);
  assert.equal(existsSync(fixture.pidfile), false);
  assert.equal(existsSync(fixture.data), false);
  await assertPortReleased(JSON.parse(readFileSync(fixture.ready)).port);
});

test('signals bound an uncooperative child and close active sink sockets', { timeout: 8000, skip: process.platform === 'win32' }, async t => {
  const fixture = makeHarness(t, 'ignore');
  const runner = start(t, fixture.harness);
  await waitUntil(() => existsSync(fixture.pidfile));
  const pid = Number(readFileSync(fixture.pidfile));
  const port = JSON.parse(readFileSync(fixture.ready)).port;
  const socket = connect(port, '127.0.0.1');
  socket.resume();
  socket.on('error', () => {});
  const closed = once(socket, 'close');
  t.after(() => socket.destroy());
  await once(socket, 'connect');
  socket.write('POST / HTTP/1.1\r\nHost: fixture\r\nContent-Length: 1000\r\n\r\nx');
  const unrelated = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
  t.after(() => unrelated.kill('SIGKILL'));
  runner.child.kill('SIGTERM');
  runner.child.kill('SIGINT'); // repeated shutdown remains one owned cleanup
  const [code, signal] = await runner.done;
  assert.equal(signal, null);
  assert.ok(code === 130 || code === 143);
  await closed;
  assert.equal(running(pid), false);
  assert.equal(running(unrelated.pid), true);
  assert.equal(existsSync(fixture.data), false);
  await assertPortReleased(port);
});

test('the fixture stops when its parent runner exits without sending a signal', { timeout: 8000, skip: process.platform === 'win32' }, async t => {
  const fixture = makeHarness(t, 'ignore');
  const parent = join(fixture.root, 'parent.mjs');
  writeFileSync(parent, `import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
const child = spawn(process.execPath, [${JSON.stringify(fixture.harness)}], { stdio: 'ignore' });
child.unref();
const timer = setInterval(() => { if (existsSync(${JSON.stringify(fixture.pidfile)})) { clearInterval(timer); process.exit(0); } }, 20);
`);
  assert.deepEqual(await start(t, parent).done, [0, null]);
  const pid = Number(readFileSync(fixture.pidfile));
  await waitUntil(() => !existsSync(fixture.data));
  assert.equal(running(pid), false);
  await assertPortReleased(JSON.parse(readFileSync(fixture.ready)).port);
});
