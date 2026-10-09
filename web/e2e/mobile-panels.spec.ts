import { expect, test, type Locator, type Page } from '@playwright/test'

test.skip(({ isMobile }) => !isMobile, 'Phone layouts are exercised in the mobile browser project')

const longName = 'production_payment_checkout_latency_'.repeat(4)
const longChart = 'http.production.eu.central.checkout.primary_endpoint.'.repeat(3)
const longHost = 'production-edge-gateway-with-a-long-unbroken-hostname-'.repeat(3)

async function contained(page: Page, panel: Locator) {
  const size = await panel.evaluate(element => ({
    left: element.getBoundingClientRect().left,
    right: element.getBoundingClientRect().right,
    width: document.documentElement.clientWidth,
    scrollWidth: element.scrollWidth,
    clientWidth: element.clientWidth,
  }))
  expect(size.left).toBeGreaterThanOrEqual(0)
  expect(size.right).toBeLessThanOrEqual(size.width + 1)
  expect(size.scrollWidth).toBeLessThanOrEqual(size.clientWidth + 1)
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).toBe(true)
}

for (const width of [320, 390, 430]) {
  test(`alarm cards retain details, recovery state and touch targets at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 844 })
    const now = Math.floor(Date.now() / 1000)
    const alarm = { id: 101, name: longName, chart: longChart, context: 'http', family: 'http', units: 'milliseconds',
      info: '持续观察服务恢复，等待下一次评估。'.repeat(12), status: 'WARNING', value: 125, last_updated: now,
      last_status_change: now - 180, active: true, recovery_hold: true }
    let silenced = false
    await page.route('**/api/v1/alarms*', async route => {
      if (new URL(route.request().url()).pathname !== '/api/v1/alarms') return route.continue()
      await route.fulfill({ json: { hostname: longHost, now, summary: { normal: 1, warning: 1, critical: 0, silent: 0 }, alarms: {
        recovery: { ...alarm, silenced },
        pending: { ...alarm, id: 102, name: 'pending_latency', status: 'CLEAR', recovery_hold: false, pending_status: 'CRITICAL', pending_until: now + 120 },
      } } })
    })
    await page.route('**/api/v1/alarms/silence', async route => {
      if (route.request().method() === 'POST') {
        const body = route.request().postDataJSON()
        expect(body.chart).toBe(longChart)
        expect(body.alarm).toBe(longName)
        silenced = true
      }
      await route.fulfill({ json: { all: false, alarms: silenced ? { [`${longChart}.${longName}`]: now + 3600 } : {} } })
    })
    await page.route('**/api/v1/alarm_log*', route => route.fulfill({ json: [{
      unique_id: 1, alarm_id: 101, when: now, hostname: longHost, name: longName, chart: longChart,
      status: 'WARNING', old_status: 'CLEAR', value: 125, units: 'milliseconds', notified: true,
    }] }))
    await page.goto('/?view=charts&token=browser-test-token')
    await page.getByTitle('告警', { exact: true }).click()
    const panel = page.locator('.panel').filter({ has: page.getByRole('heading', { name: '告警', exact: false }) })
    const row = panel.locator('tbody tr').filter({ hasText: longName })
    await expect(row).toContainText('等待恢复')
    await expect(row.locator('[data-label="图表"]')).toBeVisible()
    await expect(row.locator('[data-label="图表"]')).toHaveText(longChart)
    await expect(row.locator('[data-label="持续"]')).toBeVisible()
    await expect(panel.locator('tbody tr').filter({ hasText: 'pending_latency' })).toContainText('等待严重')
    await row.locator('summary').click()
    await expect(row.locator('details p')).toBeVisible()
    await contained(page, panel)
    for (const button of await panel.getByRole('button').all()) {
      if (!await button.isVisible()) continue
      const box = await button.boundingBox()
      expect(box?.height).toBeGreaterThanOrEqual(44)
      expect(box?.width).toBeGreaterThanOrEqual(44)
    }
    await row.getByRole('button', { name: '静默', exact: true }).click()
    await expect(row.getByRole('button', { name: '解除手动静默' })).toBeVisible()
    await contained(page, panel)
    await panel.screenshot({ path: testInfo.outputPath(`alarms-${width}.png`) })
  })

  test(`service, topology and availability cards wrap long names at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 844 })
    await page.route('**/api/v1/services*', route => route.fulfill({ json: {
      from: 1, to: 2, services: [{ name: longName, status: 'WARNING', sla: 97.5, alarms: [], children: [] }],
    } }))
    await page.route('**/api/v1/topology', route => route.fulfill({ json: {
      edges: [{ source: longHost, target: longName, kind: 'manual' }],
    } }))
    await page.route('**/api/v1/reports/availability*', route => route.fulfill({ json: {
      from: 1, to: 2, alarms: [{ name: longName, chart: longChart, uptime: 97.5, raised_seconds: 90, window_seconds: 3600 }],
    } }))
    await page.goto('/?view=services&token=browser-test-token')
    const panel = page.locator('.services')
    await expect(panel.locator('.card')).toContainText(longName)
    await expect(panel.locator('.topology')).toContainText(longHost)
    await expect(panel.locator('tbody [data-label="图表"]')).toHaveText(longChart)
    await expect(panel.locator('tbody [data-label="正常时间"]')).toHaveText('97.5%')
    await contained(page, panel)
    await panel.screenshot({ path: testInfo.outputPath(`services-${width}.png`) })
  })
}
