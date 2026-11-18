import { test } from 'node:test'
import assert from 'node:assert/strict'
import { RequestTimeoutError, withDeadline } from '../src/api/deadline.ts'

test('deadline aborts stalled work and ignores a late success', async () => {
 let signal, resolve
 const task = withDeadline(s => { signal=s; return new Promise(r=>{resolve=r}) }, undefined, 10)
 await assert.rejects(task, RequestTimeoutError)
 assert.equal(signal.aborted,true)
 resolve('late')
})
test('the deadline remains active while a response body stalls', async () => {
 let bodyCanceled=false
 const task=withDeadline(async signal=>{
   await Promise.resolve({status:200})
   return new Promise((_,reject)=>signal.addEventListener('abort',()=>{bodyCanceled=true;reject(signal.reason)}))
 },undefined,10)
 await assert.rejects(task,RequestTimeoutError)
 assert.equal(bodyCanceled,true)
})
test('explicit cancellation retains its reason and does not run already canceled work', async () => {
 const parent=new AbortController(), reason=new DOMException('navigation','AbortError')
 let called=false
 parent.abort(reason)
 await assert.rejects(withDeadline(async()=>{called=true},parent.signal,10),e=>e===reason)
 assert.equal(called,false)
 const next=new AbortController()
 const task=withDeadline(()=>new Promise(()=>{}),next.signal,1000)
 next.abort(reason)
 await assert.rejects(task,e=>e===reason)
})
test('success and failure both remove parent listeners and deadline timer',async()=>{
 const parent=new AbortController();let listeners=0
 const add=parent.signal.addEventListener.bind(parent.signal), remove=parent.signal.removeEventListener.bind(parent.signal)
 parent.signal.addEventListener=(...args)=>{listeners++;return add(...args)}
 parent.signal.removeEventListener=(...args)=>{listeners--;return remove(...args)}
 assert.equal(await withDeadline(async()=>42,parent.signal,10),42)
 assert.equal(listeners,0)
 const original=new Error('original')
 await assert.rejects(withDeadline(async()=>{throw original},parent.signal,10),e=>e===original)
 assert.equal(listeners,0)
})
