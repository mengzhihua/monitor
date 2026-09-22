<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { ConsoleResponse, ConsoleNode } from '../api'
import { api } from '../api'

const emit = defineEmits<{ close: []; pick: [id: string] }>()
const data = ref<ConsoleResponse | null>(null)
const error = ref('')

async function load() {
  try {
    data.value = await api.console()
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
}

function statusClass(n: ConsoleNode) {
  return n.status || 'offline'
}

onMounted(() => { void load() })
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>Cloud 控制台 <small>Space / Room / ACLK / 告警路由</small></h3>
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <div v-if="error" class="err">{{ error }}</div>
    <template v-else-if="data">
      <div class="aclk">
        <span :class="['pill', data.aclk.online ? 'on' : 'off']">ACLK {{ data.aclk.protocol || 'stream' }}</span>
        <span class="dim">storage={{ data.aclk.storage || 'full' }}</span>
        <span class="dim">live {{ data.aclk.live ?? 0 }}/{{ data.aclk.nodes ?? 0 }}</span>
      </div>
      <div class="routing" v-if="data.routing">
        <div class="nav-title">告警路由</div>
        <div class="chips">
          <span v-for="c in data.routing.channels" :key="c.name" class="chip">{{ c.name }}</span>
          <span v-if="!data.routing.channels?.length" class="dim">未配置通知渠道</span>
        </div>
        <div class="chips" v-if="data.routing.roles && Object.keys(data.routing.roles).length">
          <span v-for="(chs, role) in data.routing.roles" :key="role" class="chip">{{ role }} → {{ chs.join(', ') }}</span>
        </div>
      </div>
      <div v-for="sp in data.spaces" :key="sp.id" class="space">
        <h4>{{ sp.name }}</h4>
        <div v-for="rm in sp.rooms" :key="rm.id" class="room">
          <div class="room-head">
            <b>{{ rm.name }}</b>
            <span class="dim">{{ rm.nodes.length }} nodes</span>
            <span v-if="rm.alarms.critical" class="pill crit">crit {{ rm.alarms.critical }}</span>
            <span v-else-if="rm.alarms.warning" class="pill warn">warn {{ rm.alarms.warning }}</span>
          </div>
          <ul>
            <li v-for="n in rm.nodes" :key="n.id" @click="emit('pick', n.hostname || n.id)">
              <i :class="['dot', statusClass(n)]"></i>
              <span>{{ n.hostname || n.id }}</span>
              <small>{{ n.protocol || (n.aclk ? 'aclk' : '—') }} · {{ n.charts_count ?? 0 }} charts</small>
            </li>
            <li v-if="!rm.nodes.length" class="dim">暂无节点</li>
          </ul>
          <ul class="alarms" v-if="rm.alarm_list?.length">
            <li v-for="(a, i) in rm.alarm_list" :key="i">
              <span :class="['pill', String(a.status).toLowerCase()]">{{ a.status }}</span>
              {{ a.chart }}.{{ a.name }}
            </li>
          </ul>
        </div>
      </div>
      <div v-if="data.unassigned?.length" class="space">
        <h4>未分配</h4>
        <ul>
          <li v-for="n in data.unassigned" :key="n.id" @click="emit('pick', n.hostname || n.id)">
            <i :class="['dot', statusClass(n)]"></i>
            <span>{{ n.hostname || n.id }}</span>
          </li>
        </ul>
      </div>
    </template>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
h3 { margin: 0; font-size: 14px; flex: 1; }
h3 small { color: #64748b; font-weight: 400; margin-left: 8px; }
.x { background: none; border: none; color: #94a3b8; font-size: 18px; cursor: pointer; }
.err { color: #fca5a5; }
.aclk, .routing { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 10px; }
.nav-title { font-size: 11px; text-transform: uppercase; color: #64748b; width: 100%; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip, .pill { background: #1e293b; border: 1px solid #334155; border-radius: 10px; padding: 1px 8px; font-size: 11px; }
.pill.on { color: #86efac; border-color: #166534; }
.pill.off { color: #94a3b8; }
.pill.crit, .pill.critical { background: #7f1d1d; color: #fecaca; border-color: #b91c1c; }
.pill.warn, .pill.warning { background: #78350f; color: #fde68a; border-color: #b45309; }
.space { margin-top: 10px; }
h4 { margin: 0 0 6px; font-size: 13px; color: #cbd5e1; }
.room { margin: 0 0 10px 8px; padding-left: 8px; border-left: 2px solid #1e293b; }
.room-head { display: flex; gap: 8px; align-items: center; margin-bottom: 4px; }
ul { list-style: none; margin: 0; padding: 0; }
li { display: flex; gap: 8px; align-items: center; padding: 3px 0; cursor: pointer; }
li:hover { color: #e2e8f0; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: #64748b; display: inline-block; }
.dot.live { background: #22c55e; } .dot.stale { background: #fbbf24; } .dot.offline { background: #ef4444; }
.dim { color: #64748b; font-size: 11px; }
small { color: #64748b; font-size: 11px; }
.alarms li { font-size: 12px; cursor: default; }
</style>
