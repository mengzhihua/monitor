<script setup lang="ts">
import { ref } from 'vue'
import { api, ApiError } from '../api'
import type { AvailabilityRow, ServiceReport, TopologyEdge } from '../api'
import { usePolling } from '../polling'

const report = ref<ServiceReport | null>(null)
const edges = ref<TopologyEdge[]>([])
const rows = ref<AvailabilityRow[]>([])
const error = ref('')

usePolling(async signal => {
  try {
    const [services, topology, availability] = await Promise.all([api.services(signal), api.topology(signal), api.availability(signal)])
    if (signal.aborted) return
    report.value = services
    edges.value = topology.edges || []
    rows.value = availability.alarms || []
    error.value = ''
  } catch (e) {
    if (signal.aborted) return
    error.value = e instanceof ApiError ? e.message : '读取服务与拓扑失败'
  }
}, 15000)

function pie(sla: number) {
  const p = Math.max(0, Math.min(100, sla))
  const a = (p / 100) * Math.PI * 2
  const x = 16 + Math.sin(a) * 14
  const y = 16 - Math.cos(a) * 14
  const large = p > 50 ? 1 : 0
  if (p >= 99.9) return ''
  return `M 16 16 L 16 2 A 14 14 0 ${large} 1 ${x} ${y} Z`
}
</script>

<template>
  <section class="services" aria-label="服务与拓扑">
    <h1>服务与拓扑</h1>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-else-if="!report">正在读取…</p>
    <template v-else>
      <p v-if="!report.services.length" class="muted">还没有配置 services。在 monitor.yaml 里声明服务树后，这里显示状态和 24 小时可用性。</p>
      <div class="cards">
        <article v-for="s in report.services" :key="s.name" class="card" :data-service="s.name">
          <svg viewBox="0 0 32 32" class="pie" :aria-label="s.name + ' SLA ' + s.sla.toFixed(1) + '%'">
            <circle cx="16" cy="16" r="14" class="track" />
            <path v-if="pie(s.sla)" :d="pie(s.sla)" class="slice" />
            <circle v-else cx="16" cy="16" r="14" class="slice" />
          </svg>
          <div>
            <h2>{{ s.name }}</h2>
            <p>{{ s.status }} · SLA {{ s.sla.toFixed(1) }}%</p>
          </div>
        </article>
      </div>
      <h2>拓扑</h2>
      <p v-if="!edges.length" class="muted">没有 LLDP 邻居或手工 topology 边。</p>
      <ul v-else class="topology">
        <li v-for="(e, i) in edges" :key="i"><span>{{ e.source }} → {{ e.target }}</span> <small>{{ e.kind }}</small></li>
      </ul>
      <h2>可用性</h2>
      <table v-if="rows.length" aria-label="告警可用性">
        <thead><tr><th>告警</th><th>图表</th><th>正常时间</th></tr></thead>
        <tbody>
          <tr v-for="r in rows" :key="r.name + r.chart"><td data-label="告警"><span>{{ r.name }}</span></td><td data-label="图表"><span>{{ r.chart }}</span></td><td data-label="正常时间"><span>{{ r.uptime.toFixed(1) }}%</span></td></tr>
        </tbody>
      </table>
    </template>
  </section>
</template>

<style scoped>
.services { max-width: 1100px; min-width: 0; margin: 0 auto; padding: 24px; color: #e2e8f0; overflow-wrap: anywhere; }
.muted { color: #94a3b8; }
.cards { display: flex; flex-wrap: wrap; gap: 12px; }
.card { display: flex; gap: 12px; align-items: center; background: #0f172a; border: 1px solid #1e293b; border-radius: 12px; padding: 12px 16px; min-width: 220px; }
.card > div { min-width: 0; }
.pie { width: 48px; height: 48px; flex-shrink: 0; }
.track { fill: #1e293b; }
.slice { fill: #34d399; }
h1, h2 { font-weight: 650; }
h2 { font-size: 16px; margin: 0; }
table { width: 100%; border-collapse: collapse; }
td, th { text-align: left; padding: 6px 8px; border-bottom: 1px solid #1e293b; }
@media (max-width: 760px) {
  .services { padding: 16px 12px; }
  h1 { font-size: 22px; }
  .services > h2 { margin-top: 20px; margin-bottom: 10px; }
  .cards { display: grid; grid-template-columns: minmax(0, 1fr); }
  .card { min-width: 0; padding: 12px; }
  .card p { line-height: 1.5; margin-bottom: 0; }
  .topology { list-style: none; padding: 0; }
  .topology li { padding: 12px; margin-bottom: 8px; background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; line-height: 1.6; }
  .topology small { display: block; color: #94a3b8; }
  table, tbody { display: block; }
  thead { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  tbody { display: grid; gap: 8px; }
  tbody tr { display: block; min-width: 0; padding: 10px 12px; background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; }
  td { display: grid; grid-template-columns: 60px minmax(0, 1fr); gap: 10px; padding: 5px 0; border: 0; }
  td::before { content: attr(data-label); color: #94a3b8; font-size: 12px; }
  td > span { min-width: 0; }
}
</style>
