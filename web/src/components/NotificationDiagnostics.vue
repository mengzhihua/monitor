<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, ApiError } from '../api'
import type { NotificationResult, NotificationSnapshot } from '../api'
import { usePolling } from '../polling'

const snapshot = ref<NotificationSnapshot | null>(null)
const error = ref('')
const loading = ref(false)
const loadedAt = ref(0)
const query = ref('')
const outcome = ref('')
const channel = ref('')
const limit = ref(20)
const outcomes: Record<string, string> = { accepted: '通道接受', failed: '发送失败', suppressed: '已抑制', unrouted: '无匹配通道', dropped: '队列已满' }
const reasons: Record<string, string> = {
  maintenance: '维护窗口', global_silence: '全局静默', alarm_silence: '单条告警静默', silent_recipient: '规则指定静默',
  no_channel: '没有配置通道或路由未匹配', queue_full: '队列已满，本次未发送', http_status: 'HTTP 拒绝',
  timeout: '请求超时', canceled: '请求取消', network: '网络或 TLS 连接失败', provider_error: '通道配置或协议错误',
}
const time = (t: number) => t > 0 ? new Date(t * 1000).toLocaleString() : '暂无'
function detail(r: NotificationResult) {
  if (r.outcome === 'accepted') return '通道调用成功，未验证用户收件'
  return `${reasons[r.reason] || '原因未知'}${r.http_status ? ` · HTTP ${r.http_status}` : ''}`
}
const rows = computed(() => (snapshot.value?.recent || []).filter(r =>
  (!outcome.value || r.outcome === outcome.value) && (!channel.value || r.channel === channel.value) &&
  (!query.value.trim() || `${r.name} ${r.chart}`.toLowerCase().includes(query.value.trim().toLowerCase()))))
watch([query, outcome, channel], () => { limit.value = 20 })
const refresh = usePolling(async signal => {
  loading.value = true
  try {
    const next = await api.notifications(signal)
    if (signal.aborted) return
    snapshot.value = next
    loadedAt.value = Math.floor(Date.now() / 1000)
    error.value = ''
  } catch (e) {
    if (signal.aborted) return
    if (e instanceof ApiError && [401, 403].includes(e.status)) {
      snapshot.value = null
      error.value = '当前登录无法读取通知诊断，请重新登录。'
    } else error.value = snapshot.value ? '通知诊断刷新失败，当前是上次成功的数据。' : '通知诊断暂时无法读取，请重试。'
  } finally { if (!signal.aborted) loading.value = false }
}, 15000)
</script>

<template>
  <section class="diagnostics" aria-label="通知诊断">
    <div class="heading"><div><h2>通知诊断 <small>本机</small></h2><p class="muted">查看告警进入通知分发流程后的结果。每 15 秒刷新，通道接受不代表用户已收件。</p></div><button @click="refresh()" :disabled="loading">{{ loading ? '读取中…' : '刷新通知诊断' }}</button></div>
    <p v-if="error" role="alert" class="notice">{{ error }}<span v-if="snapshot"> 上次成功：{{ time(loadedAt) }}</span></p>
    <p v-if="!snapshot && loading" role="status">正在读取通知诊断…</p>
    <template v-if="snapshot">
      <p v-if="!snapshot.available" class="muted">本机健康引擎未启用，暂无通知诊断。</p>
      <template v-else>
        <p class="muted scope">实例：{{ snapshot.hostname || '本机' }} · 本次启动：{{ time(snapshot.since) }} · {{ snapshot.closed ? '已停止' : snapshot.enabled ? '告警求值中' : '告警求值已暂停' }}。此处不汇总远端 Agent 的通知。</p>
        <div class="stats">
          <div><span>等待发送</span><strong>{{ snapshot.queue_size }}<small> / {{ snapshot.queue_limit }}</small></strong><span>{{ snapshot.in_flight ? '另有 1 次通道调用进行中' : '当前无进行中的调用' }}</span></div>
          <div><span>通道接受 / 失败</span><strong><b class="ok">{{ snapshot.accepted }}</b> / <b :class="{ bad: snapshot.failed }">{{ snapshot.failed }}</b></strong><span>按通道调用次数计数</span></div>
          <div><span>静默或维护抑制</span><strong>{{ snapshot.suppressed }}</strong><span>进入分发流程的事件</span></div>
          <div><span>未路由 / 丢弃</span><strong>{{ snapshot.unrouted }} / <b :class="{ bad: snapshot.dropped }">{{ snapshot.dropped }}</b></strong><span>缺少通道 / 队列满</span></div>
        </div>
        <p v-if="snapshot.in_flight" class="sending" role="status">正在调用 {{ snapshot.in_flight.channel }}：{{ snapshot.in_flight.name }} · {{ time(snapshot.in_flight.at) }} 开始</p>
        <div class="channels" v-if="snapshot.channels.length">
          <article v-for="c in snapshot.channels" :key="c.name" :data-notification-channel="c.name">
            <div class="heading"><strong>{{ c.name }}</strong><span class="muted">{{ c.configured_count }} 个配置</span></div>
            <p>尝试 {{ c.attempts }} · 接受 {{ c.accepted }} · <span :class="{ bad: c.failed }">失败 {{ c.failed }}</span></p>
            <small>最近接受：{{ time(c.last_accepted) }}<br />最近失败：{{ time(c.last_failed) }}</small>
          </article>
        </div>
        <p v-else class="notice">未配置通知通道。请在服务端 health.notify 中配置；规则静默、维护及路由也会影响发送。</p>
        <details class="activity">
          <summary>最近通知结果 · {{ snapshot.recent.length }} 条</summary>
          <p class="muted">本次启动累计 {{ snapshot.total }} 条结果，保留最近 {{ snapshot.retention }} 条，重启后清空。一次事件可能产生多个通道结果；排队中及进行中的调用不在结果列表。延迟等待与被防抖消除的状态变化不计入这里。</p>
          <div class="filters">
            <input v-model="query" aria-label="通知关键词" placeholder="搜索告警或图表…" maxlength="256" />
            <select v-model="outcome" aria-label="通知结果"><option value="">全部结果</option><option v-for="(label, key) in outcomes" :key="key" :value="key">{{ label }}</option></select>
            <select v-model="channel" aria-label="通知通道"><option value="">全部通道</option><option v-for="c in snapshot.channels" :key="c.name" :value="c.name">{{ c.name }}</option></select>
          </div>
          <p v-if="!rows.length" class="muted">{{ snapshot.recent.length ? '当前筛选没有匹配结果。' : '本次启动暂无已完成的通知结果。' }}</p>
          <ol>
            <li v-for="r in rows.slice(0, limit)" :key="r.id" :data-notification-outcome="r.outcome">
              <div class="heading"><strong>{{ r.name }}</strong><span class="badge" :class="r.outcome">{{ outcomes[r.outcome] || r.outcome }}</span></div>
              <p>{{ r.chart }} · {{ r.severity }} · {{ r.channel || '分发前' }}<span v-if="r.repeat"> · 重复提醒</span></p>
              <p>{{ detail(r) }}</p><small>{{ time(r.at) }} · 告警事件 #{{ r.event_id }}<span v-if="r.channel"> · 耗时 {{ r.duration_ms }} ms</span></small>
            </li>
          </ol>
          <button v-if="rows.length > limit" @click="limit += 20">再显示 20 条通知结果</button>
        </details>
        <p class="muted footer">诊断不会自动重试失败通知，也不会发送测试消息。HTTP 通道成功仅表示请求返回 2xx，不验证响应中的业务码或最终收件状态；接口和日志只提供安全错误分类。</p>
      </template>
    </template>
  </section>
</template>

<style scoped>
.diagnostics { margin-top:28px; padding-top:22px; border-top:1px solid #334155; color:#e2e8f0; }
.heading,.filters { display:flex; justify-content:space-between; align-items:center; flex-wrap:wrap; gap:10px; }
h2 { margin:0; font-size:20px; } h2 small { font-size:12px; color:#5eead4; margin-left:8px; }
p { line-height:1.6; margin:8px 0; } .muted,small { color:#94a3b8; font-size:12px; } .scope,.footer { overflow-wrap:anywhere; }
.stats,.channels { display:grid; gap:12px; grid-template-columns:repeat(auto-fit,minmax(min(100%,220px),1fr)); margin:14px 0; }
.stats>div,.channels article { background:#152135; border:1px solid #29384f; border-radius:9px; padding:14px; min-width:0; }
.stats>div { display:flex; flex-direction:column; gap:8px; }.stats strong { font-size:25px; }.stats span { font-size:12px; color:#94a3b8; }
.stats b { font-weight:600; }.ok { color:#5eead4; }.bad { color:#fda4af; }
.notice,.sending { padding:10px 12px; background:#302b20; border-radius:6px; color:#fde68a; font-size:13px; }
button,input,select { font:inherit; color:#e2e8f0; background:#1e293b; border:1px solid #475569; border-radius:6px; padding:8px 11px; max-width:100%; min-width:0; }
button,summary { cursor:pointer; } button:disabled { opacity:.5; cursor:wait; }
.filters { justify-content:flex-start; margin:12px 0; } .filters input { flex:1 1 190px; }.filters select { flex:1 1 130px; }
.activity { border:1px solid #334155; border-radius:9px; padding:14px; }.activity summary { font-size:14px; }
ol { padding:0; margin:0; list-style:none; } li { border-top:1px solid #29384f; padding:14px 0; overflow-wrap:anywhere; font-size:13px; }
.badge { border-radius:4px; padding:3px 7px; background:#334155; font-size:12px; }.badge.accepted { color:#5eead4; background:#134e4a; }.badge.failed,.badge.dropped { color:#fda4af; background:#4c1d2b; }.badge.suppressed,.badge.unrouted { color:#fde68a; background:#422f19; }
</style>
