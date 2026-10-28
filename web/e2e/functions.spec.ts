import { expect, test, type Page, type Route } from '@playwright/test'
import type { FunctionInfo } from '../src/api/monitoring/types'

const pluginA = { name: 'plugin.demo.inspect', help: '检查插件状态', timeout: 60 }
const pluginB = { name: 'plugin.demo.other', help: '另一项插件诊断', timeout: 60 }
const table = (value: string) => ({ columns: ['value', 'count'], rows: [{ value, count: 2 }, { value: 'second', count: 1 }], total: 2 })
const response = (name: string, result = table(name)) => ({ function: name, time: 100, result })
function gate() {
  let release!: () => void
  const promise = new Promise<void>(resolve => { release = resolve })
  return { promise, release }
}
async function visible(page: Page, value: boolean) {
  await page.evaluate(value => {
    Object.defineProperty(document, 'hidden', { configurable: true, value: !value })
    document.dispatchEvent(new Event('visibilitychange'))
  }, value)
}

async function fixture(page: Page, functions: () => FunctionInfo[], invoke: (route: Route, url: URL) => Promise<void>) {
  await page.clock.install()
  // The function fixture supplies all HTTP data and does not exercise live charts.
  await page.routeWebSocket('**/api/v1/live*', () => {})
  await page.route(url => url.pathname.startsWith('/api/'), async route => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/v1/function') return invoke(route, url)
    const replies: Record<string, unknown> = {
      '/api/v1/info': { version: 'test', mode: 'agent', uptime: 1, charts_count: 0, metrics_count: 0, collectors: [], alarms: null,
        host: { id: 'local', hostname: 'local', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 }, user: { name: 'ops', role: 'troubleshooter' } },
      '/api/v1/charts': { charts: {} }, '/api/v1/functions': functions(), '/api/v1/alarms': { alarms: {} },
      '/api/v1/auth/oidc/status': { enabled: false },
    }
    await route.fulfill({ json: replies[url.pathname] ?? {} })
  })
  await page.goto('/?view=functions&token=browser-test-token')
  const panel = page.getByRole('region', { name: '函数与诊断', exact: true })
  await expect(panel).toBeVisible()
  return panel
}

test('plugin calls require a click and sorting, filtering or returning to the page never repeats them', async ({ page }) => {
  const calls: string[] = []
  const panel = await fixture(page, () => [pluginA], async (route, url) => {
    calls.push(url.searchParams.get('function')!)
    await route.fulfill({ json: response(pluginA.name) })
  })
  await page.clock.runFor(2500)
  expect(calls).toEqual([])
  await panel.getByRole('button', { name: '执行', exact: true }).click()
  await expect(panel.getByRole('cell', { name: pluginA.name, exact: true })).toBeVisible()
  await panel.getByRole('columnheader', { name: 'count', exact: true }).click()
  await panel.getByLabel('筛选函数结果').fill('second')
  await expect(panel.locator('tbody tr')).toHaveCount(1)
  await visible(page, false)
  await visible(page, true)
  await page.clock.runFor(2500)
  expect(calls).toEqual([pluginA.name])
  await panel.getByRole('button', { name: '再执行', exact: true }).click()
  await expect.poll(() => calls.length).toBe(2)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('changing functions and failed reruns never display another or older result', async ({ page }) => {
  const calls: string[] = []
  let failB = true
  const panel = await fixture(page, () => [pluginA, pluginB], async (route, url) => {
    const name = url.searchParams.get('function')!
    calls.push(name)
    if (name === pluginB.name && failB) await route.fulfill({ status: 503, body: 'plugin unavailable' })
    else await route.fulfill({ json: response(name) })
  })
  await panel.getByRole('button', { name: '执行', exact: true }).click()
  await expect(panel.getByRole('cell', { name: pluginA.name, exact: true })).toBeVisible()
  await panel.getByLabel('选择函数').selectOption(pluginB.name)
  await expect(panel.locator('table')).toHaveCount(0)
  await expect(panel.locator('h3 small')).toHaveCount(0)
  expect(calls).toEqual([pluginA.name])
  await panel.getByRole('button', { name: '执行', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('plugin unavailable')
  await expect(panel.locator('table')).toHaveCount(0)
  failB = false
  await panel.getByRole('button', { name: '执行', exact: true }).click()
  await expect(panel.getByRole('cell', { name: pluginB.name, exact: true })).toBeVisible()
  failB = true
  await panel.getByRole('button', { name: '再执行', exact: true }).click()
  await expect(panel.getByRole('alert')).toBeVisible()
  await expect(panel.locator('table')).toHaveCount(0)
  await expect(panel.locator('h3 small')).toHaveCount(0)
  await visible(page, false)
  await visible(page, true)
  await page.clock.runFor(2500)
  expect(calls).toEqual([pluginA.name, pluginB.name, pluginB.name, pluginB.name])
})

test('an in-flight plugin call is not duplicated and cannot overwrite a newer selection', async ({ page }) => {
  const pending = gate(), calls: string[] = []
  const panel = await fixture(page, () => [pluginA, pluginB], async (route, url) => {
    const name = url.searchParams.get('function')!
    calls.push(name)
    if (name === pluginA.name) await pending.promise
    await route.fulfill({ json: response(name) }).catch(() => {})
  })
  try {
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect.poll(() => calls.length).toBe(1)
    await expect(panel.getByRole('button', { name: '执行中…', exact: true })).toBeDisabled()
    await panel.getByLabel('选择函数').selectOption(pluginB.name)
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect(panel.getByRole('cell', { name: pluginB.name, exact: true })).toBeVisible()
    pending.release()
    await page.clock.runFor(100)
    await expect(panel.getByRole('cell', { name: pluginA.name, exact: true })).toHaveCount(0)
    expect(calls).toEqual([pluginA.name, pluginB.name])
  } finally { pending.release() }
})

test('returning to a pending plugin call keeps its one submitted result', async ({ page }) => {
  const pending = gate()
  let calls = 0
  const panel = await fixture(page, () => [pluginA], async route => {
    calls++
    await pending.promise
    await route.fulfill({ json: response(pluginA.name) }).catch(() => {})
  })
  try {
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect.poll(() => calls).toBe(1)
    await visible(page, false)
    await visible(page, true)
    pending.release()
    await expect(panel.getByRole('cell', { name: pluginA.name, exact: true })).toBeVisible()
    await page.clock.runFor(2500)
    expect(calls).toBe(1)
  } finally { pending.release() }
})

test('built-in diagnostics still poll, pass sorting and refresh when visible', async ({ page }) => {
  const calls: string[] = []
  const panel = await fixture(page, () => [{ name: 'processes', help: 'processes', timeout: 5 }], async (route, url) => {
    calls.push(url.searchParams.get('sort')!)
    await route.fulfill({ json: response('processes') })
  })
  await expect.poll(() => calls.length).toBe(1)
  await expect(panel.getByRole('cell', { name: 'processes', exact: true })).toBeVisible()
  await page.clock.runFor(2100)
  await expect.poll(() => calls.length).toBe(2)
  await panel.getByRole('columnheader', { name: 'count', exact: true }).click()
  await expect.poll(() => calls).toEqual(['cpu', 'cpu', 'count'])
  await visible(page, false)
  await page.clock.runFor(3000)
  expect(calls).toHaveLength(3)
  await visible(page, true)
  await expect.poll(() => calls.length).toBe(4)
})

test('removing a function from the live directory cancels its result without calling its replacement', async ({ page }) => {
  const pending = gate(), calls: string[] = []
  let functions = [pluginA, pluginB]
  const panel = await fixture(page, () => functions, async (route, url) => {
    const name = url.searchParams.get('function')!
    calls.push(name)
    await pending.promise
    await route.fulfill({ json: response(name) }).catch(() => {})
  })
  try {
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect.poll(() => calls.length).toBe(1)
    functions = [pluginB]
    await page.clock.runFor(30_100)
    await expect(panel.getByLabel('选择函数')).toHaveValue(pluginB.name)
    pending.release()
    await page.clock.runFor(100)
    await expect(panel.locator('table')).toHaveCount(0)
    await expect(panel.getByRole('button', { name: '执行', exact: true })).toBeEnabled()
    expect(calls).toEqual([pluginA.name])
  } finally { pending.release() }
})

test('an expired plugin request becomes retryable without replaying it automatically', async ({ page }) => {
  const pending = gate()
  let calls = 0
  const panel = await fixture(page, () => [{ ...pluginA, timeout: 1 }], async route => {
    calls++
    await pending.promise
    await route.fulfill({ json: response(pluginA.name) }).catch(() => {})
  })
  try {
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect.poll(() => calls).toBe(1)
    await page.clock.runFor(6100)
    await expect(panel.getByRole('alert')).toContainText('结果未确认')
    await expect(panel.getByRole('button', { name: '执行', exact: true })).toBeEnabled()
    pending.release()
    await page.clock.runFor(100)
    await expect(panel.locator('table')).toHaveCount(0)
    expect(calls).toBe(1)
  } finally { pending.release() }
})

test('credential changes clear displayed results and cancel pending calls without replay', async ({ page }) => {
  const pending = gate(), credentials: string[] = []
  const panel = await fixture(page, () => [pluginA], async route => {
    credentials.push(route.request().headers().authorization ?? '')
    if (credentials.length === 2) await pending.promise
    await route.fulfill({ json: response(pluginA.name) }).catch(() => {})
  })
  async function changeToken(token: string) {
    // A same-origin frame shares sessionStorage and produces a real storage event
    // in the dashboard window; the module stays mounted throughout the change.
    await page.evaluate(token => {
      let frame = document.querySelector<HTMLIFrameElement>('#credential-fixture')
      if (!frame) {
        frame = document.createElement('iframe')
        frame.id = 'credential-fixture'
        frame.hidden = true
        document.body.appendChild(frame)
      }
      frame.contentWindow!.sessionStorage.setItem('monitor.token', token)
    }, token)
  }
  try {
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect(panel.locator('table')).toBeVisible()
    await changeToken('browser-second-token')
    await expect(panel.locator('table')).toHaveCount(0)
    await expect(panel.locator('h3 small')).toHaveCount(0)
    expect(credentials).toHaveLength(1)
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect.poll(() => credentials.length).toBe(2)
    await changeToken('browser-third-token')
    await expect(panel.getByRole('button', { name: '执行', exact: true })).toBeEnabled()
    pending.release()
    await page.clock.runFor(100)
    await expect(panel.locator('table')).toHaveCount(0)
    expect(credentials).toHaveLength(2)
    await panel.getByRole('button', { name: '执行', exact: true }).click()
    await expect(panel.locator('table')).toBeVisible()
    expect(credentials).toEqual(['Bearer browser-test-token', 'Bearer browser-second-token', 'Bearer browser-third-token'])
  } finally { pending.release() }
})
