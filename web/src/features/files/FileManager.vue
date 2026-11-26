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
const selected = ref<Entry | null>(null)
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
        (sort.value === 'size' ? b.size - a.size : sort.value === 'time' ? b.modified - a.modified : 0) ||
        a.name.localeCompare(b.name, 'zh-CN', { numeric: true }),
    ),
)
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
  selected.value = null
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
  void refresh()
}
function step(delta: number) {
  index.value += delta
  filter.value = ''
  void refresh()
}
function view(value: 'grid' | 'list') {
  layout.value = value
  sessionStorage.setItem('monitor.files.layout', value)
}
function open(f: Entry) {
  selected.value = f
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
  void act(mode.value, { path: filePath(mode.value === 'mkdir' ? n : selected.value!.name), name: n })
}
async function upload(event: Event) {
  const input = event.target as HTMLInputElement,
    f = input.files?.[0]
  input.value = ''
  if (!f) return
  await uploadFile(f)
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
        <button class="home" :class="{ active: !place.node }" @click="go(null)">
          <AppIcon name="computer" /> 我的电脑
        </button>
        <button
          v-for="n in nodes"
          :key="n.id"
          class="side-host"
          :class="{ active: place.node?.id === n.id }"
          :disabled="n.status === 'offline'"
          @click="go(n)"
        >
          <AppIcon name="server" :size="15" />{{ n.hostname }}
        </button>
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
            ><button :disabled="busy" @click="ask('mkdir')"><AppIcon name="files" />新建文件夹</button
            ><button :disabled="busy" @click="uploader?.click()"><AppIcon name="save" />上传</button
            ><input ref="uploader" type="file" hidden :disabled="busy" @change="upload" />
            <template v-if="selected"
              ><button :disabled="busy" @click="ask('rename')">重命名</button
              ><button :disabled="busy" @click="ask('delete')">删除</button
              ><button v-if="selected.kind === 'file'" :disabled="busy" @click="read(selected, false)">
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
            @dragover.prevent
            @drop.prevent="$event.dataTransfer?.files[0] && uploadFile($event.dataTransfer.files[0])"
          >
            <h2>{{ title }}</h2>
            <p v-if="error || (!place.node && inventoryError)" role="alert">{{ error || inventoryError }} <button @click="refresh">重新读取</button></p>
            <p v-if="busy" role="status">{{ progress || '正在处理…' }}</p>
            <div :class="['items', layout]">
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
                v-for="f in entries.slice((page - 1) * 100, page * 100)"
                :key="f.name"
                class="item"
                :class="{ chosen: selected?.name === f.name }"
                :aria-pressed="selected?.name === f.name"
                @click="selected = f"
                @dblclick="open(f)"
                @keydown.enter.prevent="open(f)"
              >
                <span v-if="f.kind === 'folder'" class="folder" /><span
                  v-else
                  class="paper"
                  :data-ext="f.name.split('.').pop()?.toLowerCase()"
                  >{{
                    f.name.includes('.') ? f.name.split('.').pop()?.slice(0, 5).toUpperCase() : 'FILE'
                  }}</span
                >
                <span>{{ f.name }}</span
                ><small v-if="layout === 'list'">{{ f.kind === 'folder' ? '文件夹' : bytes(f.size) }}</small>
              </button>
            </div>
            <p v-if="!busy && !error && !(place.node ? entries : hosts).length">
              {{ filter ? '没有匹配项' : place.node ? '此文件夹为空' : '暂无已注册主机' }}
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
            <p v-if="selected">修改时间<br />{{ new Date(selected.modified * 1000).toLocaleString() }}</p>
            <p v-if="place.node">所在路径<br />/{{ place.path.join('/') }}</p>
          </aside>
        </div>
        <footer>
          <span>{{ place.node ? entries.length : hosts.length }} {{ place.node ? '个项目' : '台主机' }}</span>
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
          <p v-else-if="mode === 'delete'">删除「{{ selected?.name }}」？仅可删除文件或空目录。</p>
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
