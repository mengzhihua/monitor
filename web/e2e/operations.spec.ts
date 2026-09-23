import { expect, test } from '@playwright/test'
import type { OperationsSnapshot } from '../src/api'

const token = 'browser-test-token'
const headers = { Authorization: `Bearer ${token}` }

test('operations uses real metrics, persists handling and drills into the right chart', async ({ page, request }, info) => {
  const errors: string[] = []
  page.on('pageerror', e => errors.push(e.message))
  await expect.poll(async () => {
    const data: OperationsSnapshot = await (await request.get('/api/v1/operations', { headers })).json()
    return data.problems.some(p => p.name === 'browser_ram_notice')
  }).toBe(true)
  let data: OperationsSnapshot = await (await request.get('/api/v1/operations', { headers })).json()
  const p = data.problems.find(p => p.name === 'browser_ram_notice')!
  expect(data.persistent).toBe(true)
  expect(data.nodes[0]?.memory.value).not.toBeNull()
  expect((await request.post('/api/v1/operations/acknowledgements', { headers, data: {
    id: p.id, action: 'unacknowledge', note: '', revision: p.handling.revision,
  } })).ok()).toBe(true)
  await page.goto('/?token=' + token)
  await expect(page.getByRole('heading', { name: '运维总览' })).toBeVisible()
  const problem = page.locator(`[data-problem-id="${p.id}"]`)
  await expect(problem).toBeVisible()
  await problem.getByRole('textbox', { name: 'browser_ram_notice 处理备注' }).fill('已检查进程内存，继续观察。')
  await problem.getByRole('button', { name: '确认问题', exact: true }).click()
  await expect(problem.locator('.ack')).toHaveText('已确认')
  await page.reload()
  await expect(problem.locator('.ack')).toHaveText('已确认')
  await problem.locator('summary').click()
  await expect(problem).toContainText('已检查进程内存，继续观察。')
  data = await (await request.get('/api/v1/operations', { headers })).json()
  expect(data.problems.find(x => x.id === p.id)?.handling.acknowledged).toBe(true)
  await page.getByLabel('只看待确认').check()
  await expect(problem).toHaveCount(0)
  await page.getByLabel('只看待确认').uncheck()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('operations-v2.png'), fullPage: true })
  await problem.getByRole('button', { name: '定位图表' }).click()
  await expect(page.getByPlaceholder('筛选图表…')).toHaveValue('system.ram')
  await expect(page.locator('.card')).toHaveCount(1)
  await expect(page.locator('.card canvas').first()).toBeVisible()
  expect(errors).toEqual([])
})

test('saved views survive reload, export a real snapshot and stale refresh stays explicit', async ({ page }) => {
  await page.goto('/?token=' + token)
  await expect(page.getByRole('heading', { name: '运维总览' })).toBeVisible()
  await page.getByLabel('搜索主机或问题').fill('browser-test')
  await page.getByLabel('视图名称', { exact: true }).fill('值班主机')
  await page.getByRole('button', { name: '保存视图', exact: true }).click()
  await page.reload()
  await page.getByRole('button', { name: '值班主机', exact: true }).click()
  await expect(page.getByLabel('搜索主机或问题')).toHaveValue('browser-test')
  const download = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出当前快照' }).click()
  expect((await download).suggestedFilename()).toMatch(/^monitor-operations-\d+\.json$/)
  await page.route('**/api/v1/operations', route => route.abort())
  await page.getByRole('button', { name: '刷新总览' }).click()
  await expect(page.getByRole('alert')).toContainText('上次成功的数据')
  await expect(page.locator('.node-tile').first()).toContainText('browser-test')
  await page.unroute('**/api/v1/operations')
  await page.getByRole('button', { name: '刷新总览' }).click()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await page.getByRole('button', { name: '删除视图 值班主机' }).click()
  await expect(page.getByRole('button', { name: '值班主机', exact: true })).toHaveCount(0)
})

test('viewer can inspect problems but cannot acknowledge them', async ({ page, request }) => {
  await page.goto('/?token=browser-viewer-token')
  await expect(page.getByRole('heading', { name: '运维总览' })).toBeVisible()
  await expect(page.getByText('当前账号只读', { exact: false })).toBeVisible()
  await expect(page.locator('.problem').first()).toBeVisible()
  await expect(page.getByRole('button', { name: '确认问题', exact: true })).toHaveCount(0)
  const resp = await request.post('/api/v1/operations/acknowledgements', {
    headers: { Authorization: 'Bearer browser-viewer-token' }, data: { id: 'forbidden' },
  })
  expect(resp.status()).toBe(403)
})
