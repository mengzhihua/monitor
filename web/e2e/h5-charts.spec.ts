import { test, expect, type Page, type Locator } from '@playwright/test'
import { encodeTimeline, timelineKey } from '../src/chartTimeline'

const end = 1767323045
const chart = {
  id: 'test.h5_touch', title: '手机触摸采样查看', context: 'test', family: 'test', units: 'MiB',
  chart_type: 'stacked', priority: 1, update_every: 1, last_entry: end,
  dimensions: [
    { id: 'zero', name: '零值采样' },
    { id: 'missing', name: '末行缺失' },
    { id: 'long', name: '长维度_' + 'very_long_metric_dimension_'.repeat(12) },
  ],
}

async function prepare(page: Page) {
  let requests = 0
  await page.addInitScript(({ key, value }) => sessionStorage.setItem(key, value), {
    key: timelineKey, value: encodeTimeline({ window: 300, end }),
  })
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { [chart.id]: chart } } }))
  await page.route('**/api/v1/data?*', route => {
    requests++
    return route.fulfill({ json: {
      units: chart.units, dimension_ids: ['zero', 'missing', 'long'], view_update_every: 5,
      result: { data: [[end - 5, 17, 23, 1], [end, 0, null, -4]] },
    } })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.getByLabel('已加载末行采样')).toBeVisible()
  return () => requests
}

async function expectTouchTarget(locator: Locator) {
  const box = await locator.boundingBox()
  expect(box).not.toBeNull()
  expect(box!.height).toBeGreaterThanOrEqual(44)
  expect(box!.width).toBeGreaterThanOrEqual(44)
}

for (const width of [320, 390, 430]) {
  test(`touch chart at ${width}px reads real last rows and contains controls and long legends`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: 740 })
    const requests = await prepare(page)
    const sample = page.getByLabel('已加载末行采样')
    const card = page.locator('.card').filter({ has: sample })
    await expect(sample.locator('strong')).toHaveText('0.00')
    await expect(sample.locator('time')).toHaveAttribute('datetime', new Date(end * 1000).toISOString())
    await expect(sample).toContainText('采样间隔 5 秒')
    const before = requests()
    await sample.getByLabel('查看维度').selectOption('missing')
    await expect(sample.locator('strong')).toHaveText('缺失')
    await expect(sample).toContainText('不按 0 处理')
    await sample.getByLabel('查看维度').selectOption('long')
    await expect(sample.locator('strong')).toHaveText('-4.00')
    expect(requests()).toBe(before)
    await expectTouchTarget(sample.getByLabel('查看维度'))
    await expect(sample.getByLabel('查看维度')).toHaveCSS('font-size', '16px')
    await expectTouchTarget(card.getByRole('button', { name: '放大图表', exact: true }))
    await expectTouchTarget(card.getByRole('button', { name: '导出 CSV', exact: true }))
    await expectTouchTarget(card.locator('.metric-help summary'))
    const legend = card.locator('.u-legend')
    expect(await legend.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true)
    expect(await card.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await sample.scrollIntoViewIfNeeded()
    await page.screenshot({ path: info.outputPath(`touch-chart-${width}.png`) })

    await card.getByRole('button', { name: '放大图表', exact: true }).click()
    const dialog = page.getByRole('dialog')
    const detailSample = dialog.getByLabel('已加载末行采样')
    await expect(detailSample).toBeVisible()
    await expect(detailSample.locator('strong')).toHaveText('0.00')
    expect((await dialog.boundingBox())!.width).toBe(width)
    expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    for (const button of await dialog.getByRole('button').all()) await expectTouchTarget(button)
    await expectTouchTarget(dialog.getByLabel('结束时间（本地）'))
    await expect(dialog.getByLabel('结束时间（本地）')).toHaveCSS('font-size', '16px')
    await dialog.getByRole('button', { name: '关闭放大图表', exact: true }).click()
    await expect(dialog).not.toBeVisible()

    const timeline = page.getByRole('region', { name: '统一查看时间' })
    await timeline.scrollIntoViewIfNeeded()
    for (const button of await timeline.getByRole('button').all()) await expectTouchTarget(button)
    await expectTouchTarget(timeline.getByLabel('看板结束时间（本地）'))
    await expect(timeline.getByLabel('看板结束时间（本地）')).toHaveCSS('font-size', '16px')
  })
}
