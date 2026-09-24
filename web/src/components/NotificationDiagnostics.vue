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
const testChannel = ref('')
const testing = ref(false)
const testMessage = ref('')
const channelNames: Record<string, string> = { email: '邮件 SMTP', feishu: '飞书', wecom: '企业微信', dingtalk: '钉钉', ntfy: 'ntfy', gotify: 'Gotify', bark: 'Bark' }
async function sendTest() {
  if (!testChannel.value || testing.value) return
  testing.value = true
  testMessage.value = ''
  try {
    await api.testNotification(testChannel.value)
    testMessage.value = '测试已排队，请刷新最近通知结果查看发送结果。30 秒内不能重复测试。'
    testChannel.value = ''
    await refresh()
  } catch (e) {
    testMessage.value = e instanceof ApiError && e.status === 429 ? '测试过于频繁，请等待 30 秒后重试。'
      : e instanceof ApiError && [401, 403].includes(e.status) ? '只有已登录的管理员可以发送测试消息。'
      : '测试未能排队，请检查通道配置和服务状态。'
  } finally { testing.value = false }
}
const outcome = ref('')
const channel = ref('')
const limit = ref(20)
const outcomes: Record<string, string> = { accepted: '通道接受', failed: '发送失败', suppressed: '已抑制', unrouted: '无匹配通道', dropped: '队列已满' }
const reasons: Record<string, string> = {
  maintenance: '维护窗口', planned_maintenance: '计划维护窗口', global_silence: '全局静默', alarm_silence: '单条告警静默', silent_recipient: '规则指定静默',
  inhibited: '同图表的严重告警已抑制这条警告', grouped: '已并入同图表的一条通知',
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
            <div class="heading"><strong>{{ channelNames[c.name] || c.name }}</strong><span class="muted">{{ c.configured_count }} 个配置</span></div>
            <p>尝试 {{ c.attempts }} · 接受 {{ c.accepted }} · <span :class="{ bad: c.failed }">失败 {{ c.failed }}</span></p>
            <small>最近接受：{{ time(c.last_accepted) }}<br />最近失败：{{ time(c.last_failed) }}</small>
            <button v-if="snapshot.can_test && !snapshot.closed" :disabled="testing" @click="testChannel = c.name; testMessage = ''">测试 {{ channelNames[c.name] || c.name }}</button>
          </article>
        </div>
        <p v-else class="notice">未配置通知通道。请在服务端 health.notify 中配置；规则静默、维护及路由也会影响发送。</p>
        <div v-if="testChannel && snapshot.can_test" class="notice" role="group" aria-label="确认通知测试">
          <p>将向本机 {{ channelNames[testChannel] || testChannel }} 的已配置收件人发送测试消息。此操作绕过静默、维护和规则路由，不改变真实告警；每 30 秒最多一次。</p>
          <button :disabled="testing" @click="sendTest">{{ testing ? '排队中…' : '发送测试消息' }}</button>
          <button :disabled="testing" @click="testChannel = ''">取消</button>
        </div>
        <p v-if="testMessage" role="status" class="notice">{{ testMessage }}</p>
        <details class="setup"><summary>配置邮件、飞书等预警通道</summary>
          <p>在服务端 monitor.yaml 的 health.notify 中配置并重启服务。配置成功后通道卡片会显示；只有管理员能发送测试消息。</p>
          <p>邮件：填写 email.server（SMTP 主机:端口）、from、to 收件人列表、username 和 password_env。587 端口推荐 tls_mode: starttls；465 端口使用 tls_mode: tls。</p>
          <p>飞书：创建群自定义机器人，设置 feishu.webhook_url_env；开启签名校验时同时设置 secret_env。对应环境变量需注入 monitord 进程。若设置关键词，须保证告警名称/说明和测试消息能匹配。</p>
          <p>ntfy：设置 ntfy.url（服务根地址）、topic_env 和可选 token_env。Gotify：设置 gotify.url（以 /message 结尾）和 token_env。Bark：设置 bark.url（以 /push 结尾）和 device_key_env，可用 group 设置消息分组。三种通道均支持自建服务。</p>
          <p>也支持钉钉、企业微信、Slack 和通用 Webhook 等。通过 health.notify.roles 将告警规则的 to 角色映射到 email、feishu 等通道；不设置路由时发送至所有已配置通道。</p>
        </details>
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
              <div class="heading"><strong>{{ r.test ? '[测试] ' : '' }}{{ r.name }}</strong><span class="badge" :class="r.outcome">{{ outcomes[r.outcome] || r.outcome }}</span></div>
              <p>{{ r.chart }} · {{ r.severity }} · {{ r.channel || '分发前' }}<span v-if="r.repeat"> · 重复提醒</span></p>
              <p>{{ detail(r) }}</p><small>{{ time(r.at) }}<span v-if="!r.test"> · 告警事件 #{{ r.event_id }}</span><span v-else> · 手动通道测试</span><span v-if="r.channel"> · 耗时 {{ r.duration_ms }} ms</span></small>
            </li>
          </ol>
          <button v-if="rows.length > limit" @click="limit += 20">再显示 20 条通知结果</button>
        </details>
        <p class="muted footer">失败通知不会自动重试。飞书、钉钉、企业微信、ntfy、Gotify 和 Bark 同时校验 HTTP 与接口确认结果；其他 HTTP 通道以现有发送器的成功条件为准。通道接受不等于用户收件；接口和日志只提供安全错误分类。</p>
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
.setup { margin:14px 0; } .channels article button { display:block; margin-top:12px; }
.activity { border:1px solid #334155; border-radius:9px; padding:14px; }.activity summary { font-size:14px; }
ol { padding:0; margin:0; list-style:none; } li { border-top:1px solid #29384f; padding:14px 0; overflow-wrap:anywhere; font-size:13px; }
.badge { border-radius:4px; padding:3px 7px; background:#334155; font-size:12px; }.badge.accepted { color:#5eead4; background:#134e4a; }.badge.failed,.badge.dropped { color:#fda4af; background:#4c1d2b; }.badge.suppressed,.badge.unrouted { color:#fde68a; background:#422f19; }
</style>
