<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { ApiError, type Chart } from '../api'
import { ai, type AIPreview, type AIResult, type AISettings, type AIDimension, type AIProblemSelection } from '../ai'
import AppIcon from './AppIcon.vue'
import AIResolutionPanel from './AIResolutionPanel.vue'

const props = defineProps<{ node: string; charts: Chart[]; role: string; problem?: AIProblemSelection | null }>()
const emit = defineEmits<{ close: [] }>()
const settings = shallowRef<AISettings | null>(null)
const baseURL = ref('')
const model = ref('')
const apiKey = ref('')
const clearKey = ref(false)
const allowPrivate = ref(false)
const settingsOpen = ref(false)
const conflict = ref(false)
const settingsUncertain = ref(false)
const error = ref('')
const notice = ref('')
const busy = ref('')
let controller: AbortController | null = null
const preview = shallowRef<AIPreview | null>(null)
const result = shallowRef<AIResult | null>(null)
const expired = ref(false)
const previewConsumed = ref(false)
let expiryTimer: ReturnType<typeof setTimeout> | undefined
const selected = ref<string[]>([])
let selectionChosen = false
const duration = ref(900)
const filter = ref('')
const question = ref('请根据快照分析异常、可能原因和建议的核查步骤，明确指出数据不足及不能确认的结论。')
const exportQuestion = ref('')
const deleting = ref(false)
const mode = ref<'analysis' | 'resolution'>('analysis')
const targetProblem = shallowRef<AIProblemSelection | null>(null)
const isAdmin = computed(() => props.role === 'admin')
const options = [{ value: 60, label: '最近 1 分钟' }, { value: 300, label: '最近 5 分钟' }, { value: 900, label: '最近 15 分钟' }, { value: 3600, label: '最近 1 小时' }, { value: 21600, label: '最近 6 小时' }, { value: 86400, label: '最近 24 小时' }]
const shownCharts = computed(() => {
  const q = filter.value.trim().toLocaleLowerCase()
  return props.charts.filter(chart => !q || `${chart.id} ${chart.title}`.toLocaleLowerCase().includes(q))
})
const dirty = computed(() => settingsUncertain.value || !settings.value || baseURL.value.trim() !== settings.value.base_url || model.value.trim() !== settings.value.model || allowPrivate.value !== settings.value.allow_private || !!apiKey.value || clearKey.value)
const endpointChanged = computed(() => !!settings.value?.has_api_key && baseURL.value.trim() !== settings.value.base_url)
const missingReplacementKey = computed(() => endpointChanged.value && !apiKey.value.trim() && !clearKey.value)
const canAnalyze = computed(() => !!preview.value && !expired.value && !previewConsumed.value && !!settings.value?.enabled && !dirty.value && !busy.value && question.value.length <= 2000)
const snapshotJSON = computed(() => preview.value ? JSON.stringify(preview.value.snapshot, null, 2) : '')

function cancel(showNotice = true) {
  const running = !!controller
  const analyzing = busy.value === 'analyze'
  const updating = busy.value === 'save' || busy.value === 'delete'
  if (running && updating) { settingsUncertain.value = true; settingsOpen.value = true }
  controller?.abort()
  controller = null
  busy.value = ''
  if (running && showNotice) notice.value = analyzing ? (mode.value === 'resolution' ? '请求已取消。模型服务可能已经处理请求，费用以服务商记录为准；方案也可能已保存，请刷新方案列表确认。' : '请求已取消。模型服务可能已经处理请求，费用以服务商记录为准。') : updating ? '配置请求已取消，服务端可能已经完成修改。请读取最新配置版本后继续。' : '请求已取消。'
}
function invalidate() {
  cancel(false)
  preview.value = null
  result.value = null
  exportQuestion.value = ''
  expired.value = false
  previewConsumed.value = false
  clearTimeout(expiryTimer)
  notice.value = ''
}
function clearPrivateState() {
  invalidate()
  settings.value = null
  settingsUncertain.value = false
  baseURL.value = ''
  model.value = ''
  apiKey.value = ''
  clearKey.value = false
  allowPrivate.value = false
  question.value = ''
  deleting.value = false
}
function errorMessage(e: unknown) {
  if (e instanceof ApiError) {
    if (e.status === 401) {
      clearPrivateState()
      return '登录已失效，请重新登录后使用 AI 分析。'
    }
    if (e.status === 409) {
      conflict.value = true
      if (busy.value === 'preview' && targetProblem.value) return '关联问题已恢复、进入新阶段或无法访问。请刷新运维总览并重新选择问题，或解除关联后继续分析。'
      if (busy.value === 'analyze' || busy.value === 'preview') return '快照已失效、已使用或配置已变更。请重新生成预览；若配置在其他页面修改过，请先刷新配置版本。'
      return '配置或快照版本已变化。当前配置草稿已保留，请读取最新配置后重新保存并生成预览。'
    }
    if (e.status === 429) return e.message.includes('AI resolution storage full') ? '方案存储已满，请在工作台查看全部节点的个人方案，下载并删除不需要的旧方案后再生成；仍失败时联系管理员检查总容量。' : '分析请求过于频繁或模型服务额度不足，请稍后手动重试。'
    if (e.status === 408 || e.status === 504) return '模型服务响应超时，未获得完整结果。请检查模型服务后手动重试。'
    if (e.status === 502) return mode.value === 'resolution' ? '模型服务连接失败或方案格式不完整，请检查模型配置及 JSON 格式支持，再重新生成预览。' : '模型服务连接失败或返回了无效结果，请核查服务地址、模型名和 API Key。'
    if (e.status === 403) return '当前账号无权使用该配置或访问该节点，请检查权限。'
    if (e.status === 410) return '快照已过期，请重新生成预览后再分析。'
  }
  return e instanceof Error ? e.message : '请求失败，请稍后重试。'
}
async function request<T>(kind: string, operation: (signal: AbortSignal) => Promise<T>, accept: (value: T) => void) {
  if (busy.value) return
  const current = new AbortController()
  controller = current
  busy.value = kind
  error.value = ''
  notice.value = ''
  try {
    const value = await operation(current.signal)
    if (!current.signal.aborted) {
      // Applying normalized settings can invalidate drafts; the request is already complete.
      if (controller === current) { controller = null; busy.value = '' }
      accept(value)
    }
  } catch (e) {
    if (!current.signal.aborted) error.value = errorMessage(e)
  } finally {
    if (controller === current) {
      controller = null
      busy.value = ''
    }
  }
}
function applySettings(value: AISettings) {
  settingsUncertain.value = false
  settings.value = value
  baseURL.value = value.base_url
  model.value = value.model
  apiKey.value = ''
  clearKey.value = false
  allowPrivate.value = value.allow_private
  conflict.value = false
}
function loadSettings(keepDraft = false) {
  invalidate()
  return request('settings', signal => ai.settings(signal), value => {
    if (keepDraft) {
      settings.value = value
      settingsUncertain.value = false
      conflict.value = false
      notice.value = '已读取最新配置版本，草稿保留；请核对后保存。'
    } else {
      applySettings(value)
      settingsOpen.value = !value.enabled
    }
  })
}
function saveSettings() {
  if (settingsUncertain.value || missingReplacementKey.value || !settings.value || !baseURL.value.trim() || !model.value.trim()) return
  const body = {
    base_url: baseURL.value.trim(), model: model.value.trim(), allow_private: isAdmin.value && allowPrivate.value,
    clear_api_key: clearKey.value, revision: settings.value.revision,
    ...(apiKey.value.trim() && !clearKey.value ? { api_key: apiKey.value.trim() } : {}),
  }
  invalidate()
  void request('save', signal => ai.save(body, signal), value => {
    applySettings(value)
    notice.value = '个人 AI 配置已保存。请先生成并核对数据预览。'
    settingsOpen.value = false
  })
}
function removeSettings() {
  if (settingsUncertain.value || !settings.value) return
  const revision = settings.value.revision
  invalidate()
  void request('delete', signal => ai.remove(revision, signal), value => {
    applySettings(value)
    settingsOpen.value = true
    deleting.value = false
    notice.value = '个人 AI 配置和已保存密钥已删除。'
  })
}
function defaults() {
  const ids = props.charts.map(chart => chart.id)
  const preferred = ['system.cpu', 'system.ram', 'system.load'].filter(id => ids.includes(id))
  return preferred.length ? preferred : ids.slice(0, 3)
}
watch(() => props.node, () => { invalidate(); targetProblem.value = null; selectionChosen = false; selected.value = defaults(); filter.value = '' }, { immediate: true })
watch(() => props.problem, value => {
  if (!value) return
  invalidate(); targetProblem.value = value; mode.value = 'resolution'; selectionChosen = true; selected.value = [value.chart]
  question.value = `请围绕告警“${value.name}”制定人工逐步排查、修复及复核方案，注明依据、适用条件、风险和回退方法。`
}, { immediate: true })
watch(() => props.charts, () => {
  const existing = new Set(props.charts.map(chart => chart.id))
  const valid = selected.value.filter(id => existing.has(id))
  if (valid.length !== selected.value.length) selected.value = valid
  if (!selected.value.length && !selectionChosen) selected.value = defaults()
})
watch([selected, duration, baseURL, model, apiKey, clearKey, allowPrivate, mode], invalidate, { deep: true, flush: 'sync' })
function toggleChart(id: string, event: Event) {
  selectionChosen = true
  const checked = (event.target as HTMLInputElement).checked
  selected.value = checked ? [...selected.value, id].slice(0, 6) : selected.value.filter(value => value !== id)
}
function generatePreview() {
  if (!selected.value.length) return
  invalidate()
  void request('preview', signal => ai.preview(props.node, [...selected.value], duration.value, signal, targetProblem.value?.id), value => {
    preview.value = value
    const delay = value.expires_at * 1000 - Date.now()
    expired.value = delay <= 0
    expiryTimer = setTimeout(() => { expired.value = true }, Math.max(0, delay))
  })
}
function analyze() {
  if (!canAnalyze.value || !preview.value || !settings.value) return
  const id = preview.value.id
  const revision = settings.value.revision
  const prompt = question.value.trim()
  result.value = null
  previewConsumed.value = true
  exportQuestion.value = prompt
  void request('analyze', signal => ai.analyze(id, prompt, revision, signal, mode.value === 'resolution' ? 'resolution' : undefined), value => { result.value = value })
}
const valueFormatter = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 3 })
function number(value: number | null) {
  return value === null || !Number.isFinite(value) ? '缺失' : valueFormatter.format(value)
}
function change(dimension: AIDimension) {
  if (dimension.first === null || dimension.last === null) return '缺失'
  const value = dimension.last - dimension.first
  return `${value > 0 ? '+' : ''}${number(value)}`
}
function when(value: number) { return new Date(value * 1000).toLocaleString() }
function reportText() {
  if (!preview.value || !result.value) return ''
  return [
    'Monitor AI 分析（模型建议，需人工核实）',
    `节点：${preview.value.snapshot.hostname} (${preview.value.snapshot.node || 'local'})`,
    `采样窗口：${when(preview.value.snapshot.after)} — ${when(preview.value.snapshot.before)}`,
    `模型：${result.value.model} · 生成时间：${when(result.value.generated_at)}`,
    `问题：${exportQuestion.value}`, '', result.value.text,
    ...(result.value.truncated ? ['\n注意：模型结果因长度限制被截断。'] : []),
    '\n监控事实快照：', snapshotJSON.value,
  ].join('\n')
}
async function copyResult() {
  try { await navigator.clipboard.writeText(reportText()); notice.value = '分析结果和快照已复制。' }
  catch { notice.value = '当前浏览器未允许剪贴板访问，请使用下载结果。' }
}
function downloadResult() {
  const url = URL.createObjectURL(new Blob([reportText()], { type: 'text/plain;charset=utf-8' }))
  const link = document.createElement('a')
  link.href = url
  link.download = `monitor-ai-analysis-${result.value?.generated_at ?? Date.now()}.txt`
  link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
function close() { cancel(false); clearPrivateState(); emit('close') }
onMounted(() => { void loadSettings() })
onBeforeUnmount(() => { cancel(false); clearPrivateState(); clearTimeout(expiryTimer) })
</script>

<template>
  <section class="ai-panel" aria-label="AI 分析" :aria-busy="!!busy">
    <header class="ai-head">
      <div><h2><span class="heading-icon"><AppIcon name="ai" :size="23" />AI 分析</span> <small>按需分析 · 自定义模型</small></h2><p>先查看监控事实，再决定是否发送给你配置的模型服务。</p></div>
      <button class="close" type="button" aria-label="关闭 AI 分析" @click="close"><AppIcon name="close" :size="22" /></button>
    </header>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <div v-if="busy" class="activity" role="status">
      <span>{{ busy === 'analyze' ? '模型正在分析…' : busy === 'preview' ? '正在生成本地统计预览…' : '正在处理个人配置…' }}</span>
      <button type="button" @click="cancel()">取消请求</button>
    </div>

    <details class="settings" :open="settingsOpen" @toggle="settingsOpen = ($event.target as HTMLDetailsElement).open">
      <summary><AppIcon name="settings" /> 个人 AI 配置 <span>{{ settings?.enabled ? '已配置' : '未配置' }}</span></summary>
      <p class="muted">仅当前账号使用。API Key 只写入服务端，不回显、不写入浏览器存储。填写兼容 OpenAI Chat Completions 的 API 基础地址和模型名。</p>
      <p v-if="settingsUncertain" class="warning">上次配置请求的保存状态尚未确认，请刷新配置版本后再保存或分析。</p>
      <p v-if="settings && !settings.persistent" class="warning">当前配置仅保存在服务内存，服务重启后需要重新配置。</p>
      <form @submit.prevent="saveSettings">
        <fieldset :disabled="!!busy">
          <div class="config-grid">
            <label>API 基础地址<input v-model="baseURL" type="url" autocomplete="off" spellcheck="false" placeholder="https://your-provider.example/v1" /></label>
            <label>模型名<input v-model="model" autocomplete="off" spellcheck="false" placeholder="填写服务商提供的模型 ID" maxlength="128" /></label>
            <label class="key-field">API Key<input v-model="apiKey" type="password" autocomplete="new-password" :disabled="clearKey" :placeholder="settings?.has_api_key ? '已保存密钥；留空保留' : '输入密钥（本地免密模型可留空）'" /></label>
          </div>
          <label class="check"><input v-model="clearKey" type="checkbox" />清除已保存的 API Key</label>
          <label v-if="isAdmin" class="check"><input v-model="allowPrivate" type="checkbox" />允许本机或内网模型服务（含 HTTP）</label>
          <p v-else class="muted">普通账号仅可连接公网 HTTPS 模型服务。</p>
          <p v-if="missingReplacementKey" class="warning">更换服务地址时，请重新填写密钥或勾选清除旧密钥，避免把旧密钥发送给新地址。</p>
          <div class="actions">
            <button class="primary" type="submit" :disabled="settingsUncertain || !settings || !dirty || !baseURL.trim() || !model.trim() || missingReplacementKey"><AppIcon name="save" />保存个人配置</button>
            <button type="button" @click="loadSettings(true)">{{ conflict ? '读取最新配置，保留草稿' : '刷新配置版本' }}</button>
            <button v-if="settings?.enabled || settings?.has_api_key" type="button" @click="deleting = !deleting">删除个人配置</button>
          </div>
          <div v-if="deleting" class="delete-confirm"><span>将删除当前账号的模型配置及密钥。</span><button class="danger" type="button" :disabled="settingsUncertain" @click="removeSettings">确认删除配置</button><button type="button" @click="deleting = false">保留配置</button></div>
        </fieldset>
      </form>
    </details>

    <section class="step" aria-label="分析数据选择">
      <div class="prompts" role="group" aria-label="AI 工作模式"><button type="button" :aria-pressed="mode === 'analysis'" :disabled="!!busy" @click="mode = 'analysis'">分析指标</button><button type="button" :aria-pressed="mode === 'resolution'" :disabled="!!busy" @click="mode = 'resolution'">生成修复方案</button></div>
      <div v-if="targetProblem" class="scope"><strong>关联问题：{{ targetProblem.name }}</strong><p>生成预览时核对当前告警阶段。若问题已恢复或变更，请重新选择问题。</p><button type="button" :disabled="!!busy" @click="invalidate(); targetProblem = null">解除问题关联</button></div>
      <div class="step-title"><h3><AppIcon name="charts" />1. 选择监控数据</h3><span class="muted">最多 6 张图表</span></div>
      <div class="selection-controls"><label>查找图表<input v-model="filter" type="search" placeholder="按名称或 ID 查找" /></label><label>统计时间<select v-model.number="duration"><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></select></label></div>
      <div class="chart-options" role="group" aria-label="分析图表">
        <label v-for="chart in shownCharts" :key="chart.id" class="chart-option"><input type="checkbox" :checked="selected.includes(chart.id)" :disabled="!!busy || (!selected.includes(chart.id) && selected.length >= 6)" :aria-label="chart.id" @change="toggleChart(chart.id, $event)" /><span>{{ chart.title }}<small>{{ chart.id }} · {{ chart.units }}</small></span></label>
        <p v-if="!shownCharts.length" class="muted">没有匹配的图表。</p>
      </div>
      <div class="actions"><button class="primary" type="button" :disabled="!!busy || !selected.length" @click="generatePreview"><AppIcon name="eye" />生成本地预览</button><span class="muted">已选 {{ selected.length }} / 6 · 不调用外部模型</span></div>
    </section>

    <section v-if="preview" class="step snapshot" aria-label="监控事实预览">
      <div class="step-title"><h3><AppIcon name="shield" />2. 核对监控事实</h3><span :class="expired ? 'warning' : 'muted'">{{ expired ? '预览已过期，请重新生成' : `有效至 ${when(preview.expires_at)}` }}</span></div>
      <p class="scope">{{ preview.snapshot.hostname }} · {{ when(preview.snapshot.after) }} — {{ when(preview.snapshot.before) }}</p>
      <article v-for="chart in preview.snapshot.charts" :key="chart.id" class="stat-card"><h4>{{ chart.title }} <small>{{ chart.id }}</small></h4><p class="muted">{{ chart.points }} 个时间桶 · 桶宽 {{ chart.step }} 秒 · 单位 {{ chart.units }}</p>
        <div v-for="dimension in chart.dimensions" :key="dimension.id" class="dimension"><strong>{{ dimension.name || dimension.id }}</strong><dl><div><dt>最近有效桶均值</dt><dd>{{ number(dimension.last) }}</dd></div><div><dt>桶均值的平均</dt><dd>{{ number(dimension.average) }}</dd></div><div><dt>桶均值最小 / 最大</dt><dd>{{ number(dimension.min) }} / {{ number(dimension.max) }}</dd></div><div><dt>首末有效桶差</dt><dd>{{ change(dimension) }}</dd></div><div><dt>有效桶 / 缺失桶</dt><dd>{{ dimension.count }} / {{ dimension.missing }}</dd></div></dl><p v-if="dimension.last_at" class="muted">最近有效桶结束时间：{{ when(dimension.last_at) }}</p></div>
      </article>
      <div v-if="preview.snapshot.alarms.length" class="alarms"><h4>关联告警</h4><p v-for="(alarm, i) in preview.snapshot.alarms" :key="`${alarm.chart}:${alarm.name}:${i}`"><strong>{{ alarm.status }}</strong> {{ alarm.name }} · {{ number(alarm.value) }} {{ alarm.units }}</p></div>
      <p v-else class="muted">快照中未返回关联告警；请同时留意采集和告警覆盖范围。</p>
      <ul v-if="preview.snapshot.notes.length" class="notes"><li v-for="(note, i) in preview.snapshot.notes" :key="i">{{ note }}</li></ul>
      <details class="payload"><summary>查看将发送的快照 JSON</summary><pre>{{ snapshotJSON }}</pre></details>
      <p class="muted">上述统计来自时间桶的均值，最小 / 最大值不代表原始瞬时峰值；首末差值只比较有效桶。</p>
      <p class="privacy">发送范围：此处所选图表的统计值、关联告警、节点名称和你的问题。不附加日志、系统配置或监控登录密钥。模型服务按其自身规则处理数据。</p>
    </section>

    <section class="step" aria-label="请求模型分析">
      <h3><AppIcon name="ai" />3. 请求模型分析</h3>
      <div class="prompts"><button type="button" :disabled="!!busy" @click="question = '请分析异常指标及可能原因，按优先级给出可验证的排障步骤，并指出不能确认的结论。'">异常排查</button><button type="button" :disabled="!!busy" @click="question = '请根据当前窗口评估资源使用和可能的容量瓶颈，区分已观测事实与推测；不要据此预测未来负载。'">容量观察</button><button type="button" :disabled="!!busy" @click="question = '请结合关联告警和指标统计说明需要优先核查的问题，指出告警关联证据和缺失信息。'">告警解读</button></div>
      <label>分析问题<textarea v-model="question" rows="3" maxlength="2000" :disabled="!!busy" placeholder="描述希望模型重点检查的问题" /></label><p class="muted counter">{{ question.length }} / 2000</p>
      <p v-if="!settings?.enabled || dirty" class="muted">请先保存有效的个人 AI 配置。未配置时仍可使用本地预览。</p>
      <p v-else-if="previewConsumed" class="muted">该快照已用于一次请求；取消、失败或再次分析时，请重新生成本地预览。</p>
      <p v-else-if="!preview || expired" class="muted">请先生成并核对有效的数据预览。</p>
      <div class="actions"><button class="primary" type="button" :disabled="!canAnalyze" @click="analyze"><AppIcon name="send" />{{ mode === 'resolution' ? '发送快照并生成方案' : '发送快照并分析' }}</button><span class="muted">仅点击时发送；可能产生模型服务费用</span></div>
      <p v-if="mode === 'resolution'" class="muted">方案包含可能原因、步骤、预期结果、风险和回退方法，生成后保存到当前账号的工作台。</p>
    </section>

    <section v-if="result && !result.resolution" class="step result" aria-label="AI 分析结果">
      <div class="step-title"><h3>模型建议</h3><div class="actions"><button type="button" @click="copyResult"><AppIcon name="copy" />复制结果</button><button type="button" @click="downloadResult"><AppIcon name="download" />下载结果</button></div></div>
      <p class="muted">{{ result.model }} · {{ (result.duration_ms / 1000).toFixed(1) }} 秒 · {{ when(result.generated_at) }}<span v-if="result.usage"> · Tokens {{ result.usage.total_tokens }}（输入 {{ result.usage.prompt_tokens }} / 输出 {{ result.usage.completion_tokens }}）</span></p>
      <p class="warning">AI 输出可能不准确，请结合上方监控事实核实。系统不会执行其中的命令或变更。</p>
      <p v-if="result.truncated" class="warning">结果因长度限制被截断，内容可能不完整。</p>
      <pre class="answer">{{ result.text }}</pre>
    </section>
    <AIResolutionPanel :node="node" :generated="result?.resolution || null" @expired="clearPrivateState" />
  </section>
</template>

<style scoped>
.ai-panel { min-width: 0; border: 1px solid #334155; border-radius: 12px; padding: 18px; margin-bottom: 18px; background: #0b1426; color: #e2e8f0; overflow-wrap: anywhere; }
.ai-head, .step-title, .activity { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.ai-head { align-items: flex-start; flex-wrap: nowrap; }
.ai-head > div { min-width: 0; }
h2 { margin: 0; font-size: 20px; } h2 small { display: block; margin-top: 3px; font-size: 12px; color: #94a3b8; font-weight: 400; }
h3 { margin: 0 0 10px; font-size: 15px; } h4 { margin: 0; font-size: 14px; } h4 small { color: #94a3b8; font-size: 12px; font-weight: 400; }
p { margin: 8px 0; } .muted, .ai-head p { color: #94a3b8; font-size: 13px; } .warning { color: #fde68a; font-size: 13px; } .error { color: #fecaca; border: 1px solid #7f1d1d; background: #451a1a; padding: 10px; border-radius: 6px; } .notice { color: #a7f3d0; }
.settings, .step { border-top: 1px solid #25334a; padding-top: 16px; margin-top: 16px; min-width: 0; }
summary { cursor: pointer; min-height: 44px; padding: 10px 0; font-weight: 600; } summary span { color: #94a3b8; font-size: 12px; margin-left: 8px; }
fieldset { border: 0; padding: 0; margin: 0; min-width: 0; }
.config-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; } .key-field { grid-column: 1 / -1; }
label { display: flex; flex-direction: column; gap: 6px; min-width: 0; font-size: 13px; }
input, select, textarea { min-width: 0; width: 100%; min-height: 44px; border: 1px solid #475569; border-radius: 7px; background: #101d31; color: #e2e8f0; padding: 9px 10px; font: inherit; }
textarea { resize: vertical; } input[type=checkbox] { width: 18px; height: 18px; min-height: 18px; flex: 0 0 18px; accent-color: #22c55e; }
.check { flex-direction: row; align-items: center; min-height: 44px; gap: 10px; }
button { min-height: 44px; border: 1px solid #475569; border-radius: 7px; padding: 8px 12px; color: #e2e8f0; background: #16243b; font: inherit; cursor: pointer; overflow-wrap: anywhere; }
.heading-icon, h3 { display: flex; align-items: center; gap: 7px; } button { display: inline-flex; justify-content: center; align-items: center; gap: 7px; } summary :deep(svg) { vertical-align: middle; margin-right: 3px; }
button:disabled { opacity: .5; cursor: not-allowed; } button.primary { border-color: #0d9488; background: #115e59; color: #ccfbf1; } button.danger { border-color: #b91c1c; color: #fecaca; } .close { flex: 0 0 44px; width: 44px; font-size: 22px; padding: 0; }
button[aria-pressed=true] { border-color:#2dd4bf; color:#ccfbf1; background:#115e59; }
button:focus-visible, input:focus-visible, select:focus-visible, textarea:focus-visible, summary:focus-visible { outline: 2px solid #5eead4; outline-offset: 2px; }
.actions, .prompts, .delete-confirm { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 10px; } .prompts { margin: 0 0 12px; } .delete-confirm { padding: 10px; border: 1px solid #7f1d1d; border-radius: 7px; }
.selection-controls { display: grid; grid-template-columns: minmax(0, 1fr) minmax(160px, 240px); gap: 12px; margin-bottom: 10px; }
.chart-options { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); max-height: 224px; overflow: auto; overscroll-behavior: contain; border: 1px solid #25334a; border-radius: 7px; }
.chart-option { min-height: 58px; padding: 10px; flex-direction: row; align-items: center; gap: 10px; border-bottom: 1px solid #1e293b; cursor: pointer; }
.chart-option span { min-width: 0; } .chart-option small { display: block; color: #94a3b8; font-size: 11px; margin-top: 3px; }
.scope { font-size: 13px; color: #cbd5e1; }.stat-card { border: 1px solid #334155; border-radius: 8px; padding: 12px; margin: 12px 0; background: #0f1b2d; }
.dimension { margin-top: 12px; } .dimension strong { font-size: 13px; } dl { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 8px; margin: 8px 0; } dt { color: #94a3b8; font-size: 11px; } dd { margin: 4px 0 0; font-variant-numeric: tabular-nums; }
.notes { padding-left: 20px; color: #cbd5e1; font-size: 13px; } .privacy { color: #a7f3d0; font-size: 13px; } .payload pre, .answer { white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; font: 13px/1.65 ui-monospace, SFMono-Regular, Menlo, monospace; background: #07101f; border: 1px solid #25334a; border-radius: 8px; padding: 12px; margin: 8px 0; } .payload pre { max-height: 360px; overflow: auto; } .counter { text-align: right; margin: 4px 0; }
@media (max-width: 760px) { .ai-panel { padding: 12px; } .config-grid, .selection-controls, .chart-options { grid-template-columns: minmax(0, 1fr); } .key-field { grid-column: auto; } .step-title { align-items: flex-start; gap: 4px; } .step-title h3 { margin-bottom: 4px; } .chart-options { max-height: 240px; } dl { grid-template-columns: repeat(2, minmax(0, 1fr)); } .actions { align-items: stretch; } .actions > button { flex: 1 1 auto; } .prompts { gap: 6px; } .prompts button { flex: 1 1 auto; } }
</style>
