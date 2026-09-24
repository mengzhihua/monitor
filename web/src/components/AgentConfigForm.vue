<script setup lang="ts">
import type { AgentVisual } from '../api'

defineProps<{ form: AgentVisual; disabled: boolean }>()

function addUser(form: AgentVisual) {
  form.users.push({ name: '', token: '', role: 'viewer' })
}
function removeAt(list: string[], i: number) {
  list.splice(i, 1)
}
function addItem(list: string[], input: HTMLInputElement) {
  const value = input.value.trim()
  if (!value) return
  list.push(value)
  input.value = ''
}
</script>

<template>
  <fieldset class="block" :disabled="disabled">
    <legend>基本</legend>
    <label>运行模式
      <select v-model="form.mode" aria-label="运行模式">
        <option value="agent">Agent</option>
        <option value="hub">Hub</option>
      </select>
    </label>
    <label>主机名 <input v-model="form.hostname" aria-label="主机名" placeholder="空则使用系统主机名" /></label>
    <label>采集间隔（秒） <input v-model.number="form.update_every" aria-label="采集间隔" type="number" min="1" max="3600" /></label>
    <label>数据目录 <input v-model="form.data_dir" aria-label="数据目录" placeholder="./data" /></label>
  </fieldset>

  <fieldset class="block" :disabled="disabled">
    <legend>访问</legend>
    <label>Web 服务
      <select v-model="form.web_enabled" aria-label="Web 服务">
        <option value="default">默认开启</option>
        <option value="on">开启</option>
        <option value="off">关闭</option>
      </select>
    </label>
    <label>监听地址 <input v-model="form.listen" aria-label="监听地址" placeholder=":19999" /></label>
    <label>工单 Webhook <input v-model="form.ticket_webhook" aria-label="工单 Webhook" placeholder="https://example/hook" /></label>
    <div class="chips">
      <span>允许来源</span>
      <button v-for="(item, i) in form.allow_from" :key="item + i" type="button" @click="removeAt(form.allow_from, i)">{{ item }} ×</button>
      <input aria-label="添加允许来源" placeholder="IP 或 CIDR，回车添加" @keydown.enter.prevent="addItem(form.allow_from, $event.target as HTMLInputElement)" />
    </div>
    <table>
      <thead><tr><th>账号</th><th>令牌</th><th>角色</th><th></th></tr></thead>
      <tbody>
        <tr v-for="(user, i) in form.users" :key="i">
          <td><input v-model="user.name" aria-label="账号名称" /></td>
          <td><input v-model="user.token" aria-label="账号令牌" type="password" autocomplete="off" /></td>
          <td>
            <select v-model="user.role" aria-label="账号角色">
              <option value="admin">admin</option>
              <option value="troubleshooter">troubleshooter</option>
              <option value="viewer">viewer</option>
            </select>
          </td>
          <td><button type="button" @click="form.users.splice(i, 1)">移除</button></td>
        </tr>
      </tbody>
    </table>
    <button type="button" @click="addUser(form)">添加账号</button>
  </fieldset>

  <fieldset class="block" :disabled="disabled">
    <legend>采集器</legend>
    <p class="hint">启用列表为空表示全部可用采集器。这里不改每个采集器自己的地址和参数。</p>
    <div class="chips">
      <span>启用</span>
      <button v-for="(item, i) in form.collectors_enabled" :key="'e' + item + i" type="button" @click="removeAt(form.collectors_enabled, i)">{{ item }} ×</button>
      <input aria-label="添加启用采集器" placeholder="采集器名，回车添加" @keydown.enter.prevent="addItem(form.collectors_enabled, $event.target as HTMLInputElement)" />
    </div>
    <div class="chips">
      <span>禁用</span>
      <button v-for="(item, i) in form.collectors_disabled" :key="'d' + item + i" type="button" @click="removeAt(form.collectors_disabled, i)">{{ item }} ×</button>
      <input aria-label="添加禁用采集器" placeholder="采集器名，回车添加" @keydown.enter.prevent="addItem(form.collectors_disabled, $event.target as HTMLInputElement)" />
    </div>
  </fieldset>

  <fieldset class="block" :disabled="disabled">
    <legend>告警</legend>
    <label>健康引擎
      <select v-model="form.health_enabled" aria-label="健康引擎">
        <option value="default">默认开启</option>
        <option value="on">开启</option>
        <option value="off">关闭</option>
      </select>
    </label>
    <label class="check"><input v-model="form.health_silent" type="checkbox" aria-label="只评估不通知" /> 只评估，不发送通知</label>
  </fieldset>

  <fieldset class="block" :disabled="disabled">
    <legend>上报 Hub</legend>
    <label class="check"><input v-model="form.stream_enabled" type="checkbox" aria-label="启用上报" /> 启用上报</label>
    <label>协议
      <select v-model="form.stream_protocol" aria-label="上报协议">
        <option value="">默认 stream</option>
        <option value="stream">stream</option>
        <option value="mqtt">mqtt</option>
        <option value="aclk">aclk</option>
      </select>
    </label>
    <label>API Key <input v-model="form.stream_api_key" aria-label="上报 API Key" type="password" autocomplete="off" /></label>
    <div class="chips">
      <span>目的地</span>
      <button v-for="(item, i) in form.stream_destinations" :key="item + i" type="button" @click="removeAt(form.stream_destinations, i)">{{ item }} ×</button>
      <input aria-label="添加上报目的地" placeholder="hub 地址，回车添加" @keydown.enter.prevent="addItem(form.stream_destinations, $event.target as HTMLInputElement)" />
    </div>
  </fieldset>

  <fieldset class="block" :disabled="disabled">
    <legend>Hub 接入</legend>
    <label>存储
      <select v-model="form.hub_storage" aria-label="Hub 存储">
        <option value="">默认 full</option>
        <option value="full">full</option>
        <option value="proxy">proxy</option>
      </select>
    </label>
    <label>Space <input v-model="form.hub_space" aria-label="Space" /></label>
    <label>Room <input v-model="form.hub_room" aria-label="Room" /></label>
    <div class="chips">
      <span>API Keys</span>
      <button v-for="(item, i) in form.hub_api_keys" :key="item + i" type="button" @click="removeAt(form.hub_api_keys, i)">{{ item }} ×</button>
      <input aria-label="添加 Hub API Key" placeholder="回车添加" @keydown.enter.prevent="addItem(form.hub_api_keys, $event.target as HTMLInputElement)" />
    </div>
    <div class="chips">
      <span>对端 Hub</span>
      <button v-for="(item, i) in form.hub_peers" :key="item + i" type="button" @click="removeAt(form.hub_peers, i)">{{ item }} ×</button>
      <input aria-label="添加对端 Hub" placeholder="回车添加" @keydown.enter.prevent="addItem(form.hub_peers, $event.target as HTMLInputElement)" />
    </div>
  </fieldset>
</template>

<style scoped>
.block { border: 1px solid #1e293b; border-radius: 8px; margin: 0 0 10px; padding: 8px 10px; }
legend { color: #94a3b8; padding: 0 4px; }
label { display: flex; gap: 8px; align-items: center; color: #94a3b8; margin: 6px 0; }
.check { color: #e2e8f0; }
input, select, button { background: #0f172a; color: #e2e8f0; border: 1px solid #334155; border-radius: 6px; padding: 4px 8px; font-size: 13px; }
input { min-width: 0; flex: 1; }
button { cursor: pointer; background: #1e293b; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin: 6px 0; color: #94a3b8; }
.chips input { flex: 1; min-width: 160px; }
.hint { color: #64748b; margin: 0 0 6px; font-size: 12px; }
table { width: 100%; border-collapse: collapse; margin: 6px 0; }
td, th { text-align: left; padding: 4px; border-bottom: 1px solid #1e293b; }
fieldset:disabled { opacity: .6; }
</style>
