import { test, expect, type Page } from '@playwright/test'
import { compareSeries, comparisonCSV } from '../src/chartComparison'
import { summarizeSeries } from '../src/chartInspection'
import type { Chart, DataResponse } from '../src/api'

const end = 1767323045
const chart: Chart = { id: 'test.comparison', title: '时段对比测试', context: 'test', family: 'test', units: 'MiB', chart_type: 'stacked', priority: 1, update_every: 1,
  dimensions: ['a','b','zero','negative','missing'].map((id, i) => ({ id, name: ['读取','写入','零基准','负基准','缺失'][i]!, algorithm: 'absolute' })), plugin: 'test', module: 'test', labels: null, first_entry: 0, last_entry: end }
function data(previous: boolean, before = end): DataResponse {
  return { id: chart.id, units: 'MiB', after: before-300, before, view_update_every: 1, dimension_names: [],
    dimension_ids: previous ? ['b','a','negative','zero','missing'] : ['a','b','zero','negative','missing'],
    result: { labels: [], data: previous ? [[before-1,4,10,-6,0,2],[before,4,10,-2,0,4]] : [[before-1,10,6,0,-4,null],[before,30,10,2,-2,null]] } }
}
async function charts(page: Page) {
  await page.clock.setFixedTime(new Date(end*1000))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { [chart.id]: chart } } }))
}
async function open(page: Page) {
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '放大图表', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toBeVisible()
  await dialog.getByRole('button', { name: '对比上一时段', exact: true }).click()
  return { dialog, comparison: dialog.getByRole('region', { name: '上一时段对比' }) }
}

test('comparison freezes only the detail, pairs reordered dimensions and exports both periods', async ({ page }, info) => {
  await charts(page)
  const queries: URL[] = []
  await page.route('**/api/v1/data?*', route => {
    const url = new URL(route.request().url()); queries.push(url)
    const before = Number(url.searchParams.get('before')) || end
    return route.fulfill({ json: data(before === end-300, before) })
  })
  const { dialog, comparison } = await open(page)
  const table = comparison.getByRole('table', { name: '各维度时段对比' })
  await expect(table.getByRole('row', { name: /^读取/ }).locator('td')).toHaveText(['20','10','+10','+100%','2/2 · 2/2','—'])
  await expect(table.getByRole('row', { name: /^写入/ }).locator('td')).toHaveText(['8','4','+4','+100%','2/2 · 2/2','—'])
  await expect(table.getByRole('row', { name: /^零基准/ })).toContainText('仅比较差值')
  await expect(table.getByRole('row', { name: /^负基准/ })).toContainText('仅比较差值')
  await expect(table.getByRole('row', { name: /^缺失/ })).toContainText('本段无有效采样')
  const previous = queries.find(url => url.searchParams.get('before') === String(end-300))!
  expect(previous.searchParams.get('after')).toBe(String(end-600))
  expect(previous.searchParams.get('points')).toBe('300')
  await expect(page.getByRole('region', { name: '统一查看时间' })).toContainText('实时查看')
  const download = page.waitForEvent('download')
  await comparison.getByRole('button', { name: '导出时段对比 CSV', exact: true }).click()
  const stream = await (await download).createReadStream(); let csv = ''
  for await (const chunk of stream!) csv += chunk.toString('utf8')
  expect(csv).toContain('本段开始 UTC')
  expect(csv).toContain('"local","test.comparison","MiB"')
  expect(csv).toContain('"a","读取",20,10,10,100,2,2,2,2,')
  expect(csv).toContain(new Date((end-600)*1000).toISOString())
  await table.scrollIntoViewIfNeeded()
  expect(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('period-comparison.png') })
  await dialog.getByRole('button', { name: '返回实时', exact: true }).click()
  await expect(comparison).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: '对比上一时段', exact: true })).toBeVisible()
})

test('failed and incompatible comparisons do not replace current samples and empty history stays unknown', async ({ page }) => {
  await charts(page)
  let mode = 'error'
  await page.route('**/api/v1/data?*', route => {
    const before = Number(new URL(route.request().url()).searchParams.get('before')) || end
    if (before !== end-300) return route.fulfill({ json: data(false, before) })
    if (mode === 'error') return route.fulfill({ status: 503, body: '前段查询失败' })
    const result = data(true, before)
    if (mode === 'units') result.units = '%'
    if (mode === 'empty') result.result.data = []
    return route.fulfill({ json: result })
  })
  const { dialog, comparison } = await open(page)
  await expect(comparison.getByRole('alert')).toContainText('对比加载失败')
  await expect(comparison.getByRole('button', { name: '导出时段对比 CSV', exact: true })).toBeDisabled()
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
  mode = 'units'
  await comparison.getByRole('button', { name: '重试时段对比', exact: true }).click()
  await expect(comparison.getByRole('alert')).toContainText('单位不一致')
  mode = 'empty'
  await comparison.getByRole('button', { name: '重试时段对比', exact: true }).click()
  await expect(comparison.getByRole('status')).toContainText('不会按 0 处理')
  await expect(comparison.getByRole('row', { name: /^读取/ }).locator('td')).toHaveText(['20','—','—','—','2/2 · 0/0','前段无有效采样'])
})

test('closing an in-flight comparison aborts it and prevents stale results from resurfacing', async ({ page }) => {
  await charts(page)
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const errors: string[] = [], failed: string[] = []
  page.on('pageerror', e => errors.push(e.message))
  page.on('requestfailed', request => failed.push(request.url()))
  await page.route('**/api/v1/data?*', async route => {
    const before = Number(new URL(route.request().url()).searchParams.get('before')) || end
    if (before === end-300) await gate
    await route.fulfill({ json: data(before === end-300, before) }).catch(() => {})
  })
  const { dialog, comparison } = await open(page)
  await expect(comparison.getByRole('status')).toContainText('正在加载')
  await dialog.getByRole('button', { name: '关闭时段对比', exact: true }).click()
  release()
  await expect(comparison).toHaveCount(0)
  await expect.poll(() => failed.some(url => new URL(url).searchParams.get('before') === String(end-300))).toBe(true)
  await dialog.getByRole('button', { name: '对比上一时段', exact: true }).click()
  await expect(comparison.getByRole('table', { name: '各维度时段对比' })).toBeVisible()
  expect(errors).toEqual([])
})

test('comparison uses the selected node and resumes the same period after backgrounding', async ({ page }) => {
  await charts(page)
  await page.addInitScript(() => sessionStorage.setItem('monitor.node', 'comparison-node'))
  await page.route('**/api/v1/info', async route => { const result = await route.fetch(); await route.fulfill({ json: { ...await result.json(), mode: 'hub' } }) })
  await page.route('**/api/v1/nodes', route => route.fulfill({ json: { nodes: [{ id: 'comparison-node', hostname: '对比节点', status: 'live', charts_count: 1, alarms: {}, local: false }] } }))
  const calls: URL[] = []
  await page.route('**/api/v1/data?*', route => {
    const url = new URL(route.request().url()); calls.push(url)
    const before = Number(url.searchParams.get('before')) || end
    return route.fulfill({ json: data(before === end-300, before) })
  })
  const { comparison } = await open(page)
  await expect(comparison.getByRole('table', { name: '各维度时段对比' })).toBeVisible()
  expect(calls.filter(url => url.searchParams.get('before') === String(end-300)).every(url => url.searchParams.get('node') === 'comparison-node')).toBe(true)
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect(comparison.getByRole('table', { name: '各维度时段对比' })).toHaveCount(0)
  const count = calls.length
  await page.evaluate(() => { Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')) })
  await expect(comparison.getByRole('table', { name: '各维度时段对比' })).toBeVisible()
  expect(calls.length).toBeGreaterThan(count)
  expect(calls.at(-1)!.searchParams.get('before')).toBe(String(end-300))
  expect(calls.at(-1)!.searchParams.get('node')).toBe('comparison-node')
})

test('comparison math handles absent dimensions, zero and negative baselines, overflow and CSV text', () => {
  const rows = compareSeries([{ ...summarizeSeries([0,null,2]), id: 'zero', name: '=SUM(A1)', help: '' }, { ...summarizeSeries([1]), id: 'absent', name: 'missing', help: '' }], data(true))
  expect(rows[0]!.delta).toBe(1)
  expect(rows[0]!.percent).toBeNull()
  expect(rows[1]!.prior.mean).toBeNull()
  expect(rows[1]!.delta).toBeNull()
  const csv = comparisonCSV(chart, '@node', end, 300, rows)
  expect(csv).toContain('"\'@node"')
  expect(csv).toContain('"\'=SUM(A1)"')
  expect(csv).not.toContain('NaN')
  const extreme = data(true); extreme.dimension_ids = ['huge']; extreme.result.data = [[end, -Number.MAX_VALUE]]
  expect(compareSeries([{ ...summarizeSeries([Number.MAX_VALUE]), id: 'huge', name: 'huge', help: '' }], extreme)[0]!.delta).toBeNull()
})
