<script setup lang="ts">
import { computed, ref } from 'vue'
import type { PlatformInfo, PluginStatus } from '../../api/plugins/types'
import AppIcon from '../../components/AppIcon.vue'
import { allowsPluginAction, pluginAccess, pluginSource, pluginState } from './model/pluginPolicy'
import { usePlugins } from './usePlugins'

const props = defineProps<{ role: string; authenticated: boolean; platform?: PlatformInfo; hostname: string }>()
const emit = defineEmits<{ close: [] }>()
const { catalog, loading, busy, stale, error, notice, denied, updatedAt, canWrite, refresh, act, canLeave } = usePlugins(props)
const query = ref('')
const shown = computed(() => {
  const needle = query.value.trim().toLocaleLowerCase()
  return (catalog.value?.plugins || []).filter(plugin => !needle
    || [plugin.name, plugin.manifest?.name, plugin.manifest?.id, plugin.manifest?.description].join(' ').toLocaleLowerCase().includes(needle))
})
const readable = computed(() => pluginAccess(props.platform, props.role, props.authenticated).read)
const when = (seconds?: number) => seconds ? new Date(seconds * 1000).toLocaleString() : '尚无记录'
const lineBudget = (bytes?: number) => bytes ? (bytes % 1024 === 0 ? `${bytes / 1024} KiB` : `${bytes} 字节`) : '未设置'
const pluginBudget = (budgets?: { max_charts?: number; max_dimensions?: number; max_labels?: number; max_variables?: number; max_line?: number }) => {
  if (!budgets) return ''
  const parts = []
  if (budgets.max_charts) parts.push(`${budgets.max_charts} 张图表`)
  if (budgets.max_dimensions) parts.push(`每张 ${budgets.max_dimensions} 个维度`)
  if (budgets.max_labels) parts.push(`${budgets.max_labels} 个标签`)
  if (budgets.max_variables) parts.push(`${budgets.max_variables} 个变量`)
  if (budgets.max_line) parts.push(`单行 ${lineBudget(budgets.max_line)}`)
  return parts.join('，')
}
const title = (plugin: PluginStatus) => plugin.manifest?.name || plugin.name
function close() { if (canLeave()) emit('close') }
defineExpose({ canLeave })
</script>

<template>
  <section class="plugins-workspace" aria-label="本机插件管理">
    <header class="panel-heading"><div><h2><AppIcon name="plugins" :size="22" />插件管理</h2><p>管理本机 {{ hostname }} 的采集插件。切换监控节点不会改变此处的管理对象。</p></div><button :disabled="busy" title="关闭插件管理" @click="close"><AppIcon name="close" />关闭</button></header>
    <p v-if="!readable" class="error" role="alert">当前账号或服务未开放插件目录。</p>
    <template v-else>
      <div class="toolbar">
        <label>筛选插件<input v-model="query" type="search" placeholder="名称、标识或说明" /></label>
        <button :disabled="loading || busy || denied" @click="refresh"><AppIcon name="refresh" :size="16" />刷新目录</button>
        <button :disabled="!canWrite" @click="act('rescan')">重新扫描</button>
        <span v-if="catalog" class="dim">{{ catalog.plugins.length }} 个插件 · 目录版本 {{ catalog.revision }}</span>
      </div>
      <p class="dim">支持协议：{{ platform?.plugin_protocols.join('、') || '暂无' }}。清单插件包通过 plugin.json 描述身份和能力；重新扫描会读取服务器上已部署的插件。</p>
      <p v-if="catalog?.budgets" class="dim">清单包预算：{{ catalog.budgets.max_charts }} 张图表，每张 {{ catalog.budgets.max_dimensions }} 个维度、{{ catalog.budgets.max_labels }} 个标签、{{ catalog.budgets.max_variables }} 个变量，单行上限 {{ lineBudget(catalog.budgets.max_line) }}。</p>
      <p v-if="loading" role="status">正在读取插件目录…</p>
      <p v-if="busy" role="status">正在提交操作，请等待结果后切换模块。</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <p v-if="notice" class="notice" role="status">{{ notice }}</p>
      <p v-if="catalog && stale" class="warning" role="status">目录快照已过期，控制已暂停；请刷新目录后继续。</p>
      <p v-if="catalog && !catalog.running" class="warning" role="status">插件管理器未运行，暂不可执行启停或重新扫描。</p>
      <p v-if="catalog && !pluginAccess(platform, role, authenticated).control" class="dim">当前服务仅开放目录查看。</p>
      <p v-if="updatedAt && catalog" class="dim">最近读取：{{ new Date(updatedAt).toLocaleString() }}</p>

      <section v-if="catalog?.diagnostics.length" class="diagnostics" aria-label="插件目录诊断">
        <h3>目录诊断</h3><p>以下路径未能载入，请在服务器上检查文件后重新扫描。</p>
        <ul><li v-for="(diagnostic, index) in catalog.diagnostics" :key="index"><code>{{ diagnostic.path }}</code><span>{{ diagnostic.error }}</span></li></ul>
      </section>
      <p v-if="catalog && !catalog.plugins.length" class="empty" role="status">尚未发现插件。将插件脚本或带有 plugin.json 的插件包部署到服务器配置的插件目录后，点击重新扫描。</p>
      <p v-else-if="catalog && !shown.length" class="empty" role="status">没有匹配的插件。</p>
      <div class="plugin-grid">
        <article v-for="plugin in shown" :key="plugin.name" class="plugin-card" :aria-label="title(plugin)">
          <header><div><h3>{{ title(plugin) }}</h3><code>{{ plugin.name }}</code></div><span class="state" :class="{ issue: ['failed', 'waiting'].includes(plugin.state) }">{{ pluginState(plugin.state) }}</span></header>
          <p v-if="plugin.manifest?.description">{{ plugin.manifest.description }}</p>
          <p v-if="pluginBudget(plugin.budgets)" class="dim">本插件上限：{{ pluginBudget(plugin.budgets) }}。</p>
          <p v-if="plugin.functions?.length" class="dim">插件函数：{{ plugin.functions.join('、') }}</p>
          <dl class="metadata">
            <div><dt>来源</dt><dd>{{ pluginSource(plugin.source) }}</dd></div>
            <div v-if="plugin.manifest"><dt>版本</dt><dd>{{ plugin.manifest.version }} · API {{ plugin.manifest.api_version }}</dd></div>
            <div v-if="plugin.manifest"><dt>插件标识</dt><dd>{{ plugin.manifest.id }}</dd></div>
            <div><dt>协议</dt><dd>{{ plugin.manifest?.protocol || 'plugins.d/v1' }}</dd></div>
            <div v-if="plugin.manifest"><dt>声明能力</dt><dd>{{ plugin.manifest.capabilities.join('、') || '无' }}</dd></div>
            <div><dt>采集间隔</dt><dd>{{ plugin.update_every }} 秒</dd></div>
            <div><dt>进程 PID</dt><dd>{{ plugin.pid || '—' }}</dd></div>
            <div><dt>最近启动</dt><dd>{{ when(plugin.started) }}</dd></div>
            <div><dt>最近采样</dt><dd>{{ when(plugin.stats.last_data) }}</dd></div>
          </dl>
          <div class="metrics"><span>图表 <b>{{ plugin.stats.charts }}</b></span><span>采样 <b>{{ plugin.stats.samples }}</b></span><span>重启 <b>{{ plugin.restarts }}</b></span><span>解析错误 <b>{{ plugin.stats.errors }}</b></span></div>
          <p v-if="plugin.error" class="error">运行诊断：{{ plugin.error }}</p>
          <p v-if="plugin.stats.last_error" class="error">采样诊断：{{ plugin.stats.last_error }}</p>
          <p v-if="plugin.locked" class="warning">配置已锁定停用；请先修改服务器配置，此处不能启动。</p>
          <details>
            <summary>运行详情</summary>
            <dl>
              <dt>可执行文件</dt><dd><code>{{ plugin.command }}</code></dd>
              <dt>协议行数</dt><dd>{{ plugin.stats.lines }}</dd>
              <dt>诊断输出（stderr）</dt>
              <dd v-if="plugin.stderr" class="stderr-summary">
                <span>收到 {{ plugin.stderr.bytes }} 字节 · {{ plugin.stderr.lines }} 行</span>
                <span>转发到服务日志 {{ plugin.stderr.logged }} 行</span>
                <span>限流抑制 {{ plugin.stderr.suppressed }} 行 · 超长 {{ plugin.stderr.truncated }} 行</span>
                <p class="dim">超过日志限额的行不转发；超长行仅转发省略标记。计数跨插件进程重启累计，服务重启后清零。</p>
              </dd>
              <dd v-else class="dim">服务未提供诊断输出统计。</dd>
              <template v-if="plugin.functions?.length"><dt>声明函数</dt><dd>{{ plugin.functions.join('、') }}</dd></template>
            </dl>
          </details>
          <div class="actions">
            <button v-if="plugin.enabled" :disabled="!canWrite || !allowsPluginAction(catalog, 'disable', plugin.name)" @click="act('disable', plugin.name)">停用</button>
            <button v-else :disabled="!canWrite || !allowsPluginAction(catalog, 'enable', plugin.name)" @click="act('enable', plugin.name)">启用</button>
            <button :disabled="!canWrite || !allowsPluginAction(catalog, 'restart', plugin.name)" @click="act('restart', plugin.name)">重启</button>
          </div>
        </article>
      </div>
    </template>
  </section>
</template>

<style scoped>
.stderr-summary > span { display:block; line-height:1.8; }
.plugins-workspace { color:#cbd5e1; min-width:0; }.panel-heading,.panel-heading h2,.toolbar,.actions { display:flex; align-items:center; gap:10px; }.panel-heading { justify-content:space-between; align-items:flex-start; }.panel-heading h2 { margin:0; font-size:20px; }.panel-heading p,.dim { color:#94a3b8; font-size:12px; line-height:1.7; }.panel-heading p { margin:8px 0 16px; }.toolbar { flex-wrap:wrap; align-items:flex-end; }.toolbar label { display:flex; flex-direction:column; gap:6px; font-size:12px; flex:1; min-width:min(200px,100%); }
button,input { border:1px solid #475569; border-radius:7px; background:#111e30; color:#e2e8f0; font:inherit; padding:8px 11px; box-sizing:border-box; }button { cursor:pointer; display:inline-flex; gap:6px; align-items:center; justify-content:center; font-size:13px; flex-shrink:0; }button:disabled { opacity:.45; cursor:default; }button:focus-visible,input:focus-visible,summary:focus-visible { outline:2px solid #5eead4; outline-offset:3px; }input { width:100%; min-width:0; }.plugin-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(380px,100%),1fr)); gap:14px; margin-top:16px; }.plugin-card { border:1px solid #334155; border-radius:10px; background:#0f172a; padding:16px; min-width:0; overflow-wrap:anywhere; }.plugin-card>header { display:flex; justify-content:space-between; align-items:flex-start; gap:12px; }.plugin-card h3 { margin:0 0 5px; font-size:16px; }.plugin-card p { font-size:13px; line-height:1.7; }code { font-size:12px; color:#94a3b8; overflow-wrap:anywhere; }.state { color:#5eead4; font-size:12px; white-space:nowrap; padding:4px 7px; border:1px solid #2c5251; border-radius:6px; }.state.issue,.warning { color:#fcd34d; }.metadata { display:grid; grid-template-columns:1fr 1fr; gap:10px; }.metadata div { min-width:0; }dt { color:#94a3b8; font-size:12px; margin-bottom:3px; }dd { margin:0; font-size:13px; overflow-wrap:anywhere; }.metrics { display:flex; flex-wrap:wrap; gap:12px; padding-block:10px; font-size:12px; }.metrics b { color:#e2e8f0; }.actions { margin-top:12px; }.error { color:#fca5a5; overflow-wrap:anywhere; }.notice { color:#5eead4; }.diagnostics { border:1px solid #7c5425; border-radius:8px; background:#2c251b; padding:12px; font-size:13px; overflow-wrap:anywhere; }.diagnostics h3 { margin:0; }.diagnostics ul { padding-left:20px; }.diagnostics li span { display:block; color:#fcd34d; margin-block:5px 10px; }.empty { padding:18px; border:1px dashed #475569; border-radius:8px; color:#94a3b8; line-height:1.8; }details { font-size:12px; color:#94a3b8; }summary { cursor:pointer; }details dd { margin-bottom:8px; }
@media(max-width:760px) { button,input,summary { min-height:44px; }input { font-size:16px; }.plugin-card { padding:12px; }.metadata { gap:12px; }.panel-heading>button { font-size:12px; }summary { display:flex; align-items:center; }.toolbar label { flex-basis:100%; } }
</style>
