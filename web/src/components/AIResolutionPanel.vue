<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { ApiError } from '../api'
import { ai, type AIResolution, type AIResolutionSummary } from '../ai'
import AppIcon from './AppIcon.vue'

const props = defineProps<{ node: string; generated: AIResolution | null }>()
const emit = defineEmits<{ expired: [] }>()
const records = shallowRef<AIResolutionSummary[]>([])
const active = shallowRef<AIResolution | null>(null)
const drafts = ref<Record<string, { status: 'todo' | 'done' | 'skipped'; note: string }>>({})
const error = ref('')
const notice = ref('')
const busy = ref('')
const conflict = ref(false)
const uncertain = ref(false)
const deleting = ref(false)
const persistent = ref(true)
const allNodes = ref(false)
let controller: AbortController | null = null
const kinds = { diagnostic: '排查', remediation: '修复建议', verification: '复核' }
const risks = { low: '低风险', medium: '中等风险', high: '高风险', unknown: '风险待核查' }
const confidence = { low: '低', medium: '中', high: '高' }
const outcome: Record<string, string> = { observed: '已取得新数据，请人工核对效果', alarm_recovered: '关联告警当前已恢复', still_active: '关联告警仍在触发', episode_changed: '告警已进入新阶段，需重新评估', insufficient_data: '数据不足或过期，无法确认处理效果', awaiting_new_data: '尚无处理后的新采样，请稍后复核' }
const done = computed(() => active.value?.progress.filter(p => p.status === 'done').length || 0)
const fmt = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 3 })
function number(value: number | null) { return value === null || !Number.isFinite(value) ? '缺失' : fmt.format(value) }
function when(value: number) { return new Date(value * 1000).toLocaleString() }
function abort() {
  if (busy.value === 'save' || busy.value === 'review' || busy.value === 'delete') uncertain.value = true
  controller?.abort(); controller = null; busy.value = ''
}
function reset() { abort(); records.value = []; active.value = null; drafts.value = {}; conflict.value = uncertain.value = deleting.value = false; error.value = notice.value = '' }
async function request<T>(kind: string, operation: (signal: AbortSignal) => Promise<T>, accept: (value: T) => void) {
  if (busy.value) return
  const current = new AbortController(); controller = current; busy.value = kind; error.value = notice.value = ''
  try { const value = await operation(current.signal); if (!current.signal.aborted) accept(value) }
  catch (e) {
    if (current.signal.aborted) return
    if (e instanceof ApiError && e.status === 401) { reset(); emit('expired'); error.value = '登录已失效，请重新登录。' }
    else if (e instanceof ApiError && e.status === 409) { conflict.value = true; error.value = '方案版本或节点已变化，草稿已保留。请读取最新版本后核对。' }
    else if (e instanceof ApiError && e.status === 403) { error.value = '当前账号无权访问该节点或方案。'; active.value = null; drafts.value = {} }
    else if (e instanceof ApiError && e.status === 404) { error.value = '方案或图表已不存在，请刷新方案列表。' }
    else error.value = e instanceof Error ? e.message : '请求失败，请稍后重试。'
  } finally { if (controller === current) { controller = null; busy.value = '' } }
}
function refreshList() {
  void request('list', signal => ai.resolutions(allNodes.value ? undefined : props.node, signal), value => { records.value = value.records; persistent.value = value.persistent })
}
function apply(record: AIResolution, keepDraft = false) {
  active.value = record; conflict.value = uncertain.value = deleting.value = false
  const next = keepDraft ? { ...drafts.value } : {}
  for (const p of record.progress) if (!keepDraft || !next[p.id]) next[p.id] = { status: p.status, note: p.note }
  drafts.value = next
  records.value = [{ id: record.id, node: record.node, hostname: record.hostname, summary: record.plan.summary, created_at: record.created_at, updated_at: record.updated_at, done: record.progress.filter(p => p.status === 'done').length, total: record.progress.length }, ...records.value.filter(p => p.id !== record.id)].slice(0, 10)
}
function load(id: string, keepDraft = false) {
  void request('load', signal => ai.resolution(id, signal), value => { apply(value, keepDraft); if (keepDraft) notice.value = '已读取最新版本并保留草稿；请核对服务端记录后再保存。' })
}
function saveStep(id: string) {
  const record = active.value, draft = drafts.value[id]
  if (!record || !draft || uncertain.value || conflict.value) return
  void request('save', signal => ai.progress(record.id, record.revision, id, draft.status, draft.note, signal), value => {
    apply(value, true); notice.value = '人工处理记录已保存；请继续按方案核查并复核指标。'
  })
}
function review() {
  const record = active.value
  if (!record || uncertain.value || conflict.value) return
  void request('review', signal => ai.review(record.id, record.revision, signal), value => { apply(value, true); notice.value = '已读取最新指标并保存复核结果，未调用模型服务。' })
}
function remove() {
  const record = active.value
  if (!record || uncertain.value || conflict.value) return
  void request('delete', signal => ai.deleteResolution(record.id, record.revision, signal), () => {
    records.value = records.value.filter(p => p.id !== record.id); active.value = null; drafts.value = {}; deleting.value = false; notice.value = '个人方案已删除。'
  })
}
function cancel() { abort(); notice.value = uncertain.value ? '已取消等待，保存或复核可能已完成，请读取最新方案版本后继续。' : '请求已取消。' }
function download() {
  if (!active.value) return
  const r = active.value
  const content = ['Monitor AI 问题解决方案（人工操作与复核）', `节点：${r.hostname}`, `问题：${r.question}`, `模型：${r.model}`, `生成：${when(r.created_at)}`, r.plan.summary,
    ...r.plan.hypotheses.map(h => `可能原因（置信度${confidence[h.confidence]}）：${h.explanation}\n依据：${h.evidence}`),
    ...r.plan.steps.map((s, i) => { const p = r.progress[i]!; return `${i + 1}. ${s.title}（${kinds[s.kind]} / ${risks[s.risk]} / ${p.status}）\n${s.instruction}\n预期：${s.expected}\n回退：${s.rollback || '不适用'}\n已保存备注：${p.note}` }),
    r.review ? `复核：${when(r.review.at)} · ${outcome[r.review.outcome] || '待人工核对'}` : '尚未复核', '\n完整方案与事实快照：', JSON.stringify(r, null, 2)].join('\n\n')
  const url = URL.createObjectURL(new Blob([content], { type: 'text/plain;charset=utf-8' })); const a = document.createElement('a'); a.href = url; a.download = `monitor-resolution-${r.id}.txt`; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
}
watch(() => props.node, () => { reset(); allNodes.value = false; refreshList() }, { immediate: true })
watch(() => props.generated, value => {
  if (!value) return
  abort(); apply(value)
})
onBeforeUnmount(abort)
</script>

<template>
  <section class="resolution" aria-label="问题解决工作台">
    <div class="row"><h3><AppIcon name="rules" />问题解决工作台</h3><button :disabled="!!busy" @click="refreshList"><AppIcon name="refresh" />刷新方案列表</button></div>
    <p class="muted">当前账号最多保存 10 份方案，可跨设备继续处理。所有步骤由你人工执行，复核读取最新监控事实。</p>
    <label class="all-nodes"><input v-model="allNodes" type="checkbox" :disabled="!!busy" @change="abort(); refreshList()" />查看全部节点的个人方案（包括已移除节点）</label>
    <p v-if="!persistent" class="warning">当前仅保存在内存，服务重启后方案和步骤记录丢失。</p>
    <p v-if="error" role="alert" class="warning">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <div v-if="busy" class="row"><span>正在{{ busy === 'review' ? '读取最新指标' : '处理个人方案' }}…</span><button @click="cancel">取消方案请求</button></div>
    <div class="saved"><button v-for="record in records" :key="record.id" :disabled="!!busy" :aria-pressed="active?.id === record.id" @click="load(record.id)"><strong>{{ record.summary }}</strong><small>{{ record.hostname }} · {{ when(record.updated_at) }} · {{ record.done }} / {{ record.total }} 完成</small></button></div>
    <p v-if="!records.length && !busy" class="muted">暂无方案。选择上方“生成修复方案”，核对数据后发送给模型。</p>
    <article v-if="active" aria-label="当前问题解决方案">
      <h4>{{ active.plan.summary }}</h4><p class="muted">方案节点：{{ active.hostname }} · {{ active.model }} · {{ when(active.created_at) }} · {{ done }} / {{ active.plan.steps.length }} 步已记录完成</p>
      <p class="warning">以下是模型建议。先核对适用条件、风险和回退方法，再由你执行；勾选完成不会改变系统或真实告警。</p>
      <div class="row"><button :disabled="!!busy" @click="load(active.id, true)">读取最新方案版本，保留草稿</button><button @click="download"><AppIcon name="download" />下载处理报告</button><button :disabled="!!busy" @click="deleting = !deleting">删除方案</button></div>
      <div v-if="deleting" class="row warning"><span>删除当前个人方案及其步骤记录？下载的报告会保留。</span><button :disabled="!!busy || conflict || uncertain" @click="remove">确认删除方案</button><button @click="deleting = false">保留方案</button></div>
      <div v-for="(h, i) in active.plan.hypotheses" :key="i" class="hypothesis"><strong>可能原因 · 置信度{{ confidence[h.confidence] }}</strong><p>{{ h.explanation }}</p><p class="muted">模型给出的依据：{{ h.evidence }}</p></div>
      <ol class="steps"><li v-for="(step, i) in active.plan.steps" :key="step.id">
        <div class="row"><h4>{{ i + 1 }}. {{ step.title }}</h4><span :class="step.risk === 'high' || step.risk === 'unknown' ? 'warning' : 'muted'">{{ kinds[step.kind] }} · {{ risks[step.risk] }}</span></div>
        <p class="instruction">{{ step.instruction }}</p><p><strong>预期结果：</strong>{{ step.expected }}</p><p v-if="step.rollback"><strong>回退方法：</strong>{{ step.rollback }}</p><p v-if="step.chart" class="muted">参考指标：{{ step.chart }} {{ step.dimension }}</p>
        <div v-if="drafts[step.id]" class="record"><label>人工处理状态<select v-model="drafts[step.id]!.status" :disabled="!!busy" :aria-label="step.title + ' 人工处理状态'"><option value="todo">待处理</option><option value="done">已人工完成</option><option value="skipped">不适用 / 跳过</option></select></label><label>处理备注<textarea v-model="drafts[step.id]!.note" :disabled="!!busy" rows="2" maxlength="500" :aria-label="step.title + ' 处理备注'" placeholder="填写实际操作、结果或跳过原因（不会发送给模型）" /></label><button :disabled="!!busy || conflict || uncertain" @click="saveStep(step.id)">保存此步记录</button></div>
        <p class="muted">服务端已保存：{{ active.progress[i]?.status === 'done' ? '已人工完成' : active.progress[i]?.status === 'skipped' ? '已跳过' : '待处理' }}<span v-if="active.progress[i]?.note"> · {{ active.progress[i]?.note }}</span></p>
      </li></ol>
      <div class="row"><button class="primary" :disabled="!!busy || conflict || uncertain" @click="review"><AppIcon name="check" />重新采样并复核</button><span class="muted">不调用模型；请在处理后等待新采样</span></div>
      <section v-if="active.review" class="review" aria-label="处理效果复核"><h4>{{ outcome[active.review.outcome] || '待人工核对' }}</h4><p class="muted">复核时间：{{ when(active.review.at) }} · 前后均为 {{ active.baseline.before - active.baseline.after }} 秒窗口</p><div class="comparisons"><div v-for="c in active.review.comparisons" :key="c.chart + ':' + c.dimension"><strong>{{ c.chart }} · {{ c.dimension }}</strong><p>处理前 {{ number(c.before) }} → 当前 {{ number(c.after) }} {{ c.units }}</p><small>桶均值变化：{{ number(c.delta) }}</small></div></div><ul><li v-for="note in active.review.notes" :key="note">{{ note }}</li></ul></section>
      <details><summary>查看方案生成时的原始事实快照</summary><pre>{{ JSON.stringify(active.baseline, null, 2) }}</pre></details>
    </article>
  </section>
</template>

<style scoped>
.all-nodes { flex-direction:row; align-items:center; gap:8px; min-height:44px; margin:8px 0; }.all-nodes input { width:18px; min-height:18px; height:18px; accent-color:#2dd4bf; }
.resolution{margin-top:20px;border-top:1px solid #334155;padding-top:16px;min-width:0;overflow-wrap:anywhere}.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}h3{display:flex;align-items:center;gap:7px;font-size:16px}h4{font-size:15px;margin:10px 0}.muted,small{color:#94a3b8;font-size:13px}.warning{color:#fde68a}p{white-space:pre-wrap;line-height:1.65}.saved{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px;margin:12px 0}.saved button{text-align:left;display:flex;flex-direction:column;gap:6px}.saved [aria-pressed=true]{border-color:#2dd4bf}.hypothesis,.steps>li,.review{background:#101d31;border:1px solid #334155;border-radius:9px;padding:12px;margin:12px 0}.steps{padding:0;list-style:none}.instruction{font-family:ui-monospace,monospace;white-space:pre-wrap}.record{display:grid;grid-template-columns:minmax(120px,180px) minmax(0,1fr);gap:12px}.record button{grid-column:1/-1;justify-self:start}label{display:flex;flex-direction:column;gap:6px;font-size:13px;min-width:0}button,input,select,textarea{font:inherit;border:1px solid #475569;border-radius:7px;background:#16243b;color:#e2e8f0;padding:9px 12px;min-height:44px;max-width:100%;min-width:0;box-sizing:border-box}button{cursor:pointer;display:inline-flex;gap:7px;align-items:center;justify-content:center}button:disabled{opacity:.5;cursor:not-allowed}.primary{background:#115e59;border-color:#0d9488}button:focus-visible,select:focus-visible,textarea:focus-visible,summary:focus-visible{outline:2px solid #5eead4;outline-offset:2px}.comparisons{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.comparisons>div{padding:10px;background:#0b1426;border-radius:6px}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:320px;overflow:auto;font-size:12px}summary{min-height:44px;cursor:pointer;display:flex;align-items:center}@media(max-width:760px){.saved,.record,.comparisons{grid-template-columns:minmax(0,1fr)}textarea,select{font-size:16px}.record button{justify-self:stretch}.row>button{flex:1 1 auto}}
</style>
