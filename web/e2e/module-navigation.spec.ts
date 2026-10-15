import { expect, test } from '@playwright/test'
import { openTools, workspace } from './mobile-navigation'

test('one workspace is mounted, tool URLs reload, and browser back restores the prior module', async ({ page }, info) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByPlaceholder('筛选图表…').fill('system.ram')
  await expect(page.locator('.card')).toHaveCount(1)
  await openTools(page)
  await page.getByTitle('日志', { exact: true }).click()
  await expect(page.getByRole('heading', { name: /^日志/ })).toBeVisible()
  await expect(page.locator('.metrics-workspace')).toHaveCount(0)
  await expect(page.locator('.operations')).toHaveCount(0)
  expect(new URL(page.url()).searchParams.get('view')).toBe('logs')
  await openTools(page)
  await page.getByTitle('AI 分析', { exact: true }).click()
  await expect(page.locator('.ai-panel')).toBeVisible()
  await expect(page.getByRole('heading', { name: /^日志/ })).toHaveCount(0)
  await page.reload()
  await expect(page.locator('.ai-panel')).toBeVisible()
  await page.goBack()
  await expect(page.getByRole('heading', { name: /^日志/ })).toBeVisible()
  await workspace(page, 'charts')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('modular-workspace.png'), fullPage: true })
  expect(errors).toEqual([])
})

test('navigation respects unsaved config and confirmation occurs only once', async ({ page }) => {
  await page.route('**/api/v1/manage/config', route => route.fulfill({ json: { path: '/tmp/fixture.yaml', yaml: 'global:\n  hostname: fixture\n', updated: 1, writable: true } }))
  await page.goto('/?view=config&token=browser-test-token')
  const area = page.getByLabel('本机配置内容')
  await area.fill('global:\n  hostname: unsaved\n')
  let prompts = 0
  const reject = async (dialog: import('@playwright/test').Dialog) => { prompts++; await dialog.dismiss() }
  page.on('dialog', reject)
  await workspace(page, 'charts')
  await expect(area).toHaveValue('global:\n  hostname: unsaved\n')
  expect(new URL(page.url()).searchParams.get('view')).toBe('config')
  expect(prompts).toBe(1)
  page.off('dialog', reject)
  page.on('dialog', async dialog => { prompts++; await dialog.accept() })
  await workspace(page, 'charts')
  await expect(page.locator('.metrics-workspace')).toBeVisible()
  await expect(area).toHaveCount(0)
  expect(prompts).toBe(2)
})

test('viewer deep links never mount privileged modules or fetch their configuration', async ({ page }) => {
  const privileged: string[] = []
  page.on('request', request => { if (/\/api\/v1\/(manage|shell)\//.test(request.url())) privileged.push(request.url()) })
  await page.goto('/?view=config&token=browser-viewer-token')
  await expect(page.getByText('当前节点或账号暂不可使用此模块。')).toBeVisible()
  await expect(page.getByRole('region', { name: '配置管理' })).toHaveCount(0)
  await page.goto('/?view=shell')
  await expect(page.getByText('当前节点或账号暂不可使用此模块。')).toBeVisible()
  expect(privileged).toEqual([])
  await page.getByRole('button', { name: '返回运维总览' }).click()
  await expect(page.locator('.operations')).toBeVisible()
})
