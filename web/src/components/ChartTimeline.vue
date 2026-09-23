<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { historyEnd, localDateTime } from '../chartInspection'

const props = defineProps<{ end: number | null; window: number; notice?: string }>()
const emit = defineEmits<{ 'update:end': [value: number | null] }>()
const input = ref(localDateTime(props.end ?? Math.floor(Date.now()/1000)))
const error = ref('')
const expanded = ref(props.end !== null)
const range = computed(() => props.end === null ? '实时查看 · 图表随当前时间更新'
  : `历史查看 · 统一结束于 ${new Date(props.end*1000).toLocaleString()}`)
function apply(value: string) {
  try { emit('update:end', historyEnd(value, props.window)); error.value = '' }
  catch (e) { error.value = (e as Error).message }
}
function freeze() { input.value = localDateTime(Math.floor(Date.now()/1000)); apply(input.value); expanded.value = true }
function move(direction: number) {
  input.value = localDateTime(Math.min(Math.floor(Date.now()/1000), (props.end ?? Math.floor(Date.now()/1000)) + direction*props.window))
  apply(input.value)
}
function resume() { emit('update:end', null); error.value = ''; expanded.value = false }
watch(() => props.end, end => { input.value = localDateTime(end ?? Math.floor(Date.now()/1000)); error.value = '' })
</script>

<template>
  <section class="timeline" :class="{ historical: end !== null }" aria-label="统一查看时间">
    <div class="timeline-heading"><b>{{ range }}</b><div class="timeline-actions">
      <button v-if="end === null" type="button" @click="freeze">冻结整个看板</button>
      <button v-else type="button" @click="resume">整个看板返回实时</button>
      <button type="button" :aria-expanded="expanded" @click="expanded = !expanded">{{ expanded ? '收起时间设置' : '设置历史时间' }}</button>
    </div></div>
    <p v-if="end !== null">所有图表按同一结束时刻查询，个人看板保留自己的时长。仅冻结指标图表，告警与运维总览仍显示当前状态。</p>
    <form v-if="expanded" @submit.prevent="apply(input)">
      <label>看板结束时间（本地）<input v-model="input" type="datetime-local" step="1" required /></label>
      <button type="submit">应用到整个看板</button>
      <button type="button" @click="move(-1)">整个看板上一时段</button>
      <button type="button" :disabled="end === null" @click="move(1)">整个看板下一时段</button>
      <small>翻页步长按顶部时长（{{ window / 60 }} 分钟）。时间设置仅保存在当前标签页。</small>
    </form>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
  </section>
</template>

<style scoped>
.timeline { background:#102331; border:1px solid #28526a; border-radius:9px; padding:12px; margin-bottom:16px; min-width:0; } .timeline.historical { background:#292313; border-color:#a17c26; }
.timeline-heading, .timeline-actions, form { display:flex; align-items:center; flex-wrap:wrap; gap:8px; } .timeline-heading { justify-content:space-between; } b { font-size:13px; overflow-wrap:anywhere; }
button { color:#cbd5e1; background:#172337; border:1px solid #475569; border-radius:6px; padding:7px 10px; cursor:pointer; font-size:12px; } button:disabled { opacity:.4; cursor:default; } button:focus-visible, input:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
form { align-items:end; margin-top:12px; } label { display:flex; flex-direction:column; gap:5px; font-size:12px; max-width:100%; min-width:0; } input { background:#0b1120; color:#e2e8f0; color-scheme:dark; border:1px solid #475569; border-radius:5px; padding:6px; max-width:100%; min-width:0; box-sizing:border-box; }
p, small { color:#cbd5e1; font-size:12px; line-height:1.6; margin:8px 0 0; } small { flex-basis:100%; } .error { color:#fca5a5; }
</style>
