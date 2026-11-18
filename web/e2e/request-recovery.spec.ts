import { expect, test } from '@playwright/test'

test('stalled onboarding request times out and can be retried', async ({ page }) => {
  await page.clock.install()
  let stalled = true, intercepted = 0
  await page.route('**/api/v1/operations/notifications', route => {
    intercepted++
    if (!stalled) return route.continue()
    // Deliberately keep the response open until the browser deadline aborts it.
  })
  await page.goto('/?view=setup&token=browser-test-token')
  await expect.poll(() => intercepted).toBeGreaterThan(0)
  const panel = page.getByRole('region', { name: '接入与运行检查' })
  await expect(panel.getByRole('button', { name: '检查中…' })).toBeVisible()
  await page.clock.fastForward(31_000)
  await expect(panel.getByRole('alert')).toContainText('通知读取失败')
  await expect(panel.locator('[data-check="notifications"]')).toHaveAttribute('data-state', 'unknown')
  stalled = false
  await panel.getByRole('button', { name: '重新检查', exact: true }).click()
  await expect(panel.getByRole('alert')).toHaveCount(0)
  await expect(panel.locator('[data-check="notifications"]')).not.toHaveAttribute('data-state', 'unknown')
})

test('lost save response reports uncertainty, retains draft and never resubmits', async ({ page, request }) => {
  await page.clock.install()
  const headers = { Authorization: 'Bearer browser-test-token' }
  const original = await (await request.get('/api/v1/services/config', { headers })).json()
  expect((await request.put('/api/v1/services/config', { headers, data: { revision: original.revision, services: [] } })).ok()).toBe(true)
  let saves = 0, stored = false
  await page.route('**/api/v1/services/config', async route => {
    if (route.request().method() !== 'PUT') return route.continue()
    saves++
    const response = await route.fetch()
    expect(response.ok()).toBe(true)
    stored = true // The server has saved; the browser never receives its response.
  })
  await page.goto('/?view=services&token=browser-test-token')
  await page.getByRole('button', { name: '配置服务树', exact: true }).click()
  const editor = page.getByRole('region', { name: '服务配置编辑' })
  await editor.getByRole('button', { name: '添加服务' }).click()
  await editor.getByLabel('服务名称 1', { exact: true }).fill('confirmed-on-server')
  await editor.getByRole('button', { name: '校验并预览' }).click()
  await expect(editor.locator('.preview')).toBeVisible()
  await editor.getByRole('button', { name: '保存服务配置' }).click()
  await expect.poll(() => stored).toBe(true)
  await page.clock.fastForward(61_000)
  await expect(editor.getByRole('alert')).toContainText('操作结果尚未确认')
  await expect(editor.getByLabel('服务名称 1', { exact: true })).toHaveValue('confirmed-on-server')
  await expect(editor.getByRole('button', { name: '保存服务配置' })).toBeDisabled()
  expect(saves).toBe(1)
  await editor.getByRole('button', { name: '读取最新版本并保留草稿' }).click()
  await expect(editor).toContainText('已读取最新版本并保留草稿')
  expect((await (await request.get('/api/v1/services/config', { headers })).json()).services[0].name).toBe('confirmed-on-server')
})
