import { test, expect } from '@playwright/test'
import type { Chart, DataResponse } from '../src/api'

const end = 1767323045
const chart: Chart = {
  id: 'test.unit-consistency', title: '采样单位一致性', context: 'test', family: 'test', units: 'MiB',
  chart_type: 'line', priority: 1, update_every: 1,
  dimensions: [{ id: 'value', name: '内存', algorithm: 'absolute' }],
  plugin: 'test', module: 'test', labels: null, first_entry: end-600, last_entry: end,
}

test('incompatible or missing current units clear old samples and block export and comparison until retry succeeds', async ({ page }) => {
  await page.clock.setFixedTime(new Date(end*1000))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { [chart.id]: chart } } }))
  let units: string | undefined = chart.units
  const queries: URL[] = []
  await page.route('**/api/v1/data?*', route => {
    const url = new URL(route.request().url()); queries.push(url)
    const before = Number(url.searchParams.get('before')) || end
    const previous = before === end-300
    const result: Omit<DataResponse, 'units'> & { units?: string } = {
      id: chart.id, units: previous ? chart.units : units,
      after: before-300, before, view_update_every: 1,
      dimension_ids: ['value'], dimension_names: ['内存'],
      result: { labels: ['time', '内存'], data: [[before-1, previous ? 10 : 20], [before, previous ? 10 : 20]] },
    }
    return route.fulfill({ json: result })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '放大图表', exact: true }).click()
  const dialog = page.getByRole('dialog')
  const summary = dialog.getByRole('table', { name: '各维度采样统计' })
  const comparison = dialog.getByRole('region', { name: '上一时段对比' })
  await expect(summary).toBeVisible()
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()

  units = '%'
  await dialog.getByRole('button', { name: '对比上一时段', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('采样单位与图表定义不一致')
  await expect(summary).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeDisabled()
  await expect(comparison).toHaveCount(0)
  expect(queries.some(url => url.searchParams.get('before') === String(end-300))).toBe(false)

  units = undefined
  const beforeMissing = queries.length
  await dialog.getByRole('button', { name: '重试加载', exact: true }).click()
  await expect.poll(() => queries.length).toBeGreaterThan(beforeMissing)
  await expect(dialog.getByRole('alert')).toContainText('采样单位与图表定义不一致')
  await expect(dialog.getByRole('button', { name: '导出 CSV', exact: true })).toBeDisabled()
  await expect(comparison).toHaveCount(0)

  units = chart.units
  await dialog.getByRole('button', { name: '重试加载', exact: true }).click()
  await expect(summary).toBeVisible()
  await expect(comparison.getByRole('row', { name: /^内存/ }).locator('td')).toHaveText(['20', '10', '+10', '+100%', '2/2 · 2/2', '—'])
  const download = page.waitForEvent('download')
  await comparison.getByRole('button', { name: '导出时段对比 CSV', exact: true }).click()
  const stream = await (await download).createReadStream(); let csv = ''
  for await (const chunk of stream!) csv += chunk.toString('utf8')
  expect(csv).toContain('"local","test.unit-consistency","MiB"')
  expect(csv).toContain('"value","内存",20,10,10,100,2,2,2,2,')
})

test('node changes finish even when browser persistence fails and do not retain the previous node data', async ({ page }) => {
  const errors: string[] = [], sockets: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  page.on('websocket', socket => sockets.push(socket.url()))
  await page.route('**/api/v1/info', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), mode: 'hub' } })
  })
  await page.route('**/api/v1/nodes', route => route.fulfill({ json: { nodes: [
    { id: '', hostname: '本地持久化测试', local: true, status: 'live', alarms: {}, charts_count: 1 },
    { id: 'remote-unit', hostname: '远程持久化测试', local: false, status: 'live', alarms: {}, charts_count: 1 },
  ] } }))
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/v1/charts*', async route => {
    const remote = new URL(route.request().url()).searchParams.get('node') === 'remote-unit'
    if (remote) await gate
    await route.fulfill({ json: { charts: { [chart.id]: { ...chart, title: remote ? '远程节点采样' : '本地节点采样' } } } })
  })
  await page.route('**/api/v1/alarms?*', route => {
    const remote = new URL(route.request().url()).searchParams.get('node') === 'remote-unit'
    return route.fulfill({ json: { alarms: remote ? {} : { old: { id: 1, name: '本地告警', chart: chart.id, status: 'WARNING' } } } })
  })
  await page.route('**/api/v1/alarm_log?*', route => route.fulfill({ json: [] }))
  await page.route('**/api/v1/data?*', route => {
    const remote = new URL(route.request().url()).searchParams.get('node') === 'remote-unit'
    return route.fulfill({ json: { units: chart.units, dimension_ids: ['value'], result: { data: [[end, remote ? 99 : 1]] } } })
  })
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('.card .title')).toHaveText('本地节点采样')
  await expect(page.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
  await expect(page.locator('button[title="告警"]')).toContainText('1')
  await page.evaluate(() => {
    const set = Storage.prototype.setItem, remove = Storage.prototype.removeItem
    Storage.prototype.setItem = function(key, value) {
      if (key === 'monitor.node') throw new DOMException('No space', 'QuotaExceededError')
      return set.call(this, key, value)
    }
    Storage.prototype.removeItem = function(key) {
      if (key === 'monitor.node') throw new DOMException('Denied', 'SecurityError')
      return remove.call(this, key)
    }
  })
  await page.locator('.node-select-btn').click()
  await page.getByRole('option', { name: /远程持久化测试/ }).click()
  await expect(page.locator('.node-notice')).toContainText('本次切换仍然生效')
  await expect(page.locator('.card')).toHaveCount(0)
  await expect(page.locator('button[title="告警"]')).toContainText('0')
  release()
  await expect(page.locator('.card .title')).toHaveText('远程节点采样')
  await expect(page.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
  await expect.poll(() => sockets.some(url => new URL(url).searchParams.get('node') === 'remote-unit')).toBe(true)
  const download = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出 CSV', exact: true }).click()
  const stream = await (await download).createReadStream(); let csv = ''
  for await (const chunk of stream!) csv += chunk.toString('utf8')
  expect(csv).toContain('"remote-unit","test.unit-consistency"')
  expect(csv).toContain(',99\r\n')
  await page.locator('.node-select-btn').click()
  await page.getByRole('option', { name: /本地持久化测试/ }).click()
  await expect(page.locator('.card .title')).toHaveText('本地节点采样')
  await expect(page.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
  expect(errors).toEqual([])
})
