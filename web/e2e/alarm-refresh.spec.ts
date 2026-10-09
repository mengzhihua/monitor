import { expect, test, type Page, type Route, type WebSocketRoute } from '@playwright/test'

const entry = (id: number, name = `alarm_${id}`) => ({
  unique_id: id, alarm_id: id, when: id + 100, hostname: 'fixture', name, chart: 'test.cpu', context: 'test.cpu',
  family: 'cpu', status: 'WARNING', old_status: 'CLEAR', value: 90, old_value: 0,
  units: '%', info: name, recipient: '', delay: 0, notified: false,
})
const snapshot = (id = 1, name = `alarm_${id}`) => ({ hostname: 'fixture', now: 100,
  summary: { normal: 0, warning: 1, critical: 0, silent: 0 }, alarms: { [name]: {
    id, name, chart: 'test.cpu', context: 'test.cpu', family: 'cpu', units: '%', info: name,
    status: 'WARNING', value: 90, last_updated: 100, last_status_change: 100, active: true,
  } },
})
function gate() {
  let release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  return { wait, release }
}
async function visibility(page: Page, hidden: boolean) {
  await page.evaluate(value => {
    Object.defineProperty(document, 'hidden', { configurable: true, value })
    document.dispatchEvent(new Event('visibilitychange'))
  }, hidden)
}
async function fixture(page: Page, read: (route: Route, url: URL) => Promise<void>, hub = false) {
  let socket: WebSocketRoute | undefined
  await page.routeWebSocket('**/api/v1/live*', ws => { socket = ws })
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/alarms' || url.pathname === '/api/v1/alarm_log') return read(route, url)
    const replies: Record<string, unknown> = {
      '/api/v1/info': { version: 'test', mode: hub ? 'hub' : 'agent', uptime: 1, charts_count: 0, metrics_count: 0,
        host: { id: 'local', hostname: 'fixture', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 },
        collectors: [], alarms: { warning: 1, critical: 0, normal: 0, silent: 0 }, user: { name: 'reader', role: 'viewer' } },
      '/api/v1/charts': { charts: {} }, '/api/v1/functions': [], '/api/v1/auth/oidc/status': { enabled: false },
      '/api/v1/nodes': { nodes: [{ id: 'remote', hostname: 'Remote fixture', status: 'live', charts_count: 0, alarms: {} }] },
    }
    if (url.pathname in replies) return route.fulfill({ json: replies[url.pathname] })
    return route.fulfill({ json: {} })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('[title="live"]')).toBeVisible()
  return { send: (id: number, name?: string) => socket!.send(JSON.stringify({ alarm: entry(id, name) })) }
}

test('alarm history loads only for the open panel and hidden pages stop both reads', async ({ page }) => {
  await page.clock.install()
  let snapshots = 0, histories = 0
  await fixture(page, async (route, url) => {
    if (url.pathname === '/api/v1/alarms') { snapshots++; await route.fulfill({ json: snapshot() }) }
    else { histories++; await route.fulfill({ json: [entry(1, 'historic_warning')] }) }
  })
  await page.clock.fastForward(31000)
  await expect.poll(() => snapshots).toBe(2)
  expect(histories).toBe(0)
  await page.getByTitle('告警', { exact: true }).click()
  await expect(page.locator('.panel .log')).toContainText('historic_warning')
  expect(histories).toBe(1)
  await page.locator('.panel button.x').click()
  await page.clock.fastForward(31000)
  await expect.poll(() => snapshots).toBeGreaterThan(2)
  expect(histories).toBe(1)
  await visibility(page, true)
  const before = snapshots
  await page.clock.fastForward(120000)
  expect(snapshots).toBe(before)
  expect(histories).toBe(1)
})

test('a burst of unknown live alarms shares one snapshot and preserves live history during a history read', async ({ page }) => {
  const pendingSnapshot = gate(), pendingLog = gate()
  let snapshots = 0, histories = 0
  const live = await fixture(page, async (route, url) => {
    if (url.pathname === '/api/v1/alarms') {
      snapshots++
      if (snapshots > 1) await pendingSnapshot.wait
      await route.fulfill({ json: snapshot(snapshots > 1 ? 20 : 1) })
    } else {
      histories++
      await pendingLog.wait
      await route.fulfill({ json: [entry(2)] })
    }
  })
  for (let id = 2; id <= 20; id++) live.send(id)
  await expect.poll(() => snapshots).toBe(2)
  await page.getByTitle('告警', { exact: true }).click()
  await expect.poll(() => histories).toBe(1)
  live.send(21)
  await expect(page.locator('.panel .log')).toContainText('alarm_21')
  expect(snapshots).toBe(2)
  pendingSnapshot.release()
  pendingLog.release()
  await expect(page.locator('.panel tbody')).toContainText('alarm_20')
  await expect(page.locator('.panel .log li')).toHaveCount(2)
  await expect(page.locator('.panel .log li').first()).toContainText('alarm_21')
  expect(snapshots).toBe(2)
  expect(histories).toBe(1)
})

test('a refreshed history replaces events retired from the server snapshot', async ({ page }) => {
  let histories = 0
  await fixture(page, async (route, url) => {
    if (url.pathname === '/api/v1/alarms') await route.fulfill({ json: snapshot() })
    else {
      histories++
      await route.fulfill({ json: histories === 1 ? [entry(1), entry(2)] : histories === 2 ? [entry(2)] : [] })
    }
  })
  await page.getByTitle('告警', { exact: true }).click()
  await expect(page.locator('.panel .log li')).toHaveCount(2)
  await page.locator('.panel button.x').click()
  await page.getByTitle('告警', { exact: true }).click()
  await expect(page.locator('.panel .log li')).toHaveCount(1)
  await expect(page.locator('.panel .log')).not.toContainText('alarm_1')
  await page.locator('.panel button.x').click()
  await page.getByTitle('告警', { exact: true }).click()
  await expect(page.locator('.panel .log')).toContainText('暂无事件')
  expect(histories).toBe(3)
})

test('changing node cancels pending snapshot and history without replacing the new node', async ({ page }) => {
  const pending = gate()
  let localSnapshots = 0, localHistory = 0
  await fixture(page, async (route, url) => {
    const remote = url.searchParams.get('node') === 'remote'
    const history = url.pathname === '/api/v1/alarm_log'
    if (!remote) {
      if (history) localHistory++
      else localSnapshots++
      if (history || localSnapshots > 1) await pending.wait
    }
    await route.fulfill({ json: history ? [entry(remote ? 8 : 1, remote ? 'remote_history' : 'old_history')]
      : snapshot(remote ? 8 : 1, remote ? 'remote_alarm' : 'old_alarm') }).catch(() => {})
  }, true)
  await page.getByTitle('告警', { exact: true }).click()
  await expect.poll(() => localHistory).toBe(1)
  await expect.poll(() => localSnapshots).toBe(2)
  const aborted: string[] = []
  page.on('requestfailed', request => aborted.push(new URL(request.url()).pathname))
  await page.locator('.node-select-btn').click()
  await page.getByRole('option').filter({ hasText: 'Remote fixture' }).click()
  await expect(page.locator('.panel tbody')).toContainText('remote_alarm')
  await expect(page.locator('.panel .log')).toContainText('remote_history')
  await expect.poll(() => aborted).toEqual(expect.arrayContaining(['/api/v1/alarms', '/api/v1/alarm_log']))
  pending.release()
  await expect(page.locator('.panel tbody')).not.toContainText('old_alarm')
  await expect(page.locator('.panel .log')).not.toContainText('old_history')
})
