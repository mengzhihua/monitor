import { expect, test, type Page, type Route } from '@playwright/test'
import { openTools } from './mobile-navigation'

const host = { id: 'hub-local', hostname: 'session-fixture', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 }
const nodes = ['node-a', 'node-b'].map(id => ({ id, hostname: id, os: 'linux', arch: 'amd64', version: 'test',
  status: 'live', local: false, first_seen: 1, last_seen: 100, charts_count: 0, alarms: { warning: 0, critical: 0 } }))
const info = { version: 'test', mode: 'hub', uptime: 1, charts_count: 0, metrics_count: 0, host, collectors: [],
  alarms: { normal: 0, warning: 0, critical: 0, silent: 0 }, user: { name: 'reader', role: 'viewer' } }
const snapshot = { alarms: {}, summary: info.alarms }
function gate() {
  let release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  return { wait, release }
}
async function fixture(page: Page, read?: (route: Route, url: URL) => Promise<boolean>) {
  await page.routeWebSocket('**/api/v1/live*', () => {})
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    if (await read?.(route, url)) return
    const responses: Record<string, unknown> = {
      '/api/v1/info': info, '/api/v1/nodes': { nodes }, '/api/v1/charts': { charts: {} },
      '/api/v1/alarms': snapshot, '/api/v1/alarm_log': [], '/api/v1/functions': [],
      '/api/v1/auth/oidc/status': { enabled: false },
    }
    await route.fulfill({ json: responses[url.pathname] ?? {} })
  })
}
async function selectNode(page: Page, node: string) {
  await page.getByTitle('节点', { exact: true }).click()
  await page.getByRole('option').filter({ hasText: node }).click()
  await expect(page.locator('.node-select-btn')).toContainText(node)
}

for (const endpoint of ['alarms', 'functions']) {
  test(`${endpoint} authentication expiry clears the complete monitor session`, async ({ page }) => {
    await page.clock.install()
    let expired = false
    await fixture(page, async (route, url) => {
      if (!expired || url.pathname !== `/api/v1/${endpoint}`) return false
      await route.fulfill({ status: 401, body: 'expired' })
      return true
    })
    await page.goto('/?view=charts&token=browser-test-token')
    await expect(page.locator('.node-select-btn')).toContainText('session-fixture')
    await expect(page.locator('[title="live"]')).toBeVisible()
    expired = true
    await page.clock.fastForward(31000)
    await expect(page.getByLabel('登录密码或访问令牌', { exact: true })).toBeVisible()
    await expect(page.locator('.host')).toHaveCount(0)
    await expect(page.locator('.node-select-btn')).toHaveCount(0)
    await expect(page.locator('[title="live"]')).toHaveCount(0)
    // A successful subsequent login must reinitialize the cleared session.
    expired = false
    await page.getByLabel('登录密码或访问令牌', { exact: true }).fill('browser-test-token')
    await page.getByRole('button', { name: '进入', exact: true }).click()
    await expect(page.locator('.node-select-btn')).toContainText('session-fixture')
    await expect(page.locator('[title="live"]')).toBeVisible()
    await expect(page.getByLabel('登录密码或访问令牌', { exact: true })).toHaveCount(0)
  })
}

test('an expired alarm read cannot be undone by an older successful functions read', async ({ page }) => {
  const pending = gate()
  let delaying = false, functionStarted = false
  await fixture(page, async (route, url) => {
    if (!delaying) return false
    if (url.pathname === '/api/v1/functions') {
      functionStarted = true
      await pending.wait
      await route.fulfill({ json: [{ name: 'late-private-function', help: 'old session', timeout: 1 }] }).catch(() => {})
      return true
    }
    if (url.pathname === '/api/v1/alarms') {
      await expect.poll(() => functionStarted).toBe(true)
      await route.fulfill({ status: 401, body: 'expired' })
      return true
    }
    return false
  })
  await page.clock.install()
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('[title="live"]')).toBeVisible()
  delaying = true
  await page.clock.fastForward(31000)
  await expect(page.getByLabel('登录密码或访问令牌', { exact: true })).toBeVisible()
  pending.release()
  await expect(page.locator('.node-select-btn')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText('late-private-function')
})

test('switching nodes cancels an old metadata read without accepting its late 401', async ({ page }) => {
  const pending = gate()
  let oldStarted = false
  const cancelled: string[] = []
  page.on('requestfailed', request => cancelled.push(request.url()))
  await fixture(page, async (route, url) => {
    if (url.pathname !== '/api/v1/charts' || url.searchParams.get('node') !== 'node-a') return false
    oldStarted = true
    await pending.wait
    await route.fulfill({ status: 401, body: 'old session' }).catch(() => {})
    return true
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('[title="live"]')).toBeVisible()
  await selectNode(page, 'node-a')
  await expect.poll(() => oldStarted).toBe(true)
  await selectNode(page, 'node-b')
  await expect(page.locator('[title="live"]')).toBeVisible()
  await expect.poll(() => cancelled.some(url => url.includes('/api/v1/charts?node=node-a'))).toBe(true)
  pending.release()
  await expect(page.locator('.node-select-btn')).toContainText('node-b')
  await expect(page.getByLabel('登录密码或访问令牌', { exact: true })).toHaveCount(0)
})

test('blocked session storage still initializes authentication and selected node before requests', async ({ page }) => {
  const errors: string[] = [], requests: { path: string; node: string | null; token?: string }[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.addInitScript(() => {
    for (const method of ['getItem', 'setItem', 'removeItem']) {
      Object.defineProperty(Storage.prototype, method, { configurable: true, value() { throw new DOMException('storage denied', 'SecurityError') } })
    }
  })
  await fixture(page, async (route, url) => {
    if (['/api/v1/info', '/api/v1/charts'].includes(url.pathname)) {
      requests.push({ path: url.pathname, node: url.searchParams.get('node'), token: route.request().headers().authorization })
    }
    return false
  })
  await page.goto('/?view=charts&token=browser-test-token&node=node-a')
  await expect(page.locator('.node-select-btn')).toContainText('node-a')
  await expect(page.locator('[title="live"]')).toBeVisible()
  expect(new URL(page.url()).searchParams.has('token')).toBe(false)
  expect(requests.find(request => request.path === '/api/v1/charts')).toMatchObject({ node: 'node-a', token: 'Bearer browser-test-token' })
  await selectNode(page, 'node-b')
  await expect(page.getByRole('status').filter({ hasText: '浏览器无法记住' })).toBeVisible()
  expect(errors).toEqual([])
})

for (const status of [200, 401, 500]) {
  test(`a delayed logout ${status} cannot clear or report errors in a replacement login`, async ({ page }) => {
    await page.clock.install()
    const pending = gate()
    let expired = false, logoutRequests = 0, logoutSettled = 0
    const cancelled: string[] = []
    page.on('requestfailed', request => cancelled.push(new URL(request.url()).pathname))
    await fixture(page, async (route, url) => {
      if (url.pathname === '/api/v1/auth/oidc/logout') {
        logoutRequests++
        await pending.wait
        await route.fulfill({ status, body: status === 500 ? 'logout fixture failed' : '' }).catch(() => {})
        logoutSettled++
        return true
      }
      if (url.pathname !== '/api/v1/info' || !expired) return false
      const replacement = route.request().headers().authorization === 'Bearer browser-replacement-token'
      await route.fulfill(replacement
        ? { json: { ...info, host: { ...host, hostname: 'replacement-session' } } }
        : { status: 401, body: 'old session expired' })
      return true
    })
    try {
      await page.goto('/?view=charts&token=browser-test-token')
      await expect(page.locator('[title="live"]')).toBeVisible()
      await openTools(page)
      await page.getByRole('button', { name: '退出登录', exact: true }).click()
      await expect.poll(() => logoutRequests).toBe(1)
      // Repeated clicks must share the pending logout, including in the phone menu.
      await openTools(page)
      await page.getByRole('button', { name: '退出登录', exact: true }).click()

      expired = true
      // Foregrounding refreshes the expired session before the logout deadline.
      await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange')) })
      await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')) })
      await expect(page.getByLabel('登录密码或访问令牌', { exact: true })).toBeVisible()
      await page.getByLabel('登录密码或访问令牌', { exact: true }).fill('browser-replacement-token')
      await page.getByRole('button', { name: '进入', exact: true }).click()
      await expect(page.locator('.node-select-btn')).toContainText('replacement-session')
      await expect.poll(() => cancelled.filter(path => path === '/api/v1/auth/oidc/logout').length).toBe(1)
      pending.release()
      await expect.poll(() => logoutSettled).toBe(1)
      expect(logoutRequests).toBe(1)
      expect(await page.evaluate(() => sessionStorage.getItem('monitor.token'))).toBe('browser-replacement-token')
      await expect(page.getByLabel('登录密码或访问令牌', { exact: true })).toHaveCount(0)
      await expect(page.getByRole('alert')).toHaveCount(0)
      await expect(page.locator('[title="live"]')).toBeVisible()
    } finally { pending.release() }
  })
}

test('a timed-out logout releases its request so the current session can retry', async ({ page }) => {
  await page.clock.install()
  const pending = gate()
  let requests = 0, lateSettled = false
  await fixture(page, async (route, url) => {
    if (url.pathname === '/api/v1/auth/oidc/logout') {
      if (++requests === 1) {
        await pending.wait
        await route.fulfill({ status: 500, body: 'late logout failure' }).catch(() => {})
        lateSettled = true
      } else await route.fulfill({ status: 200, body: '' })
      return true
    }
    if (url.pathname === '/api/v1/info' && !route.request().headers().authorization) {
      await route.fulfill({ status: 401, body: 'login required' })
      return true
    }
    return false
  })
  try {
    await page.goto('/?view=charts&token=browser-test-token')
    await expect(page.locator('[title="live"]')).toBeVisible()
    await openTools(page)
    await page.getByRole('button', { name: '退出登录', exact: true }).click()
    await expect.poll(() => requests).toBe(1)
    await page.clock.fastForward(16_000)
    await expect(page.getByRole('alert')).toContainText('退出请求超时，请重试。')
    expect(await page.evaluate(() => sessionStorage.getItem('monitor.token'))).toBe('browser-test-token')
    await openTools(page)
    await page.getByRole('button', { name: '退出登录', exact: true }).click()
    await expect(page.getByLabel('登录密码或访问令牌', { exact: true })).toBeVisible()
    expect(await page.evaluate(() => sessionStorage.getItem('monitor.token'))).toBeNull()
    pending.release()
    await expect.poll(() => lateSettled).toBe(true)
    expect(requests).toBe(2)
    await expect(page.getByRole('alert')).toHaveCount(0)
  } finally { pending.release() }
})
