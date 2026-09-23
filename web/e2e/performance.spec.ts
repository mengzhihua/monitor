import { expect, test, type Page } from '@playwright/test'

const token = 'browser-test-token'

async function visibility(page: Page, hidden: boolean) {
  await page.evaluate((value) => {
    Object.defineProperty(document, 'hidden', { configurable: true, value })
    document.dispatchEvent(new Event('visibilitychange'))
  }, hidden)
}

test('scrolling 100 charts bounds canvases and rebuilds evicted charts', async ({ page }) => {
  const charts = Object.fromEntries(Array.from({ length: 100 }, (_, i) => {
    const id = `test.chart${i}`
    return [id, { id, title: id, context: 'test', family: 'test', units: 'value', chart_type: 'line',
      priority: i, update_every: 1, dimensions: [{ id: 'value', name: 'value' }], last_entry: Math.floor(Date.now() / 1000) }]
  }))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts } }))
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: {
    dimension_ids: ['value'], result: { data: [[1, 1], [2, 2], [3, 1]] },
  } }))
  await page.goto('/?view=charts&token=' + token)
  const cards = page.locator('.card')
  await expect(cards).toHaveCount(100)
  await expect(cards.first().locator('canvas')).toBeVisible()
  const firstCanvas = await cards.first().locator('canvas').elementHandle()
  for (let i = 0; i < 100; i += 4) {
    await cards.nth(i).scrollIntoViewIfNeeded()
    await expect(cards.nth(i).locator('canvas')).toBeVisible()
  }
  await expect.poll(() => page.locator('.card canvas').count()).toBeLessThanOrEqual(18)
  await expect(cards.first().locator('canvas')).toHaveCount(0)
  expect(await firstCanvas!.evaluate(canvas => canvas.isConnected)).toBe(false)
  await cards.first().scrollIntoViewIfNeeded()
  await expect(cards.first().locator('canvas')).toBeVisible()
  expect(await page.evaluate(canvas => canvas === document.querySelector('.card canvas'), firstCanvas)).toBe(false)
})

test('long history unsubscribes metrics and a hidden dashboard stops requests and live socket', async ({ page }) => {
  await page.clock.install()
  let requests = 0
  let sockets = 0
  let closed = 0
  const subscriptions: string[][] = []
  page.on('request', request => { if (request.url().includes('/api/v1/')) requests++ })
  page.on('websocket', socket => {
    sockets++
    socket.on('close', () => closed++)
    socket.on('framesent', frame => {
      const message = JSON.parse(String(frame.payload))
      if (message.charts) subscriptions.push(message.charts)
    })
  })
  await page.goto('/?view=charts&token=' + token)
  await expect(page.locator('[title="live"]')).toBeVisible()
  await page.getByPlaceholder('筛选图表…').fill('system.ram')
  await expect.poll(() => subscriptions.at(-1)).toEqual(['system.ram'])
  await page.locator('.controls select').selectOption('86400')
  await expect.poll(() => subscriptions.at(-1)).toEqual([' '])
  await visibility(page, true)
  await expect.poll(() => closed).toBe(sockets)
  const before = requests
  await page.setViewportSize({ width: 900, height: 700 })
  await page.clock.fastForward(120000)
  expect(requests).toBe(before)
  await visibility(page, false)
  await expect(page.locator('[title="live"]')).toBeVisible()
  await expect.poll(() => requests).toBeGreaterThan(before)
  await expect.poll(() => page.locator('.card .plot').evaluate(element =>
    element.clientWidth === element.querySelector<HTMLElement>('.uplot')?.clientWidth,
  )).toBe(true)
  await page.locator('.controls select').selectOption('300')
  await expect.poll(() => subscriptions.at(-1)).toEqual(['system.ram'])
})

test('slow function requests never overlap and background panels stop polling', async ({ page }) => {
  await page.clock.install()
  await page.route('**/api/v1/functions*', route => route.fulfill({ json: [{ name: 'processes', help: 'test', timeout: 20 }] }))
  let calls = 0
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/v1/function?*', async route => {
    calls++
    await gate
    await route.fulfill({ json: { time: 1, result: { columns: ['pid', 'cpu'], rows: [{ pid: 123, cpu: 2 }], total: 1 } } })
  })
  await page.goto('/?view=charts&token=' + token)
  await page.getByTitle('Functions（实时进程表等）', { exact: true }).click()
  await expect.poll(() => calls).toBe(1)
  await page.clock.fastForward(12000)
  expect(calls).toBe(1)
  release()
  await expect(page.locator('.panel tbody')).toContainText('123')
  await visibility(page, true)
  const before = calls
  await page.clock.fastForward(60000)
  expect(calls).toBe(before)
  await visibility(page, false)
  await expect.poll(() => calls).toBe(before + 1)
})

test('superseded history fetches are aborted when changing the time range', async ({ page }) => {
  let started!: () => void
  const pending = new Promise<void>(resolve => { started = resolve })
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const aborted: string[] = []
  page.on('requestfailed', request => aborted.push(request.url()))
  await page.route('**/api/v1/data?*', async route => {
    if (route.request().url().includes('after=-86400')) {
      started()
      await gate
      await route.abort().catch(() => {})
    } else await route.continue()
  })
  await page.goto('/?view=charts&token=' + token)
  await page.getByPlaceholder('筛选图表…').fill('system.ram')
  await expect(page.locator('.card canvas')).toBeVisible()
  await page.locator('.controls select').selectOption('86400')
  await pending
  await page.locator('.controls select').selectOption('60')
  await expect.poll(() => aborted.some(url => url.includes('after=-86400'))).toBe(true)
  release()
  await expect(page.locator('.card canvas')).toBeVisible()
})
