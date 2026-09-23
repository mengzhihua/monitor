import { test, expect, type Page } from '@playwright/test'
import { newBoard, encodeBoards, storageKey, decodeBoards } from '../src/dashboardConfig'
import { changePersonalBoards, readPersonalBoards, preferencesKey, readPreferences } from '../src/dashboardStorage'

async function open(page: Page) {
  await page.goto('/?view=charts&token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  return page.getByLabel('常用聚合看板', { exact: true })
}

test('favorites, last board and catalog folding survive reload; primary coverage filters out unrelated services', async ({ page }, info) => {
  const panel = await open(page)
  await panel.getByLabel('仅看主分组已采集的样板').check()
  await expect(panel.locator('.presets button').filter({ hasText: 'MySQL 排障' })).toHaveCount(0)
  await expect(panel.locator('.presets button').filter({ hasText: '研发总览' })).toHaveCount(1)
  await panel.locator('.presets button').filter({ hasText: '资源瓶颈' }).click()
  await panel.getByRole('button', { name: '收藏当前看板', exact: true }).click()
  await panel.getByRole('button', { name: '收藏', exact: true }).click()
  await expect(panel.locator('.presets button')).toHaveCount(1)
  await panel.getByRole('button', { name: '收起样板目录' }).click()
  await page.reload()
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  await expect(panel.getByRole('heading', { level: 2, name: '资源瓶颈', exact: true })).toBeVisible()
  await expect(panel.locator('.presets')).toBeHidden()
  await expect(panel.getByRole('button', { name: '取消收藏', exact: true })).toBeVisible()
  const compute = panel.getByLabel('CPU 与负载', { exact: true })
  await compute.getByRole('button', { name: '折叠分组 CPU 与负载', exact: true }).click()
  await expect(compute.locator('.card')).toHaveCount(0)
  await panel.getByRole('navigation', { name: '看板分组导航' }).getByRole('button', { name: /CPU 与负载/ }).click()
  await expect(compute.locator('.card').first()).toBeVisible()
  await expect(compute).toBeFocused()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: info.outputPath('optimized-dashboard.png'), fullPage: true })
})

test('two tabs preserve unrelated edits, reject stale drafts and allow saving a conflict as a copy', async ({ page, context }) => {
  const panel = await open(page)
  await panel.getByRole('button', { name: '复制为个人看板', exact: true }).click()
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  await panel.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await panel.getByLabel('看板名称', { exact: true }).fill('第一个标签的草稿')
  await expect(panel.getByRole('button', { name: '新建看板', exact: true })).toBeDisabled()
  const second = await context.newPage()
  const other = await open(second)
  await other.getByRole('button', { name: '编辑个人看板', exact: true }).click()
  await other.getByLabel('看板名称', { exact: true }).fill('另一标签的最新版本')
  await other.getByRole('button', { name: '保存看板', exact: true }).click()
  await page.bringToFront()
  await expect(panel.getByRole('status')).toContainText('当前草稿保留')
  await panel.getByRole('button', { name: '保存看板', exact: true }).click()
  await expect(panel.getByRole('alert')).toContainText('已在其他标签页修改或删除')
  await expect(panel.getByLabel('看板名称', { exact: true })).toHaveValue('第一个标签的草稿')
  await panel.getByRole('button', { name: '另存为新看板', exact: true }).click()
  const boards = decodeBoards((await page.evaluate(key => localStorage.getItem(key), storageKey))!)
  expect(boards.map(b => b.title)).toEqual(['另一标签的最新版本', '第一个标签的草稿'])
  await second.bringToFront()
  await expect(other.getByRole('status')).toContainText('已同步其他标签页')
  await second.close()
})

test('storage writes preserve other boards, reject deleted/replaced targets and never overwrite corrupt data', () => {
  const data = new Map<string, string>()
  const storage = { getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => { data.set(key, value) } }
  const a = { ...newBoard(), title: 'A', groupIds: ['compute'] }
  const b = { ...newBoard(), title: 'B', groupIds: ['memory'] }
  storage.setItem(storageKey, encodeBoards([a]))
  changePersonalBoards(storage, { type: 'import', boards: [b] })
  changePersonalBoards(storage, { type: 'save', expected: a, board: { ...a, title: 'A2' } })
  expect(readPersonalBoards(storage).map(x => x.title)).toEqual(['A2', 'B'])
  expect(() => changePersonalBoards(storage, { type: 'delete', expected: a })).toThrow('其他标签页')
  changePersonalBoards(storage, { type: 'delete', expected: b })
  expect(() => changePersonalBoards(storage, { type: 'save', expected: b, board: b })).toThrow('其他标签页')
  storage.setItem(storageKey, '{broken')
  expect(() => changePersonalBoards(storage, { type: 'import', boards: [a] })).toThrow('原始数据已保留')
  expect(storage.getItem(storageKey)).toBe('{broken')
  storage.setItem(preferencesKey, JSON.stringify({ selected: 'deleted', favorites: ['developer', 'deleted', 'developer'], collapsed: true }))
  expect(readPreferences(storage, ['developer'])).toEqual({ selected: 'developer', favorites: ['developer'], collapsed: true })
})
