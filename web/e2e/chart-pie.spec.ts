import { expect, test } from '@playwright/test'

const end = 1767323045
const chart = {
  id: 'test.capacity', title: '容量占比', context: 'test.capacity', family: 'test', units: '%',
  chart_type: 'pie', priority: 1, update_every: 1, last_entry: end,
  dimensions: [
    { id: 'used', name: '已用' },
    { id: 'free', name: '空闲' },
    { id: 'idle', name: '零值' },
  ],
}

test('pie chart draws the last positive samples and omits zeros', async ({ page }) => {
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { [chart.id]: chart } } }))
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: {
    units: chart.units, dimension_ids: ['used', 'free', 'idle'], view_update_every: 1,
    result: { data: [[end - 1, 1, 1, 1], [end, 30, 10, 0]] },
  } }))
  await page.goto('/?view=charts&token=browser-test-token')
  const plot = page.locator('.card', { hasText: '容量占比' }).locator('.plot')
  const pie = plot.getByRole('img', { name: /饼图：已用 30\.0，75%；空闲 10\.0，25%/ })
  await expect(pie).toBeVisible()
  await expect(pie.locator('path')).toHaveCount(2)
  await expect(plot).toContainText('已用 30.0 75%')
  await expect(plot).toContainText('空闲 10.0 25%')
  await expect(plot).not.toContainText('零值')
  await expect(plot.locator('.u-legend')).toHaveCount(0)
})
