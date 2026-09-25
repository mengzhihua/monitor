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
  await page.getByRole('button', { name: 'YAML', exact: true }).click()
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

test('admin edits hostname and nginx from the form and can roll back', async ({ page, request }) => {
  const headers = { Authorization: 'Bearer browser-test-token' }
  const before = await (await request.get('/api/v1/manage/config', { headers })).json()
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '配置' }).click()
  await page.getByRole('button', { name: '表单', exact: true }).click()
  await page.getByRole('textbox', { name: '主机名' }).fill('browser-visual')
  await page.getByRole('textbox', { name: 'nginx 地址' }).fill('http://127.0.0.1/stub_status')
  await page.getByRole('button', { name: '保存本机配置' }).click()
  await expect(page.getByText('配置已保存并校验通过。重启服务后生效。')).toBeVisible()
  const saved = await (await request.get('/api/v1/manage/config', { headers })).json()
  expect(saved.yaml).toContain('browser-visual')
  expect(saved.yaml).toContain('http://127.0.0.1/stub_status')
  expect(saved.yaml).toContain('browser-test-token')
  expect(saved.yaml).toContain('browser_ram_notice')
  expect(saved.yaml).toContain('browser-topic')
  page.once('dialog', dialog => dialog.accept())
  await page.getByRole('button', { name: '恢复上一份' }).click()
  await expect(page.getByText('已恢复上一份配置。重启服务后生效。')).toBeVisible()
  await expect(page.getByRole('textbox', { name: '主机名' })).toHaveValue('browser-test')
  const restored = await (await request.get('/api/v1/manage/config', { headers })).json()
  expect(restored.yaml).toContain('hostname: browser-test')
  expect(restored.yaml).not.toContain('browser-visual')
  await request.put('/api/v1/manage/config', { headers, data: { yaml: before.yaml, if_updated: restored.updated } })
})
