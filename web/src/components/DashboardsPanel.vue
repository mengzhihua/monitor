<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Chart } from '../api'
import { dashboards, chartsForGroup } from '../dashboards'
import DashboardEditor from './DashboardEditor.vue'
import DashboardImport from './DashboardImport.vue'
import { asDashboard, decodeBoards, encodeBoards, newBoard, storageKey, validateBoards, type PersonalBoard } from '../dashboardConfig'
import { changePersonalBoards, readPersonalBoards, readPreferences, preferencesKey, type DashboardPreferences } from '../dashboardStorage'
import { decodeDraft, encodeDraft, draftKey, type DashboardDraft } from '../dashboardDraft'
import MetricChart from './MetricChart.vue'

const props = defineProps<{ charts: Chart[]; window: number; end?: number | null; filter: string; node: string }>()
const personal = ref<PersonalBoard[]>([])
const error = ref('')
const notice = ref('')
const importPreview = ref<PersonalBoard[] | null>(null)
const importFileName = ref('')
const importLoading = ref(false)
let importGeneration = 0
const savedDraft = ref<DashboardDraft | null>(null)
const draftStatus = ref('')
try { const raw = sessionStorage.getItem(draftKey); if (raw) savedDraft.value = decodeDraft(raw) }
catch { draftStatus.value = '已有草稿无法读取，未自动恢复。' }
try { personal.value = readPersonalBoards(localStorage) }
catch { error.value = '无法读取个人看板配置。内置样板仍可使用；原始配置已保留，不会自动覆盖。' }
const preferences = ref<DashboardPreferences>({ selected: 'developer', favorites: [], collapsed: false })
try { preferences.value = readPreferences(localStorage, [...dashboards, ...personal.value].map(b => b.id)) } catch { /* Browser storage may be unavailable. */ }
const selected = ref(preferences.value.selected)
const editing = ref<PersonalBoard | null>(null)
const deleting = ref(false)
const collapsed = ref(preferences.value.collapsed)
const onlyAvailable = ref(false)
const folded = ref<string[]>([])
let editingBase: PersonalBoard | null = null
let deletionBase: PersonalBoard | null = null
const allBoards = computed(() => [...dashboards, ...personal.value.map(asDashboard)])
const settings = computed(() => personal.value.find(b => b.id === selected.value))
const limit = computed(() => settings.value?.limit ?? 4)
const effectiveWindow = computed(() => settings.value?.windowSec || props.window)
function apply(change: Parameters<typeof changePersonalBoards>[1]): boolean {
  try { personal.value = changePersonalBoards(localStorage, change); error.value = ''; return true }
  catch (e) { error.value = `保存失败，原配置未改变：${e instanceof Error ? e.message : '浏览器存储不可用'}`; return false }
}
function remember() {
  const next = { ...preferences.value, selected: selected.value, collapsed: collapsed.value }
  try { localStorage.setItem(preferencesKey, JSON.stringify(next)); preferences.value = next }
  catch { error.value = '浏览器无法保存查看偏好，本次仍可使用。' }
}
function toggleFavorite() {
  const id = selected.value
  const favorites = preferences.value.favorites.includes(id) ? preferences.value.favorites.filter(x => x !== id) : [...preferences.value.favorites, id]
  try { localStorage.setItem(preferencesKey, JSON.stringify({ ...preferences.value, favorites })); preferences.value.favorites = favorites }
  catch { error.value = '收藏保存失败，浏览器存储不可用。' }
}
function startEditor(copy = false) {
  if (editing.value || savedDraft.value) return
  editingBase = null
  const fresh = newBoard()
  editing.value = copy ? { ...fresh, ...(settings.value || { title: board.value.title, description: board.value.description, groupIds: board.value.groups.map(g => g.id) }), id: fresh.id, title: `${board.value.title} 副本`.slice(0,100) } : fresh
  deleting.value = false; notice.value = ''; cacheDraft(editing.value)
}
function editCurrent() {
  if (!settings.value || editing.value || savedDraft.value) return
  editingBase = JSON.parse(JSON.stringify(settings.value))
  editing.value = { ...settings.value }; notice.value = ''; deleting.value = false; cacheDraft(editing.value)
}
function cacheDraft(value: PersonalBoard) {
  try {
    const raw = encodeDraft({ version: 1, board: value, expected: editingBase })
    sessionStorage.setItem(draftKey, raw)
    savedDraft.value = decodeDraft(raw); draftStatus.value = '草稿已暂存在当前标签页，刷新后可恢复。'
  } catch { draftStatus.value = '草稿暂存失败，请及时保存看板；离开页面可能丢失未保存内容。' }
}
function clearDraft() {
  try { sessionStorage.removeItem(draftKey); savedDraft.value = null; draftStatus.value = '' }
  catch { draftStatus.value = '无法清除标签页草稿，请检查浏览器存储。' }
}
function resumeDraft() {
  if (!savedDraft.value) return
  editingBase = savedDraft.value.expected
  editing.value = savedDraft.value.board
  draftStatus.value = '已恢复草稿；保存时仍会检查原看板是否被其他标签页修改。'
}
function cancelEditor() { editing.value = null; clearDraft() }
function saveBoard(value: PersonalBoard, copy = false) {
  try {
    const [clean] = validateBoards([{ ...value, ...(copy ? { id: newBoard().id } : {}) }])
    if (apply({ type: 'save', board: clean!, expected: copy ? null : editingBase })) {
      selected.value = clean!.id; editing.value = null; clearDraft(); category.value = '我的看板'; search.value = ''; onlyAvailable.value = false; notice.value = '已保存到当前浏览器。'
    }
  } catch (e) { error.value = e instanceof Error ? e.message : '配置无效。' }
}
function requestDelete() {
  deletionBase = settings.value ? JSON.parse(JSON.stringify(settings.value)) : null
  deleting.value = !deleting.value
}
function removeBoard() {
  if (deletionBase && apply({ type: 'delete', expected: deletionBase })) { selected.value = 'developer'; deleting.value = false; resetCatalog(); notice.value = '个人看板已删除。' }
}
function syncStorage(event: StorageEvent) {
  if (event.key !== storageKey && event.key !== null) return
  try {
    personal.value = readPersonalBoards(localStorage)
    if (!allBoards.value.some(b => b.id === selected.value)) selected.value = 'developer'
    notice.value = editing.value ? '其他标签页更新了配置；当前草稿保留，保存时会检查冲突。' : '已同步其他标签页的看板配置。'
  } catch (e) { error.value = e instanceof Error ? e.message : '读取配置失败。' }
}
onMounted(() => window.addEventListener('storage', syncStorage))
onBeforeUnmount(() => { window.removeEventListener('storage', syncStorage); ++importGeneration })
function jumpToGroup(id: string) {
  folded.value = folded.value.filter(item => item !== id)
  // Wait for the collapsed section to mount before moving keyboard focus.
  requestAnimationFrame(() => {
    const section = document.getElementById(`board-group-${id}`)
    section?.scrollIntoView({ block: 'start' }); section?.focus({ preventScroll: true })
  })
}
function exportBoards() {
  const url = URL.createObjectURL(new Blob([encodeBoards(personal.value)], { type: 'application/json' }))
  const link = document.createElement('a'); link.href = url; link.download = 'monitor-dashboards.json'; link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
async function importBoards(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  const generation = ++importGeneration
  importPreview.value = null; importLoading.value = true; error.value = ''; notice.value = ''
  try {
    if (file.size > 500_000) throw new Error('配置文件不能超过 500 KB。')
    const text = await file.text()
    if (generation !== importGeneration) return
    const imported = decodeBoards(text)
    if (!imported.length) throw new Error('配置文件中没有看板。')
    importFileName.value = file.name; importPreview.value = imported
  } catch (e) { if (generation === importGeneration) error.value = `导入失败：${e instanceof Error ? e.message : '配置无效'}` }
  finally { if (generation === importGeneration) { importLoading.value = false; input.value = '' } }
}
function confirmImport(boards: PersonalBoard[]) {
  if (!boards.length) return
  const imported = boards.map(b => ({ ...b, id: newBoard().id }))
  if (apply({ type: 'import', boards: imported })) {
    importPreview.value = null; onlyAvailable.value = false; category.value = '我的看板'; search.value = ''; selected.value = imported[0]!.id; notice.value = `已导入 ${imported.length} 个副本。`
  }
}
const expanded = ref<string[]>([])
const search = ref('')
const category = ref('全部')
const categories = ['全部', '收藏', '我的看板', ...new Set(dashboards.map(b => b.category))]
const catalog = computed(() => {
  const query = search.value.trim().toLowerCase()
  return allBoards.value.filter(b => (category.value === '全部' || category.value === b.category || category.value === '收藏' && preferences.value.favorites.includes(b.id))
    && (!onlyAvailable.value || primaryAvailable.value[b.id])
    && `${b.title} ${b.description} ${b.groups.map(g => g.title).join(' ')}`.toLowerCase().includes(query))
})
const matchesByGroup = computed(() => {
  const uniqueGroups = new Map(allBoards.value.flatMap(b => b.groups).map(g => [g.id, g]))
  return new Map([...uniqueGroups.values()].map(g => [g.id, chartsForGroup(props.charts, g)]))
})
const coverage = computed(() => Object.fromEntries(allBoards.value.map(b => [b.id,
  b.groups.filter(g => matchesByGroup.value.get(g.id)?.length).length,
])))
const primaryAvailable = computed(() => Object.fromEntries(allBoards.value.map(b => [b.id,
  b.groups[0] ? !!matchesByGroup.value.get(b.groups[0].id)?.length : false,
])))
function resetCatalog() { search.value = ''; category.value = '全部'; onlyAvailable.value = false }
const board = computed(() => allBoards.value.find(b => b.id === selected.value) ?? dashboards[0]!)
const groups = computed(() => board.value.groups.map(group => {
  const matched = matchesByGroup.value.get(group.id) ?? []
  const query = props.filter.trim().toLowerCase()
  const filtered = matched.filter(c => !query || `${c.id} ${c.title} ${c.family}`.toLowerCase().includes(query))
  return { ...group, matched, filtered, visible: expanded.value.includes(group.id) ? filtered : filtered.slice(0, limit.value) }
}))
const visibleGroups = computed(() => groups.value.filter(g => !settings.value?.hideEmpty || g.matched.length))
const count = computed(() => new Set(groups.value.flatMap(g => g.matched.map(c => c.id))).size)
const available = computed(() => groups.value.filter(g => g.matched.length).length)
watch([selected, collapsed], remember)
watch(selected, () => { deleting.value = false; folded.value = [] })
watch([selected, () => props.node, () => props.filter], () => { expanded.value = [] })
</script>

<template>
  <div class="dashboards" aria-label="常用聚合看板">
    <div class="intro"><div><span class="eyebrow">开箱即用 · 自动匹配当前节点</span><h1>常用聚合看板</h1></div><span class="badge">{{ dashboards.length }} 个内置样板</span></div>
    <p class="muted">无需选指标或编写查询。样板直接使用当前节点已有采集数据，切换节点后自动更新。</p>
    <div class="toolbar">
      <button :disabled="!!editing || !!savedDraft || !!importPreview || importLoading" @click="startEditor()">新建看板</button><button :disabled="!!editing || !!savedDraft || !!importPreview || importLoading" @click="startEditor(true)">复制为个人看板</button>
      <button :disabled="!personal.length" @click="exportBoards">导出个人看板</button>
      <label class="import">导入为副本<input :disabled="!!editing" type="file" accept=".json,application/json" aria-label="导入看板配置" @change="importBoards" /></label>
      <button :aria-expanded="!collapsed" @click="collapsed = !collapsed">{{ collapsed ? '展开样板目录' : '收起样板目录' }}</button>
    </div>
    <p class="muted">个人配置仅保存在当前浏览器；可导出 JSON 备份或迁移，不包含监控数据与认证令牌。</p>
    <p v-if="error && !editing" role="alert">{{ error }}</p><p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="importLoading" role="status">正在读取看板配置…</p>
    <DashboardImport v-if="importPreview" :key="importGeneration" :boards="importPreview" :file-name="importFileName" :existing="personal" :charts="charts" @confirm="confirmImport" @cancel="importPreview = null; error = ''" />
    <aside v-if="savedDraft && !editing" class="draft-recovery" aria-label="未保存的看板草稿">
      <b>发现未保存草稿：{{ savedDraft.board.title || '未命名看板' }}</b>
      <p>草稿只属于当前标签页，恢复后仍需保存才能加入个人看板。</p>
      <button :disabled="!!importPreview || importLoading" @click="resumeDraft">恢复草稿</button><button @click="clearDraft">丢弃草稿</button>
    </aside>
    <p v-if="draftStatus && !editing && !savedDraft" class="muted">{{ draftStatus }}</p>
    <DashboardEditor v-if="editing" :key="editing.id" :initial="editing" :charts="charts" :error="error" :draft-status="draftStatus" @change="cacheDraft" @save="saveBoard" @save-copy="value => saveBoard(value, true)" @cancel="cancelEditor" />
    <div v-show="!collapsed" class="catalog-tools">
      <input v-model="search" type="search" aria-label="搜索看板样板" placeholder="搜索样板，例如 Redis、Java、DNS…" />
      <div class="categories" aria-label="看板分类">
        <button v-for="item in categories" :key="item" :aria-pressed="category === item" @click="category = item">{{ item }}</button>
      </div>
      <label class="availability-filter"><input v-model="onlyAvailable" type="checkbox" />仅看主分组已采集的样板</label>
      <span class="muted">{{ catalog.length }} 个匹配样板 · 当前查看：{{ board.title }}</span>
    </div>
    <div v-if="!collapsed && !catalog.length" class="missing">没有匹配的样板。<button class="reset" @click="resetCatalog">清除样板筛选</button></div>
    <div v-show="!collapsed" class="presets" aria-label="选择看板">
      <button v-for="item in catalog" :key="item.id" :aria-pressed="selected === item.id" @click="selected = item.id">
        <b>{{ preferences.favorites.includes(item.id) ? '★ ' : '' }}{{ item.title }}</b><span>{{ item.description }}</span><small>{{ item.category }} · {{ coverage[item.id] }}/{{ item.groups.length }} 类指标已采集</small><small v-if="!primaryAvailable[item.id]">主分组未采集</small>
      </button>
    </div>
    <div class="board-heading"><h2>{{ board.title }}</h2><span>{{ available }}/{{ groups.length }} 类指标已采集 · {{ count }} 张图表</span></div>
    <div class="toolbar"><button :aria-pressed="preferences.favorites.includes(selected)" @click="toggleFavorite">{{ preferences.favorites.includes(selected) ? '取消收藏' : '收藏当前看板' }}</button></div>
    <div v-if="settings" class="toolbar">
      <button :disabled="!!editing || !!savedDraft || !!importPreview || importLoading" @click="editCurrent">编辑个人看板</button><button :disabled="!!editing" @click="requestDelete">删除个人看板</button>
      <span class="muted">{{ settings.windowSec ? `独立时间范围：${settings.windowSec / 60} 分钟` : '跟随顶部时间范围' }} · {{ settings.columns || '自适应' }} 列 · 每组 {{ limit }} 张</span>
      <span v-if="deleting">确认删除“{{ board.title }}”？<button @click="removeBoard">确认删除</button><button @click="deleting = false">取消删除</button></span>
    </div>
    <p v-if="settings?.hideEmpty" class="muted">已隐藏 {{ groups.length - visibleGroups.length }} 个未采集分组；隐藏不代表运行正常。</p>
    <p class="muted">{{ board.description }} 各图保留原始单位；不同指标不混算。</p>
    <aside v-if="board.checklist" class="runbook" aria-label="建议排查顺序">
      <b>建议排查顺序</b>
      <ol><li v-for="step in board.checklist" :key="step">{{ step }}</li></ol>
    </aside>
    <nav v-if="visibleGroups.length" class="group-navigation" aria-label="看板分组导航">
      <button v-for="group in visibleGroups" :key="group.id" @click="jumpToGroup(group.id)">{{ group.title }} · {{ group.filtered.length }}</button>
    </nav>
    <section v-for="group in visibleGroups" :key="group.id" :id="`board-group-${group.id}`" class="board-group" tabindex="-1" :aria-label="group.title">
      <div class="group-heading"><h3>{{ group.title }}</h3><span>{{ group.filtered.length }} 张图表</span><button class="fold" :aria-expanded="!folded.includes(group.id)" :aria-label="`${folded.includes(group.id) ? '展开' : '折叠'}分组 ${group.title}`" @click="folded = folded.includes(group.id) ? folded.filter(id => id !== group.id) : [...folded, group.id]">{{ folded.includes(group.id) ? '展开分组' : '折叠分组' }}</button></div>
      <template v-if="!folded.includes(group.id)">
      <p class="muted">{{ group.hint }}</p>
      <div v-if="!group.matched.length" class="missing">当前节点尚未采集这类指标。已有采集数据接入后会自动展示，无需配置看板。</div>
      <div v-else-if="!group.filtered.length" class="missing">没有匹配当前筛选条件的图表，请清空或修改顶部筛选。</div>
      <div v-else class="board-grid" :class="settings?.columns ? `columns-${settings.columns}` : ''">
        <MetricChart v-for="chart in group.visible" :key="node + ':' + chart.id" :chart="chart" :window="effectiveWindow" :end="end" />
      </div>
      <button v-if="group.filtered.length > limit" class="more" @click="expanded = expanded.includes(group.id) ? expanded.filter(id => id !== group.id) : [...expanded, group.id]">
        {{ expanded.includes(group.id) ? '收起' : `展开其余 ${group.filtered.length - limit} 张图表` }}
      </button>
      </template>
    </section>
  </div>
</template>

<style scoped>
.draft-recovery { border:1px solid #0d9488; background:#102d30; padding:16px; border-radius:10px; margin:16px 0; } .draft-recovery p { font-size:13px; color:#cbd5e1; } .draft-recovery button { padding:8px 12px; margin-right:10px; }
.availability-filter { display:flex; align-items:center; gap:6px; font-size:13px; color:#cbd5e1; } .catalog-tools .availability-filter input { width:auto; }
.group-navigation { display:flex; flex-wrap:wrap; gap:8px; margin-top:16px; } .group-navigation button, .fold { padding:6px 10px; font-size:12px; } .board-group { scroll-margin-top:130px; }
.toolbar { display:flex; gap:10px; flex-wrap:wrap; align-items:center; margin:14px 0; }
.toolbar button, .import { padding:8px 12px; } .import { border:1px solid #334155; border-radius:8px; max-width:100%; box-sizing:border-box; } .import input { display:block; max-width:100%; margin-top:6px; }
[role=alert] { color:#fda4af; overflow-wrap:anywhere; } [role=status] { color:#5eead4; } button:disabled { opacity:.4; cursor:default; }
.board-grid.columns-1 { grid-template-columns:minmax(0,1fr); }
@media(min-width:900px) { .board-grid.columns-2 { grid-template-columns:repeat(2,minmax(0,1fr)); } .board-grid.columns-3 { grid-template-columns:repeat(3,minmax(0,1fr)); } }
.dashboards { overflow-wrap:anywhere; max-width: 1800px; margin: 0 auto; }
.intro, .board-heading, .group-heading { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
h1 { font-size:24px; margin:6px 0; } h2 { font-size:20px; margin:0; } h3 { font-size:16px; margin:0; }
.eyebrow { color:#5eead4; font-size:12px; } .badge { background:#134e4a; color:#99f6e4; border-radius:20px; padding:5px 12px; }
.muted, .board-heading span, .group-heading span { color:#94a3b8; font-size:13px; }
.presets { max-height:420px; overflow:auto; padding:4px; display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:10px; margin:20px 0 28px; }
.catalog-tools { display:flex; flex-wrap:wrap; align-items:center; gap:12px; margin-top:18px; }
.catalog-tools input { width:100%; max-width:380px; min-width:0; background:#0f172a; color:#e2e8f0; border:1px solid #334155; border-radius:8px; padding:10px; font:inherit; }
.categories { display:flex; flex-wrap:wrap; gap:6px; }
.categories button, .reset { padding:7px 10px; }
.categories button[aria-pressed=true] { color:#99f6e4; border-color:#2dd4bf; }
.presets small { color:#5eead4; font-size:11px; }
button { cursor:pointer; font:inherit; color:#e2e8f0; border:1px solid #334155; background:#0f172a; border-radius:10px; }
.presets button { text-align:left; padding:16px; display:flex; flex-direction:column; gap:8px; }
.presets button span { font-size:12px; color:#94a3b8; line-height:1.6; }
.presets button[aria-pressed=true] { border-color:#2dd4bf; background:#102d32; }
button:focus-visible { outline:2px solid #5eead4; outline-offset:3px; }
.runbook { background:#0f172a; border-left:3px solid #2dd4bf; border-radius:6px; padding:14px 18px; font-size:13px; }
.runbook ol { margin:8px 0 0; padding-left:20px; color:#cbd5e1; }
.runbook li + li { margin-top:6px; }
.board-group { margin:20px 0; padding-top:16px; border-top:1px solid #1e293b; }
.board-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(min(420px,100%),1fr)); gap:12px; }
.missing { padding:18px; border:1px dashed #334155; border-radius:8px; color:#94a3b8; font-size:13px; }
.more { padding:7px 14px; margin-top:12px; }
@media(max-width:900px) { .presets { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media(max-width:480px) { .presets button { padding:12px; } h1 { font-size:21px; } }
</style>
