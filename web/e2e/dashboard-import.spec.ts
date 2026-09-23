import { test, expect, type Page } from '@playwright/test'
import { decodeBoards, encodeBoards, newBoard, storageKey } from '../src/dashboardConfig'

const board = (title: string) => ({ ...newBoard(), title, groupIds: ['compute'], chartIds: ['absent.cpu'], windowSec: 900, columns: 2, hideEmpty: true })
async function open(page: Page) {
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  return page.getByLabel('常用聚合看板', { exact: true })
}
async function upload(page: Page, text: string, name = '看板备份.json') {
  await page.getByLabel('导入看板配置').setInputFiles({ name, mimeType: 'application/json', buffer: Buffer.from(text) })
  return page.getByRole('region', { name: '看板导入预览' })
}

test('preview supports cancellation and selected copies while preserving layout and unavailable charts', async ({ page }, info) => {
  const panel = await open(page)
  const a = board('发布观察'), b = board('数据库观察')
  const original = await page.evaluate(key => localStorage.getItem(key), storageKey)
  const preview = await upload(page, encodeBoards([a, b]))
  await expect(preview).toContainText('已选择 2 个')
  await expect(preview).toContainText('15 分钟 · 2 列 · 每组 4 张')
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBe(original)
  await preview.getByRole('button', { name: '取消导入' }).click()
  await expect(preview).toHaveCount(0)
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBe(original)
  await upload(page, encodeBoards([a, b]))
  await preview.getByRole('button', { name: '清空导入选择' }).click()
  await expect(preview.getByRole('button', { name: '确认导入所选看板' })).toBeDisabled()
  await preview.getByLabel('导入 发布观察', { exact: true }).check()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await preview.screenshot({ path: info.outputPath('dashboard-import-preview.png') })
  await preview.getByRole('button', { name: '确认导入所选看板' }).click()
  const saved = decodeBoards((await page.evaluate(key => localStorage.getItem(key), storageKey))!)
  expect(saved).toHaveLength(1)
  expect(saved[0]).toEqual({ ...a, id: saved[0]!.id })
  expect(saved[0]!.id).not.toBe(a.id)
  await expect(panel.getByRole('heading', { name: '发布观察', exact: true })).toBeVisible()
  await upload(page, encodeBoards([a]))
  await expect(preview).toContainText('已有同名看板，将新增副本')
  await preview.getByRole('button', { name: '确认导入所选看板' }).click()
  expect(decodeBoards((await page.evaluate(key => localStorage.getItem(key), storageKey))!).map(b => b.title)).toEqual(['发布观察', '发布观察'])
})

test('capacity follows other tabs and failed writes keep the preview and saved configuration intact', async ({ page, context }) => {
  const existing = Array.from({ length: 19 }, (_, i) => board(`已有 ${i}`))
  await page.addInitScript(({ key, value }) => { if (!localStorage.getItem(key)) localStorage.setItem(key, value) }, { key: storageKey, value: encodeBoards(existing) })
  await open(page)
  const preview = await upload(page, encodeBoards([board('A'), board('B')]))
  await expect(preview.getByRole('alert')).toContainText('超过剩余容量')
  await expect(preview.getByRole('button', { name: '确认导入所选看板' })).toBeDisabled()
  await preview.getByLabel('导入 B', { exact: true }).uncheck()
  await expect(preview.getByRole('button', { name: '确认导入所选看板' })).toBeEnabled()
  const second = await context.newPage()
  await second.goto('/?view=charts&token=browser-test-token')
  await second.evaluate(({ key, value }) => localStorage.setItem(key, value), { key: storageKey, value: encodeBoards([...existing, board('其他标签新增')]) })
  // Inspect the tab a user would return to, avoiding background renderer throttling.
  await page.bringToFront()
  await expect(preview).toContainText('还可添加 0 个')
  await expect(preview.getByRole('button', { name: '确认导入所选看板' })).toBeDisabled()
  await second.bringToFront()
  await second.evaluate(({ key, value }) => localStorage.setItem(key, value), { key: storageKey, value: encodeBoards(existing) })
  await page.bringToFront()
  await expect(preview.getByRole('button', { name: '确认导入所选看板' })).toBeEnabled()
  const before = await page.evaluate(key => localStorage.getItem(key), storageKey)
  await page.evaluate(key => { const original = Storage.prototype.setItem; Storage.prototype.setItem = function(k, v) { if (k === key) throw new Error('QuotaExceededError'); original.call(this, k, v) } }, storageKey)
  await preview.getByRole('button', { name: '确认导入所选看板' }).click()
  await expect(page.getByRole('alert')).toContainText('保存失败')
  await expect(preview).toBeVisible()
  await expect(preview.getByLabel('导入 A', { exact: true })).toBeChecked()
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBe(before)
  await second.close()
})

test('invalid replacement files clear older previews and corrupt browser data is never overwritten', async ({ page }) => {
  await open(page)
  const preview = await upload(page, encodeBoards([board('正常文件')]))
  await upload(page, '{invalid')
  await expect(preview).toHaveCount(0)
  await expect(page.getByRole('alert')).toContainText('导入失败')
  await upload(page, encodeBoards([board('再次预览')]))
  await page.evaluate(key => localStorage.setItem(key, '{broken'), storageKey)
  await preview.getByRole('button', { name: '确认导入所选看板' }).click()
  await expect(page.getByRole('alert')).toContainText('原始数据已保留')
  await expect(preview).toBeVisible()
  expect(await page.evaluate(key => localStorage.getItem(key), storageKey)).toBe('{broken')
})

test('a late older file read cannot replace the latest import preview', async ({ page }) => {
  await open(page)
  await page.evaluate(() => {
    const read = File.prototype.text
    ;(window as any).releaseOldFile = null
    File.prototype.text = function() {
      if (this.name === 'slow.json') return new Promise<string>(resolve => { (window as any).releaseOldFile = async () => resolve(await read.call(this)) })
      return read.call(this)
    }
  })
  await upload(page, encodeBoards([board('旧文件')]), 'slow.json')
  await expect(page.getByRole('status')).toContainText('正在读取')
  const preview = await upload(page, encodeBoards([board('新文件')]), 'fast.json')
  await expect(preview).toContainText('新文件')
  await page.evaluate(async () => { await (window as any).releaseOldFile() })
  await expect(preview).not.toContainText('旧文件')
  await preview.getByRole('button', { name: '确认导入所选看板' }).click()
  expect(decodeBoards((await page.evaluate(key => localStorage.getItem(key), storageKey))!).map(b => b.title)).toEqual(['新文件'])
})
