import { expect, test } from '@playwright/test'
import type { OperationsSnapshot } from '../src/api'
import { readFile } from 'node:fs/promises'

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
  // Host resource tile always renders a disk section; row count stays capped at
  // the panel's MAX_DISK_ROWS (6) even when the node reports many mounts.
  const tile = page.locator('.node-tile').first()
  await expect(tile.locator('.disks')).toBeVisible()
  expect(await tile.locator('.disk-row').count()).toBeLessThanOrEqual(6)
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
  await expect(page.getByRole('region', { name: '个人视图' })).toContainText('视图已保存到当前账号')
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
  await expect(page.getByRole('button', { name: '保存责任人', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '保存进度', exact: true })).toHaveCount(0)
  const handling = await request.post('/api/v1/operations/handling', {
    headers: { Authorization: 'Bearer browser-viewer-token' }, data: { action: 'progress', status: 'watching' },
  })
  expect(handling.status()).toBe(403)
})

test('operators assign, track progress, save their queue and export persisted workflow', async ({ page, request }, info) => {
  let data: OperationsSnapshot = await (await request.get('/api/v1/operations', { headers })).json()
  const p = data.problems.find(p => p.name === 'browser_ram_notice')!
  let revision = p.handling.revision
  for (const change of [{ action: 'unassign' }, { action: 'progress', status: 'open' }]) {
    const response = await request.post('/api/v1/operations/handling', { headers, data: { id: p.id, revision, ...change } })
    expect(response.ok()).toBe(true)
    revision = (await response.json()).revision
  }
  await page.goto('/?token=browser-operator-token')
  const problem = page.locator(`[data-problem-id="${p.id}"]`)
  await expect(problem.locator('.owner')).toHaveText('责任人：未分配')
  await expect(problem.getByLabel('browser_ram_notice 责任人').locator('option')).toHaveCount(3)
  await problem.getByRole('button', { name: '分配给我', exact: true }).click()
  await expect(problem.locator('.owner')).toHaveText('责任人：browser-oncall')
  await problem.getByLabel('browser_ram_notice 处理备注').fill('排查完成，观察内存回落。')
  await problem.getByLabel('browser_ram_notice 处理进度', { exact: true }).selectOption('watching')
  await problem.getByRole('button', { name: '保存进度', exact: true }).click()
  await expect(problem.locator('.progress-state')).toHaveText('观察中')
  await page.getByLabel('责任人筛选').selectOption('mine')
  await page.getByLabel('处理进度筛选').selectOption('watching')
  await page.getByLabel('视图名称', { exact: true }).fill('我的观察队列')
  await page.getByRole('button', { name: '保存视图', exact: true }).click()
  await expect(page.getByRole('region', { name: '个人视图' })).toContainText('视图已保存到当前账号')
  await page.reload()
  await page.getByRole('button', { name: '我的观察队列', exact: true }).click()
  await expect(page.getByLabel('责任人筛选')).toHaveValue('mine')
  await expect(page.getByLabel('处理进度筛选')).toHaveValue('watching')
  await expect(problem.locator('.owner')).toHaveText('责任人：browser-oncall')
  await expect(problem.locator('.progress-state')).toHaveText('观察中')
  await problem.locator('summary').click()
  await expect(problem).toContainText('未分配 → browser-oncall')
  await expect(problem).toContainText('待处理 → 观察中')
  await expect(problem).toContainText('排查完成，观察内存回落。')
  const downloadEvent = page.waitForEvent('download')
  await page.getByRole('button', { name: '导出当前快照' }).click()
  const download = await downloadEvent
  const exported = JSON.parse(await readFile((await download.path())!, 'utf8'))
  expect(exported.filters.owner).toBe('mine')
  expect(exported.problems.find((x: { id: string }) => x.id === p.id).handling).toMatchObject({ assignee: 'browser-oncall', status: 'watching' })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('operations-workflow.png'), fullPage: true })
  await page.getByLabel('责任人筛选').selectOption('unassigned')
  await expect(problem).toHaveCount(0)
  await page.getByLabel('责任人筛选').selectOption('all')
  await problem.getByLabel('browser_ram_notice 责任人', { exact: true }).selectOption('admin')
  await problem.getByRole('button', { name: '保存责任人', exact: true }).click()
  await expect(problem.locator('.owner')).toHaveText('责任人：admin')
  await problem.getByLabel('browser_ram_notice 责任人', { exact: true }).selectOption('')
  await problem.getByRole('button', { name: '保存责任人', exact: true }).click()
  await expect(problem.locator('.owner')).toHaveText('责任人：未分配')
  data = await (await request.get('/api/v1/operations', { headers })).json()
  const record = data.problems.find(x => x.id === p.id)!.handling
  expect(record.history.at(-1)).toMatchObject({ actor: 'browser-oncall', action: 'unassign', previous_assignee: 'admin' })
  expect(record.status).toBe('watching')
})

test('concurrent handling changes preserve the draft and require an explicit retry', async ({ page, request }) => {
  await page.goto('/?token=' + token)
  const problem = page.locator('.problem').filter({ has: page.getByRole('heading', { name: 'browser_ram_notice', exact: true }) })
  await expect(problem).toBeVisible()
  const id = await problem.getAttribute('data-problem-id')
  await problem.getByLabel('browser_ram_notice 处理备注').fill('这是尚未提交的处理草稿。')
  await problem.getByLabel('browser_ram_notice 处理进度', { exact: true }).selectOption('investigating')
  const before: OperationsSnapshot = await (await request.get('/api/v1/operations', { headers })).json()
  const resp = await request.post('/api/v1/operations/handling', { headers: { Authorization: 'Bearer browser-operator-token' }, data: {
    id, revision: before.problems.find(x => x.id === id)!.handling.revision,
    action: 'progress', status: 'watching', note: '另一位排障人员先保存。',
  } })
  expect(resp.ok()).toBe(true)
  // A poll/manual refresh must not silently rebase an already edited draft.
  const refreshed = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/operations' && r.request().method() === 'GET')
  await page.getByRole('button', { name: '刷新总览', exact: true }).click()
  await refreshed
  await expect(problem.getByLabel('browser_ram_notice 处理进度', { exact: true })).toHaveValue('investigating')
  await problem.getByRole('button', { name: '保存进度', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('草稿已保留')
  await expect(problem.getByLabel('browser_ram_notice 处理备注')).toHaveValue('这是尚未提交的处理草稿。')
  await expect(problem.locator('.progress-state')).toHaveText('观察中')
  await problem.getByRole('button', { name: '保存进度', exact: true }).click()
  await expect(problem.locator('.progress-state')).toHaveText('排查中')
  await expect(page.getByRole('alert')).toHaveCount(0)
  const after: OperationsSnapshot = await (await request.get('/api/v1/operations', { headers })).json()
  const history = after.problems.find(x => x.id === id)!.handling.history
  expect(history.at(-2)?.note).toBe('另一位排障人员先保存。')
  expect(history.at(-1)?.note).toBe('这是尚未提交的处理草稿。')
})
