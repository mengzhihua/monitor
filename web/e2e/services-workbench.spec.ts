import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'

test('service triage filters, exports and retains the last successful snapshot on failure', async ({ page }) => {
  let fail = false
  const windows: string[] = []
  await page.route('**/api/v1/services?*', route => {
    windows.push(new URL(route.request().url()).searchParams.get('window') || '')
    return fail ? route.fulfill({ status: 503, body: 'unavailable' }) : route.fulfill({ json: { from: 100, to: 3700, services: [
      { name: 'checkout', status: 'WARNING', sla: 97.5, alarms: ['=pay'], children: ['database'] },
      { name: 'database', status: 'CLEAR', sla: 100 },
    ] } })
  })
  await page.route('**/api/v1/topology', route => route.fulfill({ json: { edges: [{ source: 'checkout', target: 'database', kind: 'manual' }] } }))
  await page.route('**/api/v1/reports/availability?*', route => route.fulfill({ json: { from: 100, to: 3700, alarms: [
    { name: '=pay', chart: 'http.pay', uptime: 97.5, raised_seconds: 90, window_seconds: 3600 },
    { name: 'db', chart: 'database', uptime: 100, raised_seconds: 0, window_seconds: 3600 },
  ] } }))
  await page.goto('/?view=services&token=browser-test-token')
  const panel = page.locator('.services')
  await expect(panel.locator('[data-service]')).toHaveCount(2)
  await panel.getByLabel('只看异常').check()
  await expect(panel.locator('[data-service]')).toHaveCount(1)
  await expect(panel.locator('tbody tr')).toHaveCount(1)
  const download = page.waitForEvent('download')
  await panel.getByRole('button', { name: '导出筛选结果 CSV' }).click()
  const file = await download
  const csv = await readFile((await file.path())!, 'utf8')
  expect(csv).toContain("\"'=pay\"")
  expect(csv).not.toContain('"db"')
  expect(csv).toContain('1970-01-01T00:01:40.000Z')
  await panel.getByRole('button', { name: 'database', exact: true }).click()
  await expect(panel.getByLabel('只看异常')).not.toBeChecked()
  await expect(panel.locator('tbody tr')).toHaveCount(1)
  await panel.getByLabel('搜索服务与告警').fill('')
  await panel.getByLabel('统计窗口').selectOption('168h')
  await expect(panel.getByRole('status')).toContainText('168h')
  expect(windows).toContain('168h')
  fail = true
  await panel.getByLabel('统计窗口').selectOption('720h')
  await expect(panel.getByRole('alert')).toContainText('上次成功数据')
  await expect(panel.locator('[data-service]')).toHaveCount(2)
  await expect(panel.getByRole('status')).toContainText('168h')
  await expect(panel.getByRole('button', { name: '导出筛选结果 CSV' })).toBeDisabled()
  fail = false
  await panel.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('720h')
  await expect(panel.getByRole('alert')).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})
