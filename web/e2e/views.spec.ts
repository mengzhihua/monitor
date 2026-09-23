import { expect, test } from '@playwright/test'
import type { OperationsView } from '../src/api'

const token = 'browser-viewer-token'
const headers = { Authorization: `Bearer ${token}` }
const path = '/api/v1/operations/views'
const view: OperationsView = { name: '跨设备值班队列', query: 'browser-test', severity: 'WARNING', nodeStatus: 'live', pendingOnly: true, ownerFilter: 'mine', progressFilter: 'watching' }

test.beforeEach(async ({ request }) => {
  const current = await (await request.get(path, { headers })).json()
  expect((await request.post(path, { headers, data: { revision: current.revision, views: [] } })).ok()).toBe(true)
})

test('personal views sync to a fresh browser, stay private and detect concurrent edits', async ({ page, browser, request }, info) => {
  await page.goto('/?token=' + token)
  const panel = page.getByRole('region', { name: '个人视图' })
  await page.getByLabel('搜索主机或问题').fill(view.query)
  await page.getByLabel('节点状态', { exact: true }).selectOption(view.nodeStatus)
  await page.getByLabel('问题级别', { exact: true }).selectOption(view.severity)
  await page.getByLabel('责任人筛选', { exact: true }).selectOption(view.ownerFilter)
  await page.getByLabel('处理进度筛选', { exact: true }).selectOption(view.progressFilter)
  await page.getByLabel('只看待确认', { exact: true }).check()
  await panel.getByLabel('视图名称', { exact: true }).fill(view.name)
  await panel.getByRole('button', { name: '保存视图', exact: true }).click()
  await expect(panel).toContainText('视图已保存到当前账号')
  const ctx = await browser.newContext({ baseURL: test.info().project.use.baseURL, viewport: { width: 1280, height: 900 } })
  try {
    const second = await ctx.newPage()
    await second.goto('/?token=' + token)
    expect(await second.evaluate(() => localStorage.getItem('monitor.operations.views.v1'))).toBeNull()
    await second.getByRole('button', { name: view.name, exact: true }).click()
    await expect(second.getByLabel('搜索主机或问题')).toHaveValue(view.query)
    await expect(second.getByLabel('节点状态', { exact: true })).toHaveValue(view.nodeStatus)
    await expect(second.getByLabel('问题级别', { exact: true })).toHaveValue(view.severity)
    await expect(second.getByLabel('责任人筛选', { exact: true })).toHaveValue(view.ownerFilter)
    await expect(second.getByLabel('处理进度筛选', { exact: true })).toHaveValue(view.progressFilter)
    await expect(second.getByLabel('只看待确认', { exact: true })).toBeChecked()
    await second.getByRole('button', { name: '删除视图 ' + view.name }).click()
    await expect(second.getByRole('button', { name: view.name, exact: true })).toHaveCount(0)

    await panel.getByLabel('视图名称', { exact: true }).fill('待保留草稿')
    await panel.getByRole('button', { name: '保存视图', exact: true }).click()
    await expect(panel.getByRole('alert')).toContainText('其他页面已修改视图')
    await expect(panel.getByLabel('视图名称', { exact: true })).toHaveValue('待保留草稿')
    await expect(page.getByLabel('搜索主机或问题')).toHaveValue(view.query)
    expect((await (await request.get(path, { headers })).json()).views).toEqual([])
    await panel.getByRole('button', { name: '保存视图', exact: true }).click()
    await expect(panel).toContainText('视图已保存到当前账号')
    const other = await (await request.get(path, { headers: { Authorization: 'Bearer browser-operator-token' } })).json()
    expect(other.views.some((v: OperationsView) => v.name === '待保留草稿')).toBe(false)
    expect((await request.post('/api/v1/operations/handling', { headers, data: {} })).status()).toBe(403)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ path: info.outputPath('personal-views.png'), fullPage: true })
  } finally { await ctx.close() }
})

test('browser views import explicitly, retain originals and never overwrite a name collision', async ({ page, request }) => {
  await page.addInitScript(v => localStorage.setItem('monitor.operations.views.v1', JSON.stringify([v])), view)
  await page.goto('/?token=' + token)
  const panel = page.getByRole('region', { name: '个人视图' })
  await expect(panel).toContainText('0/10')
  expect((await (await request.get(path, { headers })).json()).views).toEqual([])
  await panel.locator('summary').click()
  await panel.getByRole('button', { name: view.name, exact: true }).click()
  await expect(page.getByLabel('搜索主机或问题')).toHaveValue(view.query)
  await panel.getByRole('button', { name: '导入视图 ' + view.name }).click()
  await expect(panel).toContainText('本地原件仍保留')
  const saved = await (await request.get(path, { headers })).json()
  expect(saved.views).toEqual([view])
  await panel.getByRole('button', { name: '导入视图 ' + view.name }).click()
  await expect(panel.getByRole('alert')).toContainText('已有同名视图')
  expect(await (await request.get(path, { headers })).json()).toEqual(saved)
  expect(JSON.parse(await page.evaluate(() => localStorage.getItem('monitor.operations.views.v1')) || '[]')).toEqual([view])
})

test('view storage failures preserve drafts and expired login clears private views', async ({ page }) => {
  await page.goto('/?token=' + token)
  const panel = page.getByRole('region', { name: '个人视图' })
  await panel.getByLabel('视图名称', { exact: true }).fill('连接恢复后保存')
  await page.route('**' + path, route => route.request().method() === 'POST'
    ? route.fulfill({ status: 503, body: 'storage unavailable' }) : route.continue())
  await panel.getByRole('button', { name: '保存视图', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('本次修改未保存')
  await expect(panel.getByLabel('视图名称', { exact: true })).toHaveValue('连接恢复后保存')
  await page.unroute('**' + path)
  await panel.getByRole('button', { name: '保存视图', exact: true }).click()
  await expect(panel).toContainText('视图已保存到当前账号')
  await page.route('**' + path, route => route.fulfill({ status: 401, body: 'expired' }))
  await panel.getByRole('button', { name: '刷新视图' }).click()
  await expect(panel.getByRole('alert')).toContainText('请重新登录')
  await expect(panel.getByRole('button', { name: '连接恢复后保存', exact: true })).toHaveCount(0)
  await expect(panel.getByRole('button', { name: '保存视图', exact: true })).toHaveCount(0)
})
