<script setup lang="ts">
import { nextTick } from 'vue'
import { operationsSections, type OperationsSection } from './useOperationsSection'

const props = defineProps<{ selected: OperationsSection }>()
const emit = defineEmits<{ select: [value: OperationsSection] }>()
function navigate(event: KeyboardEvent) {
  const index = operationsSections.findIndex(item => item.id === props.selected)
  let next = index
  if (event.key === 'ArrowRight') next = (index + 1) % operationsSections.length
  else if (event.key === 'ArrowLeft') next = (index + operationsSections.length - 1) % operationsSections.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = operationsSections.length - 1
  else return
  event.preventDefault()
  const id = operationsSections[next]!.id
  emit('select', id)
  void nextTick(() => document.getElementById(`operations-tab-${props.selected}`)?.focus())
}
</script>

<template>
  <div class="operations-navigation" role="tablist" aria-label="运维分区" @keydown="navigate">
    <button v-for="item in operationsSections" :id="`operations-tab-${item.id}`" :key="item.id" type="button"
      role="tab" :aria-selected="selected === item.id" :tabindex="selected === item.id ? 0 : -1"
      aria-controls="operations-content" @click="emit('select', item.id)">{{ item.label }}</button>
  </div>
</template>

<style scoped>
.operations-navigation { display:flex; gap:6px; flex-wrap:wrap; margin:10px 0 8px; padding-bottom:7px; border-bottom:1px solid #27364c; }
button { font:inherit; font-size:13px; padding:10px 14px; background:transparent; border:1px solid transparent; color:#94a3b8; border-radius:7px; cursor:pointer; }
button:hover { color:#e2e8f0; background:#162238; }
button[aria-selected="true"] { color:#99f6e4; background:#113634; border-color:#23524c; font-weight:600; }
button:focus-visible { outline:2px solid #5eead4; outline-offset:2px; }
@media(max-width:760px) { .operations-navigation { display:grid; grid-template-columns:repeat(5,minmax(0,1fr)); gap:3px; } button { padding:9px 2px; font-size:12px; min-width:0; } }
</style>
