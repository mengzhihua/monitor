import { expect, test, type Page } from '@playwright/test'
import type { HandlingChange, OperationsSnapshot } from '../src/api'

const problemID = 'local/system.ram/modular_problem'
function overview(revision: number, status: 'open' | 'watching' = 'open'): OperationsSnapshot {
  const identity = { node: 'local', hostname: 'module-host', chart: 'system.ram', name: 'modular_problem', severity: 'WARNING', since: 100 }
  const handling = { id: problemID, problem: identity, revision, acknowledged: false, assignee: '', status, history: [] }
  return {
    now: 200, persistent: true, current_user: { name: 'admin', role: 'admin' },
    assignees: [{ name: 'admin', role: 'admin' }, { name: 'operator', role: 'troubleshooter' }],
    summary: { nodes: 1, live: 1, offline: 0, stale: 0, critical: 0, warning: 1, unacknowledged: 1 },
    nodes: [], activity: [], problems: [{ ...identity, id: problemID, node_status: 'live', family: 'memory',
      info: 'Module lifecycle regression', value: 80, units: '%', updated: 200, stale: false, handling }],
  }
}

async function open(page: Page) {
  await page.goto('/?token=browser-test-token')
  const card = page.locator(`[data-problem-id="${problemID}"]`)
  await expect(card).toBeVisible()
  return card
}

async function roundTripSection(page: Page) {
  await page.getByRole('tab', { name: '主机资源', exact: true }).click()
  await expect(page.locator('.problem')).toHaveCount(0)
  await page.getByRole('tab', { name: '问题中心', exact: true }).click()
}

test('problem card remount keeps every draft and the revision captured before a newer refresh', async ({ page }) => {
  let revision = 1
  const submitted: (HandlingChange & { revision: number; note: string })[] = []
  await page.route(/\/api\/v1\/operations(?:\?.*)?$/, route => route.fulfill({ json: overview(revision) }))
  await page.route('**/api/v1/operations/handling', async route => {
    submitted.push(route.request().postDataJSON())
    if (submitted.length === 1) await route.fulfill({ status: 409, body: 'changed' })
    else await route.fulfill({ json: { revision: 3 } })
  })
  const card = await open(page)
  await card.getByLabel('modular_problem 处理备注').fill('问题卡片重建后继续编辑')
  await card.getByLabel('modular_problem 责任人', { exact: true }).selectOption('operator')
  await card.getByLabel('modular_problem 处理进度', { exact: true }).selectOption('watching')
  await card.getByRole('checkbox').check()
  await page.getByLabel('批量处理备注', { exact: true }).fill('分区切换保留统一备注')
  await roundTripSection(page)
  await expect(card.getByLabel('modular_problem 处理备注')).toHaveValue('问题卡片重建后继续编辑')
  await expect(card.getByLabel('modular_problem 责任人', { exact: true })).toHaveValue('operator')
  await expect(card.getByLabel('modular_problem 处理进度', { exact: true })).toHaveValue('watching')
  await expect(card.getByRole('checkbox')).toBeChecked()
  await expect(page.getByLabel('批量处理备注', { exact: true })).toHaveValue('分区切换保留统一备注')

  revision = 2
  await page.getByRole('button', { name: '刷新总览', exact: true }).click()
  await expect(page.getByText('正在读取节点和问题…', { exact: true })).toHaveCount(0)
  await card.getByRole('button', { name: '保存进度', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('草稿已保留')
  expect(submitted[0]).toMatchObject({ revision: 1, status: 'watching', note: '问题卡片重建后继续编辑' })
  await roundTripSection(page)
  await expect(card.getByLabel('modular_problem 处理备注')).toHaveValue('问题卡片重建后继续编辑')
  await card.getByRole('button', { name: '保存进度', exact: true }).click()
  await expect.poll(() => submitted.length).toBe(2)
  expect(submitted[1]).toMatchObject({ revision: 2, status: 'watching', note: '问题卡片重建后继续编辑' })
})

test('in-flight single handling keeps the batch mutex across section unmounts', async ({ page }) => {
  let release!: () => void
  const pending = new Promise<void>(resolve => { release = resolve })
  let started = false, completed = false
  await page.route(/\/api\/v1\/operations(?:\?.*)?$/, route => route.fulfill({ json: overview(completed ? 2 : 1, completed ? 'watching' : 'open') }))
  await page.route('**/api/v1/operations/handling', async route => {
    started = true
    await pending
    completed = true
    await route.fulfill({ json: { revision: 2 } })
  })
  try {
    const card = await open(page)
    await card.getByRole('checkbox').check()
    await card.getByLabel('modular_problem 处理进度', { exact: true }).selectOption('watching')
    await card.getByRole('button', { name: '保存进度', exact: true }).click()
    await expect.poll(() => started).toBe(true)
    await roundTripSection(page)
    await expect(card.getByRole('button', { name: '保存进度', exact: true })).toBeDisabled()
    await expect(page.getByRole('region', { name: '批量问题处置', exact: true }).getByRole('button', { name: '正在保存…', exact: true })).toBeDisabled()
    await expect(card.getByLabel('modular_problem 处理进度', { exact: true })).toBeDisabled()
    release()
    await expect(card.locator('.progress-state')).toHaveText('观察中')
    await expect(card.getByLabel('modular_problem 处理进度', { exact: true })).toBeEnabled()
    await expect(page.getByRole('region', { name: '批量问题处置', exact: true })).toContainText('已变化或恢复，请重新选择')
  } finally {
    release()
  }
})

test('saving one field preserves the remaining draft revision across concurrent updates', async ({ page }) => {
  let revision = 1
  const submitted: (HandlingChange & { revision: number; note: string })[] = []
  await page.route(/\/api\/v1\/operations(?:\?.*)?$/, route => route.fulfill({ json: overview(revision) }))
  await page.route('**/api/v1/operations/handling', route => {
    submitted.push(route.request().postDataJSON())
    if (submitted.length === 1) {
      // Our save committed revision 2; another operator committed revision 3
      // before the following overview read. The unsaved progress is still based on 2.
      revision = 3
      return route.fulfill({ json: { ...overview(2).problems[0]!.handling, assignee: 'operator' } })
    }
    return route.fulfill({ status: 409, body: 'changed' })
  })
  const card = await open(page)
  await card.getByLabel('modular_problem 责任人', { exact: true }).selectOption('operator')
  await card.getByLabel('modular_problem 处理进度', { exact: true }).selectOption('watching')
  await card.getByRole('button', { name: '保存责任人', exact: true }).click()
  await expect(card.getByRole('button', { name: '保存进度', exact: true })).toBeEnabled()
  await card.getByRole('button', { name: '保存进度', exact: true }).click()
  await expect.poll(() => submitted.length).toBe(2)
  expect(submitted[1]).toMatchObject({ revision: 2, status: 'watching' })
  await expect(page.getByRole('alert')).toContainText('草稿已保留')
  await expect(card.getByLabel('modular_problem 处理进度', { exact: true })).toHaveValue('watching')
})

test('failed overview refresh prevents single problem mutations while keeping drafts', async ({ page }) => {
  let fail = false
  await page.route(/\/api\/v1\/operations(?:\?.*)?$/, route => fail
    ? route.fulfill({ status: 503, body: 'unavailable' })
    : route.fulfill({ json: overview(1) }))
  const card = await open(page)
  await card.getByLabel('modular_problem 处理备注').fill('保留待提交备注')
  await card.getByLabel('modular_problem 责任人', { exact: true }).selectOption('operator')
  await card.getByLabel('modular_problem 处理进度', { exact: true }).selectOption('watching')
  fail = true
  await page.getByRole('button', { name: '刷新总览', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('刷新失败')
  for (const name of ['保存责任人', '分配给我', '保存进度', '确认问题', '添加备注']) {
    await expect(card.getByRole('button', { name, exact: true })).toBeDisabled()
  }
  await expect(card.getByLabel('modular_problem 处理备注')).toHaveValue('保留待提交备注')
  fail = false
  await page.getByRole('button', { name: '刷新总览', exact: true }).click()
  await expect(card.getByRole('button', { name: '添加备注', exact: true })).toBeEnabled()
})
