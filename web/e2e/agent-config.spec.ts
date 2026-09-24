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
  const editor = page.getByRole('textbox', { name: '本机配置内容' })
  await expect(editor).toBeVisible()
  const original = await editor.inputValue()
  await editor.fill('web: [')
  await page.getByRole('button', { name: '保存本机配置' }).click()
  await expect(page.getByRole('alert')).toContainText('invalid config')
  const unchanged = await (await request.get('/api/v1/manage/config', { headers })).json()
  expect(unchanged.yaml).toBe(original)

  const next = original.endsWith('\n') ? `${original}# e2e-agent-config\n` : `${original}\n# e2e-agent-config\n`
  await editor.fill(next)
  await page.getByRole('button', { name: '保存本机配置' }).click()
  await expect(page.getByText('配置已保存并校验通过。重启服务后生效。')).toBeVisible()
  const saved = await (await request.get('/api/v1/manage/config', { headers })).json()
  expect(saved.yaml).toContain('# e2e-agent-config')
  await request.put('/api/v1/manage/config', { headers, data: { yaml: original } })

  await page.goto('/?token=browser-viewer-token')
  await expect(page.getByRole('heading', { name: '运维总览' })).toBeVisible()
  await expect(page.getByRole('button', { name: '配置' })).toHaveCount(0)
})
