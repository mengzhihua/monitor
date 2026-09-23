import { expect, test } from '@playwright/test'
import type { APIRequestContext, Page } from '@playwright/test'
import type { MaintenanceSnapshot } from '../src/api'

const path = '/api/v1/operations/maintenance'
const headers = { Authorization: 'Bearer browser-test-token' }
const snapshot = async (request: APIRequestContext): Promise<MaintenanceSnapshot> => (await request.get(path, { headers })).json()
const spec = (title: string) => ({ title, reason: 'isolated browser maintenance', scope: 'all', chart: '', alarm: '', starts_at: 0, duration_seconds: 3600 })
async function form(page: Page, title: string) {
  const panel = page.getByRole('region', { name: '计划维护窗口' })
  await panel.locator('summary').click()
  await panel.getByLabel('维护标题', { exact: true }).fill(title)
  await panel.getByLabel('维护原因', { exact: true }).fill('更换内存后验证服务。')
  await panel.getByLabel('维护目标告警').selectOption(JSON.stringify(['system.ram', 'browser_ram_notice']))
  await panel.getByRole('button', { name: '预览维护计划' }).click()
  return panel
}
test.afterEach(async ({ request }) => {
  // Only cancel plans created by this isolated suite, including failed cases.
  let data = await snapshot(request)
  for (const p of data.plans.filter(p => p.title.startsWith('e2e-') && ['active', 'scheduled'].includes(p.state))) {
    const resp = await request.post(path, { headers, data: { action: 'cancel', revision: data.revision, id: p.id, reason: 'test cleanup' } })
    expect(resp.ok()).toBe(true)
    data = await resp.json()
  }
})

test('maintenance plans preview exact scope, survive reload, schedule future work and enforce read-only roles', async ({ page, request }, info) => {
  await page.goto('/?token=browser-test-token')
  const title = `e2e-active-${info.project.name}`
  const panel = await form(page, title)
  await expect(panel.getByRole('region', { name: '维护计划预览' })).toContainText('system.ram · browser_ram_notice')
  await expect(panel).toContainText('服务器收到并保存后立即开始')
  await panel.getByRole('button', { name: '确认创建维护计划' }).click()
  await expect(panel).toContainText('维护计划已保存')
  let data = await snapshot(request)
  const plan = data.plans.find(p => p.title === title)!
  expect(plan.created_by).toBe('admin')
  expect(plan.state).toBe('active')
  expect(plan.scope).toBe('alarm')
  await page.reload()
  const card = panel.locator(`[data-maintenance-id="${plan.id}"]`)
  await expect(card).toContainText('维护中')
  const nextTitle = `e2e-future-${info.project.name}`
  await panel.locator('summary').click()
  await panel.getByLabel('维护标题', { exact: true }).fill(nextTitle)
  await panel.getByLabel('维护原因', { exact: true }).fill('计划升级')
  await panel.getByLabel('维护范围', { exact: true }).selectOption('all')
  await panel.getByLabel('维护开始方式').selectOption('scheduled')
  const local = await page.evaluate(() => {
    const d = new Date(Date.now() + 3600_000)
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
  })
  await panel.getByLabel('维护开始时间').fill(local)
  await panel.getByRole('button', { name: '预览维护计划' }).click()
  await expect(panel.getByRole('region', { name: '维护计划预览' })).toContainText('本机所有告警（包括以后新建的告警）')
  await panel.getByRole('button', { name: '确认创建维护计划' }).click()
  data = await snapshot(request)
  expect(data.plans.find(p => p.title === nextTitle)?.state).toBe('scheduled')
  await card.getByRole('button', { name: '取消维护 ' + title }).click()
  await panel.getByLabel('取消维护原因').fill('变更提前完成')
  await panel.getByRole('button', { name: '确认取消该维护计划' }).click()
  await panel.getByLabel('维护计划状态').selectOption('all')
  await expect(card).toContainText('已取消')
  await expect(card).toContainText('变更提前完成')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.evaluate(() => window.scrollTo(0, 0))
  const bounds = await panel.boundingBox()
  await page.screenshot({ path: info.outputPath('maintenance-plans.png'), fullPage: true, clip: bounds! })
  await page.goto('/?token=browser-viewer-token')
  await expect(panel).toContainText('当前账号可查看')
  await expect(panel.locator('summary')).toHaveCount(0)
  await expect(panel.getByRole('button', { name: /^取消维护 / })).toHaveCount(0)
  for (const token of ['browser-viewer-token', 'browser-operator-token']) {
    const resp = await request.post(path, { headers: { Authorization: 'Bearer ' + token }, data: { action: 'create', revision: data.revision, plan: spec('forbidden') } })
    expect(resp.status()).toBe(403)
  }
})

test('maintenance changes freeze the preview revision and require explicit review after conflict', async ({ page, request }, info) => {
  await page.goto('/?token=browser-test-token')
  const title = `e2e-conflict-${info.project.name}`
  const panel = await form(page, title)
  let data = await snapshot(request)
  expect((await request.post(path, { headers, data: { action: 'create', revision: data.revision, plan: spec(`e2e-other-${info.project.name}`) } })).ok()).toBe(true)
  await panel.getByRole('button', { name: '刷新维护计划' }).click()
  await expect(panel.getByRole('button', { name: '确认创建维护计划' })).toBeDisabled()
  await panel.getByRole('button', { name: '读取最新列表并重新核对' }).click()
  await expect(panel.getByLabel('维护标题', { exact: true })).toHaveValue(title)
  await panel.getByRole('button', { name: '预览维护计划' }).click()
  await panel.getByRole('button', { name: '确认创建维护计划' }).click()
  await expect(panel).toContainText('维护计划已保存')
  data = await snapshot(request)
  const p = data.plans.find(p => p.title === title)!
  await panel.locator(`[data-maintenance-id="${p.id}"]`).getByRole('button').click()
  await panel.getByLabel('取消维护原因').fill('我的完成记录')
  expect((await request.post(path, { headers, data: { action: 'cancel', revision: data.revision, id: p.id, reason: '另一管理员已经取消' } })).ok()).toBe(true)
  await panel.getByRole('button', { name: '确认取消该维护计划' }).click()
  await expect(panel.getByRole('alert')).toContainText('本次未修改')
  await expect(panel.getByLabel('取消维护原因')).toHaveValue('我的完成记录')
  await expect(panel.getByRole('button', { name: '确认取消该维护计划' })).toBeDisabled()
})

test('maintenance storage and ambiguous network failures never claim success or automatically retry', async ({ page, request }, info) => {
  await page.goto('/?token=browser-test-token')
  const title = `e2e-error-${info.project.name}`
  const panel = await form(page, title)
  await page.route('**' + path, route => route.request().method() === 'POST' ? route.fulfill({ status: 503, body: 'storage unavailable' }) : route.continue())
  await panel.getByRole('button', { name: '确认创建维护计划' }).click()
  await expect(panel.getByRole('alert')).toContainText('本次未修改')
  await expect(panel.getByRole('region', { name: '维护计划预览' })).toContainText(title)
  expect((await snapshot(request)).plans.some(p => p.title === title)).toBe(false)
  await page.unroute('**' + path)
  await page.route('**' + path, async route => {
    if (route.request().method() !== 'POST') { await route.continue(); return }
    const response = await route.fetch()
    expect(response.ok()).toBe(true)
    await route.abort() // backend committed; browser did not receive its result
  })
  await panel.getByRole('button', { name: '确认创建维护计划' }).click()
  await expect(panel.getByRole('alert')).toContainText('保存结果不确定')
  await expect(panel.getByRole('button', { name: '确认创建维护计划' })).toBeDisabled()
  await page.unroute('**' + path)
  await panel.getByRole('button', { name: '读取最新列表并重新核对' }).click()
  const matches = (await snapshot(request)).plans.filter(p => p.title === title)
  expect(matches).toHaveLength(1)
  await expect(panel.locator(`[data-maintenance-id="${matches[0]!.id}"]`)).toContainText('维护中')
  await page.route('**' + path, route => route.fulfill({ status: 401, body: 'expired' }))
  await panel.getByRole('button', { name: '刷新维护计划' }).click()
  await expect(panel).toContainText('请重新登录')
  await expect(panel.locator('[data-maintenance-id]')).toHaveCount(0)
})

test('clearing a manual silence in the alarm panel preserves the maintenance suppression indicator', async ({ page, request }, info) => {
  const data = await snapshot(request)
  const resp = await request.post(path, { headers, data: { action: 'create', revision: data.revision,
    plan: { ...spec(`e2e-manual-${info.project.name}`), scope: 'alarm', chart: 'system.ram', alarm: 'browser_ram_notice' } } })
  expect(resp.ok()).toBe(true)
  await page.goto('/?token=browser-test-token&view=charts')
  await page.getByTitle('告警', { exact: true }).click()
  const row = page.locator('.panel tbody tr').filter({ hasText: 'browser_ram_notice' })
  await expect(row).toContainText('维护或其他抑制')
  await row.getByRole('button', { name: '静默', exact: true }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await row.getByRole('button', { name: '解除手动静默', exact: true }).click()
  await expect(row).toContainText('维护或其他抑制')
  const alarms = await (await request.get('/api/v1/alarms?all=true', { headers })).json()
  expect(Object.values(alarms.alarms).some((a: any) => a.name === 'browser_ram_notice' && a.silenced)).toBe(true)
  const silences = await (await request.get('/api/v1/alarms/silence', { headers })).json()
  expect(Object.keys(silences.alarms)).toHaveLength(0)
})
