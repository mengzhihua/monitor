import { expect, test, type Page } from '@playwright/test'
async function openShell(page:Page) {
 await page.goto('/?token=browser-admin-token')
 await expect(page.locator('header')).toContainText('browser-test')
 if (await page.getByRole('button',{name:'更多',exact:true}).isVisible()) await page.getByRole('button',{name:'更多',exact:true}).click()
 await page.getByRole('button',{name:'WebShell',exact:true}).click()
 const panel=page.getByRole('region',{name:'WebShell 主机终端'});await expect(panel.getByLabel('连接目标')).toHaveValue('browser-local-shell');return panel
}
test('real local WebShell requires an explicit connection and executes in a PTY',async({page},info)=>{
 if(info.project.name === 'mobile') await page.setViewportSize({width:320,height:780})
 const panel=await openShell(page)
 await expect(panel.getByRole('button',{name:'连接终端',exact:true})).toBeDisabled()
 await expect(panel.locator('.xterm')).toHaveCount(0)
 await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click();await expect(panel.getByText('已连接',{exact:true})).toBeVisible()
 await expect(panel.getByRole('region',{name:'我的终端会话'})).toContainText('当前页面')
 await panel.getByLabel('手机命令输入').fill("printf 'SHELL_%s_OK\\n' WEB; stty size")
 await panel.getByRole('button',{name:'发送命令',exact:true}).click()
 await expect(panel.getByLabel('交互终端输出',{exact:true})).toContainText('SHELL_WEB_OK')
 await panel.getByLabel('手机命令输入').fill("printf 'WAITING_%s\\n' SIGNAL; sleep 20");await panel.getByRole('button',{name:'发送命令',exact:true}).click()
 await expect(panel.getByLabel('交互终端输出',{exact:true})).toContainText('WAITING_SIGNAL')
 await panel.getByRole('button',{name:'Ctrl+C',exact:true}).click()
 await panel.getByLabel('手机命令输入').fill("printf 'INTERRUPT_%s\\n' OK");await panel.getByRole('button',{name:'发送命令',exact:true}).click()
 await expect(panel.getByLabel('交互终端输出',{exact:true})).toContainText('INTERRUPT_OK')
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true)
 await panel.scrollIntoViewIfNeeded();await page.screenshot({path:info.outputPath('webshell.png'),scale:'css'})
 await panel.getByRole('button',{name:'断开连接',exact:true}).click();await expect(panel.getByText('未连接',{exact:true})).toBeVisible();await expect(panel.getByRole('checkbox')).not.toBeChecked()
 // A second connect proves the previous server-side quota/PTY was released.
 await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click();await expect(panel.getByText('已连接',{exact:true})).toBeVisible()
 await panel.getByLabel('手机命令输入').fill('exit');await panel.getByRole('button',{name:'发送命令',exact:true}).click();await expect(panel.getByRole('status')).toContainText('正常退出')
})
test('closing releases the real WebSocket and reopening does not reconnect',async({page})=>{
 const panel=await openShell(page);await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click();await expect(panel.getByText('已连接',{exact:true})).toBeVisible()
 await panel.getByRole('button',{name:'关闭 WebShell'}).click();await expect(panel).toHaveCount(0)
 if(await page.getByRole('button',{name:'更多',exact:true}).isVisible())await page.getByRole('button',{name:'更多',exact:true}).click()
 await page.getByRole('button',{name:'WebShell',exact:true}).click();await expect(panel.getByText('未连接',{exact:true})).toBeVisible();await expect(panel.getByRole('checkbox')).not.toBeChecked()
 await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click();await expect(panel.getByText('已连接',{exact:true})).toBeVisible()
})
test('SSH credentials are ephemeral and host-key failures are clear',async({page})=>{
 const target={id:'ssh-test',name:'SSH fixture',kind:'ssh',node:'local',address:'127.0.0.1:22',user:'fixture',host_key:'SHA256:fixture'}
 await page.route('**/api/v1/shell/targets',r=>r.fulfill({json:{enabled:true,secure:true,audit_enabled:true,targets:[target]}}))
 await page.route('**/api/v1/shell/tickets',r=>r.fulfill({json:{ticket:'fixture-ticket',expires_at:Math.floor(Date.now()/1000)+30}}))
 let credential='';await page.routeWebSocket('**/api/v1/shell/connect',socket=>socket.onMessage(message=>{
  const data=JSON.parse(String(message));if(data.type==='connect'){credential=data.password;socket.send(JSON.stringify({type:'error',message:'SSH authentication or host key validation failed'}));socket.close()}
 }))
 await page.goto('/?token=browser-admin-token');if(await page.getByRole('button',{name:'更多',exact:true}).isVisible())await page.getByRole('button',{name:'更多',exact:true}).click();await page.getByRole('button',{name:'WebShell',exact:true}).click()
 const panel=page.getByRole('region',{name:'WebShell 主机终端'});await panel.getByLabel('SSH 密码',{exact:true}).fill('temporary-ssh-secret');await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click()
 await expect(panel.getByRole('alert')).toContainText('指纹校验失败');expect(credential).toBe('temporary-ssh-secret');await expect(panel.getByLabel('SSH 密码',{exact:true})).toHaveValue('')
 const storage=await page.evaluate(()=>JSON.stringify({session:{...sessionStorage},local:{...localStorage}}));expect(storage).not.toContain('temporary-ssh-secret')
})
test('disabled configuration and 320px layout remain usable without connecting',async({page},info)=>{
 await page.setViewportSize({width:320,height:780})
 await page.route('**/api/v1/shell/targets',r=>r.fulfill({json:{enabled:false,secure:true,audit_enabled:true,targets:[]}}))
 await page.goto('/?token=browser-admin-token');await page.getByRole('button',{name:'更多',exact:true}).click();await page.getByRole('button',{name:'WebShell',exact:true}).click()
 const panel=page.getByRole('region',{name:'WebShell 主机终端'});await expect(panel.getByRole('button',{name:'连接终端',exact:true})).toBeDisabled();await panel.locator('summary').click();await expect(panel).toContainText('enabled: true');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true)
 await panel.scrollIntoViewIfNeeded();await page.screenshot({path:info.outputPath('webshell-320.png'),scale:'css'})
})
test('shared token and viewer cannot open WebShell',async({page,request})=>{
 for (const token of ['browser-viewer-token','browser-test-token','browser-operator-token']) {
  await page.goto('/?token='+token);await expect(page.locator('header')).toContainText('browser-test')
  if(await page.getByRole('button',{name:'更多',exact:true}).isVisible())await page.getByRole('button',{name:'更多',exact:true}).click()
  await expect(page.getByRole('button',{name:'WebShell',exact:true})).toHaveCount(0)
  const resp=await request.get('/api/v1/shell/targets',{headers:{Authorization:'Bearer '+token}});expect(resp.status()).toBe(403)
 }
})

test('target users whitelist hides restricted shells from other admins',async({page,request})=>{
 test.skip(process.platform === 'win32', 'local shell fixture is Unix-only')
 const allowed=await request.get('/api/v1/shell/targets',{headers:{Authorization:'Bearer browser-admin-token'}})
 expect(allowed.status()).toBe(200)
 const allowedBody=await allowed.json()
 expect(allowedBody.targets.map((t:{id:string})=>t.id)).toEqual(expect.arrayContaining(['browser-local-shell','browser-admin-only-shell']))
 const denied=await request.get('/api/v1/shell/targets',{headers:{Authorization:'Bearer browser-admin-b-token'}})
 expect(denied.status()).toBe(200)
 const deniedBody=await denied.json()
 expect(deniedBody.targets.map((t:{id:string})=>t.id)).toContain('browser-local-shell')
 expect(deniedBody.targets.map((t:{id:string})=>t.id)).not.toContain('browser-admin-only-shell')
 const ticket=await request.post('/api/v1/shell/tickets',{headers:{Authorization:'Bearer browser-admin-b-token','Content-Type':'application/json'},data:{target:'browser-admin-only-shell'}})
 expect(ticket.status()).toBe(404)
 await page.goto('/?token=browser-admin-b-token')
 if(await page.getByRole('button',{name:'更多',exact:true}).isVisible())await page.getByRole('button',{name:'更多',exact:true}).click()
 await page.getByRole('button',{name:'WebShell',exact:true}).click()
 const panel=page.getByRole('region',{name:'WebShell 主机终端'})
 await expect(panel.getByLabel('连接目标')).toHaveValue('browser-local-shell')
 await expect(panel.locator('option[value="browser-admin-only-shell"]')).toHaveCount(0)
 await page.goto('/?token=browser-admin-token')
 if(await page.getByRole('button',{name:'更多',exact:true}).isVisible())await page.getByRole('button',{name:'更多',exact:true}).click()
 await page.getByRole('button',{name:'WebShell',exact:true}).click()
 const allowedPanel=page.getByRole('region',{name:'WebShell 主机终端'})
 await allowedPanel.getByLabel('连接目标').selectOption('browser-admin-only-shell')
 await expect(allowedPanel.getByLabel('连接目标')).toHaveValue('browser-admin-only-shell')
})

test('another page can inspect and explicitly terminate its own lingering session',async({page,context})=>{
 const first=await openShell(page)
 await first.getByRole('checkbox').check();await first.getByRole('button',{name:'连接终端',exact:true}).click();await expect(first.getByText('已连接',{exact:true})).toBeVisible()
 const secondPage=await context.newPage()
 try {
  const second=await openShell(secondPage);const sessions=second.getByRole('region',{name:'我的终端会话'})
  await expect(sessions.getByRole('button',{name:'终止会话',exact:true})).toHaveCount(1)
  await second.getByRole('checkbox').check();await second.getByRole('button',{name:'连接终端',exact:true}).click()
  await expect(second.getByRole('alert')).toContainText('遗留连接')
  secondPage.once('dialog',dialog=>dialog.dismiss());await sessions.getByRole('button',{name:'终止会话',exact:true}).click()
  await expect(first.getByText('已连接',{exact:true})).toBeVisible()
  secondPage.once('dialog',dialog=>dialog.accept());await sessions.getByRole('button',{name:'终止会话',exact:true}).click()
  await expect(sessions.getByRole('button',{name:'终止会话',exact:true})).toHaveCount(0)
  await expect(first.getByRole('status')).toContainText('其他页面终止')
  await second.getByRole('checkbox').check();await second.getByRole('button',{name:'连接终端',exact:true}).click();await expect(second.getByText('已连接',{exact:true})).toBeVisible()
  await second.getByRole('button',{name:'断开连接',exact:true}).click()
 } finally {await secondPage.close()}
})

test('failed command sends preserve the draft and successful sends retain input focus',async({page})=>{
 const panel=await openShell(page)
 await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click();await expect(panel.getByText('已连接',{exact:true})).toBeVisible()
 const input=panel.getByLabel('手机命令输入');const draft='中'.repeat(6000)
 await input.fill(draft);await panel.getByRole('button',{name:'发送命令',exact:true}).click()
 await expect(panel.getByRole('alert')).toContainText('输入内容已保留');await expect(input).toHaveValue(draft)
 // Exercise the actual transport rejection path instead of only a length guard.
 await page.evaluate(()=>{
  const original=WebSocket.prototype.send
  WebSocket.prototype.send=function(data){if(typeof data==='string' && JSON.parse(data).type==='input'){WebSocket.prototype.send=original;throw new Error('fixture network race')}return original.call(this,data)}
 })
 await input.fill("printf 'RETRY_%s\\n' OK");await panel.getByRole('button',{name:'发送命令',exact:true}).click()
 await expect(panel.getByRole('alert')).toContainText('发送失败');await expect(input).toHaveValue("printf 'RETRY_%s\\n' OK")
 await panel.getByRole('button',{name:'发送命令',exact:true}).click()
 await expect(panel.getByLabel('交互终端输出',{exact:true})).toContainText('RETRY_OK');await expect(input).toHaveValue('');await expect(input).toBeFocused()
 await input.fill('unsent draft');await panel.getByRole('button',{name:'断开连接',exact:true}).click();await expect(input).toHaveValue('unsent draft')
})

test('large output acknowledges bytes only after rendering and stays within the negotiated window',async({page})=>{
 const output=Buffer.from(`${'rendered output line\r\n'.repeat(40000)}FLOW_RENDER_COMPLETE\r\n`)
 let sent=0;let acknowledged=0;let connected=false;const acks:number[]=[]
 await page.route('**/api/v1/shell/tickets',r=>r.fulfill({json:{ticket:'flow-ticket',expires_at:Math.floor(Date.now()/1000)+30}}))
 await page.routeWebSocket('**/api/v1/shell/connect',socket=>{
  function pump(){while(sent<output.length && sent-acknowledged<65536){const end=Math.min(sent+4096,output.length,acknowledged+65536);socket.send(output.subarray(sent,end));sent=end}}
  socket.onMessage(message=>{
   const data=JSON.parse(String(message))
   if(data.type==='connect'){connected=data.flow_control===true;socket.send(JSON.stringify({type:'ready',session_id:'flow-session',flow_control:'ack',window_bytes:65536}));pump()}
   if(data.type==='ack'){acks.push(data.bytes);acknowledged=data.bytes;pump()}
  })
 })
 const panel=await openShell(page);await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click()
 await expect(panel.getByLabel('交互终端输出',{exact:true})).toContainText('FLOW_RENDER_COMPLETE')
 await expect.poll(()=>acknowledged).toBe(output.length)
 expect(connected).toBe(true)
 // ACKs are coalesced per animation frame; require progress and full coverage, not a high frame count.
 expect(acks.length).toBeGreaterThan(1);expect(acks.at(-1)).toBe(output.length)
 expect(acks.every((value,index)=>value>0 && (index===0 || value>acks[index-1]!))).toBe(true)
 await panel.getByRole('button',{name:'断开连接',exact:true}).click()
})

test('uncontrolled legacy output is bounded and forced-close reasons are explained',async({page})=>{
 let connection=0
 await page.route('**/api/v1/shell/tickets',r=>r.fulfill({json:{ticket:'legacy-ticket',expires_at:Math.floor(Date.now()/1000)+30}}))
 await page.routeWebSocket('**/api/v1/shell/connect',socket=>socket.onMessage(message=>{
  const data=JSON.parse(String(message));if(data.type!=='connect')return
  socket.send(JSON.stringify({type:'ready'}))
  if(++connection===1)socket.send(Buffer.alloc(300*1024,65))
  else socket.close({code:1000,reason:'max_duration'})
 }))
 const panel=await openShell(page);await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click()
 await expect(panel.getByRole('alert')).toContainText('限制内存占用');await expect(panel.getByText('未连接',{exact:true})).toBeVisible()
 await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click();await expect(panel.getByRole('status')).toContainText('30 分钟时限')
})

test('session snapshots refresh on demand and a pending termination remains visible',async({page})=>{
 let reads=0;let closing=false
 const target={id:'browser-local-shell',name:'Lingering shell',kind:'local',node:'local'}
 await page.route('**/api/v1/shell/sessions',r=>{reads++;return r.fulfill({json:{sessions:[{id:'fixture-session',target,started_at:1760000000,last_input_at:1760000001,output_bytes:1024,state:closing?'closing':'connected'}],now:1760000010,max_sessions:1,global_limit:4}})})
 await page.route('**/api/v1/shell/sessions/fixture-session',r=>{expect(r.request().method()).toBe('DELETE');closing=true;return r.fulfill({status:202,json:{closing:true}})})
 const panel=await openShell(page);const sessions=panel.getByRole('region',{name:'我的终端会话'})
 await expect(sessions).toContainText('Lingering shell');expect(reads).toBe(1)
 page.once('dialog',dialog=>dialog.accept());await sessions.getByRole('button',{name:'终止会话',exact:true}).click()
 await expect(sessions).toContainText('正在清理');await expect(sessions.getByRole('button',{name:'终止会话',exact:true})).toBeDisabled()
 // 202 closing schedules delayed refreshes; do not require an immediate extra read.
 await expect.poll(()=>reads,{timeout:5000}).toBeGreaterThan(1)
 await sessions.getByRole('button',{name:'刷新会话',exact:true}).click();await expect.poll(()=>reads).toBeGreaterThan(2)
})

test('mobile command box asks before sending pasted newlines',async({page})=>{
 const panel=await openShell(page)
 await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click();await expect(panel.getByText('已连接',{exact:true})).toBeVisible()
 const input=panel.getByLabel('手机命令输入')
 page.once('dialog',dialog=>{expect(dialog.message()).toContain('换行');return dialog.dismiss()})
 await input.fill("printf 'A'\nprintf 'B'")
 await panel.getByRole('button',{name:'发送命令',exact:true}).click()
 await expect(input).toHaveValue("printf 'A'\nprintf 'B'")
 await panel.getByRole('button',{name:'断开连接',exact:true}).click()
})
