import { expect, test, type Page, type Route } from '@playwright/test'
import { workspace } from './mobile-navigation'

function gate() {
  let release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  return { wait, release }
}

async function fixture(page: Page, handle?: (route: Route, url: URL) => Promise<boolean>) {
  const puts: Array<{ target: string; body: Record<string, unknown> }> = []
  let local = { path: '/tmp/module-fixture.yaml', yaml: 'hostname: local\n', updated: 10, writable: true }
  const configs: Record<string, Record<string, unknown>> = {
    'node-a': { node_id: 'node-a', yaml: 'hostname: node-a\n', updated: 20, online: true },
    'node-b': { node_id: 'node-b', yaml: 'hostname: node-b\n', updated: 30, online: true },
  }
  await page.routeWebSocket('**/api/v1/live*', () => {})
  await page.route('**/api/v1/**', async route => {
    const url = new URL(route.request().url())
    if (await handle?.(route, url)) return
    if (url.pathname === '/api/v1/manage/config') {
      if (route.request().method() === 'PUT') {
        const body = route.request().postDataJSON() as Record<string, unknown>
        puts.push({ target: 'local', body })
        local = { ...local, yaml: body.yaml as string, updated: local.updated + 1 }
      }
      await route.fulfill({ json: local })
      return
    }
    if (url.pathname === '/api/v1/hub/config') {
      const body = route.request().method() === 'PUT' ? route.request().postDataJSON() as Record<string, unknown> : undefined
      const id = body?.node_id as string | undefined ?? url.searchParams.get('node') ?? ''
      if (body) {
        puts.push({ target: id, body })
        configs[id] = { ...configs[id], yaml: body.yaml, updated: Number(configs[id]?.updated) + 1, pushed: true }
      }
      await route.fulfill({ json: configs[id] })
      return
    }
    const replies: Record<string, unknown> = {
      '/api/v1/info': { version: 'test', mode: 'hub', uptime: 1, charts_count: 0, metrics_count: 0,
        host: { id: 'local', hostname: 'fixture', os: 'linux', arch: 'amd64', labels: {}, update_every: 1 },
        collectors: [], alarms: {}, user: { name: 'admin', role: 'admin' } },
      '/api/v1/charts': { charts: {} }, '/api/v1/functions': [], '/api/v1/alarms': { alarms: {} },
      '/api/v1/auth/oidc/status': { enabled: false },
      '/api/v1/nodes': { nodes: ['node-a', 'node-b'].map(id => ({ id, hostname: id, local: false, status: 'live', first_seen: 1, last_seen: 1, charts_count: 0, alarms: {} })) },
    }
    await route.fulfill({ json: replies[url.pathname] ?? {} })
  })
  await page.goto('/?view=config&token=browser-test-token')
  const panel = page.getByRole('region', { name: '配置管理' })
  await expect(panel.getByLabel('本机配置内容')).toHaveValue(local.yaml)
  return { panel, puts }
}

test('a slow first node read completes without being repeatedly cancelled by polling', async ({ page }) => {
  await page.clock.install()
  const pending = gate()
  let reads = 0
  const { panel } = await fixture(page, async (route, url) => {
    if (url.pathname !== '/api/v1/hub/config') return false
    reads++
    await pending.wait
    await route.fulfill({ json: { node_id: 'node-a', yaml: 'hostname: slow-node\n', updated: 21 } }).catch(() => {})
    return true
  })
  try {
    await panel.getByRole('button', { name: '节点配置', exact: true }).click()
    await panel.getByLabel('选择节点').selectOption('node-a')
    await expect.poll(() => reads).toBe(1)
    await page.clock.runFor(25000)
    expect(reads).toBe(1)
    await expect(panel.getByText('正在读取节点配置…')).toBeVisible()
  } finally { pending.release() }
  await expect(panel.getByLabel('节点配置内容')).toHaveValue('hostname: slow-node\n')
})

test('changing nodes cancels the old read and a late response cannot replace the selected config', async ({ page }) => {
  const pending = gate()
  let started = false
  let cancelled = false
  page.on('requestfailed', request => {
    const url = new URL(request.url())
    if (url.pathname === '/api/v1/hub/config' && url.searchParams.get('node') === 'node-a') cancelled = true
  })
  const { panel, puts } = await fixture(page, async (route, url) => {
    if (url.pathname !== '/api/v1/hub/config' || url.searchParams.get('node') !== 'node-a') return false
    started = true
    await pending.wait
    await route.fulfill({ json: { node_id: 'node-a', yaml: 'hostname: late-a\n', updated: 99 } }).catch(() => {})
    return true
  })
  try {
    await panel.getByRole('button', { name: '节点配置', exact: true }).click()
    const select = panel.getByLabel('选择节点')
    await select.selectOption('node-a')
    await expect.poll(() => started).toBe(true)
    await select.selectOption('node-b')
    await expect(panel.getByLabel('节点配置内容')).toHaveValue('hostname: node-b\n')
    await expect.poll(() => cancelled).toBe(true)
  } finally { pending.release() }
  await panel.getByLabel('节点配置内容').fill('hostname: edited-b\n')
  await panel.getByRole('button', { name: '保存并下发节点配置' }).click()
  await expect.poll(() => puts).toEqual([{ target: 'node-b', body: { node_id: 'node-b', yaml: 'hostname: edited-b\n', if_updated: 30 } }])
  await expect(panel.getByLabel('节点配置内容')).toHaveValue('hostname: edited-b\n')
})

test('local and node drafts retain independent revisions while switching scopes', async ({ page }) => {
  const { panel, puts } = await fixture(page)
  await panel.getByLabel('本机配置内容').fill('hostname: local-draft\n')
  await panel.getByRole('button', { name: '节点配置', exact: true }).click()
  await panel.getByLabel('选择节点').selectOption('node-a')
  await panel.getByLabel('节点配置内容').fill('hostname: remote-draft\n')
  await panel.getByRole('button', { name: '本机配置', exact: true }).click()
  await expect(panel.getByLabel('本机配置内容')).toHaveValue('hostname: local-draft\n')
  await panel.getByRole('button', { name: '保存本机配置' }).click()
  await expect(panel.getByRole('status')).toContainText('配置已保存')
  await panel.getByRole('button', { name: '节点配置', exact: true }).click()
  await expect(panel.getByLabel('节点配置内容')).toHaveValue('hostname: remote-draft\n')
  await panel.getByRole('button', { name: '保存并下发节点配置' }).click()
  await expect.poll(() => puts).toEqual([
    { target: 'local', body: { yaml: 'hostname: local-draft\n', if_updated: 10 } },
    { target: 'node-a', body: { node_id: 'node-a', yaml: 'hostname: remote-draft\n', if_updated: 20 } },
  ])
  await expect(panel.getByRole('status')).toContainText('已校验并推送')
  await workspace(page, 'charts')
  await expect(panel).toHaveCount(0)
})

test('a pending save locks both scopes and navigation until its draft baseline is acknowledged', async ({ page }) => {
  const pending = gate()
  let saving = false
  const { panel } = await fixture(page, async (route, url) => {
    if (url.pathname !== '/api/v1/manage/config' || route.request().method() !== 'PUT') return false
    saving = true
    await pending.wait
    await route.fulfill({ json: { path: '/tmp/module-fixture.yaml', yaml: 'hostname: saving\n', updated: 11, writable: true } })
    return true
  })
  try {
    await panel.getByLabel('本机配置内容').fill('hostname: saving\n')
    await panel.getByRole('button', { name: '保存本机配置' }).click()
    await expect.poll(() => saving).toBe(true)
    await expect(panel.getByRole('button', { name: '节点配置', exact: true })).toBeDisabled()
    await expect(panel.getByRole('button', { name: '关闭配置管理' })).toBeDisabled()
    await workspace(page, 'charts')
    await expect(panel).toBeVisible()
  } finally { pending.release() }
  await expect(panel.getByRole('status')).toContainText('配置已保存')
  await workspace(page, 'charts')
  await expect(panel).toHaveCount(0)
})
