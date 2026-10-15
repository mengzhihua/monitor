<script setup lang="ts">
import type { HandlingChange, HandlingStatus, Problem } from '../../api/operations/types'
import AppIcon from '../../components/AppIcon.vue'
import { deliveryText, describeAction, formatTime, problemAge, progressName } from './problemPresentation'

defineProps<{
  p: Problem; now: number; assignees: { name: string; role: string }[]; currentUser: string
  canHandle: boolean; canSelfAssign: boolean; busy: string; stale: boolean
  selected: boolean; selectionFull: boolean; note?: string; owner?: string; status?: HandlingStatus
}>()
const emit = defineEmits<{
  drill: [node: string, chart: string]; solve: [problem: Problem]; select: [checked: boolean]
  owner: [value: string]; status: [value: HandlingStatus]; note: [value: string]
  assign: []; update: [change: HandlingChange]
}>()
</script>

<template>
  <article class="problem" :class="p.severity.toLowerCase()" :data-problem-id="p.id">
    <label v-if="canHandle" class="problem-select"><input type="checkbox" :checked="selected"
      :disabled="!!busy || stale || (selectionFull && !selected)"
      :aria-label="'选择问题 ' + p.hostname + ' · ' + p.name" @change="emit('select', ($event.target as HTMLInputElement).checked)" />选择此问题</label>
    <div class="row"><div class="problem-title"><span class="badge" :class="p.severity.toLowerCase()">{{ p.severity === 'CRITICAL' ? '严重' : '警告' }}</span><h3>{{ p.name }}</h3><span class="ack" v-if="p.handling.acknowledged">已确认</span><span class="muted" v-else>待确认</span></div><span class="muted">{{ problemAge(p.since, now) }}</span></div>
    <p>{{ p.info || '暂无规则说明' }}</p>
    <p class="muted">{{ p.hostname }} · {{ p.chart }} · {{ p.value === null ? '暂无数值' : p.value.toFixed(2) + ' ' + p.units }}</p>
    <p v-if="p.recovery_hold" class="muted">等待恢复：警告和严重条件已不成立，恢复表达式尚未成立。</p>
    <p v-else-if="p.hold_until && now < p.hold_until" class="muted">恢复保持至 {{ formatTime(p.hold_until) }}</p>
    <p v-if="p.delivery" class="muted">通知 {{ p.delivery.channel }} {{ deliveryText(p.delivery) }}。通道结果不代表用户已收件。</p>
    <div class="workflow-state"><span class="owner">责任人：{{ p.handling.assignee || '未分配' }}</span><span class="badge progress-state" :class="p.handling.status">{{ progressName[p.handling.status] }}</span></div>
    <p v-if="p.stale" class="stale-text">历史告警状态，最近观测 {{ formatTime(p.updated) }}；节点恢复更新前不视为当前健康结论。</p>
    <div v-if="canHandle" class="workflow-actions">
      <div>
        <label>责任人 <select :value="owner ?? p.handling.assignee" :disabled="busy === p.id" :aria-label="p.name + ' 责任人'" @change="emit('owner', ($event.target as HTMLSelectElement).value)">
          <option value="">未分配</option>
          <option v-if="p.handling.assignee && !assignees.some(u => u.name === p.handling.assignee)" :value="p.handling.assignee" disabled>{{ p.handling.assignee }}（不在可指派列表）</option>
          <option v-for="u in assignees" :key="u.name" :value="u.name">{{ u.name }}{{ u.name === currentUser ? '（我）' : '' }}</option>
        </select></label>
        <button :disabled="!!busy || (owner ?? p.handling.assignee) === p.handling.assignee" @click="emit('assign')">保存责任人</button>
        <button v-if="canSelfAssign && p.handling.assignee !== currentUser" :disabled="!!busy" @click="emit('update', { action: 'assign', assignee: currentUser })">分配给我</button>
      </div>
      <div>
        <label>处理进度 <select :value="status ?? p.handling.status" :disabled="busy === p.id" :aria-label="p.name + ' 处理进度'" @change="emit('status', ($event.target as HTMLSelectElement).value as HandlingStatus)">
          <option value="open">待处理</option><option value="investigating">排查中</option><option value="watching">观察中</option>
        </select></label>
        <button :disabled="!!busy || (status ?? p.handling.status) === p.handling.status" @click="emit('update', { action: 'progress', status: status ?? p.handling.status })">保存进度</button>
      </div>
    </div>
    <div class="problem-actions"><button @click="emit('drill', p.node, p.chart)">定位图表</button><button @click="emit('solve', p)"><AppIcon name="ai" :size="16" />AI 解决问题</button><template v-if="canHandle"><input :value="note" @input="emit('note', ($event.target as HTMLInputElement).value)" :disabled="busy === p.id" :aria-label="p.name + ' 处理备注'" placeholder="处理备注（可选）" maxlength="512" /><button :disabled="!!busy" @click="emit('update', { action: p.handling.acknowledged ? 'unacknowledge' : 'acknowledge' })">{{ busy === p.id ? '正在保存…' : p.handling.acknowledged ? '撤销确认' : '确认问题' }}</button><button :disabled="!!busy || !note?.trim()" @click="emit('update', { action: 'comment' })">添加备注</button></template></div>
    <details v-if="p.handling.history.length"><summary>处理记录 · 最近 {{ p.handling.history.length }} 条</summary><ol><li v-for="(h,i) in [...p.handling.history].reverse()" :key="i"><span>{{ formatTime(h.at) }} · {{ h.actor }} · {{ describeAction(h) }}</span><p v-if="h.note">{{ h.note }}</p></li></ol></details>
  </article>
</template>

<style scoped>
.problem { background:#0f1a2d; border:1px solid #27364c; border-left:3px solid #fbbf24; border-radius:8px; padding:16px; margin:12px 0; overflow-wrap:anywhere; }
.problem.critical { border-left-color:#fb7185; }
.problem-select { display:flex; align-items:center; gap:6px; color:#94a3b8; font-size:12px; margin-bottom:10px; }
.problem-title,.problem-actions,.workflow-state,.workflow-actions,.workflow-actions>div { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
.problem-title { flex:1; }.problem-actions { margin-top:14px; }.problem-actions input { flex:1; min-width:min(200px,100%); }
.workflow-state { font-size:12px; margin:12px 0; gap:10px; }.owner { overflow-wrap:anywhere; }
.workflow-actions { border-top:1px solid #27364c; padding-top:12px; margin-top:12px; gap:12px 22px; }
.workflow-actions>div { min-width:0; max-width:100%; gap:10px; }.workflow-actions label { display:flex; align-items:center; gap:8px; font-size:12px; min-width:0; max-width:100%; }.workflow-actions select { min-width:0; max-width:220px; flex-shrink:1; }
h3 { font-size:14px; margin:0; overflow-wrap:anywhere; } p { font-size:13px; line-height:1.65; margin:8px 0; }.muted { color:#94a3b8; }
.row { display:flex; gap:12px; justify-content:space-between; align-items:center; flex-wrap:wrap; }
.badge { font-size:11px; padding:3px 7px; border-radius:5px; background:#26334a; color:#cbd5e1; white-space:nowrap; }
.badge.critical { color:#fda4af; background:#4c1d2a; }.badge.warning { color:#fde68a; background:#46361c; }
.badge.investigating { background:#1e3a5f; color:#93c5fd; }.badge.watching { background:#3b2c5b; color:#d8b4fe; }.ack { color:#6ee7b7; font-size:11px; }.stale-text { color:#fcd34d; }
button,input,select { font:inherit; font-size:12px; border:1px solid #334155; border-radius:7px; background:#111d31; color:#e2e8f0; padding:8px 10px; max-width:100%; }
button { cursor:pointer; } button:hover:not(:disabled) { border-color:#5eead4; color:#5eead4; } button:disabled { opacity:.5; cursor:default; } input:focus,select:focus,button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
details { margin-top:12px; border-top:1px solid #27364c; padding-top:12px; font-size:12px; } summary { cursor:pointer; color:#94a3b8; } ol { padding-left:20px; color:#94a3b8; } li { margin:10px 0; } li p { color:#e2e8f0; white-space:pre-wrap; }
@media(max-width:760px) { .problem-actions input { flex-basis:100%; } }
</style>
