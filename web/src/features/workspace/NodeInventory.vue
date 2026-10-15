<script setup lang="ts">
import type { NodeInfo } from '../../api/hub/types'
defineProps<{ nodes: NodeInfo[]; canManage: boolean }>()
const emit = defineEmits<{ pick: [id: string]; forget: [node: NodeInfo] }>()
</script>

<template>
  <section aria-label="节点管理">
    <h1>节点管理</h1><p>选择节点查看指标；仅管理员可以清理离线节点。</p>
    <div class="node-overview">
      <article v-for="node in nodes" :key="node.id" :class="['node-card', node.status]">
        <h2>{{ node.hostname }}</h2><span>{{ node.status === 'live' ? '在线' : node.status === 'stale' ? '数据过期' : '离线' }}</span>
        <small>{{ node.charts_count }} 图表 · {{ node.alarms?.critical || 0 }} 严重告警{{ node.replica ? ' · 副本' : '' }}</small>
        <small v-if="node.last_data">最后数据：{{ new Date(node.last_data * 1000).toLocaleString() }}</small>
        <div><button @click="emit('pick', node.local ? '' : node.id)">查看指标</button><button v-if="canManage && node.status === 'offline' && !node.local" class="node-del" @click="emit('forget', node)">删除节点</button></div>
      </article>
    </div>
    <p v-if="!nodes.length">尚无可见节点。</p>
  </section>
</template>

<style scoped>
h1 { margin:0 0 8px; font-size:24px; } p,small { color:#94a3b8; } .node-overview { display:grid; grid-template-columns:repeat(auto-fill,minmax(min(270px,100%),1fr)); gap:12px; margin-top:24px; }
.node-card { display:flex; flex-direction:column; gap:12px; border:1px solid #334155; border-radius:12px; padding:20px; background:#0f172a; min-width:0; overflow-wrap:anywhere; } h2 { margin:0; font-size:16px; }
.node-card.live>span { color:#5eead4; }.node-card.stale>span,.node-card.offline>span { color:#fbbf24; }
button { padding:8px 12px; margin:4px 8px 0 0; background:#172337; border:1px solid #334155; border-radius:6px; color:#e2e8f0; font:inherit; cursor:pointer; }.node-del { color:#fda4af; }
</style>
