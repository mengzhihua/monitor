import { test, expect, type Download, type Page } from '@playwright/test'
import { draftKey, decodeDraft, encodeDraft } from '../src/dashboardDraft'
import { newBoard, storageKey } from '../src/dashboardConfig'
import { chartCSV } from '../src/chartExport'
import type { Chart } from '../src/api'

async function open(page: Page) {
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  return page.getByLabel('常用聚合看板', { exact: true })
}
async function content(download: Download) {
  const stream = await download.createReadStream()
  let result = ''
  for await (const chunk of stream!) result += chunk.toString('utf8')
  return result
}
const sample: Chart = { id: 'test.export', context: 'test.export', title: 'Export sample', units: 'MiB', chart_type: 'stacked', priority: 1, update_every: 1, plugin: 'test', module: 'test', family: 'test', labels: null, first_entry: 0, last_entry: Math.floor(Date.now()/1000), dimensions: [{ id: 'read', name: '读取', algorithm: 'absolute' }, { id: 'write', name: '写入', algorithm: 'absolute' }] }

test('unfinished drafts survive reload and workspace changes and disappear only on save or cancel', async ({ page }) => {
  const panel = await open(page)
  await panel.getByRole('button', { name: '新建看板', exact: true }).click()
  await panel.getByLabel('看板说明', { exact: true }).fill('名称和分组还没有选择')
  await page.reload()
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  await expect(panel.getByLabel('未保存的看板草稿')).toContainText('未命名看板')
  await panel.getByRole('button', { name: '恢复草稿', exact: true }).click()
  await expect(panel.getByLabel('看板说明', { exact: true })).toHaveValue('名称和分组还没有选择')
  await panel.getByLabel('看板名称', { exact: true }).fill('恢复后完成')
  await panel.getByRole('checkbox', { name: 'CPU 与负载', exact: true }).check()
  await page.getByRole('button', { name: '运维总览', exact: true }).click()
  await page.getByRole('button', { name: '指标图表', exact: true }).click()
  await panel.getByRole('button', { name: '恢复草稿', exact: true }).click()
  await expect(panel.getByLabel('看板名称', { exact: true })).toHaveValue('恢复后完成')
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  expect(await page.evaluate(key => sessionStorage.getItem(key), draftKey)).toBeNull()
  await panel.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await panel.getByLabel('看板名称', { exact: true }).fill('放弃修改')
  await panel.getByRole('button', { name: '取消编辑', exact: true }).click()
  expect(await page.evaluate(key => sessionStorage.getItem(key), draftKey)).toBeNull()
  await expect(panel.getByRole('heading', { level: 2, name: '恢复后完成', exact: true })).toBeVisible()
})

test('resuming a draft preserves the original conflict baseline', async ({ page }) => {
  const panel = await open(page)
  await panel.getByRole('button', { name: '复制为个人看板', exact: true }).click()
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  await panel.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await panel.getByLabel('看板名称', { exact: true }).fill('恢复的旧草稿')
  await page.evaluate(key => {
    const data = JSON.parse(localStorage.getItem(key)!)
    data.boards[0].title = '外部已更新'
    localStorage.setItem(key, JSON.stringify(data))
  }, storageKey)
  await page.reload()
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  await panel.getByRole('button', { name: '恢复草稿', exact: true }).click()
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('其他标签页修改或删除')
  await expect(panel.getByLabel('看板名称', { exact: true })).toHaveValue('恢复的旧草稿')
  await panel.getByRole('button', { name: '另存为新看板', exact: true }).click()
  expect(await page.evaluate(key => JSON.parse(localStorage.getItem(key)!).boards.map((b: {title: string}) => b.title), storageKey)).toEqual(['外部已更新', '恢复的旧草稿'])
})

test('zoom dialog resizes the chart, exports raw stacked values and restores keyboard focus', async ({ page }, info) => {
  const now = Math.floor(Date.now()/1000)
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { [sample.id]: sample } } }))
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: { units: sample.units, dimension_ids: ['read','write'], result: { data: [[now-20,1.25,3],[now-10,null,4],[now,-2,null]] } } }))
  await page.goto('/?view=charts&token=browser-test-token')
  const source = page.locator('.card').first()
  await expect(source.locator('canvas')).toBeVisible()
  const normalHeight = (await source.locator('canvas').boundingBox())!.height
  await source.getByRole('button', { name: '放大图表', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Export sample 放大图表', exact: true })
  await expect(dialog).toBeVisible()
  await expect(dialog.locator('canvas')).toBeVisible()
  expect((await dialog.locator('canvas').boundingBox())!.height).toBeGreaterThan(normalHeight)
  const download = page.waitForEvent('download')
  await dialog.getByRole('button', { name: '导出 CSV', exact: true }).click()
  const csv = await content(await download)
  expect(csv).toContain('读取 [read] (MiB)')
  expect(csv).toContain(',1.25,3\r\n')
  expect(csv).toContain(',,4\r\n')
  expect(csv).toContain(',-2,\r\n')
  expect(csv).toContain('"local","test.export"')
  expect((await dialog.boundingBox())!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  await page.screenshot({ path: info.outputPath('chart-zoom.png') })
  await page.keyboard.press('Escape')
  await expect(dialog).not.toBeVisible()
  await expect(source.getByRole('button', { name: '放大图表', exact: true })).toBeFocused()
  await expect(page.locator('.card')).toHaveCount(1)
})

test('node switch rebuilds same-ID charts and labels CSV with the actual queried node', async ({ page }) => {
  await page.route('**/api/v1/info', async route => { const r = await route.fetch(); await route.fulfill({ json: { ...await r.json(), mode: 'hub' } }) })
  await page.route('**/api/v1/nodes', route => route.fulfill({ json: { nodes: [
    { id: '', hostname: 'Local export', local: true, status: 'live', alarms: {}, charts_count: 1 },
    { id: 'remote-export', hostname: 'Remote export', local: false, status: 'live', alarms: {}, charts_count: 1 },
  ] } }))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: { [sample.id]: sample } } }))
  await page.route('**/api/v1/data?*', route => route.fulfill({ json: { units: sample.units, dimension_ids: ['read','write'], result: { data: [[Math.floor(Date.now()/1000), new URL(route.request().url()).searchParams.get('node') ? 99 : 1, 0]] } } }))
  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
  await page.locator('.node-select-btn').click()
  await page.getByRole('option', { name: /Remote export/ }).click()
  await expect(page.getByRole('button', { name: '导出 CSV', exact: true })).toBeEnabled()
  const download = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出 CSV', exact: true }).click()
  const csv = await content(await download)
  expect(csv).toContain('"remote-export","test.export"')
  expect(csv).toContain(',99,0\r\n')
})

test('draft validation accepts unfinished fields and CSV escapes text without changing numeric values', () => {
  const b = newBoard()
  expect(decodeDraft(encodeDraft({ version: 1, board: b, expected: null })).board).toEqual(b)
  expect(() => decodeDraft('{broken')).toThrow()
  expect(() => decodeDraft(JSON.stringify({ version: 2, board: b, expected: null }))).toThrow()
  const csv = chartCSV({ ...sample, dimensions: [{ id: 'read', name: '=SUM(A1)', algorithm: 'absolute' }] }, '@node', [1,2,3], ['read'], [[-2, null, Number.NaN]])
  expect(csv).toContain('"\'=SUM(A1) [read] (MiB)"')
  expect(csv).toContain('"\'@node"')
  expect(csv).toContain(',-2\r\n')
  expect(csv).not.toContain('NaN')
})
