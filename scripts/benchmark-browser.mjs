#!/usr/bin/env node
// Build Web assets and core/bin/monitord first. This measures retained browser
// resources with synthetic chart data, not production rendering throughput.
import fs from 'node:fs';
import { spawn } from 'node:child_process';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { randomBytes } from 'node:crypto';
import { setTimeout as sleep } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const { values } = parseArgs({ options: {
  binary: { type: 'string', default: path.join(root, 'core/bin/monitord') },
  label: { type: 'string', default: 'local' },
  output: { type: 'string' },
  channel: { type: 'string', default: process.env.MONITOR_BROWSER_CHANNEL || 'chrome' },
  timeout: { type: 'string', default: '45' },
  help: { type: 'boolean', short: 'h' },
} });

if (values.help) {
  console.log(`Usage: node scripts/benchmark-browser.mjs [options]
  --binary PATH     monitord binary (default: core/bin/monitord)
  --label NAME      label written to the result (default: local)
  --output PATH     JSON result (default: new directory in system temp directory)
  --channel NAME    Playwright browser channel (default: chrome; chromium uses bundled browser)
  --timeout SECONDS startup and page timeout, greater than zero (default: 45)

Uses 100 synthetic charts, visits every fourth card at 1280x900, then returns
to the top. Reports canvas count, DOM nodes, event listeners and JS heap after
forced GC. Creates a temporary authenticated local daemon and stops it afterward.
Requires npm ci in web/ and an installed Chrome or Playwright Chromium browser.`);
  process.exit(0);
}

const timeout = Number(values.timeout) * 1000;
if (!Number.isFinite(timeout) || timeout <= 0) {
  throw new Error('--timeout must be a finite number greater than zero');
}
const binary = path.resolve(values.binary);
fs.accessSync(binary, fs.constants.X_OK);
let chromium, expect;
try {
  ({ chromium, expect } = await import('../web/node_modules/@playwright/test/index.mjs'));
} catch {
  throw new Error('Playwright is unavailable; run npm ci in web/ first');
}

const output = path.resolve(values.output || path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'monitor-browser-result-')), 'result.json'));
fs.mkdirSync(path.dirname(output), { recursive: true });
const probe = net.createServer();
await new Promise((resolve, reject) => {
  probe.once('error', reject);
  probe.listen(0, '127.0.0.1', resolve);
});
const port = probe.address().port;
await new Promise(resolve => probe.close(resolve));
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'monitor-browser-'));
const token = randomBytes(32).toString('hex');
const config = path.join(temporary, 'config.yaml');
const logPath = path.join(temporary, 'server.log');
let log;
try {
  fs.writeFileSync(config, `web:\n  token: ${token}\ncollectors:\n  enabled: [cpu, mem, load]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n`, { mode: 0o600 });
  log = fs.openSync(logPath, 'w');
} catch (error) {
  fs.rmSync(temporary, { recursive: true, force: true });
  throw error;
}
const child = spawn(binary, ['-config', config, '-listen', `127.0.0.1:${port}`, '-data-dir', path.join(temporary, 'data')], {
  cwd: temporary, stdio: ['ignore', log, log],
});
let spawnError;
child.on('error', error => { spawnError = error; });
const exited = new Promise(resolve => {
  child.on('exit', (code, signal) => resolve({ code, signal }));
  child.on('error', () => resolve({ spawn_error: true }));
});
let browser;
let interrupted = false;
const interrupt = () => {
  interrupted = true;
  child.kill('SIGINT');
  if (browser) void browser.close();
};
process.on('SIGINT', interrupt);
process.on('SIGTERM', interrupt);
const result = {
  label: values.label, binary,
  host: { os: os.platform(), arch: os.arch() },
  method: {
    charts: 100, stride: 4, viewport: { width: 1280, height: 900 },
    browser_channel: values.channel, mock_history_rows: 3,
    heap: 'CDP HeapProfiler.collectGarbage then Performance.getMetrics JSHeapUsedSize',
    canvas: 'all .card canvas elements after complete forward traversal',
    backend_collectors: ['cpu', 'mem', 'load'],
  },
};
const redact = text => String(text).replaceAll(token, '[redacted]');

try {
  const deadline = performance.now() + timeout;
  while (true) {
    if (interrupted) throw new Error('interrupted');
    if (spawnError) throw spawnError;
    if (child.exitCode !== null) throw new Error('daemon exited before becoming ready');
    if (performance.now() >= deadline) throw new Error('daemon startup timed out');
    try {
      const response = await fetch(`http://127.0.0.1:${port}/api/v1/info`, {
        headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(3000),
      });
      await response.arrayBuffer();
      if (response.ok) break;
    } catch { /* Retry only until the startup deadline. */ }
    await sleep(100);
  }
  browser = await chromium.launch({ headless: true, channel: values.channel === 'chromium' ? undefined : values.channel });
  result.browser_version = browser.version();
  if (interrupted) throw new Error('interrupted');
  const page = await browser.newPage({ viewport: result.method.viewport });
  page.setDefaultTimeout(timeout);
  page.setDefaultNavigationTimeout(timeout);
  const errors = [];
  page.on('pageerror', error => errors.push(redact(error.message)));
  const charts = Object.fromEntries(Array.from({ length: 100 }, (_, index) => {
    const id = `test.chart${index}`;
    return [id, {
      id, title: id, context: 'test', family: 'test', units: 'value', chart_type: 'line',
      priority: index, update_every: 1, dimensions: [{ id: 'value', name: 'value' }],
      last_entry: Math.floor(Date.now() / 1000),
    }];
  }));
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts } }));
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: {
    dimension_ids: ['value'], result: { data: [[1, 1], [2, 2], [3, 1]] },
  } }));
  // Use an isolated browser session; keep the temporary credential out of URLs.
  await page.addInitScript(value => sessionStorage.setItem('monitor.token', value), token);
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Performance.enable');
  const snapshot = async () => {
    await cdp.send('HeapProfiler.collectGarbage');
    const metrics = Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map(item => [item.name, item.value]));
    const dom = await cdp.send('Memory.getDOMCounters');
    return {
      canvas_count: await page.locator('.card canvas').count(),
      JSHeapUsedSize: metrics.JSHeapUsedSize, JSHeapTotalSize: metrics.JSHeapTotalSize,
      DOMNodes: dom.nodes, JSEventListeners: dom.jsEventListeners, Documents: dom.documents,
    };
  };
  await page.goto(`http://127.0.0.1:${port}/`);
  const cards = page.locator('.card');
  await expect(cards).toHaveCount(100, { timeout });
  await expect(cards.first().locator('canvas')).toBeVisible({ timeout });
  result.initial = await snapshot();
  const begin = performance.now();
  for (let index = 0; index < 100; index += 4) {
    await cards.nth(index).scrollIntoViewIfNeeded();
    await expect(cards.nth(index).locator('canvas')).toBeVisible({ timeout });
  }
  await sleep(500);
  result.forward_scroll_ms = performance.now() - begin;
  result.after_forward = await snapshot();
  await cards.first().scrollIntoViewIfNeeded();
  await expect(cards.first().locator('canvas')).toBeVisible({ timeout });
  await sleep(500);
  result.after_return = await snapshot();
  result.page_errors = errors;
  if (errors.length) process.exitCode = 1;
} catch (error) {
  result.error = redact(error.message);
  process.exitCode = interrupted ? 130 : 1;
} finally {
  if (browser) await browser.close().catch(() => {});
  if (child.exitCode === null && child.signalCode === null) child.kill('SIGINT');
  let timer;
  result.server_exit = await Promise.race([
    exited,
    new Promise(resolve => {
      timer = setTimeout(() => { child.kill('SIGKILL'); resolve({ forced_kill: true }); }, 15000);
    }),
  ]);
  clearTimeout(timer);
  await exited;
  if (result.server_exit.code !== 0) process.exitCode ||= 1;
  process.off('SIGINT', interrupt);
  process.off('SIGTERM', interrupt);
  fs.closeSync(log);
  try {
    fs.copyFileSync(logPath, output + '.server.log');
  } finally {
    // Remove only this run's temporary data and credential file.
    fs.rmSync(temporary, { recursive: true, force: true });
  }
  fs.writeFileSync(output, JSON.stringify(result, null, 2) + '\n');
}
console.log(JSON.stringify(result, null, 2));
console.log('Full result:', output);
