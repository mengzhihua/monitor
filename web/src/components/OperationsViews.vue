<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import type { OperationsView, OperationsViews } from '../api'

const props = defineProps<{ filters: Omit<OperationsView, 'name'> }>()
const emit = defineEmits<{ apply: [view: OperationsView] }>()
const collection = ref<OperationsViews | null>(null)
const name = ref('')
const busy = ref(false)
const message = ref('')
const error = ref('')
const loaded = ref(false)
const legacy = ref<OperationsView[]>([])
const controller = new AbortController()
let disposed = false
onBeforeUnmount(() => { disposed = true; controller.abort() })

// Old browser views remain local until explicitly imported into the signed-in
// account. Shared browsers must never silently upload another person's filters.
try {
  const entries = JSON.parse(localStorage.getItem('monitor.operations.views.v1') || '[]')
  if (Array.isArray(entries)) legacy.value = entries.filter(v => v && typeof v.name === 'string' && v.name.trim()
    && typeof v.query === 'string' && typeof v.pendingOnly === 'boolean'
    && ['all', 'WARNING', 'CRITICAL'].includes(v.severity) && ['all', 'live', 'stale', 'offline'].includes(v.nodeStatus))
    .slice(0, 10).map(v => ({ name: v.name, query: v.query, pendingOnly: v.pendingOnly, severity: v.severity, nodeStatus: v.nodeStatus,
      ownerFilter: ['all', 'mine', 'unassigned', 'assigned'].includes(v.ownerFilter) ? v.ownerFilter : 'all',
      progressFilter: ['all', 'open', 'investigating', 'watching'].includes(v.progressFilter) ? v.progressFilter : 'all' }))
} catch { /* Local storage is optional. */ }

function failure(e: unknown) {
  if (e instanceof ApiError) {
    if ([401, 403].includes(e.status)) { collection.value = null; loaded.value = false; return '无法访问个人视图，请重新登录有效账号。' }
    if (e.status === 400) return '视图格式不符合要求：名称最多 40 字，搜索词最多 1024 字节，最多 10 个视图。'
    if (e.status === 422) return '服务端视图存储已满，请联系管理员。'
    if (e.status === 503) return '服务端暂时无法保存视图，本次修改未保存，请稍后重试。'
  }
  return '视图请求失败，请检查连接并刷新视图；监控筛选仍可使用。'
}

async function read() {
  try {
    const next = await api.operationsViews(controller.signal)
    if (disposed) return false
    collection.value = next
    loaded.value = true
    return true
  } catch (e) {
    if (!disposed) { loaded.value = false; error.value = failure(e) }
    return false
  }
}
async function refresh() {
  if (busy.value) return
  busy.value = true; error.value = ''; message.value = ''
  await read()
  if (!disposed) busy.value = false
}
onMounted(refresh)

async function replace(views: OperationsView[], success: string) {
  if (busy.value || !loaded.value || !collection.value?.enabled) return false
  busy.value = true; error.value = ''; message.value = ''
  try {
    const next = await api.saveOperationsViews(collection.value.revision, views)
    if (disposed) return false
    collection.value = next
    message.value = next.persistent ? success : '本次视图仅保存在服务端内存，重启后会丢失。'
    return true
  } catch (e) {
    if (disposed) return false
    if (e instanceof ApiError && e.status === 409) {
      const refreshed = await read()
      if (!disposed && refreshed) error.value = '其他页面已修改视图，已读取最新列表。名称和筛选草稿已保留，请核对后再次保存或删除。'
    } else error.value = failure(e)
    return false
  } finally { if (!disposed) busy.value = false }
}
async function save() {
  const trimmed = name.value.trim()
  if (!trimmed || !collection.value) return
  const view = { name: trimmed, ...props.filters }
  const others = collection.value.views.filter(v => v.name !== trimmed)
  if (others.length >= collection.value.limit) { error.value = '已达到 10 个视图上限，请先删除一个视图。'; return }
  if (await replace([view, ...others], '视图已保存到当前账号，可在连接同一服务的其他设备使用。') && name.value.trim() === trimmed) name.value = ''
}
async function remove(view: OperationsView) {
  if (collection.value) await replace(collection.value.views.filter(v => v.name !== view.name), '视图已从当前账号删除。')
}
async function importView(view: OperationsView) {
  if (!collection.value) return
  if (collection.value.views.some(v => v.name === view.name)) { error.value = '账号中已有同名视图。请先应用浏览器视图，再用新名称保存。'; return }
  if (collection.value.views.length >= collection.value.limit) { error.value = '已达到 10 个视图上限，请先删除一个视图。'; return }
  await replace([view, ...collection.value.views], '浏览器视图已导入当前账号，本地原件仍保留。')
}
</script>

<template>
  <section class="personal-views" aria-label="个人视图">
    <div class="row">
      <strong>个人视图 <small v-if="collection?.enabled">{{ collection.user.name || '当前账号' }} · {{ collection.views.length }}/{{ collection.limit }}</small></strong>
      <button :disabled="busy" @click="refresh">刷新视图</button>
    </div>
    <p class="hint" v-if="collection?.enabled">保存当前搜索与筛选条件。同名保存会更新已有视图；跨设备使用需连接同一服务并登录同一账号。</p>
    <p class="hint" v-else-if="collection">匿名访问和临时分享链接不支持个人视图，请使用账号登录。已有浏览器视图仍可应用。</p>
    <p v-if="collection?.enabled && !collection.persistent" class="error">服务端未启用持久化，视图会在重启后丢失。</p>
    <form v-if="collection?.enabled" class="row" @submit.prevent="save">
      <input v-model="name" placeholder="视图名称" aria-label="视图名称" maxlength="40" />
      <button type="submit" :disabled="busy || !loaded || !name.trim()">保存视图</button>
    </form>
    <div class="saved-views">
      <span v-for="view in collection?.views || []" :key="view.name" class="view">
        <button @click="emit('apply', view)">{{ view.name }}</button>
        <button :aria-label="'删除视图 ' + view.name" :disabled="busy || !loaded" @click="remove(view)">×</button>
      </span>
    </div>
    <p v-if="busy" role="status">正在同步视图…</p>
    <p v-if="message" role="status">{{ message }}</p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <details v-if="legacy.length" class="legacy-views">
      <summary>本浏览器旧视图（{{ legacy.length }}）</summary>
      <p class="hint">旧视图尚未上传。先点击名称查看筛选，再选择是否导入当前账号。本地原件会保留。</p>
      <div class="saved-views">
        <span v-for="view in legacy" :key="view.name" class="view">
          <button @click="emit('apply', view)">{{ view.name }}</button>
          <button :disabled="busy || !loaded || !collection?.enabled" :aria-label="'导入视图 ' + view.name" @click="importView(view)">导入账号</button>
        </span>
      </div>
    </details>
  </section>
</template>

<style scoped>
.personal-views { background:#0c1526; border:1px solid #23334b; border-radius:10px; padding:14px; margin:10px 0 22px; }
.row,.saved-views,.view { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
.row:first-child { justify-content:space-between; }
small,.hint { color:#9cacbf; font-size:12px; font-weight:normal; }
.hint { line-height:1.6; }
.saved-views { margin-top:10px; }
.view { gap:2px; max-width:100%; }
button,input { background:#111d30; color:#dce6f2; border:1px solid #35455c; border-radius:6px; padding:7px 10px; font:inherit; max-width:100%; }
button { cursor:pointer; overflow-wrap:anywhere; }
button:hover { border-color:#6cb5ff; }
button:disabled { opacity:.5; cursor:default; }
input { width:180px; box-sizing:border-box; }
.error { color:#fba7a7; font-size:13px; }
.legacy-views { margin-top:12px; border-top:1px solid #23334b; padding-top:10px; }
summary { cursor:pointer; color:#9cacbf; font-size:13px; }
</style>
