<script setup lang="ts">
import { watch } from 'vue'
import type { AgentVisual, AgentSNMPTarget, AgentRedfishTarget } from '../api'

const props = defineProps<{ form: AgentVisual; disabled: boolean }>()
let savedSNMPTargets = new WeakSet<AgentSNMPTarget>()
let savedRedfishTargets = new WeakSet<AgentRedfishTarget>()
watch(() => props.form, form => {
  savedSNMPTargets = new WeakSet(form.snmp_targets ?? [])
  savedRedfishTargets = new WeakSet(form.redfish_targets ?? [])
}, { immediate: true })

function enableCollector(name: string) {
  const form = props.form
  form.collectors_disabled = form.collectors_disabled.filter(value => value !== name)
  if (form.collectors_enabled.length && !form.collectors_enabled.includes(name)) form.collectors_enabled.push(name)
}
function collectorDisabled(name: string) {
  return props.form.collectors_disabled.includes(name) || (props.form.collectors_enabled.length > 0 && !props.form.collectors_enabled.includes(name))
}
function uniqueName(prefix: string, names: string[]) {
  let index = 1
  while (names.includes(`${prefix}-${index}`)) index++
  return `${prefix}-${index}`
}
function addSNMP() {
  if (props.disabled || props.form.snmp_legacy || (props.form.snmp_targets?.length ?? 0) >= 64) return
  const list = props.form.snmp_targets ??= []
  list.push({ name: uniqueName('device', list.map(t => t.name)), address: '', profile: 'network', version: '2c', community: '', user: '', auth_protocol: '', priv_protocol: '', timeout: '3s' })
  enableCollector('snmp')
}
function addRedfish() {
  if (props.disabled || props.form.redfish_legacy || (props.form.redfish_targets?.length ?? 0) >= 64) return
  const list = props.form.redfish_targets ??= []
  list.push({ name: uniqueName('bmc', list.map(t => t.name)), url: '', user: '', password: '', timeout: '5s' })
  enableCollector('redfish')
}
</script>

<template>
  <fieldset class="devices" :disabled="disabled" aria-label="设备接入">
    <legend>设备接入</legend>
    <p class="hint">通过标准协议采集网络设备、UPS、打印机、服务器和 NAS。设备需要开启对应协议；可用指标取决于设备公开的数据。保存后重启服务生效。</p>
    <p class="hint">设备标识用于历史图表，保存后保持不变；长度 1–48，只能包含字母、数字、下划线和连字符。自定义 OID 等高级选项在 YAML 中设置。</p>

    <p class="hint">SNMP 需要在采集主机安装 Net-SNMP 的 snmpwalk。各类型使用标准 MIB，设备不支持的指标不会自动补全。</p>
    <div class="section-head"><h4>SNMP 设备 <small>{{ form.snmp_targets?.length ?? 0 }}/64</small></h4><button type="button" :disabled="form.snmp_legacy || (form.snmp_targets?.length ?? 0) >= 64" @click="addSNMP">添加 SNMP 设备</button></div>
    <p v-if="form.snmp_legacy" role="status" class="notice">已有旧版 SNMP 单设备配置，保存表单会保留。请先在 YAML 中迁移为带 name 的 targets 列表，再管理多个设备。</p>
    <p v-if="form.snmp_targets?.length && collectorDisabled('snmp')" class="notice">SNMP 采集器当前未启用。<button type="button" @click="enableCollector('snmp')">启用 SNMP 采集器</button></p>
    <article v-for="(target, i) in form.snmp_targets" :key="i" class="device-card" :aria-label="`SNMP 设备 ${i + 1}`">
      <div class="card-head"><strong>SNMP · {{ target.name || `设备 ${i + 1}` }}</strong><button type="button" :aria-label="`移除 SNMP 设备 ${i + 1}`" @click="form.snmp_targets?.splice(i, 1)">移除</button></div>
      <div class="fields">
        <label>设备标识<input v-model="target.name" :readonly="savedSNMPTargets.has(target)" :aria-label="`SNMP 设备 ${i + 1} 标识`" maxlength="48" pattern="[a-zA-Z0-9_-]{1,48}" autocomplete="off" /></label>
        <label>设备类型<select v-model="target.profile" :aria-label="`SNMP 设备 ${i + 1} 类型`"><option value="">默认网络设备</option><option value="network">交换机 / 路由器 / 防火墙</option><option value="ups">UPS 电源</option><option value="printer">打印机</option><option value="host">服务器 / NAS（HOST-RESOURCES）</option></select></label>
        <label>设备地址<input v-model="target.address" :aria-label="`SNMP 设备 ${i + 1} 地址`" placeholder="192.168.1.10 或 udp:192.168.1.10:161" autocomplete="off" /></label>
        <label>超时<input v-model="target.timeout" :aria-label="`SNMP 设备 ${i + 1} 超时`" placeholder="3s，最长 1m" /></label>
        <label>协议版本<select v-model="target.version" :aria-label="`SNMP 设备 ${i + 1} 版本`"><option value="">默认 v2c</option><option value="3">SNMP v3</option><option value="2c">SNMP v2c</option><option value="1">SNMP v1</option></select></label>
        <label v-if="target.version !== '3'">Community<input v-model="target.community" :aria-label="`SNMP 设备 ${i + 1} Community`" type="password" autocomplete="new-password" placeholder="设备配置的只读 Community" /></label>
        <template v-else>
          <label>USM 用户<input v-model="target.user" :aria-label="`SNMP 设备 ${i + 1} 用户`" autocomplete="off" /></label>
          <label>认证协议<select v-model="target.auth_protocol" :aria-label="`SNMP 设备 ${i + 1} 认证协议`"><option value="">默认 SHA（配置口令后认证）</option><option value="SHA">SHA</option><option value="SHA-256">SHA-256</option><option value="MD5">MD5</option></select></label>
          <label>认证口令<input v-model="target.auth_passphrase" :aria-label="`SNMP 设备 ${i + 1} 认证口令`" type="password" autocomplete="new-password" /></label>
          <label>加密协议<select v-model="target.priv_protocol" :aria-label="`SNMP 设备 ${i + 1} 加密协议`"><option value="">默认 AES（配置口令后加密）</option><option value="AES">AES</option><option value="DES">DES</option></select></label>
          <label>加密口令<input v-model="target.priv_passphrase" :aria-label="`SNMP 设备 ${i + 1} 加密口令`" type="password" autocomplete="new-password" /></label>
        </template>
      </div>
    </article>

    <div class="section-head"><h4>Redfish 服务器管理口 <small>{{ form.redfish_targets?.length ?? 0 }}/64</small></h4><button type="button" :disabled="form.redfish_legacy || (form.redfish_targets?.length ?? 0) >= 64" @click="addRedfish">添加 Redfish 设备</button></div>
    <p v-if="form.redfish_legacy" role="status" class="notice">已有旧版 Redfish 单设备配置，保存表单会保留。请先在 YAML 中迁移为带 name 的 targets 列表，再管理多个设备。</p>
    <p v-if="form.redfish_targets?.length && collectorDisabled('redfish')" class="notice">Redfish 采集器当前未启用。<button type="button" @click="enableCollector('redfish')">启用 Redfish 采集器</button></p>
    <article v-for="(target, i) in form.redfish_targets" :key="i" class="device-card" :aria-label="`Redfish 设备 ${i + 1}`">
      <div class="card-head"><strong>Redfish · {{ target.name || `设备 ${i + 1}` }}</strong><button type="button" :aria-label="`移除 Redfish 设备 ${i + 1}`" @click="form.redfish_targets?.splice(i, 1)">移除</button></div>
      <div class="fields">
        <label>设备标识<input v-model="target.name" :readonly="savedRedfishTargets.has(target)" :aria-label="`Redfish 设备 ${i + 1} 标识`" maxlength="48" pattern="[a-zA-Z0-9_-]{1,48}" autocomplete="off" /></label>
        <label>Redfish 服务根地址<input v-model="target.url" :aria-label="`Redfish 设备 ${i + 1} 地址`" placeholder="https://bmc.example/redfish/v1" autocomplete="off" /></label>
        <label>用户名<input v-model="target.user" :aria-label="`Redfish 设备 ${i + 1} 用户`" autocomplete="off" /></label>
        <label>密码<input v-model="target.password" :aria-label="`Redfish 设备 ${i + 1} 密码`" type="password" autocomplete="new-password" /></label>
        <label>超时<input v-model="target.timeout" :aria-label="`Redfish 设备 ${i + 1} 超时`" placeholder="5s，最长 1m" /></label>
        <label>私有 CA 文件<input :value="target.tls?.ca_file ?? ''" :aria-label="`Redfish 设备 ${i + 1} CA 文件`" placeholder="采集主机上的 PEM 路径，可选" @input="target.tls = { ...target.tls, ca_file: ($event.target as HTMLInputElement).value }" /></label>
      </div>
      <label class="check"><input type="checkbox" :checked="target.tls?.insecure_skip_verify ?? false" :aria-label="`Redfish 设备 ${i + 1} 跳过证书校验`" @change="target.tls = { ...target.tls, insecure_skip_verify: ($event.target as HTMLInputElement).checked }" />跳过 TLS 证书校验（不推荐，优先配置私有 CA）</label>
    </article>
    <p class="hint">多设备建议将基本设置中的采集间隔调整为 15 秒。慢设备较多时，按每 4 台一批预留各设备的超时时间，整轮采集受全局间隔限制。</p>
    <p class="hint">新增设备会启用对应采集器。移除设备会停止后续采集，已有历史数据按保留策略清理。</p>
  </fieldset>
</template>

<style scoped>
.devices { min-width: 0; border: 1px solid #334155; border-radius: 8px; margin: 0 0 10px; padding: 10px; }
legend, label { color: #94a3b8; }
.hint { color: #94a3b8; font-size: 12px; line-height: 1.6; margin: 4px 0 10px; }
.section-head, .card-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
h4 { margin: 8px 0; font-size: 14px; } small { color: #94a3b8; font-weight: normal; }
.device-card { min-width: 0; padding: 10px; margin: 8px 0 12px; border: 1px solid #334155; border-radius: 8px; }
.card-head strong { overflow-wrap: anywhere; }
.fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; margin-top: 10px; }
label { display: flex; flex-direction: column; gap: 5px; font-size: 12px; min-width: 0; }
input, select, button { box-sizing: border-box; min-width: 0; min-height: 38px; color: #e2e8f0; background: #0f172a; border: 1px solid #475569; border-radius: 6px; padding: 7px 9px; font: inherit; }
input, select { width: 100%; } input[readonly] { color: #94a3b8; background: #1e293b; }
button { cursor: pointer; } button:disabled, fieldset:disabled { opacity: .6; }
.check { flex-direction: row; align-items: center; margin-top: 10px; line-height: 1.5; }
.check input { width: 18px; min-height: 18px; flex-shrink: 0; }
.notice { color: #fcd34d; font-size: 12px; line-height: 1.6; }
@media (max-width: 600px) { .fields { grid-template-columns: minmax(0, 1fr); } input, select { font-size: 16px; } button { min-height: 44px; } }
</style>
