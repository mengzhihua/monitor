import { expect, test } from '@playwright/test'

const token = 'browser-test-token'
const headers = { Authorization: `Bearer ${token}` }

test('admin edits the local config file and keeps invalid yaml off disk', async ({ page, request }) => {
  const before = await (await request.get('/api/v1/manage/config', { headers })).json()
  expect(before.writable).toBe(true)
  expect(before.yaml).toContain('hostname: browser-test')
  const viewer = await request.get('/api/v1/manage/config', { headers: { Authorization: 'Bearer browser-viewer-token' } })
  expect(viewer.status()).toBe(403)

  await page.goto('/?token=' + token)
  await page.getByRole('button', { name: '配置' }).click()
  const editor = page.getByRole('textbox', { name: '本机配置 YAML' })
  await expect(editor).toBeVisible()
  const original = await editor.inputValue()
  await editor.fill('web: [')
  await page.getByRole('button', { name: '保存配置' }).click()
  await expect(page.getByRole('alert')).toContainText('invalid config')
  const unchanged = await (await request.get('/api/v1/manage/config', { headers })).json()
  expect(unchanged.yaml).toBe(original)

  const next = original.endsWith('\n') ? `${original}# e2e-agent-config\n` : `${original}\n# e2e-agent-config\n`
  await editor.fill(next)
  await page.getByRole('button', { name: '保存配置' }).click()
  await expect(page.getByText('已写入配置文件')).toBeVisible()
  await expect(page.getByText('磁盘上的配置与当前进程不一致')).toBeVisible()
  const saved = await (await request.get('/api/v1/manage/config', { headers })).json()
  expect(saved.yaml).toContain('# e2e-agent-config')
  expect(saved.restart_required).toBe(true)
  expect(saved.backup).toBe(true)
  page.once('dialog', dialog => dialog.accept())
  await page.getByRole('button', { name: '恢复上一份' }).click()
  await expect(editor).toHaveValue(original)

  await page.goto('/?token=browser-viewer-token')
  await expect(page.getByRole('heading', { name: '运维总览' })).toBeVisible()
  await expect(page.getByRole('button', { name: '配置' })).toHaveCount(0)
})
