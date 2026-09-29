import { expect, test } from '@playwright/test'

test('services workspace shows SLA, topology and availability', async ({ page }) => {
  await page.route('**/api/v1/services*', route => route.fulfill({
    json: { from: 1, to: 2, services: [{ name: 'checkout', status: 'WARNING', sla: 97.5, alarms: ['pay'], children: [] }] },
  }))
  await page.route('**/api/v1/topology', route => route.fulfill({
    json: { edges: [{ source: 'core', target: 'edge-sw', kind: 'lldp' }, { source: 'a', target: 'b', kind: 'manual' }] },
  }))
  await page.route('**/api/v1/reports/availability*', route => route.fulfill({
    json: { from: 1, to: 2, alarms: [{ name: 'pay', chart: 'http.pay', uptime: 97.5, raised_seconds: 90, window_seconds: 3600 }] },
  }))
  await page.goto('/?view=services&token=browser-test-token')
  const panel = page.locator('.services')
  await expect(panel.getByRole('heading', { name: '服务与拓扑' })).toBeVisible()
  await expect(panel.locator('[data-service="checkout"]')).toContainText('WARNING')
  await expect(panel.locator('[data-service="checkout"]')).toContainText('97.5%')
  await expect(panel).toContainText('core → edge-sw')
  await expect(panel).toContainText('manual')
  await expect(panel).toContainText('pay')
})
