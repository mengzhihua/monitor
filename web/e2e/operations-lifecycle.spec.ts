import { expect, test, type Page, type Route } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import type { OperationsSnapshot } from '../src/api'

function snapshot(now = 100, hostname = 'baseline'): OperationsSnapshot {
  return {
    now, current_user: { name: 'admin', role: 'admin' }, assignees: [], problems: [], persistent: true,
    summary: { nodes: 1, live: 1, offline: 0, stale: 0, critical: 0, warning: 0, unacknowledged: 0 },
    nodes: [{ id: 'local', hostname, os: 'linux', arch: 'amd64', labels: {}, update_every: 1, version: 'test',
      status: 'live', local: true, first_seen: 1, last_seen: now, charts_count: 0, alarms: { warning: 0, critical: 0 },
      cpu: { value: 1, at: now, state: 'fresh' }, memory: { value: 2, at: now, state: 'fresh' }, disks: [], alarm_coverage: 'local' }],
    activity: [{ id: hostname, acknowledged: false, revision: 1, assignee: '', status: 'open', history: [],
      problem: { node: 'local', hostname, chart: 'test.chart', name: 'test', severity: 'WARNING', since: now } }],
    page: { limit: 50, nodes_matched: 1, problems_matched: 0, families: [] },
  }
}

async function fixture(page: Page, overview: (route: Route, url: URL) => Promise<void>) {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/operations') return overview(route, url)
    const replies: Record<string, unknown> = {
      '/api/v1/info': { version: 'test', mode: 'agent', uptime: 1, charts_count: 0, metrics_count: 0,
        host: { id: 'local', hostname: 'baseline', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 },
        collectors: [], alarms: null, user: { name: 'admin', role: 'admin' } },
      '/api/v1/charts': { charts: {} }, '/api/v1/functions': [], '/api/v1/auth/oidc/status': { enabled: false },
      '/api/v1/operations/views': { revision: 1, views: [], enabled: true, persistent: false, limit: 10, user: { name: 'admin', role: 'admin' } },
      '/api/v1/operations/history': { records: [], total: 0, stored: 0, capacity: 100, actions_per_record: 20, snapshot: 'fixture', next_cursor: '', filter: {} },
      '/api/v1/operations/notifications': { available: false },
      '/api/v1/operations/maintenance': { available: false, plans: [], targets: [], revision: 1 },
    }
    if (url.pathname in replies) return route.fulfill({ json: replies[url.pathname] })
    return route.continue()
  })
  await page.goto('/?token=browser-test-token')
  await expect(page.getByRole('button', { name: '导出当前快照', exact: true })).toBeEnabled()
  return errors
}

function gate() {
  let release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  return { wait, release }
}

const isExport = (url: string) => {
  const parsed = new URL(url)
  return parsed.pathname === '/api/v1/operations' && parsed.searchParams.get('limit') === '200'
}

test('snapshot export uses its own response time and records while overview refreshes', async ({ page }) => {
  const pending = gate(), completed = gate()
  let exports = 0, overviewReads = 0
  const errors = await fixture(page, async (route, url) => {
    if (url.searchParams.get('limit') === '200') {
      exports++
      await pending.wait
      await route.fulfill({ json: snapshot(150, 'export-source') })
      completed.release()
    } else await route.fulfill({ json: snapshot(++overviewReads * 100, overviewReads === 1 ? 'baseline' : 'new-overview') })
  })
  const downloaded = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出当前快照', exact: true }).click()
  await expect.poll(() => exports).toBe(1)
  await expect(page.getByRole('button', { name: '正在导出…', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '刷新总览', exact: true }).click()
  await expect(page.locator('.node-tile h3')).toHaveText('new-overview')
  pending.release()
  await completed.wait
  const download = await downloaded
  const data = JSON.parse(await readFile((await download.path())!, 'utf8'))
  expect(download.suggestedFilename()).toBe('monitor-operations-150.json')
  expect(data.captured_at).toBe(150)
  expect(data.nodes[0].hostname).toBe('export-source')
  expect(data.activity[0].problem.hostname).toBe('export-source')
  expect(data.filters.query).toBe('')
  expect(data.stale).toBe(false)
  expect(exports).toBe(1)
  expect(errors).toEqual([])
})

for (const change of ['filter', 'workspace', 'authentication']) {
  test(`pending snapshot export is canceled on ${change} change without downloading`, async ({ page }) => {
    const pending = gate(), completed = gate()
    let exportStarted = false, rejectOverview = false, downloads = 0
    page.on('download', () => downloads++)
    const errors = await fixture(page, async (route, url) => {
      if (url.searchParams.get('limit') === '200') {
        exportStarted = true
        await pending.wait
        await route.fulfill({ json: snapshot(150, 'old-export') }).catch(() => {})
        completed.release()
      } else if (rejectOverview) await route.fulfill({ status: 401, body: 'expired' })
      else await route.fulfill({ json: snapshot(200, url.searchParams.get('q') || 'baseline') })
    })
    await page.getByRole('button', { name: '导出当前快照', exact: true }).click()
    await expect.poll(() => exportStarted).toBe(true)
    const aborted = page.waitForEvent('requestfailed', request => isExport(request.url()))
    if (change === 'filter') {
      await page.getByLabel('搜索主机或问题').fill('new-filter')
      await expect(page.locator('.node-tile h3')).toHaveText('new-filter')
    } else if (change === 'workspace') {
      await page.getByRole('button', { name: '指标图表', exact: true }).click()
      await expect(page.locator('.operations')).toHaveCount(0)
    } else {
      rejectOverview = true
      await page.getByRole('button', { name: '刷新总览', exact: true }).click()
      await expect(page.getByText('登录已失效，请重新登录。', { exact: true })).toBeVisible()
    }
    await aborted
    pending.release()
    await completed.wait
    expect(downloads).toBe(0)
    expect(errors).toEqual([])
  })
}

for (const status of [401, 503]) {
  test(`failed snapshot export ${status} does not silently download stale data`, async ({ page }) => {
    let downloads = 0
    page.on('download', () => downloads++)
    const errors = await fixture(page, async (route, url) => {
      if (url.searchParams.get('limit') === '200') await route.fulfill({ status, body: 'export failed' })
      else await route.fulfill({ json: snapshot() })
    })
    await page.getByRole('button', { name: '导出当前快照', exact: true }).click()
    await expect(page.getByRole('alert').filter({ hasText: '本次未生成文件' })).toBeVisible()
    expect(downloads).toBe(0)
    expect(errors).toEqual([])
    if (status === 401) await expect(page.locator('.node-tile')).toHaveCount(0)
    else await expect(page.getByRole('button', { name: '导出当前快照', exact: true })).toBeEnabled()
  })
}

test('typing coalesces overview requests and aborts the older request immediately', async ({ page }) => {
  const pending = gate(), completed = gate()
  const queries: string[] = []
  const errors = await fixture(page, async (route, url) => {
    const query = url.searchParams.get('q') || ''
    queries.push(query)
    if (queries.length === 2) {
      await pending.wait
      await route.fulfill({ json: snapshot(50, 'browser-old-response') }).catch(() => {})
      completed.release()
    } else await route.fulfill({ json: snapshot(100, query || 'baseline') })
  })
  await page.getByRole('button', { name: '刷新总览', exact: true }).click()
  await expect.poll(() => queries.length).toBe(2)
  const aborted = page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === '/api/v1/operations')
  // Separate microtasks flush Vue's watcher for each keystroke without relying
  // on browser automation speed or real-time sleeps for the debounce assertion.
  await page.getByLabel('搜索主机或问题').evaluate(async input => {
    for (const value of ['b', 'br', 'bro', 'brow', 'browse', 'browser']) {
      const field = input as HTMLInputElement
      field.value = value
      field.dispatchEvent(new Event('input', { bubbles: true }))
      await Promise.resolve()
    }
  })
  await aborted
  await expect(page.locator('.node-tile h3')).toHaveText('browser')
  expect(queries).toEqual(['', '', 'browser'])
  pending.release()
  await completed.wait
  await expect(page.locator('.node-tile h3')).toHaveText('browser')
  expect(errors).toEqual([])
})
