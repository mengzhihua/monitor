import { expect, test } from '@playwright/test'
import { openTools } from './mobile-navigation'

const headers = { Authorization: 'Bearer browser-test-token' }

test('invalid device YAML is rejected before replacing the saved config', async ({ request }) => {
  const before = await (await request.get('/api/v1/manage/config', { headers })).json()
  for (const invalid of ['profile: unsupported-device', 'version: 4', 'targets: [{name: nested, address: 192.0.2.3}]']) {
    const yaml = before.yaml.replace('collectors:\n', `collectors:
  modules:
    snmp:
      targets:
        - name: device-1
          address: 192.0.2.10
          community: e2e-private-community
          ${invalid}
`)
    const response = await request.put('/api/v1/manage/config', { headers, data: { yaml, if_updated: before.updated } })
    expect(response.status()).toBe(400)
    expect(await response.text()).not.toContain('e2e-private-community')
    const unchanged = await (await request.get('/api/v1/manage/config', { headers })).json()
    expect(unchanged.yaml).toBe(before.yaml)
    expect(unchanged.updated).toBe(before.updated)
  }
})

test('admin manages SNMP and Redfish devices from a mobile-friendly form', async ({ page, request }) => {
  const before = await (await request.get('/api/v1/manage/config', { headers })).json()
  try {
    await page.goto('/?token=browser-test-token')
    await openTools(page)
    await page.getByRole('button', { name: '配置', exact: true }).click()
    await page.getByRole('button', { name: '添加 SNMP 设备', exact: true }).click()
    await page.getByLabel('SNMP 设备 1 标识', { exact: true }).fill('ups-office')
    await page.getByLabel('SNMP 设备 1 地址', { exact: true }).fill('192.0.2.10')
    await page.getByLabel('SNMP 设备 1 类型', { exact: true }).selectOption('ups')
    const community = page.getByLabel('SNMP 设备 1 Community', { exact: true })
    await expect(community).toHaveAttribute('type', 'password')
    await community.fill('e2e-readonly-community')
    await page.getByRole('button', { name: '添加 SNMP 设备', exact: true }).click()
    await page.getByRole('button', { name: '移除 SNMP 设备 2', exact: true }).click()
    await page.getByRole('button', { name: '添加 Redfish 设备', exact: true }).click()
    await page.getByLabel('Redfish 设备 1 标识', { exact: true }).fill('rack-bmc')
    await page.getByLabel('Redfish 设备 1 地址', { exact: true }).fill('https://192.0.2.20/redfish/v1')
    await page.getByLabel('Redfish 设备 1 用户', { exact: true }).fill('readonly-monitor')
    const password = page.getByLabel('Redfish 设备 1 密码', { exact: true })
    await expect(password).toHaveAttribute('type', 'password')
    await password.fill('e2e-bmc-password')
    await expect(page.getByLabel('Redfish 设备 1 跳过证书校验', { exact: true })).not.toBeChecked()

    // Cards remain usable at the same narrow viewport as the H5 dashboard.
    const card = page.getByRole('article', { name: 'Redfish 设备 1', exact: true })
    await card.scrollIntoViewIfNeeded()
    const bounds = await card.boundingBox()
    expect(bounds).not.toBeNull()
    expect(bounds!.x).toBeGreaterThanOrEqual(0)
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
    await page.getByRole('button', { name: '保存本机配置', exact: true }).click()
    await expect(page.getByText('配置已保存并校验通过。重启服务后生效。')).toBeVisible()
    let saved = await (await request.get('/api/v1/manage/config', { headers })).json()
    expect(saved.form.snmp_targets).toHaveLength(1)
    expect(saved.form.snmp_targets[0]).toMatchObject({ name: 'ups-office', profile: 'ups', community: 'e2e-readonly-community' })
    expect(saved.form.redfish_targets[0]).toMatchObject({ name: 'rack-bmc', password: 'e2e-bmc-password' })
    expect(saved.form.collectors_enabled).toEqual(expect.arrayContaining(['snmp', 'redfish']))
    await expect(page.getByLabel('SNMP 设备 1 标识', { exact: true })).toHaveAttribute('readonly', '')
    await expect(page.getByLabel('Redfish 设备 1 标识', { exact: true })).toHaveAttribute('readonly', '')

    await page.getByRole('textbox', { name: '主机名', exact: true }).fill('device-form-edit')
    await page.getByRole('button', { name: '保存本机配置', exact: true }).click()
    await expect(page.getByText('配置已保存并校验通过。重启服务后生效。')).toBeVisible()
    saved = await (await request.get('/api/v1/manage/config', { headers })).json()
    expect(saved.form.snmp_targets[0].community).toBe('e2e-readonly-community')
    expect(saved.form.redfish_targets[0].password).toBe('e2e-bmc-password')

    await page.getByRole('button', { name: '移除 SNMP 设备 1', exact: true }).click()
    await page.getByRole('button', { name: '保存本机配置', exact: true }).click()
    await expect(page.getByText('配置已保存并校验通过。重启服务后生效。')).toBeVisible()
    saved = await (await request.get('/api/v1/manage/config', { headers })).json()
    expect(saved.form.snmp_targets).toEqual([])
    expect(saved.form.redfish_targets).toHaveLength(1)
  } finally {
    await request.put('/api/v1/manage/config', { headers, data: { yaml: before.yaml } })
  }
})

test('legacy device settings stay intact when editing unrelated form fields', async ({ page, request }) => {
  const before = await (await request.get('/api/v1/manage/config', { headers })).json()
  const yaml = before.yaml.replace('collectors:\n', `collectors:
  modules:
    snmp:
      address: 192.0.2.10
      community: e2e-legacy-community
    redfish:
      url: https://192.0.2.20/redfish/v1
      user: readonly-monitor
      password: e2e-legacy-password
`)
  try {
    const seed = await request.put('/api/v1/manage/config', { headers, data: { yaml, if_updated: before.updated } })
    expect(seed.ok()).toBe(true)
    await page.goto('/?token=browser-test-token')
    await openTools(page)
    await page.getByRole('button', { name: '配置', exact: true }).click()
    await expect(page.getByRole('button', { name: '添加 SNMP 设备', exact: true })).toBeDisabled()
    await expect(page.getByRole('button', { name: '添加 Redfish 设备', exact: true })).toBeDisabled()
    await expect(page.getByText('已有旧版 SNMP 单设备配置', { exact: false })).toBeVisible()
    await page.getByRole('textbox', { name: '主机名', exact: true }).fill('legacy-device-preserved')
    await page.getByRole('button', { name: '保存本机配置', exact: true }).click()
    await expect(page.getByText('配置已保存并校验通过。重启服务后生效。')).toBeVisible()
    const saved = await (await request.get('/api/v1/manage/config', { headers })).json()
    expect(saved.yaml).toContain('e2e-legacy-community')
    expect(saved.yaml).toContain('e2e-legacy-password')
    expect(saved.form.snmp_targets).toBeUndefined()
    expect(saved.form.redfish_targets).toBeUndefined()
  } finally {
    await request.put('/api/v1/manage/config', { headers, data: { yaml: before.yaml } })
  }
})
