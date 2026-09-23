import { test, expect } from '@playwright/test'

test('built-in dashboards use real charts, filter, range and responsive layout', async ({ page }, info) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  const panel = page.getByLabel('常用聚合看板', { exact: true })
  await expect(panel.getByRole('heading', { name: '研发总览', exact: true })).toBeVisible()
  await expect(panel.locator('.presets button')).toHaveCount(42)
  await expect(panel.locator('.card .id')).toContainText(['system.cpu', 'system.load', 'system.ram'])
  await panel.locator('.card').first().scrollIntoViewIfNeeded()
  await expect(panel.locator('.card canvas').first()).toBeVisible()
  await expect(panel.getByLabel('接口与网关', { exact: true })).toContainText('当前节点尚未采集')
  await page.getByPlaceholder('筛选图表…').fill('system.ram')
  await expect(panel.locator('.card')).toHaveCount(1)
  await Promise.all([
    page.waitForResponse(r => r.url().includes('/api/v1/data?') && r.url().includes('after=-900')),
    (async () => {
      await page.locator('.controls select').selectOption('900')
      await panel.locator('.card').scrollIntoViewIfNeeded()
    })(),
  ])
  await page.getByPlaceholder('筛选图表…').fill('')
  const memory = panel.getByLabel('内存与交换', { exact: true })
  if (await memory.getByRole('button', { name: /展开其余/ }).count()) {
    await expect(memory.locator('.card')).toHaveCount(4)
    await memory.getByRole('button', { name: /展开其余/ }).click()
    expect(await memory.locator('.card').count()).toBeGreaterThan(4)
    await memory.getByRole('button', { name: '收起', exact: true }).click()
    await expect(memory.locator('.card')).toHaveCount(4)
  }
  for (const name of ['接口与网络', '数据库与缓存', '容器工作台', '资源瓶颈', '发布观察']) {
    await panel.getByRole('button', { name, exact: false }).click()
    await expect(panel.getByRole('heading', { name, exact: true, level: 2 })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  await panel.getByRole('button', { name: '研发总览', exact: false }).click()
  await page.screenshot({ path: info.outputPath('presets.png'), fullPage: true })
  await page.getByRole('button', { name: '全部指标', exact: true }).click()
  await expect(panel).toHaveCount(0)
  await expect(page.locator('.card').first()).toBeVisible()
  expect(errors).toEqual([])
})

test('presets match semantic contexts, fall back to IDs and exclude unrelated charts', async () => {
  const { chartsForGroup, dashboards } = await import('../src/dashboards')
  const database = dashboards.find(b => b.id === 'data')!.groups[0]!
  const disk = dashboards.find(b => b.id === 'resources')!.groups.find(g => g.id === 'disk')!
  const charts = [
    { id: 'custom.instance', context: 'mysql.queries', priority: 2 },
    { id: 'postgres.connections', context: '', priority: 1 },
    { id: 'notmysql.queries', context: '', priority: 0 },
    { id: 'disk_await.sda', context: 'disk.await', priority: 3 },
  ] as import('../src/api').Chart[]
  expect(chartsForGroup(charts, database).map(c => c.id)).toEqual(['postgres.connections', 'custom.instance'])
  expect(chartsForGroup(charts, disk).map(c => c.id)).toEqual(['disk_await.sda'])
  expect(chartsForGroup([], database)).toEqual([])
  const containers = dashboards.find(b => b.id === 'containers')!.groups[0]!
  expect(chartsForGroup([{ id: 'k8s_kubelet.kubelet_pods_running', context: '', priority: 1 }] as import('../src/api').Chart[], containers)).toHaveLength(1)
})

test('node changes replace preset charts and scope history to the selected node', async ({ page }) => {
  await page.route('**/api/v1/info', async route => {
    const response = await route.fetch()
    await route.fulfill({ json: { ...await response.json(), mode: 'hub' } })
  })
  await page.route('**/api/v1/nodes', route => route.fulfill({ json: { now: Date.now() / 1000, nodes: [
    { id: '', hostname: 'local-test', local: true, status: 'live', alarms: {}, charts_count: 1 },
    { id: 'remote-test', hostname: 'remote-test', local: false, status: 'live', alarms: {}, charts_count: 1 },
  ] } }))
  await page.route('**/api/v1/charts?node=remote-test', async route => {
    const response = await route.fetch({ url: 'http://127.0.0.1:19997/api/v1/charts' })
    const body = await response.json()
    await route.fulfill({ json: { ...body, charts: { 'system.ram': body.charts['system.ram'] } } })
  })
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  await expect(page.locator('.dashboards .card .id').first()).toHaveText('system.cpu')
  const history = page.waitForRequest(r => r.url().includes('/api/v1/data?') && new URL(r.url()).searchParams.get('node') === 'remote-test')
  await page.locator('.node-select').selectOption('remote-test')
  await expect(page.locator('.dashboards .card .id')).toHaveText(['system.ram'])
  await page.locator('.dashboards .card').scrollIntoViewIfNeeded()
  await history
  await page.locator('.node-select').selectOption('')
  await expect(page.locator('.dashboards .card .id').first()).toHaveText('system.cpu')
})


test('catalog search and categories discover specialized dashboards without changing chart filters', async ({ page }) => {
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  const panel = page.getByLabel('常用聚合看板', { exact: true })
  const search = panel.getByRole('searchbox', { name: '搜索看板样板' })
  await panel.getByRole('button', { name: '数据服务', exact: true }).click()
  await expect(panel.locator('.presets button')).toHaveCount(6)
  await search.fill('redis')
  await expect(panel.locator('.presets button')).toHaveCount(1)
  await panel.locator('.presets button').click()
  await expect(panel.getByRole('heading', { name: 'Redis 缓存', exact: true })).toBeVisible()
  await expect(panel.getByLabel('Redis 与内存缓存', { exact: true })).toContainText('尚未采集')
  await expect(panel.getByLabel('内存与交换', { exact: true }).locator('.card')).not.toHaveCount(0)
  await expect(page.getByPlaceholder('筛选图表…')).toHaveValue('')
  await search.fill('no-such-preset')
  await expect(panel.locator('.presets button')).toHaveCount(0)
  await panel.getByRole('button', { name: '清除样板筛选' }).click()
  await expect(panel.locator('.presets button')).toHaveCount(42)
  for (const name of ['MySQL 排障', 'PostgreSQL 排障', 'Redis 缓存', '消息队列', 'Java / Tomcat', '搜索与分析', 'Kubernetes 工作台', 'DNS 与连通性', '存储与磁盘健康', 'GPU 工作台', '服务存活', '日志管道']) {
    await search.fill(name)
    await panel.locator('.presets button').filter({ has: page.getByText(name, { exact: true }) }).click()
    await expect(panel.getByRole('heading', { name, exact: true, level: 2 })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
})

test('specialized groups recognize collector contexts without prefix collisions', async () => {
  const { chartsForGroup, dashboards } = await import('../src/dashboards')
  const cases = [
    ['mysql', 'mysql.queries'], ['postgres', 'pgbouncer.db_client_connections'],
    ['redis', 'redis.keyspace'], ['queues', 'rabbitmq.queue_messages'],
    ['java', 'tomcat.jvm_memory_usage'], ['search', 'elasticsearch.cluster_health_status'],
    ['kubernetes', 'k8s_kubelet.kubelet_pods_running'], ['dns', 'dns_query.query_time'],
    ['storage', 'disk.await'], ['gpu', 'nvidia_smi.gpu_utilization'],
    ['availability', 'supervisord.processes'], ['logs', 'fluentd.buffer_queue_length'],
  ]
  for (const [id, context] of cases) {
    const group = dashboards.find(b => b.id === id)!.groups[0]!
    const charts = [
      { id: 'instance-1', context, priority: 1 },
      { id: 'instance-2', context: 'unrelated.' + context, priority: 2 },
    ] as import('../src/api').Chart[]
    expect(chartsForGroup(charts, group).map(c => c.id), id).toEqual(['instance-1'])
  }
  expect(new Set(dashboards.map(b => b.id)).size).toBe(dashboards.length)
})


test('operations catalog exposes focused runbooks and accurate collector matching', async ({ page }) => {
  const { dashboards, chartsForGroup } = await import('../src/dashboards')
  const cases = [
    ['oncall', 'ping.loss'], ['saturation', 'system.memory_full_pressure'],
    ['capacity', 'disk.inodes'], ['tcp', 'netfilter.conntrack_errors'],
    ['tls', 'httpcheck.cert_expiry'], ['clock', 'system.clock_sync_state'],
    ['hardware', 'upsd.ups_battery_charge'], ['files', 'filecheck.file_modification_time_ago'],
  ]
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  const panel = page.getByLabel('常用聚合看板', { exact: true })
  await panel.getByRole('button', { name: '运维值班', exact: true }).click()
  await expect(panel.locator('.presets button')).toHaveCount(8)
  for (const [id, context] of cases) {
    const board = dashboards.find(b => b.id === id)!
    const charts = [{ id: 'instance', context, priority: 1 }, { id: 'other', context: 'unrelated.' + context, priority: 0 }] as import('../src/api').Chart[]
    expect(chartsForGroup(charts, board.groups[0]!).map(c => c.id)).toEqual(['instance'])
    await panel.locator('.presets button').filter({ has: page.getByText(board.title, { exact: true }) }).click()
    await expect(panel.getByRole('heading', { name: board.title, level: 2, exact: true })).toBeVisible()
    await expect(panel.getByLabel('建议排查顺序').locator('li')).toHaveCount(3)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  const tls = dashboards.find(b => b.id === 'tls')!.groups[0]!
  expect(chartsForGroup([{ id: 'httpcheck.cert_expiry.production', context: '', priority: 1 }, { id: 'httpcheck.status.production', context: '', priority: 2 }] as import('../src/api').Chart[], tls)).toHaveLength(1)
})


test('platform dashboards match service instances and remain usable without collectors', async ({ page }, info) => {
  const { dashboards, chartsForGroup } = await import('../src/dashboards')
  const cases = [
    ['virtualization', 'libvirt.vm_status'], ['proxies', 'nginx.requests'],
    ['shares', 'nfsd.io'], ['vpn', 'wireguard.peer_latest_handshake_ago'],
    ['mail', 'postfix.qemails'], ['discovery', 'consul.health_checks'],
    ['identity', 'openldap.connections'], ['devices', 'snmp.device_net_operstatus'],
  ]
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  const panel = page.getByLabel('常用聚合看板', { exact: true })
  await panel.getByRole('button', { name: '平台服务', exact: true }).click()
  await expect(panel.locator('.presets button')).toHaveCount(9)
  for (const [id, context] of cases) {
    const board = dashboards.find(b => b.id === id)!
    const charts = [
      { id: 'instance', context, priority: 1 },
      { id: context + '.server', context: '', priority: 2 },
      { id: 'unrelated', context: 'other.' + context, priority: 0 },
    ] as import('../src/api').Chart[]
    expect(chartsForGroup(charts, board.groups[0]!).map(c => c.id)).toEqual(['instance', context + '.server'])
    await panel.locator('.presets button').filter({ has: page.getByText(board.title, { exact: true }) }).click()
    await expect(panel.getByRole('heading', { name: board.title, exact: true, level: 2 })).toBeVisible()
    await expect(panel.getByLabel(board.groups[0]!.title, { exact: true })).toContainText('尚未采集')
    await expect(panel.getByLabel('建议排查顺序').locator('li')).toHaveCount(3)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  await panel.getByLabel('建议排查顺序').scrollIntoViewIfNeeded()
  await page.screenshot({ path: info.outputPath('platform-dashboard.png') })
})


test('specialist dashboards isolate their primary service and expose runbooks', async ({ page }) => {
  const { dashboards, chartsForGroup } = await import('../src/dashboards')
  const cases = [
    ['mongodb', 'mongodb.connections'], ['cassandra', 'cassandra.compaction_pending_tasks_count'],
    ['ceph', 'ceph.cluster_status'], ['zfs', 'zfspool.pool_health_state'],
    ['workers', 'phpfpm.queue'], ['http-cache', 'varnish.cache_hit_ratio_total'],
    ['firewall', 'fail2ban.jail_banned_ips'], ['firewall', 'netfilter.nftables_packets'], ['bmc', 'redfish.system_health_state'],
  ]
  await page.goto('/?token=browser-test-token')
  await page.getByRole('button', { name: '常用聚合看板', exact: true }).click()
  const panel = page.getByLabel('常用聚合看板', { exact: true })
  const errors: string[] = []
  page.on('pageerror', e => errors.push(e.message))
  for (const [id, context] of cases) {
    const board = dashboards.find(b => b.id === id)!
    const charts = [
      { id: 'real-instance', context, priority: 1 },
      { id: context + '.instance', context: '', priority: 2 },
      { id: 'unrelated', context: 'unrelated.' + context, priority: 0 },
    ] as import('../src/api').Chart[]
    expect(chartsForGroup(charts, board.groups[0]!).map(c => c.id)).toEqual(['real-instance', context + '.instance'])
    await panel.getByRole('searchbox', { name: '搜索看板样板' }).fill(board.title)
    await panel.locator('.presets button').filter({ has: page.getByText(board.title, { exact: true }) }).click()
    await expect(panel.getByRole('heading', { name: board.title, exact: true, level: 2 })).toBeVisible()
    await expect(panel.getByLabel(board.groups[0]!.title, { exact: true })).toContainText('尚未采集')
    await expect(panel.getByLabel('建议排查顺序').locator('li')).toHaveCount(3)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  }
  expect(errors).toEqual([])
})
