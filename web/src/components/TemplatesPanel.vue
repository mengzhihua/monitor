<script setup lang="ts">
import { onMounted, ref } from 'vue'
import type { HealthOverlay, NodeInfo, NodeInventoryView, Room, Space, Template } from '../api'
import { api } from '../api'

const props = defineProps<{ role: string; nodes: NodeInfo[] }>()
const emit = defineEmits<{ close: [] }>()
const canManage = props.role === 'admin'

const tab = ref<'templates' | 'inventory'>('templates')
const error = ref('')
const list = ref<Template[]>([])
const spaces = ref<Space[]>([])
const rooms = ref<Room[]>([])

// editor state
const editing = ref<Template | null>(null)
const draft = ref<Template | null>(null)
const rulesYaml = ref('')
const saving = ref(false)

// effective preview
const previewNode = ref('')
const preview = ref<HealthOverlay | null>(null)

// inventory
const inventory = ref<Record<string, NodeInventoryView>>({})
const invNode = ref('')
const invDraft = ref<Record<string, string>>({})
const invView = ref<NodeInventoryView | null>(null)

async function load() {
  try {
    const [t, sp] = await Promise.all([api.templates(), api.spaces()])
    list.value = t.templates || []
    spaces.value = sp.spaces || []
    const rm = await api.rooms()
    rooms.value = rm.rooms || []
    error.value = ''
  } catch (e) { error.value = (e as Error).message }
}

function rulesToYaml(tpl: Template): string {
  if (!tpl.rules?.length) return ''
  const lines = ['alarms:']
  for (const rule of tpl.rules) {
    const r = rule as unknown as Record<string, unknown>
    const keys = Object.keys(r).filter((k) => r[k] !== undefined && r[k] !== '' && k !== 'macros')
    if (!keys.length) continue
    const first = keys[0]!
    lines.push(`  - ${first}: ${yamlScalar(r[first])}`)
    for (const k of keys.slice(1)) lines.push(`    ${k}: ${yamlScalar(r[k])}`)
    if (r.macros && Object.keys(r.macros).length) {
      lines.push('    macros:')
      for (const [mk, mv] of Object.entries(r.macros)) lines.push(`      ${mk}: ${yamlScalar(mv)}`)
    }
  }
  return lines.join('\n')
}
function yamlScalar(v: unknown): string {
  const s = String(v)
  return s.startsWith('$') || s.includes(':') || s.includes('#') ? JSON.stringify(s) : s
}

function edit(tpl?: Template) {
  editing.value = tpl || null
  draft.value = tpl
    ? JSON.parse(JSON.stringify(tpl))
    : { name: '', description: '', macros: {}, tags: {}, assign: { rooms: [], nodes: [], labels: {} }, removed: [], disabled: [] }
  rulesYaml.value = tpl ? rulesToYaml(tpl) : ''
}

async function saveWithYaml() {
  const t = draft.value
  if (!t?.name) { error.value = '模板名称必填'; return }
  saving.value = true
  try {
    const body: Record<string, unknown> = { ...t }
    delete body.updated
    if (rulesYaml.value.trim()) body.rules_yaml = rulesYaml.value
    body.if_updated = t.updated
    await api.putTemplateRaw(body, t.id)
    error.value = ''
    editing.value = null
    await load()
  } catch (e) { error.value = (e as Error).message } finally { saving.value = false }
}
async function del(tpl: Template) {
  if (!tpl.id || !window.confirm(`删除模板 ${tpl.name}？`)) return
  try { await api.deleteTemplate(tpl.id); await load() } catch (e) { error.value = (e as Error).message }
}

function kvEntries(rec: Record<string, string> | undefined) { return Object.entries(rec || {}) }
function setKV(rec: Record<string, string>, oldKey: string, k: string, v: string) {
  if (oldKey !== k) delete rec[oldKey]
  if (k) rec[k] = v
}
function addKV(rec: Record<string, string>) { rec['key' + (Object.keys(rec).length + 1)] = '' }
function delKV(rec: Record<string, string>, k: string) { delete rec[k] }

function toggleAssign(which: 'rooms' | 'nodes', v: string) {
  const a = (draft.value!.assign ||= {})
  const arr = (a[which] ||= [])
  const i = arr.indexOf(v)
  if (i >= 0) arr.splice(i, 1); else arr.push(v)
}
function toggleList(key: 'removed' | 'disabled', v: string) {
  const arr = (draft.value![key] ||= [])
  const i = arr.indexOf(v)
  if (i >= 0) arr.splice(i, 1); else arr.push(v)
}

async function runPreview() {
  if (!previewNode.value) return
  try { preview.value = await api.templateEffective(previewNode.value) } catch (e) { error.value = (e as Error).message }
}

async function loadInventory() {
  try { inventory.value = (await api.inventory()).inventory || {} } catch (e) { error.value = (e as Error).message }
}
async function editInv(nodeID: string) {
  invNode.value = nodeID
  try {
    invView.value = await api.nodeInventory(nodeID)
    invDraft.value = { ...(invView.value.manual || {}) }
  } catch (e) { error.value = (e as Error).message }
}
async function saveInv() {
  try {
    invView.value = await api.putInventory(invNode.value, invDraft.value, invView.value?.updated)
    invDraft.value = { ...(invView.value.manual || {}) }
    await loadInventory()
  } catch (e) { error.value = (e as Error).message }
}

onMounted(() => { void load(); void loadInventory() })
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>模板与资产 <small>Zabbix templates / host groups / inventory</small></h3>
      <div class="tabs">
        <button :class="{ on: tab === 'templates' }" @click="tab = 'templates'">模板</button>
        <button :class="{ on: tab === 'inventory' }" @click="tab = 'inventory'">资产清单</button>
      </div>
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <div v-if="error" class="err">{{ error }}</div>

    <template v-if="tab === 'templates'">
      <div class="row">
        <button v-if="canManage" @click="edit()">新建模板</button>
        <select v-model="previewNode" @change="runPreview">
          <option value="">节点有效配置预览…</option>
          <option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.hostname || n.id }}</option>
        </select>
      </div>
      <div v-if="preview" class="preview">
        <div class="nav-title">有效 overlay（rev {{ preview.rev }}）— {{ previewNode }}</div>
        <div class="dim">templates: {{ (preview.templates || []).join(', ') || '无' }}</div>
        <div class="dim">macros: {{ kvEntries(preview.macros).map(([k, v]) => `${k}=${v}`).join(', ') || '无' }}</div>
        <div class="dim">rules: {{ (preview.rules || []).map((r) => r.name || r.on).join(', ') || '无' }}</div>
        <div class="dim">removed: {{ (preview.removed || []).join(', ') || '无' }} · disabled: {{ (preview.disabled || []).join(', ') || '无' }}</div>
        <div class="dim">tags: {{ kvEntries(preview.tags).map(([k, v]) => `${k}=${v}`).join(', ') || '无' }}</div>
      </div>
      <table class="list">
        <thead><tr><th>名称</th><th>描述</th><th>规则</th><th>分配</th><th v-if="canManage"></th></tr></thead>
        <tbody>
          <tr v-for="t in list" :key="t.id">
            <td>{{ t.name }}</td>
            <td class="dim">{{ t.description }}</td>
            <td>{{ t.rules?.length || 0 }}</td>
            <td class="dim">
              {{ (t.assign?.rooms || []).length }} rooms · {{ (t.assign?.nodes || []).length }} nodes ·
              {{ kvEntries(t.assign?.labels).map(([k, v]) => `${k}=${v}`).join(',') || '—' }}
            </td>
            <td v-if="canManage" class="ops">
              <button @click="edit(t)">编辑</button>
              <button @click="del(t)">删除</button>
            </td>
          </tr>
          <tr v-if="!list.length"><td colspan="5" class="dim">暂无模板</td></tr>
        </tbody>
      </table>

      <div v-if="draft" class="editor">
        <div class="nav-title">{{ editing ? '编辑模板' : '新建模板' }}</div>
        <label>名称 <input v-model="draft.name" :disabled="!canManage" /></label>
        <label>描述 <input v-model="draft.description" :disabled="!canManage" /></label>

        <div class="nav-title">宏 {$NAME}</div>
        <div v-for="[k] in kvEntries(draft.macros)" :key="k" class="kv">
          <input :value="k" placeholder="{$ENV}" :disabled="!canManage"
            @change="setKV(draft!.macros!, k, ($event.target as HTMLInputElement).value, draft!.macros![k]!)" />
          <input :value="draft.macros![k]" :disabled="!canManage"
            @change="draft!.macros![k] = ($event.target as HTMLInputElement).value" />
          <button v-if="canManage" @click="delKV(draft!.macros!, k)">×</button>
        </div>
        <button v-if="canManage" class="mini" @click="addKV((draft!.macros ||= {}))">+ 宏</button>

        <div class="nav-title">告警规则（YAML，同告警规则编辑器）</div>
        <textarea v-model="rulesYaml" rows="8" placeholder="alarms:&#10;  - on: system.cpu&#10;    calc: $this &gt; {$CPU_MAX}&#10;    warn: $this &gt; {$CPU_MAX}" :disabled="!canManage"></textarea>

        <div class="nav-title">禁用基准规则（removed）/ 采集器（disabled）</div>
        <div class="row">
          <input placeholder="规则名，回车加入" :disabled="!canManage" @keydown.enter.prevent="toggleList('removed', ($event.target as HTMLInputElement).value); ($event.target as HTMLInputElement).value = ''" />
          <span v-for="r in draft.removed" :key="r" class="pill">{{ r }} <button v-if="canManage" @click="toggleList('removed', r)">×</button></span>
        </div>
        <div class="row">
          <input placeholder="采集器名，回车加入" :disabled="!canManage" @keydown.enter.prevent="toggleList('disabled', ($event.target as HTMLInputElement).value); ($event.target as HTMLInputElement).value = ''" />
          <span v-for="c in draft.disabled" :key="c" class="pill">{{ c }} <button v-if="canManage" @click="toggleList('disabled', c)">×</button></span>
        </div>

        <div class="nav-title">资产标签</div>
        <div v-for="[k] in kvEntries(draft.tags)" :key="k" class="kv">
          <input :value="k" :disabled="!canManage" @change="setKV(draft!.tags!, k, ($event.target as HTMLInputElement).value, draft!.tags![k]!)" />
          <input :value="draft.tags![k]" :disabled="!canManage" @change="draft!.tags![k] = ($event.target as HTMLInputElement).value" />
          <button v-if="canManage" @click="delKV(draft!.tags!, k)">×</button>
        </div>
        <button v-if="canManage" class="mini" @click="addKV((draft!.tags ||= {}))">+ 标签</button>

        <div class="nav-title">分配（任一 room / 节点 / 全部标签匹配）</div>
        <div class="assign">
          <div>
            <div class="dim">Rooms</div>
            <label v-for="r in rooms" :key="r.id"><input type="checkbox" :checked="draft.assign?.rooms?.includes(r.id)" :disabled="!canManage" @change="toggleAssign('rooms', r.id)" /> {{ r.name }}</label>
          </div>
          <div>
            <div class="dim">Nodes</div>
            <label v-for="n in nodes" :key="n.id"><input type="checkbox" :checked="draft.assign?.nodes?.includes(n.id)" :disabled="!canManage" @change="toggleAssign('nodes', n.id)" /> {{ n.hostname || n.id }}</label>
          </div>
          <div>
            <div class="dim">Labels（全部相等）</div>
            <div v-for="[k] in kvEntries(draft.assign?.labels)" :key="k" class="kv">
              <input :value="k" :disabled="!canManage" @change="setKV(draft!.assign!.labels!, k, ($event.target as HTMLInputElement).value, draft!.assign!.labels![k]!)" />
              <input :value="draft.assign!.labels![k]" :disabled="!canManage" @change="draft!.assign!.labels![k] = ($event.target as HTMLInputElement).value" />
              <button v-if="canManage" @click="delKV(draft!.assign!.labels!, k)">×</button>
            </div>
            <button v-if="canManage" class="mini" @click="addKV((draft!.assign!.labels ||= {}))">+ 标签匹配</button>
          </div>
        </div>

        <div class="row">
          <button v-if="canManage" :disabled="saving" @click="saveWithYaml">{{ saving ? '保存中…' : '保存' }}</button>
          <button @click="draft = null">取消</button>
        </div>
      </div>
    </template>

    <template v-else>
      <table class="list">
        <thead><tr><th>节点</th><th>自动字段</th><th>模板标签</th><th>手工字段</th><th></th></tr></thead>
        <tbody>
          <tr v-for="(v, id) in inventory" :key="id">
            <td>{{ v.auto?.hostname || id }}</td>
            <td class="dim">{{ kvEntries(v.auto).map(([k, x]) => `${k}=${x}`).join(' ') }}</td>
            <td class="dim">{{ kvEntries(v.tags).map(([k, x]) => `${k}=${x}`).join(' ') || '—' }}</td>
            <td class="dim">{{ kvEntries(v.manual).map(([k, x]) => `${k}=${x}`).join(' ') || '—' }}</td>
            <td class="ops"><button @click="editInv(id)">编辑</button></td>
          </tr>
          <tr v-if="!Object.keys(inventory).length"><td colspan="5" class="dim">暂无节点</td></tr>
        </tbody>
      </table>
      <div v-if="invView" class="editor">
        <div class="nav-title">资产字段 — {{ invView.auto?.hostname || invNode }}（手工覆盖模板/自动）</div>
        <div v-for="[k] in kvEntries(invDraft)" :key="k" class="kv">
          <input :value="k" @change="setKV(invDraft, k, ($event.target as HTMLInputElement).value, invDraft[k]!)" />
          <input :value="invDraft[k]" @change="invDraft[k] = ($event.target as HTMLInputElement).value" />
          <button @click="delKV(invDraft, k)">×</button>
        </div>
        <button class="mini" @click="addKV(invDraft)">+ 字段</button>
        <div class="row"><button @click="saveInv">保存</button><button @click="invView = null">取消</button></div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.panel { position: fixed; inset: 60px 12px 12px auto; width: min(720px, 96vw); background: #0f172a; border: 1px solid #1e293b; border-radius: 8px; padding: 12px; overflow: auto; z-index: 40; color: #e2e8f0; }
.head { display: flex; align-items: center; gap: 10px; }
.head h3 { margin: 0; flex: none; }
.head small { color: #64748b; }
.tabs { display: flex; gap: 4px; flex: 1; }
.tabs button { background: #1e293b; color: #cbd5e1; border: 0; border-radius: 4px; padding: 4px 10px; }
.tabs button.on { background: #334155; }
.x { margin-left: auto; }
.err { color: #f87171; margin: 8px 0; }
.row { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin: 8px 0; }
.list { width: 100%; border-collapse: collapse; font-size: 13px; }
.list td, .list th { text-align: left; padding: 4px 8px; border-bottom: 1px solid #1e293b; }
.dim { color: #64748b; font-size: 12px; }
.nav-title { font-size: 11px; text-transform: uppercase; color: #64748b; margin-top: 12px; }
.kv { display: flex; gap: 6px; margin: 4px 0; }
.kv input { flex: 1; }
input, select, textarea { background: #1e293b; border: 1px solid #334155; color: #e2e8f0; border-radius: 4px; padding: 4px 8px; font-size: 13px; }
textarea { width: 100%; font-family: monospace; }
button { background: #1e293b; color: #cbd5e1; border: 0; border-radius: 4px; padding: 4px 10px; cursor: pointer; }
button:hover { background: #334155; }
button.mini { font-size: 12px; padding: 2px 8px; }
.pill { background: #1e293b; border-radius: 10px; padding: 2px 8px; font-size: 12px; display: inline-flex; gap: 4px; align-items: center; }
.assign { display: flex; gap: 16px; flex-wrap: wrap; }
.assign label { display: block; font-size: 13px; }
.preview { background: #1e293b; border-radius: 6px; padding: 8px; margin: 8px 0; }
.editor { border-top: 1px solid #1e293b; margin-top: 12px; padding-top: 8px; }
.editor label { display: block; margin: 6px 0; font-size: 13px; }
.editor label input { width: 100%; box-sizing: border-box; }
@media (max-width: 700px) { .panel { inset: 49px 0 0 0; width: auto; border-radius: 0; } }
</style>
