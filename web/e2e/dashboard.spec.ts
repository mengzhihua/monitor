import { test, expect } from '@playwright/test'

const token = 'browser-test-token'

test('login, real chart, long history, layout and logout', async ({page, request}, info) => {
  const errors: string[]=[]
  page.on('pageerror',error=>errors.push(error.message))
  expect((await request.get('/api/v1/info')).status()).toBe(401)
  await page.goto('/')
  await expect(page.getByPlaceholder('token',{exact:true})).toBeVisible()
  await page.getByPlaceholder('token',{exact:true}).fill(token)
  await page.getByRole('button',{name:'进入',exact:true}).click()
  await expect(page.locator('.host')).toContainText('browser-test')
  await expect(page.locator('[title="live"]')).toBeVisible()
  await page.getByPlaceholder('筛选图表…').fill('system.ram')
  await expect(page.locator('.card')).toHaveCount(1)
  await expect(page.locator('.card .id')).toHaveText('system.ram')
  await expect(page.locator('.card canvas').first()).toBeVisible()
  await expect.poll(async()=>{
    const response=await request.get('/api/v1/data?chart=system.ram&after=-60&points=60',{headers:{Authorization:`Bearer ${token}`}})
    const body=await response.json()
    return body.result.data.some((row: (number|null)[])=>row.slice(1).some(v=>v!==null))
  }).toBe(true)
  await Promise.all([
    page.waitForResponse(response => response.url().includes('/api/v1/data?') && response.url().includes('after=-86400')),
    page.locator('.controls select').selectOption('86400'),
  ])
  await expect(page.locator('.card canvas').first()).toBeVisible()
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true)
  const plot = await page.locator('.card .u-over').boundingBox()
  expect(plot).not.toBeNull()
  await page.mouse.move(plot!.x+plot!.width-0.05,plot!.y+plot!.height/2)
  await expect(page.locator('.card .u-value').nth(1)).toContainText(/\d/)
  await page.screenshot({path:info.outputPath('dashboard.png'),fullPage:true})
  expect(errors).toEqual([])
  await page.getByRole('button',{name:'退出登录'}).click()
  await expect(page.getByPlaceholder('token',{exact:true})).toBeVisible()
  expect(await page.evaluate(()=>sessionStorage.getItem('monitor.token'))).toBeFalsy()
})

test('token URL is removed, live frames are numeric, reload and reconnect work', async ({page,context})=>{
  await page.goto('/?token='+token)
  await expect(page.locator('[title="live"]')).toBeVisible()
  expect(new URL(page.url()).searchParams.has('token')).toBe(false)
  const frame = await page.evaluate(async (token) => {
    return await new Promise<{chart:string;t:number;v:Record<string,number>}>((resolve,reject)=>{
      const ws=new WebSocket(`ws://${location.host}/api/v1/live?charts=system.ram`,['monitor','bearer.'+btoa(token).replace(/=+$/,'')])
      const timer=setTimeout(()=>{ws.close();reject(new Error('live frame timed out'))},15000)
      ws.onerror=()=>{clearTimeout(timer);reject(new Error('socket failed'))}
      ws.onmessage=event=>{const value=JSON.parse(event.data);if(value.chart==='system.ram'){clearTimeout(timer);ws.close();resolve(value)}}
    })
  },token)
  expect(frame.t).toBeGreaterThan(0)
  expect(Object.values(frame.v).every(Number.isFinite)).toBe(true)
  await page.reload()
  await expect(page.locator('[title="live"]')).toBeVisible()
  await context.setOffline(true)
  await expect(page.locator('[title="reconnecting"]')).toBeVisible({timeout:15000})
  await context.setOffline(false)
  await expect(page.locator('[title="live"]')).toBeVisible({timeout:20000})
})
