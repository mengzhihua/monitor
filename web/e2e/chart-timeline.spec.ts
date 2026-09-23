import { test, expect, type Page } from '@playwright/test'
import { decodeTimeline, encodeTimeline, timelineKey } from '../src/chartTimeline'
import { newBoard, encodeBoards, storageKey } from '../src/dashboardConfig'
import { preferencesKey } from '../src/dashboardStorage'

const fixed = 1767323045
const chartList = Object.fromEntries(['system.cpu','system.ram'].map(id => [id, {
  id, title: id, context: id, family: 'test', units: '%', chart_type: 'line', priority: 1, update_every: 1,
  dimensions: [{ id: 'value', name: '数值' }], last_entry: 1,
}]))
async function fixtures(page: Page) {
  const calls: URL[] = []
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: chartList } }))
  await page.route('**/api/v1/data?*', route => {
    const url = new URL(route.request().url()); calls.push(url)
    const end = Number(url.searchParams.get('before')) || Math.floor(Date.now()/1000)
    return route.fulfill({ json: { units: '%', dimension_ids: ['value'], result: { data: [[end-1,1],[end,2]] } } })
  })
  return calls
}
async function seed(page: Page, range = 300) {
  await page.addInitScript(({ key, value }) => { sessionStorage.setItem(key, value) }, { key: timelineKey, value: encodeTimeline({ window: range, end: fixed }) })
}

test('all charts freeze at one endpoint, survive reload, and restore live queries', async ({ page }, info) => {
  await page.clock.install()
  const calls = await fixtures(page)
  await page.goto('/?view=charts&token=browser-test-token')
  const control = page.getByRole('region', { name: '统一查看时间' })
  await control.getByRole('button', { name: '冻结整个看板', exact: true }).click()
  const saved = await page.evaluate(key => JSON.parse(sessionStorage.getItem(key)!), timelineKey)
  expect(saved.end).toBeGreaterThan(0)
  for (const card of await page.locator('.card').all()) await card.scrollIntoViewIfNeeded()
  await expect.poll(() => new Set(calls.filter(u => u.searchParams.get('before') === String(saved.end)).map(u => u.searchParams.get('chart'))).size).toBe(2)
  expect(calls.filter(u => u.searchParams.get('before') === String(saved.end)).every(u => Number(u.searchParams.get('after')) === saved.end-300)).toBe(true)
  const before = calls.length
  await page.clock.fastForward(65000)
  expect(calls).toHaveLength(before)
  await page.reload()
  await expect(control).toContainText('历史查看')
  await expect(page.locator('.history-badge')).toHaveCount(2)
  expect(await page.evaluate(key => JSON.parse(sessionStorage.getItem(key)!).end, timelineKey)).toBe(saved.end)
  await control.scrollIntoViewIfNeeded()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('unified-history.png') })
  await control.getByRole('button', { name: '整个看板返回实时', exact: true }).click()
  await page.locator('.card').first().scrollIntoViewIfNeeded()
  await expect.poll(() => calls.at(-1)?.searchParams.get('after')).toBe('-300')
  await expect(page.locator('.history-badge')).toHaveCount(0)
})

test('personal duration and zoom inherit the endpoint while local zoom navigation remains independent', async ({ page }) => {
  await seed(page)
  const board = { ...newBoard(), title: '一小时排查', groupIds: ['compute','memory'], windowSec: 3600 }
  await page.addInitScript(({ config, prefs, boardJSON, id }) => {
    localStorage.setItem(config, boardJSON)
    localStorage.setItem(prefs, JSON.stringify({ selected: id, favorites: [], collapsed: true }))
  }, { config: storageKey, prefs: preferencesKey, boardJSON: encodeBoards([board]), id: board.id })
  const calls = await fixtures(page)
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  const panel = page.getByLabel('常用聚合看板', { exact: true })
  await panel.locator('.card').first().getByRole('button', { name: '放大图表', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('table', { name: '各维度采样统计' })).toBeVisible()
  expect(calls.at(-1)?.searchParams.get('before')).toBe(String(fixed))
  expect(calls.at(-1)?.searchParams.get('after')).toBe(String(fixed-3600))
  await dialog.getByRole('button', { name: '上一时段', exact: true }).click()
  await expect.poll(() => calls.at(-1)?.searchParams.get('before')).toBe(String(fixed-3600))
  await dialog.getByRole('button', { name: '关闭放大图表', exact: true }).click()
  await expect.poll(() => calls.at(-1)?.searchParams.get('before')).toBe(String(fixed))
  expect(await page.evaluate(key => JSON.parse(sessionStorage.getItem(key)!).end, timelineKey)).toBe(fixed)
  const control = page.getByRole('region', { name: '统一查看时间' })
  await control.getByRole('button', { name: '整个看板上一时段', exact: true }).click()
  await panel.locator('.card').first().scrollIntoViewIfNeeded()
  await expect.poll(() => calls.at(-1)?.searchParams.get('before')).toBe(String(fixed-300))
  expect(calls.at(-1)?.searchParams.get('after')).toBe(String(fixed-3900))
})

test('node switches and global range changes retain the fixed endpoint', async ({ page }) => {
  await seed(page)
  const calls = await fixtures(page)
  await page.route('**/api/v1/info', async route => { const response = await route.fetch(); await route.fulfill({ json: { ...await response.json(), mode: 'hub' } }) })
  await page.route('**/api/v1/nodes', route => route.fulfill({ json: { nodes: [
    { id: '', hostname: 'Local timeline', local: true, status: 'live', alarms: {}, charts_count: 2 },
    { id: 'history-node', hostname: 'Remote timeline', local: false, status: 'live', alarms: {}, charts_count: 2 },
  ] } }))
  await page.goto('/?view=charts&token=browser-test-token')
  await page.locator('.node-select-btn').click()
  await page.getByRole('option', { name: /Remote timeline/ }).click()
  await page.locator('.controls select').selectOption('900')
  await page.locator('.card').first().scrollIntoViewIfNeeded()
  await expect.poll(() => calls.at(-1)?.searchParams.get('after')).toBe(String(fixed-900))
  expect(calls.at(-1)?.searchParams.get('before')).toBe(String(fixed))
  expect(calls.at(-1)?.searchParams.get('node')).toBe('history-node')
})

test('current operations drill-down exits historical mode instead of showing an old incident window', async ({ page }) => {
  await seed(page)
  await page.goto('/?token=browser-test-token')
  const problem = page.locator('[data-problem-id]').filter({ hasText: 'browser_ram_notice' })
  await expect(problem).toBeVisible()
  await problem.getByRole('button', { name: '定位图表', exact: true }).click()
  await expect(page.getByRole('region', { name: '统一查看时间' })).toContainText('已返回实时')
  await expect(page.getByPlaceholder('筛选图表…')).toHaveValue('system.ram')
  expect(await page.evaluate(key => JSON.parse(sessionStorage.getItem(key)!).end, timelineKey)).toBeNull()
  await expect(page.locator('.history-badge')).toHaveCount(0)
  await page.getByRole('button', { name: '冻结整个看板', exact: true }).click()
  await page.getByRole('button', { name: '运维总览', exact: true }).click()
  await page.evaluate(key => {
    const original = Storage.prototype.setItem
    Storage.prototype.setItem = function (name, value) {
      if (name === key) throw new DOMException('quota', 'QuotaExceededError')
      return original.call(this, name, value)
    }
  }, timelineKey)
  await problem.getByRole('button', { name: '定位图表', exact: true }).click()
  await expect(page.getByRole('region', { name: '统一查看时间' })).toContainText('已返回实时')
  await expect(page.getByRole('region', { name: '统一查看时间' })).toContainText('暂存失败')
  await expect(page.locator('.history-badge')).toHaveCount(0)
})

test('corrupt persistence and future dates do not silently change the current query', async ({ page }) => {
  await page.addInitScript(key => sessionStorage.setItem(key, '{broken'), timelineKey)
  await fixtures(page)
  await page.goto('/?view=charts&token=browser-test-token')
  const control = page.getByRole('region', { name: '统一查看时间' })
  await expect(control).toContainText('无法恢复查看时间')
  expect(await page.evaluate(key => sessionStorage.getItem(key), timelineKey)).toBe('{broken')
  await control.getByRole('button', { name: '设置历史时间', exact: true }).click()
  await control.getByLabel('看板结束时间（本地）').fill('2099-01-01T00:00')
  await control.getByRole('button', { name: '应用到整个看板', exact: true }).click()
  await expect(control.getByRole('alert')).toContainText('不能晚于当前时间')
  await expect(control).toContainText('实时查看')
  await control.getByRole('button', { name: '冻结整个看板', exact: true }).click()
  expect(decodeTimeline(await page.evaluate(key => sessionStorage.getItem(key)!, timelineKey)).end).not.toBeNull()
  expect(() => decodeTimeline(JSON.stringify({ version: 1, window: 300, end: -10 }))).toThrow()
  expect(() => decodeTimeline(JSON.stringify({ version: 1, window: 0, end: null }))).toThrow()
})
