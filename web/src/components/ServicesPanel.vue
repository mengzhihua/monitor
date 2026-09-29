<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import type { AvailabilityRow, ServiceReport, TopologyEdge } from '../api'

const report = ref<ServiceReport | null>(null)
const edges = ref<TopologyEdge[]>([])
const rows = ref<AvailabilityRow[]>([])
const error = ref('')

onMounted(async () => {
  try {
    const [services, topology, availability] = await Promise.all([api.services(), api.topology(), api.availability()])
    report.value = services
    edges.value = topology.edges || []
    rows.value = availability.alarms || []
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '读取服务与拓扑失败'
  }
})

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
      <ul v-else>
        <li v-for="(e, i) in edges" :key="i">{{ e.source }} → {{ e.target }} <small>{{ e.kind }}</small></li>
      </ul>
      <h2>可用性</h2>
      <table v-if="rows.length">
        <thead><tr><th>告警</th><th>图表</th><th>正常时间</th></tr></thead>
        <tbody>
          <tr v-for="r in rows" :key="r.name + r.chart"><td>{{ r.name }}</td><td>{{ r.chart }}</td><td>{{ r.uptime.toFixed(1) }}%</td></tr>
        </tbody>
      </table>
    </template>
  </section>
</template>

<style scoped>
.services { max-width: 1100px; margin: 0 auto; padding: 24px; color: #e2e8f0; }
.muted { color: #94a3b8; }
.cards { display: flex; flex-wrap: wrap; gap: 12px; }
.card { display: flex; gap: 12px; align-items: center; background: #0f172a; border: 1px solid #1e293b; border-radius: 12px; padding: 12px 16px; min-width: 220px; }
.pie { width: 48px; height: 48px; }
.track { fill: #1e293b; }
.slice { fill: #34d399; }
h1, h2 { font-weight: 650; }
h2 { font-size: 16px; margin: 0; }
table { width: 100%; border-collapse: collapse; }
td, th { text-align: left; padding: 6px 8px; border-bottom: 1px solid #1e293b; }
</style>
