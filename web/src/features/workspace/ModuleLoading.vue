<script setup lang="ts">
// Module props/events must not become attributes of the temporary placeholder.
defineOptions({ inheritAttrs: false })
</script>

<template>
  <section class="module-loading" role="status" aria-live="polite" aria-label="正在加载工作区">
    <p>正在加载工作区…</p>
    <div class="skeleton" aria-hidden="true">
      <div class="skeleton-title" /><div class="skeleton-toolbar" />
      <div class="skeleton-grid"><div v-for="i in 4" :key="i" class="skeleton-card" /></div>
    </div>
  </section>
</template>

<style scoped>
.module-loading { min-height:360px; color:#94a3b8; font-size:13px; }
.module-loading p { margin:0 0 16px; }
.skeleton-title,.skeleton-toolbar,.skeleton-card { background:#111e30; border:1px solid #26354a; border-radius:10px; animation:breathe 1.6s ease-in-out infinite alternate; }
.skeleton-title { width:36%; height:28px; margin-bottom:18px; }
.skeleton-toolbar { height:66px; margin-bottom:20px; }
.skeleton-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:16px; }
.skeleton-card { height:170px; }
@keyframes breathe { to { opacity:.45; } }
@media(max-width:760px) { .skeleton-grid { grid-template-columns:minmax(0,1fr); } .skeleton-card:nth-child(n+3) { display:none; } }
@media(prefers-reduced-motion:reduce) { .skeleton-title,.skeleton-toolbar,.skeleton-card { animation:none; } }
</style>
