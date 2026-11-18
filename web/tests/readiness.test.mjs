import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readiness } from '../src/features/onboarding/model/readiness.ts'
const now = 1000
const evidence = () => ({
 info: { collectors: [], db: { persistence: { finished: 995, interval_seconds: 10 } } },
 charts: [{ last_entry: 999, update_every: 1 }],
 alarms: { alarms: { cpu: { active: true, status: 'CLEAR', last_updated: 999, update_every: 1 } } },
 notifications: { available: true, enabled: true, closed: false, channels: [{ configured_count: 1, last_accepted: 990, last_failed: 0 }] },
 services: { services: [{ status: 'CLEAR' }] },
})
const states = e => Object.fromEntries(readiness(e, now).map(c => [c.id, c.state]))
test('missing sources never report completion', () => {
 assert.deepEqual(Object.values(states({})), Array(5).fill('unknown'))
 assert.deepEqual(Object.values(states(evidence())), Array(5).fill('ready'))
})
test('empty charts, stale samples, collector errors and failed persistence need attention', () => {
 const e = evidence(); e.charts = []
 assert.equal(states(e).collection, 'attention')
 e.charts = [{last_entry:800,update_every:1}]
 assert.equal(states(e).collection,'attention')
 e.charts = [{last_entry:999,update_every:1}]; e.info.collectors=[{enabled:true,error:'failed'}]
 assert.equal(states(e).collection,'attention')
 e.info.db.persistence.error='disk full'
 assert.equal(states(e).storage,'attention')
 e.info.db.persistence={finished:1,interval_seconds:10}
 assert.equal(states(e).storage,'attention')
})
test('zero alarms, undefined evaluation and disabled engine are not protection', () => {
 const e=evidence();e.alarms.alarms={}
 assert.equal(states(e).alarms,'attention')
 e.alarms.alarms={cpu:{active:true,status:'UNDEFINED',last_updated:999,update_every:1}}
 assert.equal(states(e).alarms,'attention')
 e.alarms.alarms.cpu.status='CLEAR';e.notifications.enabled=false
 assert.equal(states(e).alarms,'attention')
})
test('configured channels still require evidence and a newer failure invalidates acceptance', () => {
 const e=evidence();e.notifications.channels[0].last_accepted=0
 assert.equal(states(e).notifications,'attention')
 e.notifications.channels[0].last_accepted=990;e.notifications.channels[0].last_failed=995
 assert.equal(states(e).notifications,'attention')
 e.services.services=[{status:'UNKNOWN'}]
 assert.equal(states(e).services,'attention')
 const text=readiness(evidence(),now).find(c=>c.id==='notifications').detail
 assert.match(text,/不代表用户已收件/)
})
