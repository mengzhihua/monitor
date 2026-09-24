import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

const toolbarButton = 'header button[title="配置：本机与节点"]'

type FixtureOpts = {
  mode?: 'standalone' | 'hub'
  role?: 'admin' | 'viewer'
  managePut?: number
  push?: { pushed: boolean; online: boolean }
  apply?: { rev: number; state: 'applied' | 'rejected' | 'deferred'; error?: string; at: number }
}

/** Fully mocked dashboard: the panel under test never reaches a real backend. */
async function fixture(page: Page, opts: FixtureOpts = {}) {
  await page.route('**/api/v1/info', route => route.fulfill({ json: {
    version: 'test', mode: opts.mode ?? 'standalone', uptime: 10, charts_count: 0, metrics_count: 0,
    host: { id: 'hub', hostname: 'hub-local', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 },
    collectors: [], alarms: null, user: { name: 'admin', role: opts.role ?? 'admin' },
  } }))
  await page.route('**/api/v1/charts*', route => route.fulfill({ json: { charts: {} } }))

  let manage = { path: '/etc/monitor/monitor.yaml', yaml: 'hostname: hub\n', updated: 100, size: 16 }
  const managePuts: Array<Record<string, unknown>> = []
  await page.route('**/api/v1/manage/config', async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: manage })
    const body = route.request().postDataJSON() as { yaml: string }
    managePuts.push(body as Record<string, unknown>)
    if (opts.managePut === 409) {
      manage = { ...manage, yaml: 'hostname: elsewhere\n', updated: 200 }
      return route.fulfill({ status: 409, json: { error: 'modified elsewhere' } })
    }
    if (opts.managePut === 400) return route.fulfill({ status: 400, json: { error: 'bad yaml: unknown field' } })
    manage = { ...manage, yaml: body.yaml, updated: manage.updated + 1 }
    return route.fulfill({ json: manage })
  })

  let restarts = 0
  await page.route('**/api/v1/manage/restart', route => { restarts++; return route.fulfill({ json: { ok: true } }) })

  let node: Record<string, unknown> = {}
  const nodePuts: Array<Record<string, unknown>> = []
  const nodeQueries: string[] = []
  if (opts.mode === 'hub') {
    await page.route('**/api/v1/nodes*', route => route.fulfill({ json: { now: 5000, nodes: [
      { id: '', hostname: 'hub-local', os: 'linux', arch: 'amd64', version: 'test', local: true, status: 'live', first_seen: 1, last_seen: 2, charts_count: 1, alarms: {} },
      { id: 'agent-1', hostname: 'agent-one', os: 'linux', arch: 'amd64', version: 'test', local: false, status: 'live', first_seen: 1, last_seen: 2, charts_count: 1, alarms: {} },
    ] } }))
    node = {
      node_id: 'agent-1', yaml: 'hostname: agent\n', reported: 'hostname: agent-reported\n', updated: 50,
      report_at: 4000, online: opts.push ? opts.push.online : true, pending: opts.push ? !opts.push.pushed : false,
      ...(opts.apply ? { apply: opts.apply } : {}),
    }
    await page.route('**/api/v1/hub/config*', async route => {
      nodeQueries.push(new URL(route.request().url()).searchParams.get('node') ?? '')
      if (route.request().method() === 'GET') return route.fulfill({ json: node })
      const body = route.request().postDataJSON() as Record<string, unknown>
      nodePuts.push(body)
      node = { ...node, yaml: body.yaml, updated: (node.updated as number) + 1, ...(opts.push ?? {}) }
      return route.fulfill({ json: { ...node, ...(opts.push ?? {}) } })
    })
  }
  return {
    managePuts, nodePuts, nodeQueries,
    restarted: () => restarts,
    panel: page.getByRole('region', { name: '配置管理' }),
  }
}

async function open(page: Page, opts: FixtureOpts = {}) {
  const f = await fixture(page, opts)
  await page.goto('/?view=charts&token=browser-test-token')
  await page.locator(toolbarButton).click()
  await expect(f.panel).toBeVisible()
  return f
}

test('local config loads, saves with concurrency token and hides the node tab on standalone', async ({ page }) => {
  const f = await open(page)
  await expect(f.panel).toContainText('/etc/monitor/monitor.yaml')
  const area = f.panel.getByLabel('本机配置内容')
  await expect(area).toHaveValue('hostname: hub\n')
  await expect(f.panel.getByRole('button', { name: '节点配置' })).toHaveCount(0)
  await area.fill('hostname: edited\n')
  await f.panel.getByRole('button', { name: '保存本机配置' }).click()
  await expect(f.panel.getByRole('status')).toContainText('配置已保存并校验通过')
  expect(f.managePuts).toEqual([{ yaml: 'hostname: edited\n', if_updated: 100 }])
})

test('a 409 rereads the server copy and asks for review', async ({ page }) => {
  const f = await open(page, { managePut: 409 })
  const area = f.panel.getByLabel('本机配置内容')
  await expect(area).toHaveValue('hostname: hub\n')
  await area.fill('hostname: mine\n')
  await f.panel.getByRole('button', { name: '保存本机配置' }).click()
  await expect(f.panel.getByRole('alert')).toContainText('配置已在别处被修改，已重新读取')
  await expect(area).toHaveValue('hostname: elsewhere\n')
})

test('a 400 keeps the draft and shows the server reason', async ({ page }) => {
  const f = await open(page, { managePut: 400 })
  const area = f.panel.getByLabel('本机配置内容')
  await area.fill('hostname: broken\n')
  await f.panel.getByRole('button', { name: '保存本机配置' }).click()
  await expect(f.panel.getByRole('alert')).toContainText('配置无效，未保存：bad yaml: unknown field')
  await expect(area).toHaveValue('hostname: broken\n')
})

test('restart posts only after confirmation', async ({ page }) => {
  const f = await open(page)
  const messages: string[] = []
  page.on('dialog', dialog => {
    messages.push(dialog.message())
    void (messages.length === 1 ? dialog.dismiss() : dialog.accept())
  })
  const button = f.panel.getByRole('button', { name: '重启服务' })
  await button.click()
  expect(messages[0]).toContain('systemd 自动拉起')
  expect(f.restarted()).toBe(0)
  await button.click()
  expect(f.restarted()).toBe(1)
  await expect(f.panel.getByRole('status')).toContainText('重启指令已发出')
})

test('node tab lists remote nodes only and pushes config to online agents', async ({ page }) => {
  const f = await open(page, { mode: 'hub', push: { pushed: true, online: true }, apply: { rev: 1, state: 'rejected', error: 'invalid stream key', at: 4500 } })
  await f.panel.getByRole('button', { name: '节点配置' }).click()
  const select = f.panel.getByLabel('选择节点')
  await expect.poll(async () => await select.locator('option').allTextContents()).toEqual(['选择节点', 'agent-one'])
  await select.selectOption('agent-1')
  const area = f.panel.getByLabel('节点配置内容')
  await expect(area).toHaveValue('hostname: agent-reported\n')
  await expect(f.panel.getByRole('alert')).toContainText('节点拒绝应用：invalid stream key')
  await expect(f.panel).toContainText('在线')
  await area.fill('hostname: agent-new\n')
  await f.panel.getByRole('button', { name: '保存并下发节点配置' }).click()
  await expect(f.panel.getByRole('status')).toContainText('已校验并推送给节点')
  expect(f.nodePuts).toEqual([{ node_id: 'agent-1', yaml: 'hostname: agent-new\n', if_updated: 50 }])
  expect(f.nodeQueries.length).toBeGreaterThan(0)
  expect(f.nodeQueries.every(q => q === 'agent-1')).toBe(true)
})

test('node tab queues the config for offline nodes', async ({ page }) => {
  const f = await open(page, { mode: 'hub', push: { pushed: false, online: false } })
  await f.panel.getByRole('button', { name: '节点配置' }).click()
  await f.panel.getByLabel('选择节点').selectOption('agent-1')
  await expect(f.panel).toContainText('离线')
  await expect(f.panel).toContainText('待应用')
  await f.panel.getByLabel('节点配置内容').fill('hostname: queued\n')
  await f.panel.getByRole('button', { name: '保存并下发节点配置' }).click()
  await expect(f.panel.getByRole('status')).toContainText('已保存，节点上线后自动下发')
})

test('non-admin gets a read-only editor', async ({ page }) => {
  const f = await open(page, { role: 'viewer' })
  await expect(f.panel.getByLabel('本机配置内容')).toHaveAttribute('readonly')
  await expect(f.panel.getByRole('button', { name: '保存本机配置' })).toBeDisabled()
  await expect(f.panel.getByRole('button', { name: '重启服务' })).toBeDisabled()
  await expect(f.panel).toContainText('只有管理员可以编辑配置。')
})
