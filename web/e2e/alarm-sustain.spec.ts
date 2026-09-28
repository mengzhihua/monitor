import { test, expect } from '@playwright/test'

test('alarm panel shows pending and recovery hold without counting a pending clear as a problem', async ({ page }) => {
  let mode: 'pending' | 'hold' = 'pending'
  await page.route('**/api/v1/alarms*', async route => {
    const url = new URL(route.request().url())
    if (url.pathname !== '/api/v1/alarms') return route.continue()
    const now = Math.floor(Date.now() / 1000)
    const alarm = mode === 'pending'
      ? { id: 7, name: 'ram_hot', chart: 'system.ram', context: 'system.ram', family: 'ram', units: '%', info: 'hot', status: 'CLEAR', value: 85, last_updated: now, last_status_change: now, active: true, pending_status: 'WARNING', pending_since: now, pending_until: now + 40 }
      : { id: 7, name: 'ram_hot', chart: 'system.ram', context: 'system.ram', family: 'ram', units: '%', info: 'hot', status: 'CRITICAL', value: 10, last_updated: now, last_status_change: now, active: true, hold_until: now + 50 }
    await route.fulfill({ json: { hostname: 'browser-test', now, summary: { normal: 1, warning: 0, critical: mode === 'hold' ? 1 : 0, silent: 0 }, alarms: { 'system.ram.ram_hot': alarm } } })
  })
  await page.route('**/api/v1/alarm_log*', route => route.fulfill({ json: [] }))
  await page.goto('/?view=charts&token=browser-test-token')
  const button = page.locator('button[title="告警"]')
  await expect(button).toContainText('0')
  await button.click()
  const panel = page.locator('.panel')
  await expect(panel.locator('tbody tr').filter({ hasText: 'ram_hot' })).toContainText('等待警告')
  await expect(panel.locator('tbody tr').filter({ hasText: 'ram_hot' })).toContainText('CLEAR')
  await expect(button).toContainText('0')
  await panel.locator('button.x').click()
  mode = 'hold'
  await button.click()
  await expect(panel.locator('tbody tr').filter({ hasText: 'ram_hot' })).toContainText('恢复保持')
  await expect(panel.locator('tbody tr').filter({ hasText: 'ram_hot' })).toContainText('CRITICAL')
  await expect(button).toContainText('1')
})

test('alarm panel shows a recovery expression hold as an open problem', async ({ page }) => {
  const now = Math.floor(Date.now() / 1000)
  await page.route('**/api/v1/alarms*', async route => {
    const url = new URL(route.request().url())
    if (url.pathname !== '/api/v1/alarms') return route.continue()
    await route.fulfill({ json: { hostname: 'browser-test', now, summary: { normal: 0, warning: 1, critical: 0, silent: 0 }, alarms: { 'system.ram.ram_hot': {
      id: 8, name: 'ram_hot', chart: 'system.ram', context: 'system.ram', family: 'ram', units: '%', info: 'hot',
      status: 'WARNING', value: 75, last_updated: now, last_status_change: now, active: true, recovery_hold: true,
    } } } })
  })
  await page.route('**/api/v1/alarm_log*', route => route.fulfill({ json: [] }))
  await page.goto('/?view=charts&token=browser-test-token')
  const button = page.locator('button[title="告警"]')
  await expect(button).toContainText('1')
  await button.click()
  const row = page.locator('.panel tbody tr').filter({ hasText: 'ram_hot' })
  await expect(row).toContainText('等待恢复')
  await expect(row).toContainText('WARNING')
})
