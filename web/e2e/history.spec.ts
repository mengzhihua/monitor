import { expect, test } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import type { HandlingHistoryPage, OperationsSnapshot } from '../src/api'

const headers = { Authorization: 'Bearer browser-test-token' }

test('history searches beyond the latest 100 episodes with independent filters and retention labels', async ({ page }, info) => {
  await page.goto('/?token=browser-viewer-token')
  const history = page.getByRole('region', { name: '处理记录查询' })
  await history.getByLabel('历史关键词').fill('history_case_')
  await history.getByRole('button', { name: '查询历史', exact: true }).click()
  await expect(history.locator('.history-count')).toHaveText('已显示 25 / 130 个处理阶段')
  await history.getByRole('button', { name: '加载更多历史' }).click()
  await expect(history.locator('.history-record')).toHaveCount(50)
  await history.getByLabel('历史关键词').fill('history_case_129')
  await history.getByLabel('历史责任人').fill('fixture-owner')
  await history.getByLabel('历史操作者').fill('fixture-oncall')
  await history.getByLabel('历史处理进度').selectOption('watching')
  await history.getByLabel('历史严重级别').selectOption('WARNING')
  await history.getByLabel('历史确认状态').selectOption('true')
  await history.getByLabel('历史时间范围').selectOption('1')
  await expect(history.getByRole('button', { name: '导出全部匹配 JSON' })).toBeDisabled()
  await history.getByRole('button', { name: '查询历史', exact: true }).click()
  await expect(history.locator('.history-count')).toHaveText('已显示 1 / 1 个处理阶段')
  await history.locator('.history-record summary').click()
  await expect(history).toContainText('共发生 25 次操作，仅保留最近 20 次')
  await expect(history).toContainText('handover-129')
  await expect(history).toContainText('不在当前告警快照')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await history.screenshot({ path: info.outputPath('handling-history.png') })
  await history.getByLabel('历史操作者').fill('missing-operator')
  await history.getByRole('button', { name: '查询历史', exact: true }).click()
  await expect(history).toContainText('没有匹配的处理历史')
})

test('history exports all matching rows and rejects a changed snapshot before download', async ({ page, request }) => {
  await page.goto('/?token=browser-viewer-token')
  const history = page.getByRole('region', { name: '处理记录查询' })
  await history.getByLabel('历史关键词').fill('history_case_')
  await history.getByRole('button', { name: '查询历史', exact: true }).click()
  await expect(history.locator('.history-count')).toHaveText('已显示 25 / 130 个处理阶段')
  const jsonEvent = page.waitForEvent('download')
  await history.getByRole('button', { name: '导出全部匹配 JSON' }).click()
  const json: HandlingHistoryPage = JSON.parse(await readFile((await (await jsonEvent).path())!, 'utf8'))
  expect(json.records).toHaveLength(130)
  expect(json.next_cursor).toBe('')
  expect(json.records.find(r => r.problem.name === 'history_case_129')?.history.at(-1)?.note).toBe('=SUM(1,2)\nhandover-129')
  const csvEvent = page.waitForEvent('download')
  await history.getByRole('button', { name: '导出全部匹配 CSV' }).click()
  const csv = await readFile((await (await csvEvent).path())!, 'utf8')
  expect(csv).toContain('history_case_129')
  expect(csv).toContain("'=SUM(1,2)\nhandover-129")
  const current: OperationsSnapshot = await (await request.get('/api/v1/operations', { headers })).json()
  const p = current.problems.find(p => p.name === 'browser_ram_notice')!
  expect((await request.post('/api/v1/operations/handling', { headers, data: {
    id: p.id, action: 'comment', note: 'Invalidate history snapshot during a handover.', revision: p.handling.revision,
  } })).ok()).toBe(true)
  await history.getByRole('button', { name: '导出全部匹配 JSON' }).click()
  await expect(history.getByRole('alert')).toContainText('处理记录已变化或服务已重启')
  await expect(history.getByRole('button', { name: '加载更多历史' })).toBeDisabled()
  await history.getByRole('button', { name: '查询历史', exact: true }).click()
  await expect(history.getByRole('alert')).toHaveCount(0)
  await expect(history.locator('.history-count')).toHaveText('已显示 25 / 130 个处理阶段')
})
