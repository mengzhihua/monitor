<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import AppIcon from '../../components/AppIcon.vue'
import type { NodeInfo } from '../../api/hub/types'
import DesktopClock from './DesktopClock.vue'

type App = 'files' | 'hosts' | 'recent' | 'help'
type Window = App | 'desktop'
const props = defineProps<{
  nodes: NodeInfo[]
  recent: { nodeID: string; path: string[] }[]
  inventoryError: string
  loading: boolean
  busy: boolean
}>()
const emit = defineEmits<{
  navigate: [node: NodeInfo | null, path: string[]]
  refresh: []
  exit: []
}>()
const apps = {
  files: { title: '文件管理', shortcut: '我的电脑', icon: 'computer' },
  hosts: { title: '主机信息', shortcut: '主机信息', icon: 'server' },
  recent: { title: '最近位置', shortcut: '最近位置', icon: 'history' },
  help: { title: '使用帮助', shortcut: '使用帮助', icon: 'info' },
} as const
const active = ref<Window>('desktop')
const running = ref<App[]>([])
const maximized = ref(true)
const titlebar = ref<HTMLHeadingElement>()
const frame = ref<HTMLElement>()
const position = ref<{ left: number; top: number } | null>(null)
let drag: { pointer: number; left: number; top: number; x: number; y: number } | null = null

function beginDrag(event: PointerEvent) {
  if (maximized.value || event.button !== 0 || !frame.value || matchMedia('(max-width:720px)').matches) return
  const handle = event.currentTarget as HTMLElement
  handle.setPointerCapture(event.pointerId)
  drag = {
    pointer: event.pointerId,
    left: frame.value.offsetLeft,
    top: frame.value.offsetTop,
    x: event.clientX,
    y: event.clientY,
  }
}
function moveDrag(event: PointerEvent) {
  if (drag?.pointer !== event.pointerId || !frame.value) return
  position.value = { left: drag.left + event.clientX - drag.x, top: drag.top + event.clientY - drag.y }
}
function toggleSize() {
  if (!matchMedia('(max-width:720px)').matches) maximized.value = !maximized.value
}
const desktopButton = ref<HTMLButtonElement>()
const online = computed(() => props.nodes.filter((n) => n.status === 'live').length)
const recentHosts = computed(() =>
  props.recent.flatMap((location) => {
    const node = props.nodes.find((n) => n.id === location.nodeID)
    return node ? [{ node, path: location.path }] : []
  }),
)
function activate(app: Window) {
  if (app !== 'desktop' && !running.value.includes(app)) running.value.push(app)
  active.value = app
  void nextTick(() =>
    (app === 'desktop' ? desktopButton.value : titlebar.value)?.focus({ preventScroll: true }),
  )
}
function openLocation(node: NodeInfo | null, path: string[] = []) {
  emit('navigate', node, path)
  activate('files')
}
function closeWindow() {
  if (active.value === 'files') emit('navigate', null, [])
  running.value = running.value.filter((id) => id !== active.value)
  activate('desktop')
}
function status(node: NodeInfo) {
  return node.status === 'live' ? '在线' : node.status === 'stale' ? '数据过期' : '离线'
}
</script>

<template>
  <div class="desktop-shell">
    <nav class="app-dock" aria-label="桌面应用栏">
      <div class="brand" title="Monitor"><AppIcon name="charts" :size="25" /></div>
      <button
        ref="desktopButton"
        :aria-current="active === 'desktop' ? 'page' : undefined"
        @click="activate('desktop')"
      >
        <AppIcon name="computer" />
        桌面
      </button>
      <button
        v-for="(app, id) in apps"
        :key="id"
        :aria-current="active === id ? 'page' : undefined"
        @click="activate(id)"
      >
        <AppIcon :name="id === 'files' ? 'files' : app.icon" />
        {{ app.title }}
      </button>
      <button class="leave" @click="emit('exit')">
        <AppIcon name="logout" />
        返回监控
      </button>
    </nav>
    <div class="surface">
      <section
        v-show="active === 'desktop' || !maximized"
        :inert="active !== 'desktop'"
        class="desktop-space"
        aria-label="桌面快捷方式"
      >
        <DesktopClock class="clock-widget" />
        <div class="shortcuts">
          <button
            v-for="(app, id) in apps"
            :key="id"
            @click="id === 'files' ? openLocation(null) : activate(id)"
          >
            <span :class="['app-art', id]"><AppIcon :name="app.icon" :size="34" /></span>
            {{ app.shortcut }}
          </button>
        </div>
      </section>
      <section
        ref="frame"
        v-show="active !== 'desktop'"
        :style="position ? { '--left': position.left + 'px', '--top': position.top + 'px' } : undefined"
        class="app-window"
        :class="{ floating: !maximized }"
        aria-label="应用窗口"
      >
        <div class="titlebar">
          <h2
            ref="titlebar"
            tabindex="-1"
            @pointerdown="beginDrag"
            @pointermove="moveDrag"
            @lostpointercapture="drag = null"
            @dblclick="toggleSize"
          >
            <AppIcon :name="active === 'desktop' ? 'computer' : apps[active].icon" :size="16" />
            {{ active === 'desktop' ? '桌面' : apps[active].title }}
          </h2>
          <button aria-label="最小化窗口" title="最小化" @click="activate('desktop')">−</button>
          <button class="maximize" :aria-label="maximized ? '还原窗口' : '最大化窗口'" @click="toggleSize">
            <AppIcon name="expand" :size="15" />
          </button>
          <button :disabled="active === 'files' && busy" aria-label="关闭窗口" @click="closeWindow">
            <AppIcon name="close" :size="15" />
          </button>
        </div>
        <div v-show="active === 'files'" class="file-content"><slot /></div>
        <div v-if="active === 'hosts'" class="utility">
          <header>
            <div>
              <h3>注册主机</h3>
              <p>{{ nodes.length }} 台主机 · {{ online }} 台在线</p>
            </div>
            <button :disabled="loading" @click="emit('refresh')">
              <AppIcon name="refresh" :size="16" />
              刷新主机
            </button>
          </header>
          <p v-if="inventoryError" role="alert">{{ inventoryError }}</p>
          <p v-else-if="loading" role="status">正在读取主机…</p>
          <p v-else-if="!nodes.length">暂无可见主机</p>
          <ul>
            <li v-for="node in nodes" :key="node.id" class="host-info">
              <span class="host-symbol"><AppIcon name="server" :size="26" /></span>
              <div>
                <strong>{{ node.hostname }}</strong>
                <span>{{ [node.os, node.arch, node.version].filter(Boolean).join(' · ') }}</span>
              </div>
              <small :class="node.status">{{ status(node) }}</small>
              <button :disabled="node.status === 'offline'" @click="openLocation(node)">浏览文件</button>
            </li>
          </ul>
        </div>
        <div v-if="active === 'recent'" class="utility">
          <h3>最近访问的目录</h3>
          <p v-if="!recentHosts.length">从「我的电脑」打开主机目录后，可以在这里再次访问。</p>
          <ul>
            <li v-for="entry in recentHosts" :key="JSON.stringify([entry.node.id, entry.path])">
              <button
                class="recent-place"
                :disabled="entry.node.status === 'offline'"
                @click="openLocation(entry.node, entry.path)"
              >
                <AppIcon name="files" :size="26" />
                <span>
                  <strong>{{ entry.path.at(-1) || entry.node.hostname }}</strong>
                  <small>{{ entry.node.hostname }} / {{ entry.path.join(' / ') }}</small>
                </span>
              </button>
            </li>
          </ul>
        </div>
        <div v-if="active === 'help'" class="utility guide">
          <h3>桌面使用帮助</h3>
          <h4>打开主机文件</h4>
          <p>点击桌面的「我的电脑」，选择在线主机。可以双击文件夹进入，也可以选中项目后点「打开」。</p>
          <h4>在应用间切换</h4>
          <p>
            左侧应用栏可切换到桌面、文件管理或主机信息。最小化后，点任务栏中的文件管理，可继续浏览原来的目录。
          </p>
          <h4>上传和下载</h4>
          <p>
            进入目录后点「上传」，或把一个文件拖入文件区。选中文件后可下载，单文件上限为 128 MiB。小于等于 512
            KiB 的文本和常用图片可直接预览。
          </p>
        </div>
      </section>
      <div class="taskbar" aria-label="桌面任务栏">
        <button aria-label="显示桌面" title="显示桌面" @click="activate('desktop')">
          <AppIcon name="operations" :size="17" />
        </button>
        <div class="tasks">
          <button
            v-for="id in running"
            :key="id"
            :aria-pressed="active === id"
            @click="activate(active === id ? 'desktop' : id)"
          >
            <AppIcon :name="apps[id].icon" :size="15" />
            {{ apps[id].title }}
          </button>
        </div>
        <button class="connections" aria-label="查看主机连接状态" @click="activate('hosts')">
          <i :class="{ live: online }" />
          {{ inventoryError ? '读取失败' : loading ? '读取中' : online + ' / ' + nodes.length + ' 在线' }}
        </button>
      </div>
    </div>
  </div>
</template>
<style scoped src="./desktop.css"></style>
