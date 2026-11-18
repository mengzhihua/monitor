import { expect, test } from '@playwright/test'

test('setup is reachable from overview and shows scoped evidence without writes', async ({ page }) => {
  const writes: string[] = []
  const scoped: string[] = []
  page.on('request', r => {
    if (r.url().includes('/api/') && !['GET', 'HEAD'].includes(r.method())) writes.push(r.url())
    if (/\/api\/v1\/(charts|alarms|services|operations\/notifications)(\?|$)/.test(r.url())) scoped.push(r.url())
  })
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '接入与运行检查', exact: true }).click()
  const panel = page.getByRole('region', { name: '接入与运行检查' })
  await expect(panel.locator('[data-check]')).toHaveCount(5)
  await expect(panel.locator('[data-check="collection"]')).toHaveAttribute('data-state', 'ready')
  await expect(panel).toContainText('此页不跟随顶部远端节点选择')
  await expect(panel.locator('[data-check="notifications"]')).toContainText('不代表用户已收件')
  await panel.getByRole('button', { name: '打开通知诊断与测试' }).click()
  await expect(panel.locator('.diagnostics')).toBeVisible()
  expect(writes).toEqual([])
  expect(scoped.every(url => !new URL(url).searchParams.get('node'))).toBe(true)
  await panel.getByRole('button', { name: '查看本机图表' }).click()
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  expect(new URL(page.url()).searchParams.get('node')).toBeNull()
})

test('failed evidence becomes unknown, other checks remain usable and retry restores it', async ({ page }) => {
  let fail = false
  await page.route('**/api/v1/operations/notifications', route => fail ? route.fulfill({ status: 503, body: 'unavailable' }) : route.continue())
  await page.goto('/?view=setup&token=browser-viewer-token')
  const panel = page.getByRole('region', { name: '接入与运行检查' })
  await expect(panel.locator('[data-check="alarms"]')).not.toHaveAttribute('data-state', 'unknown')
  await expect(panel.getByRole('button', { name: '检查保存配置' })).toHaveCount(0)
  fail = true
  await panel.getByRole('button', { name: '重新检查', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('通知读取失败')
  await expect(panel.locator('[data-check="alarms"]')).toHaveAttribute('data-state', 'unknown')
  await expect(panel.locator('[data-check="notifications"]')).toHaveAttribute('data-state', 'unknown')
  await expect(panel.locator('[data-check="collection"]')).toHaveAttribute('data-state', 'ready')
  fail = false
  await panel.getByRole('button', { name: '重新检查', exact: true }).click()
  await expect(panel.getByRole('alert')).toHaveCount(0)
  await expect(panel.locator('[data-check="notifications"]')).not.toHaveAttribute('data-state', 'unknown')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
