import { expect, test, type Page } from '@playwright/test'
async function openShell(page:Page) {
 await page.goto('/?token=browser-test-token')
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
 await page.goto('/?token=browser-test-token');if(await page.getByRole('button',{name:'更多',exact:true}).isVisible())await page.getByRole('button',{name:'更多',exact:true}).click();await page.getByRole('button',{name:'WebShell',exact:true}).click()
 const panel=page.getByRole('region',{name:'WebShell 主机终端'});await panel.getByLabel('SSH 密码',{exact:true}).fill('temporary-ssh-secret');await panel.getByRole('checkbox').check();await panel.getByRole('button',{name:'连接终端',exact:true}).click()
 await expect(panel.getByRole('alert')).toContainText('指纹校验失败');expect(credential).toBe('temporary-ssh-secret');await expect(panel.getByLabel('SSH 密码',{exact:true})).toHaveValue('')
 const storage=await page.evaluate(()=>JSON.stringify({session:{...sessionStorage},local:{...localStorage}}));expect(storage).not.toContain('temporary-ssh-secret')
})
test('disabled configuration and 320px layout remain usable without connecting',async({page},info)=>{
 await page.setViewportSize({width:320,height:780})
 await page.route('**/api/v1/shell/targets',r=>r.fulfill({json:{enabled:false,secure:true,audit_enabled:true,targets:[]}}))
 await page.goto('/?token=browser-test-token');await page.getByRole('button',{name:'更多',exact:true}).click();await page.getByRole('button',{name:'WebShell',exact:true}).click()
 const panel=page.getByRole('region',{name:'WebShell 主机终端'});await expect(panel.getByRole('button',{name:'连接终端',exact:true})).toBeDisabled();await panel.locator('summary').click();await expect(panel).toContainText('enabled: true');expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true)
 await panel.scrollIntoViewIfNeeded();await page.screenshot({path:info.outputPath('webshell-320.png'),scale:'css'})
})
test('viewer cannot see WebShell and server rejects terminal access',async({page,request})=>{
 await page.goto('/?token=browser-viewer-token');await expect(page.locator('header')).toContainText('browser-test');if(await page.getByRole('button',{name:'更多',exact:true}).isVisible())await page.getByRole('button',{name:'更多',exact:true}).click();await expect(page.getByRole('button',{name:'WebShell',exact:true})).toHaveCount(0)
 const resp=await request.get('/api/v1/shell/targets',{headers:{Authorization:'Bearer browser-viewer-token'}});expect(resp.status()).toBe(403)
})
