import { test, expect } from '@playwright/test'
import { decodeBoards, encodeBoards, newBoard, storageKey } from '../src/dashboardConfig'
import { dashboards, chartsForGroup } from '../src/dashboards'

async function open(page: import('@playwright/test').Page) {
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  return page.getByLabel('常用聚合看板', { exact: true })
}
test('personal dashboard creation, order, range, persistence, export, editing and deletion', async ({ page }, info) => {
  const panel = await open(page)
  await panel.getByRole('button', { name: '新建看板', exact: true }).click()
  const editor = panel.getByRole('form', { name: '个人看板编辑器' })
  await editor.getByLabel('看板名称', { exact: true }).fill('我的发布观察')
  await editor.getByLabel('CPU 与负载', { exact: true }).check()
  await editor.getByLabel('内存与交换', { exact: true }).check()
  await editor.getByRole('button', { name: '上移分组 memory', exact: true }).click()
  await editor.getByLabel('搜索可选图表').fill('system.cpu')
  await editor.getByRole('checkbox', { name: /system.cpu/ }).check()
  await editor.getByLabel('默认时间范围').selectOption('900')
  await editor.getByLabel('图表列数').selectOption('2')
  await editor.getByLabel('每组默认显示').selectOption('2')
  await editor.getByRole('button', { name: '保存看板', exact: true }).click()
  await expect(panel.getByRole('heading', { level: 2, name: '我的发布观察', exact: true })).toBeVisible()
  await expect(panel.locator('.board-group').first()).toHaveAttribute('aria-label', '内存与交换')
  await expect(panel.locator('.board-grid').first()).toHaveClass(/columns-2/)
  await expect(panel.locator('.board-group').first().locator('.card')).toHaveCount(2)
  await Promise.all([
    page.waitForResponse(r => r.url().includes('/api/v1/data?') && r.url().includes('after=-900')),
    panel.locator('.card').first().scrollIntoViewIfNeeded(),
  ])
  const downloaded = page.waitForEvent('download')
  await panel.getByRole('button', { name: '导出个人看板', exact: true }).click()
  const download = await downloaded
  expect(download.suggestedFilename()).toBe('monitor-dashboards.json')
  const saved = await page.evaluate(key => localStorage.getItem(key), storageKey)
  expect(decodeBoards(saved!)[0]?.groupIds).toEqual(['memory', 'compute'])
  await page.reload()
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  await panel.getByRole('button', { name: '我的看板', exact: true }).click()
  await panel.locator('.presets button').filter({ hasText: '我的发布观察' }).click()
  await expect(panel.locator('.board-group').first()).toHaveAttribute('aria-label', '内存与交换')
  await panel.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await editor.getByLabel('看板名称', { exact: true }).fill('未保存名称')
  await editor.getByRole('button', { name: '取消编辑' }).click()
  await expect(panel.getByRole('heading', { level: 2, name: '我的发布观察' })).toBeVisible()
  await panel.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await editor.getByLabel('看板名称', { exact: true }).fill('已修改看板')
  await editor.getByRole('button', { name: '保存看板', exact: true }).click()
  await panel.getByRole('button', { name: '收起样板目录' }).click()
  await expect(panel.locator('.presets')).toBeHidden()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('personal-dashboard.png'), fullPage: true })
  await panel.getByRole('button', { name: '删除个人看板', exact: true }).click()
  await panel.getByRole('button', { name: '确认删除', exact: true }).click()
  expect(await page.evaluate(key => JSON.parse(localStorage.getItem(key)!).boards.length, storageKey)).toBe(0)
})

test('copy and import are independent; invalid imports and storage failures preserve saved data', async ({ page }) => {
  const panel = await open(page)
  await panel.getByRole('button', { name: '复制为个人看板', exact: true }).click()
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  const before = await page.evaluate(key => localStorage.getItem(key), storageKey)
  await panel.getByLabel('导入看板配置').setInputFiles({ name: 'bad.json', mimeType: 'application/json', buffer: Buffer.from('{"version":99,"boards":[]}') })
  await expect(panel.getByRole('alert')).toContainText('导入失败')
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBe(before)
  await panel.getByLabel('导入看板配置').setInputFiles({ name: 'good.json', mimeType: 'application/json', buffer: Buffer.from(before!) })
  await panel.getByRole('button', { name: '确认导入所选看板', exact: true }).click()
  await expect(panel.getByRole('status')).toContainText('已导入 1 个副本')
  const boards = decodeBoards((await page.evaluate(key => localStorage.getItem(key), storageKey))!)
  expect(boards).toHaveLength(2)
  expect(boards[0]!.id).not.toBe(boards[1]!.id)
  await page.evaluate(() => { Storage.prototype.setItem = () => { throw new Error('QuotaExceededError') } })
  await panel.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await panel.getByLabel('看板名称', { exact: true }).fill('无法保存')
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('保存失败')
  await expect(panel.getByRole('form', { name: '个人看板编辑器' })).toBeVisible()
  expect(decodeBoards((await page.evaluate(key => localStorage.getItem(key), storageKey))!)).toHaveLength(2)
})

test('configuration validation rejects invalid fields and preserves absent pinned charts', () => {
  const b = { ...newBoard(), title: 'Example', chartIds: ['missing.cpu', 'system.ram'] }
  expect(decodeBoards(encodeBoards([b]))).toEqual([b])
  for (const change of [{ groupIds: ['unknown'] }, { columns: 100 }, { windowSec: -1 }, { chartIds: [] }, { title: ' ' }, { hideEmpty: 'yes' }, { limit: 0 }]) {
    expect(() => encodeBoards([{ ...b, ...change } as typeof b])).toThrow()
  }
  expect(() => encodeBoards([b, b])).toThrow()
  expect(() => decodeBoards('{')).toThrow()
  const huge = Array.from({ length: 20 }, (_, i) => ({ ...b, id: `custom-${i}`, chartIds: Array.from({ length: 100 }, (_, j) => `${j}${'长'.repeat(490)}`) }))
  expect(() => encodeBoards(huge)).toThrow('500 KB')
  expect(chartsForGroup([], { id: 'exact', title: '', hint: '', patterns: [], chartIds: b.chartIds })).toEqual([])
})

test('new presets match actual contexts and show missing groups honestly', async ({ page }) => {
  const panel = await open(page)
  for (const [id, context] of [['cloud-metrics','cloudwatch.metric'], ['documents','couchdb.activity'], ['printing','cups.jobs'], ['sessions','logind.sessions']]) {
    const board = dashboards.find(b => b.id === id)!
    expect(board.groups[0]!.patterns.some(p => p.test(context!))).toBe(true)
    await panel.getByLabel('搜索看板样板', { exact: true }).fill(board.title)
    await panel.locator('.presets button').click()
    await expect(panel.getByRole('heading', { level: 2, name: board.title, exact: true })).toBeVisible()
    await expect(panel.getByLabel(board.groups[0]!.title, { exact: true })).toContainText('当前节点尚未采集')
  }
})

test('unavailable groups can be hidden while missing pinned IDs survive editing', async ({ page }) => {
  const saved = encodeBoards([{ ...newBoard(), title: '缺失指标观察', groupIds: ['printing'], chartIds: ['not-collected.cpu'], hideEmpty: true }])
  await page.addInitScript(({ key, saved }) => localStorage.setItem(key, saved), { key: storageKey, saved })
  const panel = await open(page)
  await panel.getByRole('button', { name: '我的看板', exact: true }).click()
  await panel.locator('.presets button').click()
  await expect(panel).toContainText('已隐藏 2 个未采集分组')
  await expect(panel.locator('.board-group')).toHaveCount(0)
  await panel.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await expect(panel.getByLabel('已选图表顺序')).toContainText('not-collected.cpu（当前节点未采集）')
  await panel.getByLabel('隐藏未采集的分组').uncheck()
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  await expect(panel.locator('.board-group')).toHaveCount(2)
  await expect(panel.getByLabel('指定图表', { exact: true })).toContainText('当前节点尚未采集')
  expect(decodeBoards((await page.evaluate(key => localStorage.getItem(key), storageKey))!)[0]?.chartIds).toEqual(['not-collected.cpu'])
})

test('corrupt stored configuration does not prevent built-in dashboards from loading', async ({ page }) => {
  await page.addInitScript(key => localStorage.setItem(key, '{invalid'), storageKey)
  const panel = await open(page)
  await expect(panel.getByRole('alert')).toContainText('无法读取个人看板配置')
  await expect(panel.locator('.presets button')).toHaveCount(54)
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBe('{invalid')
})
