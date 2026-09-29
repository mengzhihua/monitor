import { expect, test } from '@playwright/test'

test('operations problem card shows recovery hold and keep-firing deadline', async ({ page }) => {
  let mode: 'recovery' | 'keep' = 'recovery'
  const now = Math.floor(Date.now() / 1000)
  await page.route('**/api/v1/operations*', async route => {
    const url = new URL(route.request().url())
    if (url.pathname !== '/api/v1/operations') return route.continue()
    const handling = {
      problem: { node: 'local', hostname: 'ops-test', chart: 'system.ram', name: 'ram_high', severity: 'WARNING', since: now },
      id: 'local/system.ram/ram_high', acknowledged: false, revision: 1, assignee: '', status: 'open', history: [],
    }
    const problem = {
      id: 'local/system.ram/ram_high', node: 'local', hostname: 'ops-test', node_status: 'live',
      chart: 'system.ram', name: 'ram_high', severity: 'WARNING', family: 'ram', info: 'memory high',
      value: 75, units: '%', since: now, updated: now, stale: false, handling,
      ...(mode === 'recovery' ? { recovery_hold: true } : { hold_until: now + 120 }),
    }
    await route.fulfill({
      json: {
        current_user: { name: 'admin', role: 'admin' },
        assignees: [{ name: 'admin', role: 'admin' }],
        activity: [],
        now,
        nodes: [],
        problems: [problem],
        summary: { nodes: 1, live: 1, warning: 1, critical: 0, unacknowledged: 1, unassigned: 1, offline: 0, stale: 0 },
        persistent: true,
      },
    })
  })
  await page.goto('/?token=browser-test-token')
  const card = page.locator('[data-problem-id="local/system.ram/ram_high"]')
  await expect(card).toContainText('等待恢复')
  await expect(card).not.toContainText('恢复保持至')
  mode = 'keep'
  await page.getByRole('button', { name: '刷新总览' }).click()
  await expect(card).toContainText('恢复保持至')
  await expect(card).not.toContainText('等待恢复')
})
