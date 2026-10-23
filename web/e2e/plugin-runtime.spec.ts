import { expect, test } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'
import { chmod, cp, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import type { DataResponse } from '../src/api/monitoring/types'
import type { PluginAction, PluginCatalog, PluginStatus } from '../src/api/plugins/types'

// Authentication belongs only to this temporary installation. Do not retain it
// in a Playwright trace, URL, stdout or a request attachment.
test.use({ trace: 'off' })
test.skip(process.platform === 'win32', 'The heartbeat example is a POSIX shell process fixture.')

async function unusedPort(): Promise<number> {
  const listener = createServer()
  await new Promise<void>((resolve, reject) => {
    listener.once('error', reject)
    listener.listen(0, '127.0.0.1', resolve)
  })
  const address = listener.address()
  if (!address || typeof address === 'string') throw new Error('Could not allocate a loopback test port')
  await new Promise<void>((resolve, reject) => listener.close(error => error ? reject(error) : resolve()))
  return address.port
}

function processExists(pid: number): boolean {
  try { process.kill(pid, 0); return true }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ESRCH') return false
    throw error
  }
}

test('real manifest plugin samples and responds to browser disable, enable and restart', async ({ page }) => {
  test.setTimeout(60_000)
  const root = await mkdtemp(join(tmpdir(), 'monitor-plugin-ui-'))
  const pluginPIDs = new Set<number>()
  let child: ChildProcess | undefined
  let exited: Promise<{ code: number | null; signal: NodeJS.Signals | null }> | undefined
  let exitResult: { code: number | null; signal: NodeJS.Signals | null } | undefined
  let spawnError: Error | undefined
  try {
    const pluginDir = join(root, 'plugins', 'heartbeat')
    await cp(fileURLToPath(new URL('../../examples/plugins/heartbeat', import.meta.url)), pluginDir, { recursive: true })
    await chmod(join(pluginDir, 'collector.sh'), 0o700)
    const config = join(root, 'config.yaml')
    // Keep authentication absent so monitord generates a private web-password.
    await writeFile(config, `global:\n  hostname: plugin-ui-runtime\ncollectors:\n  enabled: [mem]\nplugins:\n  enabled: true\n  dir: ${JSON.stringify(join(root, 'plugins'))}\nhealth:\n  enabled: false\n`, { mode: 0o600 })
    const origin = `http://127.0.0.1:${await unusedPort()}`
    child = spawn(fileURLToPath(new URL('../../core/bin/monitord', import.meta.url)), [
      '-config', config, '-data-dir', join(root, 'data'), '-listen', new URL(origin).host,
    ], { stdio: ['ignore', 'ignore', 'ignore'] })
    exited = new Promise(resolve => {
      child!.once('error', error => { spawnError = error; resolve({ code: null, signal: null }) })
      child!.once('exit', (code, signal) => { exitResult = { code, signal }; resolve(exitResult) })
    })
    await expect.poll(async () => {
      if (spawnError) throw new Error(`Could not start isolated monitord: ${spawnError.message}`)
      if (exitResult) throw new Error(`Isolated monitord exited before ready: ${exitResult.code ?? exitResult.signal}`)
      try { return (await fetch(`${origin}/healthz`, { signal: AbortSignal.timeout(1000) })).status }
      catch { return 0 }
    }, { timeout: 20_000, message: 'isolated monitord becomes ready' }).toBe(200)

    const token = (await readFile(join(root, 'data', 'web-password'), 'utf8')).trim()
    async function getJSON<T>(path: string): Promise<T> {
      const response = await fetch(`${origin}${path}`, {
        headers: { Authorization: `Bearer ${token}` }, signal: AbortSignal.timeout(3000),
      })
      expect(response.status, `isolated runtime GET ${path}`).toBe(200)
      return response.json() as Promise<T>
    }
    async function catalog(): Promise<PluginCatalog> {
      const current = await getJSON<PluginCatalog>('/api/v1/plugins')
      for (const plugin of current.plugins) if (plugin.pid) pluginPIDs.add(plugin.pid)
      return current
    }
    function heartbeat(current: PluginCatalog): PluginStatus {
      const plugin = current.plugins.find(item => item.name === 'heartbeat')
      expect(plugin, 'heartbeat manifest is discovered').toBeDefined()
      return plugin!
    }
    async function running(previousPID?: number): Promise<PluginStatus> {
      let plugin: PluginStatus | undefined
      await expect.poll(async () => {
        plugin = heartbeat(await catalog())
        return plugin.enabled && plugin.state === 'running' && !!plugin.pid
          && plugin.pid !== previousPID && plugin.stats.samples >= 2 && plugin.stats.charts === 1
      }, { timeout: 12_000, message: 'heartbeat process publishes real samples' }).toBe(true)
      expect(plugin!.stats.errors).toBe(0)
      expect(processExists(plugin!.pid!)).toBe(true)
      return plugin!
    }

    const initial = await running()
    expect(initial.source).toBe('manifest')
    expect(initial.manifest).toMatchObject({ id: 'heartbeat', name: 'Heartbeat example', version: '1.0.0', protocol: 'plugins.d/v1', capabilities: ['metrics'] })
    await expect.poll(async () => {
      const data = await getJSON<DataResponse>('/api/v1/data?chart=plugin.heartbeat.pulse&after=-30&points=30')
      expect(data.units).toBe('pulses')
      expect(data.dimension_ids).toEqual(['count'])
      return data.result.data.some(row => typeof row[1] === 'number' && Number.isFinite(row[1]) && row[1] > 0)
    }, { message: 'heartbeat samples are queryable through the real metrics API' }).toBe(true)
    // Use session storage directly so the generated credential is never in a URL.
    await page.addInitScript(({ origin, token }) => {
      if (location.origin === origin) sessionStorage.setItem('monitor.token', token)
    }, { origin, token })
    await page.goto(`${origin}/?view=plugins`)
    const panel = page.getByRole('region', { name: '本机插件管理', exact: true })
    const card = panel.getByRole('article', { name: 'Heartbeat example', exact: true })
    await expect(card).toBeVisible()
    await expect(page.locator('.workspace-context')).toContainText('本机 · plugin-ui-runtime')
    await expect(card).toContainText('清单插件包')
    await expect(card).toContainText('1.0.0 · API 1')
    await expect(card).toContainText('plugins.d/v1')
    await expect(card.locator('.metrics span').filter({ hasText: /^采样 / }).locator('b')).toHaveText(/[1-9]\d*/)
    const pid = card.locator('.metadata div').filter({ has: page.getByText('进程 PID', { exact: true }) }).locator('dd')
    await expect(pid).toHaveText(String(initial.pid))

    async function control(label: string, action: PluginAction): Promise<PluginCatalog> {
      const responsePromise = page.waitForResponse(response => response.url() === `${origin}/api/v1/plugins/actions` && response.request().method() === 'POST')
      await card.getByRole('button', { name: label, exact: true }).click()
      const response = await responsePromise
      expect(response.status(), `browser ${action} reaches the real plugin API`).toBe(200)
      const request = response.request().postDataJSON()
      expect(request).toMatchObject({ name: 'heartbeat', action })
      const accepted = await response.json() as PluginCatalog
      expect(accepted.revision).toBe(request.if_revision + 1)
      return accepted
    }
    const stopped = heartbeat(await control('停用', 'disable'))
    expect(stopped.enabled).toBe(false)
    expect(stopped.state).toBe('disabled')
    expect(stopped.pid || 0).toBe(0)
    await expect.poll(() => processExists(initial.pid!), { message: 'disabled plugin process exits' }).toBe(false)
    expect(heartbeat(await catalog()).enabled).toBe(false)
    await expect(card.getByRole('button', { name: '启用', exact: true })).toBeEnabled()
    await expect(card.locator('.state')).toHaveText('已停用')
    await expect(pid).toHaveText('—')

    await control('启用', 'enable')
    const enabled = await running(initial.pid)
    await panel.getByRole('button', { name: '刷新目录', exact: true }).click()
    await expect(pid).toHaveText(String(enabled.pid))
    await expect(card.locator('.state')).toHaveText('运行中')

    await control('重启', 'restart')
    const restarted = await running(enabled.pid)
    await expect.poll(() => processExists(enabled.pid!), { message: 'restart replaces the previous plugin process' }).toBe(false)
    await panel.getByRole('button', { name: '刷新目录', exact: true }).click()
    await expect(pid).toHaveText(String(restarted.pid))
    await expect(card.locator('.metrics span').filter({ hasText: /^采样 / }).locator('b')).toHaveText(/[1-9]\d*/)
    expect((await catalog()).diagnostics).toEqual([])
  } finally {
    let shutdownError: string | undefined
    if (child && exited && !spawnError) {
      if (!exitResult) child.kill('SIGINT')
      const result = await Promise.race([exited, delay(10_000, undefined, { ref: false })])
      if (!result) {
        child.kill('SIGKILL')
        await exited
        shutdownError = 'Isolated monitord did not stop within 10 seconds of SIGINT'
      } else if (result.code !== 0) {
        shutdownError = `Isolated monitord exited with ${result.code ?? result.signal}`
      }
    }
    for (const pid of pluginPIDs) {
      if (!processExists(pid)) continue
      // Only this fixture's recorded plugin process groups are eligible for cleanup.
      try { process.kill(-pid, 'SIGKILL') } catch { /* already exited */ }
      shutdownError = 'Isolated monitord left a heartbeat process alive after shutdown'
    }
    await rm(root, { recursive: true, force: true })
    expect(shutdownError, 'isolated runtime and plugins shut down cleanly').toBeUndefined()
  }
})
