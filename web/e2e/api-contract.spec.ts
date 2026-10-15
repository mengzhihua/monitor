import { expect, test } from '@playwright/test'
import { ApiError, api, auth, get, selection, send } from '../src/api'
import { monitoringApi } from '../src/api/monitoring/client'
import { operationsApi } from '../src/api/operations/client'
import { configurationApi } from '../src/api/configuration/client'
import { alarmsApi } from '../src/api/alarms/client'
import { hubApi } from '../src/api/hub/client'

// These contract tests execute the same clients used by the UI without starting
// a browser or making network requests. Each worker owns its mocked globals.
interface Call { url: URL; init: RequestInit }
let calls: Call[] = []
let response: () => Response
let originalFetch: typeof fetch

function lastCall(): Call { return calls[calls.length - 1]! }

test.beforeEach(() => {
  originalFetch = globalThis.fetch
  calls = []
  response = () => Response.json({ ok: true })
  globalThis.fetch = async (input, init = {}) => {
    calls.push({ url: new URL(String(input), 'https://monitor.test'), init })
    return response()
  }
  selection.node = ''
  auth.token = 'api-contract-token'
})

test.afterEach(() => {
  globalThis.fetch = originalFetch
  selection.node = ''
  auth.token = ''
})

test('compatibility facade exports the same domain clients and a shared node selection', async () => {
  const clients = [monitoringApi, alarmsApi, operationsApi, configurationApi, hubApi]
  const keys = clients.flatMap(client => Object.keys(client))
  expect(new Set(keys).size).toBe(keys.length)
  expect(Object.keys(api).sort()).toEqual(keys.sort())
  for (const client of clients) {
    for (const [name, method] of Object.entries(client)) {
      expect(api[name as keyof typeof api]).toBe(method)
    }
  }
  selection.node = 'node from legacy facade'
  await monitoringApi.charts()
  expect(lastCall().url.searchParams.get('node')).toBe(selection.node)
})

test('metric and alarm reads carry the selected node with encoded parameters and AbortSignal', async () => {
  const controller = new AbortController()
  const signal = controller.signal
  selection.node = 'branch/上海 & rack=3'
  const reads = [
    () => api.charts(signal),
    () => api.functions(signal),
    () => api.function('processes', { filter: 'cpu > 20%' }, signal),
    () => api.weights('anomaly-rate', { after: -60 }, signal),
    () => api.logs({ query: 'a & b' }, signal),
    () => api.data('disk./dev/sda', -300, 0, 120, signal),
    () => api.contexts(signal),
    () => api.alarms(signal),
    () => api.alarmLog(47, signal),
  ]
  for (const read of reads) {
    await read()
    expect(lastCall().url.searchParams.get('node')).toBe(selection.node)
    expect(lastCall().init.signal).toBe(signal)
    expect(new Headers(lastCall().init.headers).get('Authorization')).toBe('Bearer api-contract-token')
  }
  expect(calls[2]!.url.searchParams.get('filter')).toBe('cpu > 20%')
  expect(calls[4]!.url.searchParams.get('query')).toBe('a & b')
  expect(calls[5]!.url.searchParams.get('chart')).toBe('disk./dev/sda')
  expect(calls[5]!.url.searchParams.get('points')).toBe('120')
  expect(calls[7]!.url.searchParams.get('all')).toBe('true')
  expect(calls[8]!.url.searchParams.get('after')).toBe('47')
  await api.closeAlarm(42, 'confirmed')
  expect(lastCall().url.searchParams.get('node')).toBe(selection.node)
  expect(JSON.parse(String(lastCall().init.body))).toEqual({ alarm_id: 42, comment: 'confirmed' })
  await api.function('processes', { node: 'explicit-function-node' })
  expect(lastCall().url.searchParams.get('node')).toBe('explicit-function-node')
  selection.node = ''
  await api.charts()
  expect(lastCall().url.search).toBe('')
})

test('local management and global operations never inherit the selected remote node', async () => {
  selection.node = 'remote-should-not-leak'
  const reads = [
    () => api.info(), () => api.nodes(), () => api.manageHealth(),
    () => api.manageConfig(), () => api.alertRules(), () => api.silenceState(),
    () => api.operations(undefined, { q: 'cpu & memory', limit: '20' }),
    () => api.operationsViews(), () => api.services(), () => api.topology(),
    () => api.availability(), () => api.notifications(), () => api.maintenance(),
    () => api.handlingHistory({ status: 'watching' }), () => api.spaces(),
    () => api.rooms(), () => api.claims(), () => api.console(),
    () => api.templates(), () => api.inventory(),
  ]
  for (const read of reads) {
    await read()
    expect(lastCall().url.searchParams.has('node')).toBe(false)
  }
  expect(calls[6]!.url.searchParams.get('q')).toBe('cpu & memory')
  expect(calls[6]!.url.searchParams.get('limit')).toBe('20')
  await api.nodeConfig('explicit/配置 & node')
  expect(lastCall().url.searchParams.get('node')).toBe('explicit/配置 & node')
  await api.nodeInventory('asset/1')
  expect(lastCall().url.searchParams.get('node')).toBe('asset/1')
  await api.templateEffective('template/1')
  expect(lastCall().url.searchParams.get('node')).toBe('template/1')
  await api.forgetNode('offline/node')
  expect(lastCall().url.searchParams.get('node')).toBe('offline/node')
})

test('transport preserves JSON, body-free requests, empty responses and structured HTTP errors', async () => {
  await send('PUT', '/api/test', { text: '中文', revision: 0 })
  expect(lastCall().init.method).toBe('PUT')
  expect(new Headers(lastCall().init.headers).get('Content-Type')).toBe('application/json')
  expect(lastCall().init.body).toBe('{"text":"中文","revision":0}')
  response = () => new Response(null, { status: 204 })
  expect(await send<void>('DELETE', '/api/test')).toBeUndefined()
  expect(lastCall().init.body).toBeUndefined()
  expect(new Headers(lastCall().init.headers).has('Content-Type')).toBe(false)
  response = () => new Response('', { status: 200 })
  expect(await send('POST', '/api/test')).toBeUndefined()
  auth.token = ''
  response = () => Response.json({ answer: 42 })
  expect(await get('/api/test')).toEqual({ answer: 42 })
  expect(new Headers(lastCall().init.headers).has('Authorization')).toBe(false)
  response = () => new Response('revision conflict', { status: 409 })
  await expect(send('PUT', '/api/test', {})).rejects.toMatchObject({ status: 409, message: '/api/test: 409 revision conflict' })
  response = () => new Response('unauthorized', { status: 401 })
  await expect(get('/api/test')).rejects.toBeInstanceOf(ApiError)
  await expect(get('/api/test')).rejects.toMatchObject({ status: 401, message: '/api/test: 401 unauthorized' })
})

test('transport propagates fetch cancellation without replacing it with an HTTP error', async () => {
  const controller = new AbortController()
  globalThis.fetch = (_input, init = {}) => new Promise((_resolve, reject) => {
    init.signal!.addEventListener('abort', () => reject(init.signal!.reason), { once: true })
  })
  const result = get('/api/slow', controller.signal)
  const reason = new DOMException('Canceled by navigation', 'AbortError')
  controller.abort(reason)
  await expect(result).rejects.toBe(reason)
})

test('downloads preserve authentication, full body, filename and global query scope', async () => {
  selection.node = 'unrelated-selected-node'
  const controller = new AbortController()
  const body = '{"rows":[1,2],"name":"监控"}'
  response = () => new Response(body, { headers: { 'Content-Disposition': 'attachment; filename="diagnostic-test.json"' } })
  const exported = await api.exportDiagnostic({ node: 'explicit-node' }, controller.signal)
  expect(exported.filename).toBe('diagnostic-test.json')
  expect(exported.blob.type).toBe('application/json;charset=utf-8')
  expect(await exported.blob.text()).toBe(body)
  expect(lastCall().init.signal).toBe(controller.signal)
  expect(lastCall().url.searchParams.get('node')).toBe('explicit-node')
  expect(new Headers(lastCall().init.headers).get('Authorization')).toBe('Bearer api-contract-token')
  const history = await api.exportHandlingHistory({ format: 'csv', query: 'a & b' }, controller.signal)
  expect(await history.text()).toBe(body)
  expect(lastCall().url.searchParams.has('node')).toBe(false)
  expect(lastCall().url.searchParams.get('query')).toBe('a & b')
  response = () => new Response('{}')
  expect((await api.exportDiagnostic()).filename).toMatch(/^monitor-diagnostic-\d+\.json$/)
  response = () => new Response('forbidden', { status: 403 })
  await expect(api.exportDiagnostic()).rejects.toMatchObject({ status: 403, message: '403 forbidden' })
  await expect(api.exportHandlingHistory({})).rejects.toMatchObject({ status: 403, message: '403 forbidden' })
})

test('live connection keeps tokens in subprotocols and preserves node and chart URL encoding', () => {
  const originalLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
  Object.defineProperty(globalThis, 'location', { configurable: true, value: { protocol: 'https:', host: 'monitor.test:8443' } })
  try {
    selection.node = 'node/上海 & rack=3'
    auth.token = '令牌 + / ='
    const url = new URL(api.liveURL(['system.cpu', 'disk./dev/sda']))
    expect(url.protocol).toBe('wss:')
    expect(url.host).toBe('monitor.test:8443')
    expect(url.searchParams.get('node')).toBe(selection.node)
    expect(url.searchParams.get('charts')).toBe('system.cpu,disk./dev/sda')
    expect(url.searchParams.has('token')).toBe(false)
    expect(api.liveProtocols()).toEqual(['monitor', `bearer.${Buffer.from(auth.token).toString('base64url')}`])
    auth.token = ''
    expect(api.liveProtocols()).toEqual(['monitor'])
  } finally {
    if (originalLocation) Object.defineProperty(globalThis, 'location', originalLocation)
    else Reflect.deleteProperty(globalThis, 'location')
  }
})
