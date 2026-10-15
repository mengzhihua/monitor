<script setup lang="ts">
import { computed } from 'vue'
import type { Info } from '../../api'
const props = defineProps<{ info: Info }>()
const issues = computed(() => props.info.collectors.filter(c => !c.enabled || c.error).length
  + (props.info.plugins || []).filter(p => p.state !== 'running' && p.state !== 'starting').length)
</script>

<template>
  <details class="collector-status">
    <summary>采集器与插件 <span>{{ info.collectors.length }} 个采集器 · {{ info.plugins?.length || 0 }} 个插件</span><b v-if="issues">{{ issues }} 项需关注</b></summary>
    <p>这里显示本机采集状态；未启用或缺少依赖的采集器不代表设备故障。</p>
    <div class="collector-grid">
      <div v-for="collector in info.collectors" :key="collector.name" class="collector" :class="{ issue: !collector.enabled || collector.error }">
        <b>{{ collector.name }}</b><span>{{ !collector.enabled ? '未启用' : collector.error ? '采集异常' : `${collector.last_run_ms} ms` }}</span>
        <small v-if="collector.error">{{ collector.error }}</small>
      </div>
      <div v-for="plugin in info.plugins || []" :key="'plugin:' + plugin.name" class="collector" :class="{ issue: plugin.state !== 'running' && plugin.state !== 'starting' }">
        <b>{{ plugin.name }}</b><span>{{ plugin.state === 'running' ? `${plugin.stats.charts} 张图表` : plugin.state }}</span><small v-if="plugin.error">{{ plugin.error }}</small>
      </div>
    </div>
  </details>
</template>

<style scoped>
.collector-status { border:1px solid #2a3a4b; border-radius:10px; padding:12px 14px; color:#a7b6c8; margin-top:20px; min-width:0; }
summary { cursor:pointer; font-weight:600; color:#d3deeb; line-height:1.8; overflow-wrap:anywhere; }
summary span { font-size:12px; font-weight:400; margin-left:12px; } summary b { font-size:12px; color:#fcd34d; margin-left:12px; }
p { font-size:12px; line-height:1.7; } .collector-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(240px,100%),1fr)); gap:8px; }
.collector { background:#121d2b; padding:10px; display:flex; flex-wrap:wrap; justify-content:space-between; gap:6px; border-radius:6px; font-size:12px; min-width:0; overflow-wrap:anywhere; }
.collector b { color:#c9d6e5; } .collector.issue span { color:#fcd34d; } small { flex-basis:100%; color:#fca5a5; }
summary:focus-visible { outline:2px solid #5eead4; outline-offset:4px; }
@media (max-width:760px) { summary { min-height:44px; } summary span { display:block; margin-left:0; } }
</style>
