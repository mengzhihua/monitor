<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { auth, get } from '../api'
import { handoverHTML, type ServiceHandover } from '../features/services/model/handover'
const props = defineProps<{ service: string; windowRange: string }>()
const emit = defineEmits<{ close: [] }>()
const report = ref<ServiceHandover | null>(null), error = ref(''), loading = ref(false)
let controller: AbortController | undefined
async function load() {
  controller?.abort(); const request = controller = new AbortController()
  report.value = null; error.value = ''; loading.value = true
  try {
    const value = await get<ServiceHandover>('/api/v1/services/report?' + new URLSearchParams({ service: props.service, window: props.windowRange }), request.signal)
    if (!request.signal.aborted) report.value = value
  } catch (e) { if (!request.signal.aborted) error.value = String(e) }
  finally { if (!request.signal.aborted) loading.value = false }
}
watch(() => [props.service, props.windowRange], load, { immediate: true })
const unsubscribe = auth.onChange(() => { controller?.abort(); report.value = null; emit('close') })
onBeforeUnmount(() => { controller?.abort(); unsubscribe() })
function download(format: 'html' | 'json') {
  if (!report.value) return
  const content = format === 'html' ? handoverHTML(report.value) : JSON.stringify(report.value, null, 2)
  const url = URL.createObjectURL(new Blob([content], { type: format === 'html' ? 'text/html;charset=utf-8' : 'application/json' }))
  const a = document.createElement('a'); a.href = url; a.download = `monitor-service-handover-${report.value.captured_at}.${format}`; a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
</script>
<template>
  <section class="handover" aria-label="服务交接报告预览">
    <h2>{{ service }} · 交接报告</h2>
    <p>范围为本机服务及递归依赖。核对报告与处理备注后再分享；导出文件只包含这份预览快照，不会在下载时重新采集。</p>
    <button :disabled="loading" @click="load">重新采集报告</button><button @click="emit('close')">关闭报告</button>
    <p v-if="loading" role="status">正在汇集服务证据…</p><p v-if="error" role="alert">{{ error }}</p>
    <template v-if="report">
      <p>读取时间：{{ new Date(report.captured_at * 1000).toLocaleString() }} · 服务 {{ report.report.services.length }} 个 · 告警 {{ report.alarms.length }} 个 · 处理记录 {{ report.handling_history.length }} / {{ report.history_matched }} 条</p>
      <p>统计区间：{{ new Date(report.report.from * 1000).toLocaleString() }} — {{ new Date(report.report.to * 1000).toLocaleString() }}</p>
      <ul><li v-for="note in report.notes" :key="note">{{ note }}</li></ul>
      <details><summary>查看报告全部内容（包括原文备注）</summary><pre>{{ JSON.stringify(report, null, 2) }}</pre></details>
      <button @click="download('html')">下载可打印 HTML</button><button @click="download('json')">下载交接 JSON</button>
    </template>
  </section>
</template>
<style scoped>
.handover { margin:20px 0; padding:18px; border:1px solid #5eead4; border-radius:12px; background:#0f172a; overflow-wrap:anywhere; }
h2 { margin-top:0; } p, li { line-height:1.65; } pre { max-height:350px; overflow:auto; white-space:pre-wrap; overflow-wrap:anywhere; }
button { color:inherit; font:inherit; background:#1e293b; border:1px solid #475569; border-radius:6px; padding:8px 12px; margin:4px; cursor:pointer; }button:disabled {opacity:.5;}
</style>
