import { expect, test } from '@playwright/test'
import type { NotificationSnapshot } from '../src/api'

const path = '/api/v1/operations/notifications'
const token = 'browser-viewer-token'
const headers = { Authorization: `Bearer ${token}` }

test('notification diagnostics shows real local channel outcomes and filters on mobile too', async ({ page, request }, info) => {
  await expect.poll(async () => {
    const data: NotificationSnapshot = await (await request.get(path, { headers })).json()
    return data.accepted >= 2 && data.failed >= 2
  }).toBe(true)
  const raw = await (await request.get(path + '?node=unknown', { headers })).text()
  expect(raw).not.toContain('private-test')
  const data: NotificationSnapshot = JSON.parse(raw)
  expect(data.scope).toBe('local')
  expect(data.hostname).toBe('browser-test')
  await page.goto('/?token=' + token)
  const panel = page.getByRole('region', { name: '通知诊断' })
  await expect(panel).toContainText('此处不汇总远端 Agent 的通知')
  await expect(panel.locator('[data-notification-channel="webhook"]')).toContainText('接受 2')
  await expect(panel.locator('[data-notification-channel="slack"]')).toContainText('失败 2')
  await panel.locator('.activity summary').click()
  await panel.getByLabel('通知结果', { exact: true }).selectOption('failed')
  await expect(panel.locator('[data-notification-outcome="failed"]')).toHaveCount(2)
  await expect(panel.locator('[data-notification-outcome="accepted"]')).toHaveCount(0)
  await expect(panel.locator('li').first()).toContainText('HTTP 503')
  await panel.getByLabel('通知关键词').fill('browser_ram_secondary')
  await expect(panel.locator('li')).toHaveCount(1)
  await panel.getByLabel('通知通道', { exact: true }).selectOption('webhook')
  await expect(panel).toContainText('当前筛选没有匹配结果')
  await panel.getByLabel('通知结果', { exact: true }).selectOption('accepted')
  await expect(panel.locator('li')).toHaveCount(1)
  await expect(panel.locator('li')).toContainText('未验证用户收件')
  await panel.getByLabel('通知关键词').fill('')
  await panel.getByLabel('通知通道', { exact: true }).selectOption('')
  await panel.getByLabel('通知结果', { exact: true }).selectOption('')
  await expect(panel).toContainText('重启后清空')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  // Capture the region from document coordinates so the fixed application
  // header does not cover its heading in a scrolled element screenshot.
  await page.evaluate(() => window.scrollTo(0, 0))
  const bounds = await panel.boundingBox()
  expect(bounds).not.toBeNull()
  await page.screenshot({ path: info.outputPath('notification-diagnostics.png'), fullPage: true, clip: bounds! })
})

test('notification diagnostics retains stale results on connection failure and clears them on auth failure', async ({ page }) => {
  await page.goto('/?token=' + token)
  const panel = page.getByRole('region', { name: '通知诊断' })
  await expect(panel.locator('[data-notification-channel="slack"]')).toBeVisible()
  await page.route('**' + path, route => route.abort())
  await panel.getByRole('button', { name: '刷新通知诊断' }).click()
  await expect(panel.getByRole('alert')).toContainText('上次成功的数据')
  await expect(panel.locator('[data-notification-channel="slack"]')).toBeVisible()
  await page.unroute('**' + path)
  await panel.getByRole('button', { name: '刷新通知诊断' }).click()
  await expect(panel.getByRole('alert')).toHaveCount(0)
  await page.route('**' + path, route => route.fulfill({ status: 401, body: 'unauthorized' }))
  await panel.getByRole('button', { name: '刷新通知诊断' }).click()
  await expect(panel.getByRole('alert')).toContainText('请重新登录')
  await expect(panel.locator('[data-notification-channel]')).toHaveCount(0)
})

test('notification diagnostics distinguishes disabled engine and unconfigured channels', async ({ page }) => {
  await page.route('**' + path, route => route.fulfill({ json: { available: false, scope: 'local', channels: [], recent: [] } }))
  await page.goto('/?token=' + token)
  const panel = page.getByRole('region', { name: '通知诊断' })
  await expect(panel).toContainText('本机健康引擎未启用')
  await expect(panel.locator('.stats')).toHaveCount(0)
  await page.unroute('**' + path)
  await page.route('**' + path, async route => {
    const response = await route.fetch()
    const data: NotificationSnapshot = await response.json()
    data.channels = []
    data.recent = []
    data.total = 0
    await route.fulfill({ json: data })
  })
  await panel.getByRole('button', { name: '刷新通知诊断' }).click()
  await expect(panel).toContainText('未配置通知通道')
  await panel.locator('.activity summary').click()
  await expect(panel).toContainText('本次启动暂无已完成的通知结果')
})


test('notification test controls require admin and confirm the destination', async ({ page, request }, info) => {
  await page.goto('/?token=browser-viewer-token')
  const panel = page.getByRole('region', { name: '通知诊断' })
  await expect(panel.locator('[data-notification-channel="webhook"]')).toBeVisible()
  await expect(panel.getByRole('button', { name: '测试 webhook', exact: true })).toHaveCount(0)
  const denied = await request.post(path + '/test', { headers, data: { channel: 'webhook' } })
  expect(denied.status()).toBe(403)
  await page.goto('/?token=browser-test-token')
  await panel.getByRole('button', { name: '测试 webhook', exact: true }).click()
  const confirmation = panel.getByRole('group', { name: '确认通知测试' })
  await expect(confirmation).toContainText('绕过静默、维护和规则路由')
  await confirmation.getByRole('button', { name: '取消', exact: true }).click()
  await expect(confirmation).toHaveCount(0)
  await panel.getByRole('button', { name: '测试 webhook', exact: true }).click()
  // UI request/response behavior is isolated here so shared real delivery
  // counters stay deterministic across projects. Runtime/API tests send live.
  let calls = 0
  await page.route('**' + path + '/test', async route => {
    expect(route.request().postDataJSON()).toEqual({ channel: 'webhook' })
    calls++
    await route.fulfill({ status: calls === 1 ? 202 : 429, json: calls === 1 ? { queued: true } : { error: 'rate limited' } })
  })
  await confirmation.getByRole('button', { name: '发送测试消息', exact: true }).click()
  await expect(panel).toContainText('测试已排队')
  await expect(confirmation).toHaveCount(0)
  await panel.getByRole('button', { name: '测试 webhook', exact: true }).click()
  await confirmation.getByRole('button', { name: '发送测试消息', exact: true }).click()
  await expect(panel).toContainText('测试过于频繁')
  await panel.locator('.setup summary').click()
  await expect(panel).toContainText('secret_env')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await panel.screenshot({ path: info.outputPath('notification-test-controls.png') })
})
