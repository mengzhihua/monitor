import { test } from 'node:test'
import assert from 'node:assert/strict'
import { handoverHTML } from '../src/features/services/model/handover.ts'

test('handover is self-contained, escapes notes and preserves unknown values and limits',()=>{
 const payload='<img src=x onerror=alert(1)>'
 const html=handoverHTML({schema:'monitor.service-handover.v1',service:payload,hostname:payload,captured_at:100,completed_at:101,config_revision:'revision',
 report:{from:0,to:100,services:[{name:payload,status:'UNKNOWN',sla:null}]},
 alarms:[{name:'down',chart:'cpu',status:'UNDEFINED',updated:0,value:null,units:'%'}],availability:[{name:'down',chart:'cpu',uptime:null,raised_seconds:20,known_seconds:30,unknown_seconds:70,window_seconds:100}],
 handling_history:[{problem:{name:'down',chart:'cpu'},history:[{at:50,actor:payload,action:'comment',note:payload}],revision:23,status:'investigating',acknowledged:false,history_truncated:true}],
 history_matched:205,history_limit:200,history_capacity:5000,actions_per_record:20,history_snapshot:'snapshot',history_persistent:true,notes:[payload]})
 assert.ok(!html.includes('<img'));assert.ok(!html.includes('<script'))
 assert.ok(html.includes('&lt;img'));assert.ok(html.includes('数据不足'));assert.ok(html.includes('未知秒数'))
 assert.ok(html.includes('匹配 205 条'));assert.ok(html.includes('操作历史已裁剪'))
 assert.ok(html.includes('Content-Security-Policy'));assert.ok(html.includes('@media print'))
})
