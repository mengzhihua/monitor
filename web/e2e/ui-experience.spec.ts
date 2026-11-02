import { expect, test, type Page } from '@playwright/test'
import { workspace } from './mobile-navigation'

async function metrics(page: Page) {
  const charts = Object.fromEntries(Array.from({ length: 30 }, (_, i) => {
    const id = `test.chart${i}`
    return [id, { id, title: `测试指标 ${i}`, context: 'test', family: 'test', units: 'value', chart_type: 'line',
      priority: i, update_every: 1, dimensions: [{ id: 'value', name: 'value' }], last_entry: Math.floor(Date.now() / 1000) }]
  }))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts } }))
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: {
    units: 'value', dimension_ids: ['value'], result: { data: [[1, 1], [2, 2], [3, 1]] },
  } }))
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('.card')).toHaveCount(12)
  await expect(page.locator('.card canvas').first()).toBeVisible()
}

test('continuous search keeps mounted charts until the query settles and clear is immediate', async ({ page }) => {
  await metrics(page)
  await page.clock.install()
  await page.clock.pauseAt(new Date())
  const canvas = await page.locator('.card canvas').first().elementHandle()
  const input = page.getByRole('searchbox', { name: '筛选图表', exact: true })
  await input.fill('test.c')
  await page.clock.runFor(100)
  await input.fill('test.chart19')
  await page.clock.runFor(100)
  await expect(page.locator('.card')).toHaveCount(12)
  expect(await canvas!.evaluate(el => el.isConnected)).toBe(true)
  await page.clock.runFor(60)
  await expect(page.locator('.card')).toHaveCount(1)
  await expect(page.locator('.card .id')).toHaveText('test.chart19')
  await input.press('Escape')
  await expect(input).toHaveValue('')
  await expect(input).toBeFocused()
  await expect(page.locator('.card')).toHaveCount(12)
})

test('composition keeps results stable and quick pagination is reachable above the charts', async ({ page }) => {
  await metrics(page)
  const pager = page.getByRole('navigation', { name: '快速分页', exact: true })
  await pager.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(pager).toContainText('2 / 3')
  await page.clock.install()
  await page.clock.pauseAt(new Date())
  const input = page.getByRole('searchbox', { name: '筛选图表', exact: true })
  await input.dispatchEvent('compositionstart')
  await input.evaluate(el => { (el as HTMLInputElement).value = '测试指标 29'; el.dispatchEvent(new Event('input', { bubbles: true })) })
  await page.clock.runFor(500)
  await expect(pager).toContainText('2 / 3')
  await expect(page.locator('.card')).toHaveCount(12)
  await input.dispatchEvent('compositionend')
  await page.clock.runFor(160)
  await expect(page.locator('.card')).toHaveCount(1)
  await expect(page.locator('.card .id')).toHaveText('test.chart29')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('workspace navigation resets the reading position and search shortcut respects editors', async ({ page }, testInfo) => {
  await metrics(page)
  await page.screenshot({ path: testInfo.outputPath('metrics.png'), fullPage: false })
  await page.locator('#workspace-main').focus()
  await page.keyboard.press('/')
  const input = page.getByRole('searchbox', { name: '筛选图表', exact: true })
  await expect(input).toBeFocused()
  await page.keyboard.type('/')
  await expect(input).toHaveValue('/')
  await input.press('Escape')
  // Preserve the long document during navigation so browser height clamping
  // cannot accidentally pass the scroll-reset assertion.
  await page.locator('#workspace-main').evaluate(el => { el.style.minHeight = '6000px' })
  await page.evaluate(() => scrollTo(0, 1600))
  await workspace(page, 'operations')
  await expect.poll(() => page.locator('#workspace-main').evaluate(el => Math.abs(el.getBoundingClientRect().top))).toBeLessThan(90)
  await expect(page.locator('#workspace-main')).toBeFocused()
})

test('a delayed module shows a loading surface that disappears when its chunk resolves', async ({ page }) => {
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/assets/MetricsWorkspace-*.js', async route => { await gate; await route.continue() })
  await page.goto('/?view=charts&token=browser-test-token')
  try {
    await expect(page.getByRole('status', { name: '正在加载工作区', exact: true })).toBeVisible()
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await expect(page.locator('.skeleton-card').first()).toHaveCSS('animation-name', 'none')
  } finally { release() }
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  await expect(page.locator('.module-loading')).toHaveCount(0)
})
