import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'

test('service report previews one snapshot and exports printable scoped artifacts', async ({ page, request, context }, testInfo) => {
  const headers = { Authorization: 'Bearer browser-test-token' }
  const initial = await (await request.get('/api/v1/services/config', { headers })).json()
  expect((await request.put('/api/v1/services/config', { headers, data: { revision: initial.revision, services: [
    { name: 'checkout', children: ['database'] }, { name: 'database', alarms: ['browser_ram_notice'] }, { name: 'unrelated' },
  ] } })).ok()).toBe(true)
  await page.goto('/?view=services&token=browser-test-token')
  await page.getByRole('button', { name: '预览 checkout 交接报告', exact: true }).click()
  const preview = page.getByRole('region', { name: '服务交接报告预览' })
  await expect(preview).toContainText('服务 2 个')
  await preview.getByText('查看报告全部内容（包括原文备注）', { exact: true }).click()
  const snapshot = JSON.parse(await preview.locator('pre').innerText())
  const jsonDownload = page.waitForEvent('download')
  await preview.getByRole('button', { name: '下载交接 JSON' }).click()
  const json = JSON.parse(await readFile((await (await jsonDownload).path())!, 'utf8'))
  expect(json).toEqual(snapshot)
  expect(json.report.services.map((s: { name: string }) => s.name)).toEqual(['checkout', 'database'])
  expect(json.alarms.every((a: { name: string }) => a.name === 'browser_ram_notice')).toBe(true)
  const htmlDownload = page.waitForEvent('download')
  await preview.getByRole('button', { name: '下载可打印 HTML' }).click()
  const html = await readFile((await (await htmlDownload).path())!, 'utf8')
  const artifact = await context.newPage()
  const external: string[] = []
  artifact.on('request', r => external.push(r.url()))
  await artifact.setContent(html)
  await expect(artifact.getByRole('heading', { name: 'checkout · 服务交接报告' })).toBeVisible()
  await expect(artifact.locator('body')).toContainText('配置版本：' + snapshot.config_revision)
  await expect(artifact.locator('body')).toContainText('数据不足')
  await artifact.screenshot({ path: testInfo.outputPath('handover.png'), fullPage: true })
  expect(external).toEqual([])
  expect(await artifact.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await artifact.close()
})

test('viewer cannot open a service handover', async ({ page }) => {
  await page.goto('/?view=services&token=browser-viewer-token')
  await expect(page.locator('.services h1')).toBeVisible()
  await expect(page.getByRole('button', { name: /预览 .* 交接报告/ })).toHaveCount(0)
})
