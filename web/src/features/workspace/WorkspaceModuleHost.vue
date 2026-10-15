<script setup lang="ts">
import { onBeforeUnmount, onErrorCaptured, ref, shallowRef, watch, type Component } from 'vue'

defineOptions({ inheritAttrs: false })
defineProps<{ component: Component }>()
const child = shallowRef<{ canLeave?: () => boolean; showChart?: (id: string) => void } | null>(null)
const failed = ref(false)
let disposed = false
let pendingChart: string | null = null
function canLeave() { return child.value?.canLeave?.() !== false }
function showChart(id: string) {
  if (child.value?.showChart) child.value.showChart(id)
  else pendingChart = id
}
watch(child, value => {
  if (!disposed && value?.showChart && pendingChart !== null) {
    value.showChart(pendingChart)
    pendingChart = null
  }
}, { flush: 'post' })
onErrorCaptured(() => {
  if (!disposed) failed.value = true
  return false
})
onBeforeUnmount(() => { disposed = true; pendingChart = null })
const reload = () => location.reload()
defineExpose({ canLeave, showChart })
</script>

<template>
  <div v-if="failed" class="module-error" role="alert">当前模块加载失败，请刷新页面重试。 <button @click="reload">刷新页面</button></div>
  <component v-else :is="component" ref="child" v-bind="$attrs" />
</template>

<style scoped>
.module-error { background:#3a1e2a; border:1px solid #7f1d1d; color:#fecaca; padding:12px; border-radius:8px; font-size:13px; overflow-wrap:anywhere; }
button { color:#fecaca; border:1px solid #9f4c59; border-radius:6px; background:#421f2a; padding:6px 10px; font:inherit; cursor:pointer; }
</style>
