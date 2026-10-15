<script setup lang="ts">
import type { OperationsNode, ResourceMetric } from '../../api/operations/types'
import AppIcon from '../../components/AppIcon.vue'

defineProps<{ nodes: OperationsNode[]; total: number }>()
const emit = defineEmits<{ drill: [node: string, chart: string] }>()
const statusName: Record<string, string> = { live: '在线', stale: '数据过期', offline: '离线' }
const formatTime = (t: number) => t > 0 ? new Date(t * 1000).toLocaleString() : '暂无样本'
const formatMetric = (m: ResourceMetric) => m.value === null ? (m.state === 'stale' ? '数据过期' : '暂无数据') : `${m.value.toFixed(1)}%`
// The server returns the busiest mounts first; keep each card bounded.
const MAX_DISK_ROWS = 6
</script>

<template>
  <section aria-label="主机资源列表">
      <h2><AppIcon name="server" />主机资源 <small>{{ total }} 个节点</small></h2>
      <div class="node-grid">
        <article v-for="n in nodes" :key="n.id" class="node-tile">
          <div class="row"><h3>{{ n.hostname }}</h3><span class="badge" :class="n.status">{{ statusName[n.status] }}</span></div>
          <p class="muted">{{ n.local ? '本机' : n.replica ? '副本' : n.peer ? '其他 Hub' : 'Agent' }} · {{ n.os }}/{{ n.arch }} · {{ n.charts_count }} 图表</p>
          <div class="resources"><div><span><AppIcon name="cpu" :size="14" />CPU</span><b :class="{ muted: n.cpu.value === null }">{{ formatMetric(n.cpu) }}</b><progress v-if="n.cpu.value !== null" :value="n.cpu.value" max="100" aria-label="CPU 使用率" /></div><div><span><AppIcon name="memory" :size="14" />内存</span><b :class="{ muted: n.memory.value === null }">{{ formatMetric(n.memory) }}</b><progress v-if="n.memory.value !== null" :value="n.memory.value" max="100" aria-label="内存使用率" /></div></div>
          <small class="muted">CPU 样本 {{ formatTime(n.cpu.at) }}<br />内存样本 {{ formatTime(n.memory.at) }}</small>
          <div class="disks" aria-label="磁盘使用率">
            <template v-if="n.disks?.length">
              <div v-for="d in n.disks.slice(0, MAX_DISK_ROWS)" :key="d.mount" class="disk-row">
                <span class="disk-mount">{{ d.mount }}</span>
                <b :class="{ muted: d.value === null }">{{ formatMetric(d) }}</b>
                <progress v-if="d.value !== null" :value="d.value" max="100" :aria-label="d.mount + ' 磁盘使用率'" />
              </div>
              <small v-if="n.disks.length > MAX_DISK_ROWS" class="disk-more">+{{ n.disks.length - MAX_DISK_ROWS }} 个挂载点</small>
            </template>
            <p v-else class="disk-empty">暂无数据</p>
          </div>
          <div class="row tile-bottom"><span>{{ n.alarms?.critical || 0 }} 严重 · {{ n.alarms?.warning || 0 }} 警告</span><button @click="emit('drill', n.id, '')">查看主机</button></div>
        </article>
      </div>
      <p v-if="!nodes.length" class="empty">没有匹配的节点，可调整搜索条件。</p>

  </section>
</template>

<style scoped>
h2 { display:flex; align-items:center; gap:7px; font-size:17px; margin:20px 0 12px; } h2 .app-icon { color:#5eead4; } h2 small { font-size:12px; color:#94a3b8; font-weight:400; } h3 { font-size:14px; margin:0; overflow-wrap:anywhere; }
p { font-size:13px; line-height:1.65; margin:8px 0; } .muted { color:#94a3b8; } small { font-size:11px; }
button { font:inherit; font-size:12px; border:1px solid #334155; border-radius:7px; background:#111d31; color:#e2e8f0; padding:8px 10px; max-width:100%; cursor:pointer; } button:hover { border-color:#5eead4; color:#5eead4; } button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
.row { display:flex; gap:12px; justify-content:space-between; align-items:center; flex-wrap:wrap; }.node-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(min(290px,100%),1fr)); gap:12px; } .node-tile { background:#0f1a2d; border:1px solid #27364c; border-radius:10px; padding:16px; min-width:0; } .badge { font-size:11px; padding:3px 7px; border-radius:5px; background:#26334a; color:#cbd5e1; white-space:nowrap; } .badge.live { color:#6ee7b7; }.badge.offline { color:#fda4af; background:#4c1d2a; } .badge.stale { color:#fde68a; background:#46361c; }
.resources { display:grid; grid-template-columns:1fr 1fr; gap:20px; margin:18px 0; } .resources>div { display:flex; flex-direction:column; gap:7px; } .resources span { display:flex; align-items:center; gap:6px; font-size:11px; color:#94a3b8; } .resources b { font-size:21px; font-weight:500; } .resources progress { height:5px; width:100%; accent-color:#2dd4bf; } .tile-bottom { margin-top:16px; font-size:12px; } .tile-bottom span { color:#cbd5e1; }
.disks { display:flex; flex-direction:column; gap:8px; margin:12px 0 0; } .disk-row { display:flex; flex-wrap:wrap; align-items:baseline; gap:2px 10px; } .disk-mount { font-size:11px; color:#94a3b8; overflow-wrap:anywhere; min-width:0; } .disk-row b { font-size:16px; font-weight:600; margin-left:auto; } .disk-row progress { flex-basis:100%; height:5px; width:100%; accent-color:#2dd4bf; } .disk-more, .disk-empty { font-size:11px; color:#94a3b8; } .disk-empty { margin:0; }
.empty { padding:20px; border:1px dashed #334155; border-radius:8px; color:#94a3b8; }
</style>
