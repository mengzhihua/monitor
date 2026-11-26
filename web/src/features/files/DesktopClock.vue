<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const now = ref(new Date())
let timer: number
onMounted(() => {
  timer = window.setInterval(() => {
    now.value = new Date()
  }, 1000)
})
onBeforeUnmount(() => clearInterval(timer))
const time = computed(() => now.value.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }))
const date = computed(() =>
  now.value.toLocaleDateString('zh-CN', { month: 'long', day: 'numeric', weekday: 'long' }),
)
</script>
<template>
  <time class="desk-clock" :datetime="now.toISOString()" :aria-label="date + ' ' + time">
    <span class="face" aria-hidden="true">
      <span
        v-for="n in 12"
        :key="n"
        class="number"
        :style="{
          left: 50 + 39 * Math.sin((n * Math.PI) / 6) + '%',
          top: 50 - 39 * Math.cos((n * Math.PI) / 6) + '%',
        }"
      >
        {{ n }}
      </span>
      <i
        class="hour"
        :style="{ transform: 'rotate(' + (now.getHours() * 30 + now.getMinutes() / 2) + 'deg)' }"
      />
      <i
        class="minute"
        :style="{ transform: 'rotate(' + (now.getMinutes() * 6 + now.getSeconds() / 10) + 'deg)' }"
      />
      <i class="second" :style="{ transform: 'rotate(' + now.getSeconds() * 6 + 'deg)' }" />
    </span>
    <span>{{ date }}</span>
  </time>
</template>
<style scoped>
.desk-clock {
  display: grid;
  justify-items: center;
  gap: 12px;
  color: white;
  font:
    500 13px/1.3 system-ui,
    sans-serif;
  text-shadow: 0 1px 6px #132139;
}
.face {
  position: relative;
  display: block;
  width: clamp(98px, 12vw, 162px);
  aspect-ratio: 1;
  border-radius: 50%;
  background: radial-gradient(circle, #fff1, transparent 70%);
  box-shadow: 0 2px 32px #06122220;
}
.face:before {
  content: '';
  position: absolute;
  inset: 0;
  border-radius: 50%;
  background: repeating-conic-gradient(#fff9 0 0.8deg, transparent 0.8deg 6deg);
  mask: radial-gradient(transparent 67%, #000 68%);
}
.number {
  position: absolute;
  transform: translate(-50%, -50%);
  font-size: clamp(12px, 1.5vw, 20px);
  font-weight: 650;
}
i {
  position: absolute;
  bottom: 50%;
  left: calc(50% - 1.5px);
  width: 3px;
  border-radius: 4px;
  background: white;
  transform-origin: 50% 100%;
}
.hour {
  height: 24%;
}
.minute {
  height: 34%;
}
.second {
  height: 40%;
  width: 1px;
  left: 50%;
  background: #ffc158;
}
.face:after {
  content: '';
  position: absolute;
  inset: calc(50% - 3px);
  background: #ffc158;
  border-radius: 50%;
}
</style>
