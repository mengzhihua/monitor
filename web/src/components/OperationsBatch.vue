<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { HandlingChange, HandlingStatus, Problem } from '../api'

const selected = defineModel<Problem[]>('selected', { required: true })
const props = defineProps<{
  current: Problem[]; visible: Problem[]; assignees: { name: string; role: string }[]
  busy: boolean; stale: boolean; error: string; message: string; completed: number
}>()
const emit = defineEmits<{ submit: [change: HandlingChange & { note: string }] }>()
const action = ref<HandlingChange['action']>('acknowledge')
const assignee = ref('')
const status = ref<HandlingStatus>('investigating')
const note = ref('')
const labels: Record<string, string> = { acknowledge: '确认问题', unacknowledge: '撤销确认', assign: '指派责任人', unassign: '取消指派', progress: '更新进度', comment: '添加备注' }
const progressNames: Record<HandlingStatus, string> = { open: '待处理', investigating: '排查中', watching: '观察中' }
const changedIDs = computed(() => new Set(selected.value.filter(p => {
  const latest = props.current.find(n => n.id === p.id)
  return !latest || latest.handling.revision !== p.handling.revision
}).map(p => p.id)))
const valid = computed(() => selected.value.length > 0 && !props.busy && !props.stale && changedIDs.value.size === 0
  && (action.value !== 'assign' || props.assignees.some(u => u.name === assignee.value))
  && (action.value !== 'comment' || note.value.trim().length > 0))
const description = computed(() => action.value === 'assign' ? `指派给 ${assignee.value || '未选择'}`
  : action.value === 'progress' ? `更新为 ${progressNames[status.value]}` : labels[action.value])
watch(() => props.completed, () => { note.value = '' })
function submit() {
  if (!valid.value) return
  emit('submit', { action: action.value, note: note.value,
    ...(action.value === 'assign' ? { assignee: assignee.value } : {}),
    ...(action.value === 'progress' ? { status: status.value } : {}),
  })
}
</script>

<template>
  <section class="batch" aria-label="批量问题处置">
    <div class="row">
      <strong>批量处置 · 已选择 {{ selected.length }} / 50 项</strong>
      <button :disabled="busy || stale || !visible.length" @click="selected = visible.slice(0, 50)">选择当前显示（最多 50 项）</button>
      <button :disabled="busy || !selected.length" @click="selected = []">清空选择</button>
    </div>
    <p class="hint">只处理明确选中的问题；改变筛选会清空选择。刷新不会自动加入新问题或更新已选版本。整批保存成功后才生效。</p>
    <template v-if="selected.length">
      <details open>
        <summary>核对本次范围（{{ selected.length }} 项）</summary>
        <ul><li v-for="p in selected" :key="p.id">
          <span>{{ p.hostname }} · {{ p.name }} · {{ p.severity === 'CRITICAL' ? '严重' : '警告' }} · {{ p.handling.assignee || '未分配' }} · {{ progressNames[p.handling.status] }} · {{ p.handling.acknowledged ? '已确认' : '待确认' }}</span>
          <strong v-if="changedIDs.has(p.id)" class="error">已变化或恢复，请重新选择</strong>
          <button :disabled="busy" :aria-label="'移除选择 ' + p.hostname + ' · ' + p.name" @click="selected = selected.filter(x => x.id !== p.id)">移除</button>
        </li></ul>
      </details>
      <form @submit.prevent="submit">
        <div class="row">
          <label>操作 <select v-model="action" aria-label="批量操作" :disabled="busy">
            <option value="acknowledge">确认问题</option><option value="unacknowledge">撤销确认</option><option value="assign">指派责任人</option><option value="unassign">取消指派</option><option value="progress">更新进度</option><option value="comment">添加备注</option>
          </select></label>
          <label v-if="action === 'assign'">责任人 <select v-model="assignee" aria-label="批量责任人" :disabled="busy"><option value="" disabled>选择责任人</option><option v-for="u in assignees" :key="u.name" :value="u.name">{{ u.name }}</option></select></label>
          <label v-if="action === 'progress'">进度 <select v-model="status" aria-label="批量处理进度" :disabled="busy"><option value="open">待处理</option><option value="investigating">排查中</option><option value="watching">观察中</option></select></label>
        </div>
        <label class="note">统一备注（{{ action === 'comment' ? '必填' : '可选' }}）<textarea v-model="note" aria-label="批量处理备注" :disabled="busy" maxlength="512" rows="2" /></label>
        <p>本次将对 {{ selected.length }} 项执行：<b>{{ description }}</b>。每个问题分别记录操作者和备注。</p>
        <button type="submit" :disabled="!valid">{{ busy ? '正在保存…' : `对选中 ${selected.length} 项执行` }}</button>
      </form>
    </template>
    <p v-if="stale" class="error">总览数据刷新失败，请先刷新总览再进行批量处置。</p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="message" role="status">{{ message }}</p>
  </section>
</template>

<style scoped>
.batch { margin:12px 0; padding:14px; background:#102035; border:1px solid #30516e; border-radius:10px; font-size:13px; }
.row,li { display:flex; gap:8px; flex-wrap:wrap; align-items:center; }
.hint { color:#94a3b8; font-size:12px; line-height:1.6; }
.error { color:#fca5a5; }
details { border-top:1px solid #30516e; padding-top:10px; margin-top:12px; }
summary { cursor:pointer; color:#cbd5e1; }
ul { padding:0; max-height:220px; overflow:auto; }
li { padding:6px 0; overflow-wrap:anywhere; }
li span { flex:1; min-width:160px; }
li button { padding:4px 8px; }
form { border-top:1px solid #30516e; padding-top:12px; }
label { display:flex; align-items:center; gap:8px; max-width:100%; }
button,select,textarea { font:inherit; border:1px solid #405870; border-radius:6px; background:#111d31; color:#e2e8f0; padding:7px 10px; max-width:100%; }
select { min-width:0; }
button { cursor:pointer; }
button:disabled { opacity:.5; cursor:default; }
.note { align-items:flex-start; flex-direction:column; margin:12px 0; }
textarea { width:100%; box-sizing:border-box; resize:vertical; }
button:focus-visible,select:focus,textarea:focus { outline:2px solid #5eead4; outline-offset:2px; }
</style>
