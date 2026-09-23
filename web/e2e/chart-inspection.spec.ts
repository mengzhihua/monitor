import { test, expect, type Page } from '@playwright/test'
import { historyEnd, localDateTime, summarizeSeries } from '../src/chartInspection'

const chart = { id: 'test.inspection', title: '历史排查', context: 'test', family: 'test', units: 'MiB', chart_type: 'stacked', priority: 1, update_every: 1,
  anomaly: true, dimensions: [{ id: 'a', name: '读取', anomaly: true }, { id: 'b', name: '写入' }], last_entry: 1 }
async function prepare(page: Page) {
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { [chart.id]: chart } } }))
}
async function detail(page: Page) {
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '放大图表', exact: true }).click()
  return page.getByRole('dialog')
}
function payload(before: number) {
  return { units: chart.units, dimension_ids: ['a','b'], dimension_anomaly: [100,0], result: { data: [[before-2,2,10],[before-1,null,20],[before,-4,null]] } }
}

test('fixed history uses absolute bounds, stops polling, paginates and resumes live', async ({ page }) => {
  await page.clock.install()
  await prepare(page)
  const calls: URL[] = []
  await page.route('**/api/v1/data?*', route => {
    const url = new URL(route.request().url()); calls.push(url)
    return route.fulfill({ json: payload(Number(url.searchParams.get('before')) || Math.floor(Date.now()/1000)) })
  })
  const dialog = await detail(page)
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toBeVisible()
  await dialog.getByLabel('结束时间（本地）').fill('2026-01-02T03:04:05')
  const end = await page.evaluate(() => new Date('2026-01-02T03:04:05').getTime()/1000)
  await dialog.getByRole('button', { name: '查看该时段', exact: true }).click()
  await expect.poll(() => calls.at(-1)?.searchParams.get('before')).toBe(String(end))
  expect(calls.at(-1)?.searchParams.get('after')).toBe(String(end-300))
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toBeVisible()
  await expect(dialog.locator('.head .anom')).toHaveCount(0)
  await expect(dialog.getByText('数据过期', { exact: true })).toHaveCount(0)
  const total = calls.length
  await page.clock.fastForward(65000)
  expect(calls).toHaveLength(total)
  await dialog.getByRole('button', { name: '上一时段', exact: true }).click()
  await expect.poll(() => calls.at(-1)?.searchParams.get('before')).toBe(String(end-300))
  await dialog.getByRole('button', { name: '下一时段', exact: true }).click()
  await expect.poll(() => calls.at(-1)?.searchParams.get('before')).toBe(String(end))
  await dialog.getByRole('button', { name: '返回实时', exact: true }).click()
  await expect.poll(() => calls.at(-1)?.searchParams.get('after')).toBe('-300')
  expect(calls.at(-1)?.searchParams.get('before')).toBe('0')
  await expect(dialog.locator('.head .anom')).toHaveCount(1)
})

test('summary uses raw values, excludes missing values and fits the mobile dialog', async ({ page }, info) => {
  await prepare(page)
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: payload(Math.floor(Date.now()/1000)) }))
  const dialog = await detail(page)
  const table = dialog.getByRole('table', { name: '各维度采样统计' })
  await expect(table.getByRole('row', { name: /^读取/ }).locator('td')).toHaveText(['-4.00','-4.00','2.00','-1.00','2 / 3'])
  await expect(table.getByRole('row', { name: /^写入/ }).locator('td')).toHaveText(['—','10.0','20.0','15.0','2 / 3'])
  await table.scrollIntoViewIfNeeded()
  expect((await dialog.boundingBox())!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('history-summary.png') })
})

test('invalid time preserves the query; failed and empty history cannot export old samples and retry recovers', async ({ page }) => {
  await prepare(page)
  let fail = false, empty = false, count = 0
  await page.route('**/api/v1/data?*', route => {
    count++
    if (fail) return route.fulfill({ status: 503, body: '测试采样暂不可用' })
    return route.fulfill({ json: empty ? { units: chart.units, dimension_ids: ['a','b'], result: { data: [] } } : payload(Math.floor(Date.now()/1000)) })
  })
  const dialog = await detail(page)
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toBeVisible()
  const before = count
  await dialog.getByLabel('结束时间（本地）').fill('2099-01-01T00:00')
  await dialog.getByRole('button', { name: '查看该时段', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('不能晚于当前时间')
  expect(count).toBe(before)
  fail = true
  await dialog.getByLabel('结束时间（本地）').fill('2026-01-02T03:04:05')
  await dialog.getByRole('button', { name: '查看该时段', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('采样加载失败')
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeDisabled()
  fail = false; empty = true
  await dialog.getByRole('button', { name: '重试加载', exact: true }).click()
  await expect(dialog.getByRole('status')).toContainText('没有采样')
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeDisabled()
  empty = false
  await dialog.getByRole('button', { name: '上一时段', exact: true }).click()
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toBeVisible()
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
})

test('date validation and statistics preserve zeros, missing rows and finite numeric boundaries', () => {
  const now = Math.floor(Date.now()/1000)
  expect(historyEnd(localDateTime(now), 300, now)).toBe(now)
  expect(() => historyEnd('2026-02-30T12:00', 300, now)).toThrow()
  expect(() => historyEnd('1970-01-01T00:00', 86400, now)).toThrow()
  expect(() => historyEnd(localDateTime(now+60), 300, now)).toThrow()
  expect(summarizeSeries([0,null,-4,2,NaN,Infinity])).toEqual({ count: 3, total: 6, last: null, min: -4, max: 2, mean: -2/3 })
  expect(summarizeSeries([null])).toEqual({ count: 0, total: 1, last: null, min: null, max: null, mean: null })
  expect(summarizeSeries([Number.MAX_VALUE, Number.MAX_VALUE]).mean).toBe(Number.MAX_VALUE)
})

test('fixed current time reads real embedded-server history within the requested bounds', async ({ page }) => {
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByPlaceholder('筛选图表…').fill('system.ram')
  await page.getByRole('button', { name: '放大图表', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toBeVisible()
  const response = page.waitForResponse(r => {
    const url = new URL(r.url())
    return url.pathname === '/api/v1/data' && Number(url.searchParams.get('before')) > 0
  })
  await dialog.getByRole('button', { name: '固定当前时间', exact: true }).click()
  const result = await response
  expect(result.ok()).toBe(true)
  const url = new URL(result.url()), body = await result.json()
  expect(body.result.data.length).toBeGreaterThan(0)
  expect(body.result.data.every((row: number[]) => row[0]! >= Number(url.searchParams.get('after')) && row[0]! <= Number(url.searchParams.get('before')))).toBe(true)
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
})
