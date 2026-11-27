<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import FileDesktop from './FileDesktop.vue'
import AppIcon from '../../components/AppIcon.vue'
import { hubApi } from '../../api/hub/client'
import type { NodeInfo } from '../../api/hub/types'
import { send } from '../../api/transport'

defineEmits<{ close: [] }>()
type Entry = { name: string; kind: 'folder' | 'file' | 'other'; size: number; modified: number }
type Place = { node: NodeInfo | null; path: string[] }
const places = ref<Place[]>([{ node: null, path: [] }])
const index = ref(0)
const place = computed(() => places.value[index.value]!)
const nodes = ref<NodeInfo[]>([])
const recent = ref<{ nodeID: string; path: string[] }[]>([])
const inventoryError = ref('')
const loadingNodes = ref(false)
let inventoryRequest: AbortController | undefined
let inventoryTimer: number

async function loadNodes() {
  inventoryRequest?.abort()
  const request = new AbortController()
  inventoryRequest = request
  loadingNodes.value = true
  inventoryError.value = ''
  try {
    const result = await hubApi.nodes(request.signal)
    if (!request.signal.aborted) nodes.value = result.nodes
  } catch (reason) {
    if (!request.signal.aborted) inventoryError.value = (reason as Error).message
  } finally {
    if (!request.signal.aborted) loadingNodes.value = false
  }
}

function rememberDirectory(nodeID: string, path: string[]) {
  const existing = recent.value.filter(r => r.nodeID !== nodeID || r.path.join('/') !== path.join('/'))
  recent.value = [{ nodeID, path: [...path] }, ...existing].slice(0, 20)
}
const files = ref<Entry[]>([])
const picked = ref<string[]>([])
const filter = ref('')
const layout = ref<'grid' | 'list'>(
  sessionStorage.getItem('monitor.files.layout') === 'list' ? 'list' : 'grid',
)
const details = ref(false)
const error = ref('')
const busy = ref(false)
const uploader = ref<HTMLInputElement>()
const dialog = ref<HTMLDialogElement>()
const windowDialog = ref<HTMLDialogElement>()
const mode = ref<'mkdir' | 'rename' | 'delete' | 'preview'>('mkdir')
const name = ref('')
const preview = ref('')
const imageURL = ref('')
const sort = ref('name')
const sortDesc = ref(false)
const address = ref('')
const menu = ref<{ x: number; y: number; name: string } | null>(null)
const dropping = ref(false)
const page = ref(1)
const progress = ref('')
let controller = new AbortController()
let epoch = 0
const status = (n: NodeInfo) => (n.status === 'live' ? '在线' : n.status === 'stale' ? '数据过期' : '离线')
const matches = (name: string) => name.toLocaleLowerCase().includes(filter.value.toLocaleLowerCase())
const hosts = computed(() => nodes.value.filter((n) => matches(n.hostname)))
const entries = computed(() =>
  files.value
    .filter((f) => matches(f.name))
    .sort(
      (a, b) =>
        Number(b.kind === 'folder') - Number(a.kind === 'folder') ||
        (sort.value === 'size'
          ? (sortDesc.value ? a.size - b.size : b.size - a.size)
          : sort.value === 'time'
            ? (sortDesc.value ? a.modified - b.modified : b.modified - a.modified)
            : sort.value === 'kind'
              ? (sortDesc.value ? b.kind.localeCompare(a.kind) : a.kind.localeCompare(b.kind))
              : 0) ||
        (sortDesc.value ? b.name.localeCompare(a.name, 'zh-CN', { numeric: true }) : a.name.localeCompare(b.name, 'zh-CN', { numeric: true })),
    ),
)
const selection = computed(() => entries.value.filter((f) => picked.value.includes(f.name)))
const menuEntry = computed(() => entries.value.find((f) => f.name === menu.value?.name) ?? null)
const selected = computed(() => selection.value[0] ?? null)
const selectedBytes = computed(() => selection.value.reduce((sum, f) => sum + (f.kind === 'file' ? f.size : 0), 0))
const kindLabel = (f: Entry) =>
  f.kind === 'folder' ? '文件夹' : f.kind === 'other' ? '其他' : f.name.includes('.') ? f.name.split('.').pop()!.toUpperCase() : '文件'
const when = (f: Entry) => (f.modified ? new Date(f.modified * 1000).toLocaleString() : '')
const pageItems = computed(() => entries.value.slice((page.value - 1) * 100, page.value * 100))
const bytes = (n: number) =>
  n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KiB' : (n / 1048576).toFixed(1) + ' MiB'
const title = computed(() => place.value.path.at(-1) || place.value.node?.hostname || '我的电脑')
function filePath(child?: string) {
  return [...place.value.path, ...(child ? [child] : [])].join('/') || '.'
}
function call<T>(action: string, args: Record<string, string>) {
  const node = place.value.node!
  return send<T>(
    'POST',
    '/api/v1/files/' + action + '?node=' + encodeURIComponent(node.local ? '' : node.id),
    args,
    controller.signal,
  )
}
function reset() {
  epoch++
  controller.abort()
  controller = new AbortController()
  progress.value = ''
  page.value = 1
  picked.value = []
  menu.value = null
  error.value = ''
  busy.value = false
  files.value = []
  dialog.value?.close()
}
async function refresh() {
  reset()
  const current = epoch
  busy.value = true
  try {
    if (place.value.node) {
      const list = await call<Entry[]>('list', { path: filePath() })
      if (current === epoch) {
        files.value = list
        rememberDirectory(place.value.node!.id, place.value.path)
      }
    } else {
      await loadNodes()
    }
  } catch (e) {
    if (current === epoch) error.value = String((e as Error).message)
  } finally {
    if (current === epoch) busy.value = false
  }
}
function go(node: NodeInfo | null, path: string[] = []) {
  places.value.splice(index.value + 1, Infinity, { node, path })
  index.value++
  filter.value = ''
  address.value = node ? '/' + path.join('/') : ''
  void refresh()
}
function up() {
  if (!place.value.node) return
  if (place.value.path.length) go(place.value.node, place.value.path.slice(0, -1))
  else go(null)
}
function jump() {
  if (!place.value.node) return
  go(place.value.node, address.value.split(/[/\\]+/).filter((part) => part && part !== '.' && part !== '..'))
}
function choose(f: Entry, event: MouseEvent) {
  menu.value = null
  const names = pageItems.value.map((item) => item.name)
  if (event.shiftKey && picked.value.length) {
    const anchor = names.indexOf(picked.value[picked.value.length - 1]!)
    const target = names.indexOf(f.name)
    const [from, to] = anchor < target ? [anchor, target] : [target, anchor]
    picked.value = names.slice(Math.max(0, from), to + 1)
    return
  }
  if (event.ctrlKey || event.metaKey) {
    picked.value = picked.value.includes(f.name) ? picked.value.filter((name) => name !== f.name) : [...picked.value, f.name]
    return
  }
  picked.value = [f.name]
}
function showMenu(event: MouseEvent, f?: Entry) {
  event.preventDefault()
  if (f && !picked.value.includes(f.name)) picked.value = [f.name]
  menu.value = {
    x: Math.max(8, Math.min(event.clientX, window.innerWidth - 180)),
    y: Math.max(8, Math.min(event.clientY, window.innerHeight - 240)),
    name: f?.name || '',
  }
}
function onPaneKey(event: KeyboardEvent) {
  const tag = (event.target as HTMLElement).tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
  if (event.key === 'Backspace') {
    event.preventDefault()
    up()
  } else if (event.key === 'Delete' && selection.value.length) {
    event.preventDefault()
    ask('delete')
  } else if (event.key === 'F2' && selection.value.length === 1) {
    event.preventDefault()
    ask('rename')
  } else if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'a' && place.value.node) {
    event.preventDefault()
    picked.value = pageItems.value.map((item) => item.name)
  }
}
function order(key: string) {
  if (sort.value === key) sortDesc.value = !sortDesc.value
  else {
    sort.value = key
    sortDesc.value = false
  }
}
function selectPage() {
  picked.value = pageItems.value.map((item) => item.name)
}
function openRecent(item: { nodeID: string; path: string[] }) {
  const node = nodes.value.find((n) => n.id === item.nodeID)
  if (node && node.status !== 'offline') go(node, item.path)
}
function step(delta: number) {
  index.value += delta
  filter.value = ''
  address.value = place.value.node ? '/' + place.value.path.join('/') : ''
  void refresh()
}
function view(value: 'grid' | 'list') {
  layout.value = value
  sessionStorage.setItem('monitor.files.layout', value)
}
function open(f: Entry) {
  picked.value = [f.name]
  menu.value = null
  if (f.kind === 'folder') go(place.value.node, [...place.value.path, f.name])
  else if (f.kind === 'file') void read(f, true)
}
function ask(action: 'mkdir' | 'rename' | 'delete') {
  mode.value = action
  name.value = action === 'rename' ? selected.value?.name || '' : ''
  dialog.value?.showModal()
}
async function act(action: string, args: Record<string, string>) {
  const current = epoch
  busy.value = true
  error.value = ''
  try {
    await call(action, args)
    if (current === epoch) await refresh()
  } catch (e) {
    if (current === epoch) error.value = (e as Error).message
  } finally {
    if (current === epoch) busy.value = false
  }
}
function submit() {
  if (mode.value === 'preview') return
  const n = name.value.trim()
  if (mode.value !== 'delete' && (!n || n === '.' || n === '..' || /[\\/:]/.test(n))) {
    error.value = '请输入有效的单个名称'
    dialog.value?.close()
    return
  }
  dialog.value?.close()
  if (mode.value === 'delete') {
    void remove(picked.value.length ? [...picked.value] : [selected.value!.name])
    return
  }
  void act(mode.value, { path: filePath(mode.value === 'mkdir' ? n : selected.value!.name), name: n })
}
async function remove(names: string[]) {
  const current = epoch
  busy.value = true
  error.value = ''
  try {
    for (const item of names) {
      if (current !== epoch) return
      await call('delete', { path: filePath(item) })
    }
    if (current === epoch) await refresh()
  } catch (e) {
    if (current === epoch) error.value = (e as Error).message
  } finally {
    if (current === epoch) busy.value = false
  }
}
async function upload(event: Event) {
  const input = event.target as HTMLInputElement
  const list = [...(input.files || [])]
  input.value = ''
  for (const f of list) await uploadFile(f)
}
async function dropFiles(list: FileList | undefined) {
  dropping.value = false
  for (const f of [...(list || [])]) await uploadFile(f)
}
function encodeChunk(data: Uint8Array) {
  let binary = ''
  for (let i = 0; i < data.length; i += 8192) binary += String.fromCharCode(...data.subarray(i, i + 8192))
  return btoa(binary)
}
const chunkSize = 512 * 1024
const maxTransfer = 128 * 1024 * 1024
async function uploadFile(f: File) {
  if (!place.value.node) return
  if (busy.value) {
    error.value = '请等待当前操作完成后再上传'
    return
  }
  if (f.size > maxTransfer) {
    error.value = '单个文件上限为 128 MiB'
    return
  }
  const current = epoch,
    path = filePath(f.name),
    node = place.value.node
  let id = ''
  busy.value = true
  error.value = ''
  try {
    if (f.size > chunkSize) id = await call<string>('upload-start', { path, size: String(f.size) })
    for (let offset = 0; offset < f.size || offset === 0; offset += chunkSize) {
      const data = encodeChunk(new Uint8Array(await f.slice(offset, offset + chunkSize).arrayBuffer()))
      if (current !== epoch) return
      progress.value =
        '正在上传 ' + Math.round((100 * Math.min(offset + chunkSize, f.size)) / Math.max(1, f.size)) + '%'
      if (id) await call('upload-chunk', { path, id, offset: String(offset), data })
      else await call('upload', { path, data })
    }
    if (current !== epoch) return
    if (id) {
      await call('upload-finish', { path, id })
      id = ''
    }
    if (current === epoch) await refresh()
  } catch (e) {
    if (current === epoch) error.value = (e as Error).message
  } finally {
    if (id)
      void send('POST', '/api/v1/files/upload-cancel?node=' + encodeURIComponent(node.local ? '' : node.id), {
        id,
        path,
      }).catch(() => {})
    if (current === epoch) {
      busy.value = false
      progress.value = ''
    }
  }
}
async function downloadFile(f: Entry, current: number) {
  if (f.size > maxTransfer) throw Error('单个文件上限为 128 MiB')
  const parts: ArrayBuffer[] = []
  let offset = 0,
    revision = '',
    size = 0
  do {
    if (current !== epoch) return
    const part = await call<{ data: string; revision: string; size: number }>('read-chunk', {
      path: filePath(f.name),
      offset: String(offset),
      revision,
    })
    const chunk = Uint8Array.from(atob(part.data), (c) => c.charCodeAt(0))
    size = part.size
    revision = part.revision
    if (!chunk.length && offset < size) throw Error('下载中断，请重新下载')
    parts.push(chunk.buffer)
    offset += chunk.length
    if (current !== epoch) return
    progress.value = '正在下载 ' + Math.round((100 * offset) / Math.max(size, 1)) + '%'
  } while (offset < size)
  const url = URL.createObjectURL(new Blob(parts)),
    a = document.createElement('a')
  a.href = url
  a.download = f.name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
function clearPreview() {
  if (imageURL.value) URL.revokeObjectURL(imageURL.value)
  imageURL.value = ''
  preview.value = ''
}
async function read(f: Entry, show: boolean) {
  if (show && f.size > chunkSize) {
    error.value = '预览上限为 512 KiB，可选择下载'
    return
  }
  const current = epoch
  busy.value = true
  error.value = ''
  try {
    if (!show) {
      await downloadFile(f, current)
      return
    }
    const encoded = await call<string>('read', { path: filePath(f.name) })
    if (current !== epoch || selected.value?.name !== f.name) return
    const data = Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0))
    const mime = (
      {
        png: 'image/png',
        jpg: 'image/jpeg',
        jpeg: 'image/jpeg',
        gif: 'image/gif',
        webp: 'image/webp',
      } as Record<string, string>
    )[f.name.split('.').pop()!.toLowerCase()]
    const blob = new Blob([data], { type: mime || 'application/octet-stream' })
    clearPreview()
    if (mime) imageURL.value = URL.createObjectURL(blob)
    else {
      try {
        preview.value = new TextDecoder('utf-8', { fatal: true }).decode(data)
        if (preview.value.includes('\0')) throw Error()
      } catch {
        preview.value = '此文件可下载到本地查看。'
      }
    }
    mode.value = 'preview'
    dialog.value?.showModal()
  } catch (e) {
    if (current === epoch) error.value = (e as Error).message
  } finally {
    if (current === epoch) {
      busy.value = false
      progress.value = ''
    }
  }
}
onMounted(() => {
  windowDialog.value?.showModal()
  void loadNodes()
  inventoryTimer = window.setInterval(() => { if (!document.hidden && !loadingNodes.value) void loadNodes() }, 30000)
})
onBeforeUnmount(() => {
  clearInterval(inventoryTimer)
  inventoryRequest?.abort()
  epoch++
  controller.abort()
  clearPreview()
})
</script>

<template>
  <Teleport to="body"
    ><dialog ref="windowDialog" class="explorer" @cancel.prevent="$emit('close')" aria-label="文件管理">
      <FileDesktop :nodes="nodes" :recent="recent" :busy="busy" :inventory-error="inventoryError" :loading="loadingNodes"
        @refresh="loadNodes" @navigate="go" @exit="$emit('close')">
      <aside class="rail">
        <h1><AppIcon name="files" :size="23" /> 文件管理</h1>
        <p class="group">位置</p>
        <button class="home" :class="{ active: !place.node }" @click="go(null)">
          <AppIcon name="computer" /> 我的电脑
        </button>
        <button
          v-for="n in nodes"
          :key="n.id"
          class="side-host"
          :class="{ active: place.node?.id === n.id && !place.path.length }"
          :disabled="n.status === 'offline'"
          @click="go(n)"
        >
          <AppIcon name="server" :size="15" />{{ n.hostname }}
        </button>
        <template v-if="recent.length">
          <p class="group">最近</p>
          <button v-for="(item, i) in recent" :key="i" class="side-host" @click="openRecent(item)">
            <AppIcon name="files" :size="15" />{{ item.path.at(-1) || '根目录' }}
          </button>
        </template>
      </aside>
      <div class="browser">
        <header>
          <button :disabled="!index" aria-label="后退" @click="step(-1)"><AppIcon name="arrow-left" /></button
          ><button :disabled="index >= places.length - 1" aria-label="前进" @click="step(1)">
            <AppIcon name="arrow-right" />
          </button>
          <nav aria-label="文件路径">
            <button @click="go(null)">我的电脑</button
            ><template v-if="place.node"
              ><span>›</span><button @click="go(place.node)">{{ place.node.hostname }}</button></template
            ><template v-for="(part, i) in place.path" :key="i"
              ><span>›</span
              ><button @click="go(place.node, place.path.slice(0, i + 1))">{{ part }}</button></template
            >
            <input
              v-if="place.node"
              v-model="address"
              aria-label="地址"
              placeholder="/目录"
              @keydown.enter.prevent="jump"
            />
          </nav>
          <button aria-label="刷新" @click="refresh"><AppIcon name="refresh" /></button>
          <label
            ><AppIcon name="search" :size="16" /><input
              v-model="filter"
              @input="page = 1"
              :placeholder="place.node ? '搜索当前目录' : '搜索主机'"
              aria-label="搜索"
          /></label>
        </header>
        <div class="toolbar">
          <template v-if="place.node"
            ><button :disabled="busy" @click="up">上级</button><span class="split" />
            <button :disabled="busy" @click="ask('mkdir')"><AppIcon name="files" />新建文件夹</button
            ><button :disabled="busy" @click="uploader?.click()"><AppIcon name="save" />上传</button
            ><input ref="uploader" type="file" multiple hidden :disabled="busy" @change="upload" /><span class="split" />
            <button v-if="pageItems.length" :disabled="busy" @click="selectPage">全选</button>
            <template v-if="selection.length"
              ><button :disabled="busy || selection.length !== 1" @click="ask('rename')">重命名</button
              ><button :disabled="busy" @click="ask('delete')">删除</button
              ><button
                v-if="selection.length === 1 && selection[0]!.kind === 'file'"
                :disabled="busy"
                @click="read(selection[0]!, false)"
              >
                下载
              </button></template
            ></template
          >
          <strong v-else>已注册主机</strong>
          <div class="spacer" />
          <select v-if="place.node" v-model="sort" aria-label="排序">
            <option value="name">按名称</option>
            <option value="time">按修改时间</option>
            <option value="size">按大小</option>
          </select>
          <button
            v-for="v in ['grid', 'list'] as const"
            :key="v"
            :aria-label="v === 'grid' ? '图标视图' : '列表视图'"
            :aria-pressed="layout === v"
            @click="view(v)"
          >
            <AppIcon :name="v" />
          </button>
          <button aria-label="详情" :aria-pressed="details" @click="details = !details">
            <AppIcon name="info" />
          </button>
        </div>
        <div class="body">
          <div
            class="pane"
            :class="{ dropping }"
            tabindex="0"
            @dragover.prevent="dropping = true"
            @dragleave="dropping = false"
            @drop.prevent="dropFiles($event.dataTransfer?.files)"
            @click="menu = null"
            @contextmenu="showMenu($event)"
            @keydown="onPaneKey"
          >
            <h2>{{ title }}</h2>
            <p v-if="error || (!place.node && inventoryError)" role="alert">{{ error || inventoryError }} <button @click="refresh">重新读取</button></p>
            <p v-if="busy" role="status">{{ progress || '正在处理…' }}</p>
            <div v-if="place.node && layout === 'list'" class="cols" role="row">
              <button type="button" :aria-pressed="sort === 'name'" @click="order('name')">名称</button>
              <button type="button" :aria-pressed="sort === 'size'" @click="order('size')">大小</button>
              <button type="button" :aria-pressed="sort === 'kind'" @click="order('kind')">类型</button>
              <button type="button" :aria-pressed="sort === 'time'" @click="order('time')">修改时间</button>
            </div>
            <div :class="['items', layout, { sheet: place.node && layout === 'list' }]">
              <button
                v-for="n in !place.node ? hosts : []"
                :key="n.id"
                class="item host"
                :disabled="n.status === 'offline'"
                @click="go(n)"
              >
                <span class="machine"><AppIcon name="computer" :size="44" /></span
                ><span>{{ n.hostname }}</span
                ><small><i :class="n.status" />{{ status(n) }} · {{ n.os }}</small>
              </button>
              <button
                v-for="f in pageItems"
                :key="f.name"
                class="item"
                :class="{ chosen: picked.includes(f.name), row: layout === 'list' }"
                :aria-pressed="picked.includes(f.name)"
                @click.stop="choose(f, $event)"
                @dblclick.stop="open(f)"
                @contextmenu.stop="showMenu($event, f)"
                @keydown.enter.prevent="open(f)"
              >
                <span class="namecell">
                  <span v-if="f.kind === 'folder'" class="folder" /><span
                    v-else
                    class="paper"
                    :data-ext="f.name.split('.').pop()?.toLowerCase()"
                    >{{
                      f.name.includes('.') ? f.name.split('.').pop()?.slice(0, 5).toUpperCase() : 'FILE'
                    }}</span
                  ><span>{{ f.name }}</span>
                </span>
                <template v-if="layout === 'list'">
                  <small>{{ f.kind === 'folder' ? '—' : bytes(f.size) }}</small>
                  <small>{{ kindLabel(f) }}</small>
                  <small>{{ when(f) }}</small>
                </template>
              </button>
            </div>
            <div v-if="menu" class="menu" :style="{ left: menu.x + 'px', top: menu.y + 'px' }" @click.stop>
              <button v-if="menuEntry" @click="open(menuEntry)">打开</button>
              <button :disabled="busy || !place.node" @click="menu = null; ask('mkdir')">新建文件夹</button>
              <button :disabled="busy || !place.node" @click="menu = null; uploader?.click()">上传</button>
              <button v-if="menuEntry" :disabled="busy" @click="menu = null; picked = [menuEntry.name]; ask('rename')">重命名</button>
              <button v-if="menuEntry?.kind === 'file'" :disabled="busy" @click="picked = [menuEntry.name]; menu = null; read(menuEntry, false)">下载</button>
              <button v-if="selection.length" :disabled="busy" @click="menu = null; ask('delete')">删除</button>
            </div>
            <p v-if="!busy && !error && !(place.node ? entries : hosts).length" class="empty">
              {{ filter ? '没有匹配项' : place.node ? '此文件夹为空，可把文件拖到这里上传' : '暂无已注册主机' }}
            </p>
          </div>
          <aside v-if="details" class="detail">
            <h3>详细信息</h3>
            <AppIcon :name="selected ? 'files' : 'computer'" :size="38" />
            <h3>{{ selected?.name || title }}</h3>
            <p>
              {{
                selected
                  ? selected.kind === 'folder'
                    ? '文件夹'
                    : bytes(selected.size)
                  : place.node?.os || '主机位置'
              }}
            </p>
            <p v-if="selected">修改时间<br />{{ when(selected) }}</p>
            <p v-if="selection.length > 1">已选 {{ selection.length }} 项<br />{{ bytes(selectedBytes) }}</p>
            <p v-if="place.node">所在路径<br />/{{ place.path.join('/') }}</p>
          </aside>
        </div>
        <footer>
          <span
            >{{ place.node ? entries.length : hosts.length }} {{ place.node ? '个项目' : '台主机'
            }}<template v-if="selection.length"> · 已选 {{ selection.length }} 项<template v-if="selectedBytes">，{{ bytes(selectedBytes) }}</template></template></span
          >
          <div v-if="place.node && entries.length > 100">
            <button :disabled="page === 1" @click="page--">上一页</button> {{ page }} /
            {{ Math.ceil(entries.length / 100) }}
            <button :disabled="page * 100 >= entries.length" @click="page++">下一页</button>
          </div>
          <button v-if="selected && selected.kind !== 'other'" :disabled="busy" @click="open(selected)">
            打开 {{ selected.name }}
          </button>
        </footer>
      </div>
      </FileDesktop>
      <dialog ref="dialog" class="prompt" @cancel.stop @close="clearPreview">
        <form @submit.prevent="submit">
          <h3>
            {{
              mode === 'preview'
                ? selected?.name
                : mode === 'mkdir'
                  ? '新建文件夹'
                  : mode === 'rename'
                    ? '重命名'
                    : '删除项目'
            }}
          </h3>
          <template v-if="mode === 'preview'"
            ><img v-if="imageURL" :src="imageURL" :alt="selected?.name" />
            <pre v-else>{{ preview }}</pre>
          </template>
          <p v-else-if="mode === 'delete'">
            {{ selection.length > 1 ? '删除选中的 ' + selection.length + ' 项？' : '删除「' + selected?.name + '」？' }}仅可删除文件或空目录。
          </p>
          <input v-else v-model="name" required autofocus aria-label="名称" maxlength="255" />
          <div>
            <button type="button" @click="dialog?.close()">{{ mode === 'preview' ? '关闭' : '取消' }}</button
            ><button v-if="mode !== 'preview'" class="primary">
              {{ mode === 'delete' ? '确认删除' : '确定' }}
            </button>
          </div>
        </form>
      </dialog>
    </dialog></Teleport
  >
</template>
<style scoped src="./explorer.css"></style>
