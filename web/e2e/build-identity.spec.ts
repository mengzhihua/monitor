import { test, expect } from '@playwright/test'
import { readFile, writeFile } from 'node:fs/promises'

test('header shows local agent build identity from /api/v1/info', async ({ page, request }) => {
  const info = await (await request.get('/api/v1/info', {
    headers: { Authorization: 'Bearer browser-test-token' },
  })).json()
  expect(info.version).toBeTruthy()
  expect(typeof info.commit === 'string' || info.commit === undefined).toBeTruthy()

  await page.goto('/?view=charts&token=browser-test-token')
  await expect(page.locator('header .brand')).toContainText('Monitor')
  // Build identity is a desktop-only meta chip on the local agent (hidden when a hub node is selected).
  const chip = page.locator('header .meta .desktop-detail').filter({ hasText: info.version })
  await expect(chip.first()).toBeVisible({ timeout: 15_000 })
  if (info.commit) {
    await expect(chip.first()).toContainText(info.commit.slice(0, 12))
    const title = await chip.first().getAttribute('title')
    expect(title || '').toContain(info.commit)
    if (info.build_time) expect(title || '').toContain(info.build_time)
    expect(title || '').toMatch(/未提交修改|工作区干净/)
  }
})

test('diagnostic export includes commit, build_time and dirty', async ({ baseURL }, info) => {
  const resp = await fetch(new URL('/api/v1/operations/diagnostic/export?include=ops,alarms', baseURL!), {
    headers: { Authorization: 'Bearer browser-operator-token' },
  })
  expect(resp.status).toBe(200)
  const exported = await resp.json()
  expect(exported.schema).toBe('monitor.diagnostic.v1')
  expect(exported.host).toEqual(expect.objectContaining({
    version: expect.any(String),
  }))
  // Fields are always present on the live e2e agent (ldflags or vcs fallback).
  expect(exported.host).toHaveProperty('commit')
  expect(exported.host).toHaveProperty('build_time')
  expect(exported.host).toHaveProperty('dirty')
  expect(typeof exported.host.dirty).toBe('boolean')
  const saved = info.outputPath('diagnostic-build-identity.json')
  await writeFile(saved, JSON.stringify(exported.host, null, 2))
  expect((await readFile(saved, 'utf8')).length).toBeGreaterThan(10)
})
