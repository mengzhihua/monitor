<script setup lang="ts">
import { computed } from 'vue'
import { iconPaths, type IconName } from '../iconPaths'

const props = withDefaults(defineProps<{ name: string; size?: number | string }>(), { size: 18 })
const path = computed(() => Object.prototype.hasOwnProperty.call(iconPaths, props.name) ? iconPaths[props.name as IconName] : iconPaths.grid)
</script>

<template>
  <svg class="app-icon" :data-icon="name" :width="size" :height="size" viewBox="0 0 24 24" fill="none"
    stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <path :d="path" />
  </svg>
</template>

<style scoped>
.app-icon { display:inline-block; flex:none; vertical-align:-.15em; pointer-events:none; }
</style>
