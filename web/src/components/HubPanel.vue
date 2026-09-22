<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import type { Claim, Room, Space } from '../api'
import { api } from '../api'

const emit = defineEmits<{ close: [] }>()
const spaces = ref<Space[]>([])
const rooms = ref<Room[]>([])
const claims = ref<Claim[]>([])
const spaceID = ref('')
const roomID = ref('')
const spaceName = ref('')
const roomName = ref('')
const minted = ref('')
const shareURL = ref('')
const nodeID = ref('')
const disabled = ref('')
const error = ref('')
const copied = ref(false)

async function load() {
  try {
    const [s, c] = await Promise.all([api.spaces(), api.claims()])
    spaces.value = s.spaces
    claims.value = c.claims
    if (!spaceID.value && spaces.value[0]) spaceID.value = spaces.value[0].id
    await loadRooms()
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function loadRooms() {
  if (!spaceID.value) { rooms.value = []; roomID.value = ''; return }
  const r = await api.rooms(spaceID.value)
  rooms.value = r.rooms
  if (!rooms.value.some((x) => x.id === roomID.value)) roomID.value = rooms.value[0]?.id ?? ''
}

watch(spaceID, () => { void loadRooms() })

async function addSpace() {
  const name = spaceName.value.trim()
  if (!name) return
  const sp = await api.createSpace(name)
  spaceName.value = ''
  spaceID.value = sp.id
  await load()
}

async function dropSpace() {
  if (!spaceID.value) return
  await api.deleteSpace(spaceID.value)
  spaceID.value = ''
  await load()
}

async function addRoom() {
  const name = roomName.value.trim()
  if (!name || !spaceID.value) return
  const rm = await api.createRoom(spaceID.value, name)
  roomName.value = ''
  roomID.value = rm.id
  await load()
}

async function dropRoom() {
  if (!roomID.value) return
  await api.deleteRoom(roomID.value)
  roomID.value = ''
  await load()
}

async function mint() {
  if (!spaceID.value || !roomID.value) return
  const cl = await api.issueClaim(spaceID.value, roomID.value)
  minted.value = cl.token
  copied.value = false
  await load()
}

async function copyToken() {
  if (!minted.value) return
  try {
    await navigator.clipboard.writeText(minted.value)
    copied.value = true
  } catch {
    copied.value = false
  }
}

async function mintShare() {
  const s = await api.share('24h')
  shareURL.value = location.origin + (s.url || ('/?token=' + s.token))
  copied.value = false
}

async function saveConfig() {
  const id = nodeID.value.trim()
  if (!id) return
  const list = disabled.value.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean)
  await api.putNodeConfig({ node_id: id, disabled: list })
  error.value = ''
}

function when(t: number) {
  if (!t) return '—'
  return new Date(t * 1000).toLocaleString()
}

onMounted(() => { void load() })
</script>

<template>
  <div class="panel">
    <div class="head">
      <h3>Hub <small>Space / Room / claim</small></h3>
      <button class="x" @click="emit('close')" title="关闭">×</button>
    </div>
    <div v-if="error" class="err">{{ error }}</div>

    <div class="row">
      <label>Space
        <select v-model="spaceID">
          <option v-for="s in spaces" :key="s.id" :value="s.id">{{ s.name }}</option>
        </select>
      </label>
      <input v-model="spaceName" placeholder="新 Space 名" @keyup.enter="addSpace" />
      <button @click="addSpace">创建</button>
      <button class="danger" :disabled="!spaceID" @click="dropSpace">删除</button>
    </div>
    <div class="row">
      <label>Room
        <select v-model="roomID">
          <option v-for="r in rooms" :key="r.id" :value="r.id">{{ r.name }} ({{ r.nodes?.length ?? 0 }})</option>
        </select>
      </label>
      <input v-model="roomName" placeholder="新 Room 名" @keyup.enter="addRoom" />
      <button @click="addRoom">创建</button>
      <button class="danger" :disabled="!roomID" @click="dropRoom">删除</button>
    </div>
    <div class="row">
      <button :disabled="!spaceID || !roomID" @click="mint">签发 claim token</button>
      <code v-if="minted" class="tok" :title="minted">{{ minted }}</code>
      <button v-if="minted" @click="copyToken">{{ copied ? '已复制' : '复制' }}</button>
      <button @click="mintShare">只读分享链接</button>
      <code v-if="shareURL" class="tok" :title="shareURL">{{ shareURL }}</code>
    </div>
    <p class="hint">Agent 在 <code>stream.claim_token</code> 填入一次性 token，连接 Hub 后换成长生命周期 stream key 并加入该 Room。</p>

    <h3>近期 token</h3>
    <table>
      <thead>
        <tr><th>token</th><th>节点</th><th>过期</th><th>使用</th></tr>
      </thead>
      <tbody>
        <tr v-for="c in claims.slice(0, 8)" :key="c.token">
          <td class="dim"><code>{{ c.token.slice(0, 18) }}…</code></td>
          <td>{{ c.node_id || '—' }}</td>
          <td class="dim">{{ when(c.expires) }}</td>
          <td>{{ c.used_at ? when(c.used_at) : '未用' }}</td>
        </tr>
        <tr v-if="!claims.length"><td colspan="4" class="dim">还没有 claim token</td></tr>
      </tbody>
    </table>

    <h3>配置下发</h3>
    <div class="row">
      <input v-model="nodeID" placeholder="node id" />
      <input v-model="disabled" placeholder="禁用采集器，逗号分隔，如 nvidia,ping" />
      <button :disabled="!nodeID.trim()" @click="saveConfig">下发</button>
    </div>
  </div>
</template>

<style scoped>
.panel { background: #0f172a; border: 1px solid #334155; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; font-size: 13px; }
.head { display: flex; justify-content: space-between; align-items: center; }
h3 { margin: 0 0 8px; font-size: 13px; color: #cbd5e1; text-transform: uppercase; letter-spacing: .05em; }
h3 small { color: #64748b; font-weight: 400; margin-left: 6px; text-transform: none; }
.x { background: none; border: 0; color: #94a3b8; font-size: 18px; cursor: pointer; }
.err { color: #fecaca; background: #7f1d1d; padding: 6px 8px; border-radius: 6px; margin-bottom: 8px; }
.row { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 8px; }
label { color: #94a3b8; display: flex; gap: 6px; align-items: center; }
input, select, button { background: #0f172a; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 4px 8px; font-size: 13px; }
button { cursor: pointer; background: #1e293b; }
button:disabled { opacity: .5; cursor: not-allowed; }
.danger { color: #fecaca; border-color: #7f1d1d; }
.tok { max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: #bbf7d0; }
.hint { color: #64748b; margin: 0 0 12px; font-size: 12px; }
table { width: 100%; border-collapse: collapse; margin-bottom: 16px; }
th { text-align: left; color: #64748b; font-weight: 500; font-size: 11px; padding: 4px 6px; border-bottom: 1px solid #1e293b; }
td { padding: 4px 6px; border-bottom: 1px solid #111827; white-space: nowrap; }
.dim { color: #64748b; }
code { font-size: 12px; }
</style>
