import { defineAsyncComponent, type Component } from 'vue'
import type { PlatformInfo } from '../../api/plugins/types'
import { pluginAccess } from '../plugins/model/pluginPolicy'
import type { WorkspaceScope } from './model/workspaceScope'

export type WorkspaceId = 'operations' | 'charts' | 'services' | 'alarms' | 'rules' | 'functions' | 'logs' | 'shell' | 'ai' | 'weights' | 'contexts' | 'nodes' | 'hub' | 'cloud' | 'templates' | 'config' | 'plugins'
export interface ModuleContext { hub: boolean; admin: boolean; personal: boolean; authenticated: boolean; functions: number; logs: boolean; platform?: PlatformInfo }
export interface WorkspaceModule {
  id: WorkspaceId; label: string; title: string; description: string
  group: '监控' | '诊断分析' | '管理'; scope: WorkspaceScope; component: Component
  available?: (context: ModuleContext) => boolean
}

// One registration controls the desktop rail, phone launcher, URL and lazy view.
// API permissions remain authoritative; availability only controls navigation.
export const modules: WorkspaceModule[] = [
  { id: 'operations', scope: 'global', label: '运维总览', title: '运维总览', description: '问题、主机资源与处理进度', group: '监控', component: defineAsyncComponent(() => import('../../components/OperationsPanel.vue')) },
  { id: 'charts', scope: 'node', label: '指标图表', title: '指标图表', description: '按资源分类浏览实时与历史指标', group: '监控', component: defineAsyncComponent(() => import('../metrics/MetricsWorkspace.vue')) },
  { id: 'services', scope: 'global', label: '服务与拓扑', title: '服务与拓扑', description: '服务状态与可用性', group: '监控', component: defineAsyncComponent(() => import('../../components/ServicesPanel.vue')) },
  { id: 'alarms', scope: 'node', label: '告警', title: '告警中心', description: '当前告警与状态变化', group: '监控', component: defineAsyncComponent(() => import('../../components/AlarmsPanel.vue')) },
  { id: 'ai', scope: 'node', label: 'AI 分析', title: 'AI 分析', description: '分析监控快照与生成修复方案', group: '诊断分析', component: defineAsyncComponent(() => import('../../components/AIAnalysisPanel.vue')) },
  { id: 'functions', scope: 'node', label: '进程与诊断', title: 'Functions（实时进程表等）', description: '实时进程与诊断', group: '诊断分析', available: c => c.functions > 0, component: defineAsyncComponent(() => import('../../components/FunctionsPanel.vue')) },
  { id: 'logs', scope: 'node', label: '日志', title: '日志', description: '查询当前节点日志', group: '诊断分析', available: c => c.logs, component: defineAsyncComponent(() => import('../../components/LogsPanel.vue')) },
  { id: 'shell', scope: 'node', label: 'WebShell', title: 'WebShell 主机终端', description: '本机 Shell 与 SSH 交互终端', group: '诊断分析', available: c => c.admin && c.personal && c.authenticated, component: defineAsyncComponent(() => import('../../components/ShellPanel.vue')) },
  { id: 'weights', scope: 'node', label: '异常顾问', title: '异常顾问 / 关联分析', description: '异常与指标关联', group: '诊断分析', component: defineAsyncComponent(() => import('../../components/WeightsPanel.vue')) },
  { id: 'contexts', scope: 'node', label: '指标目录', title: 'Context 总览', description: '按指标类型浏览', group: '诊断分析', component: defineAsyncComponent(() => import('../../components/ContextsPanel.vue')) },
  { id: 'rules', scope: 'local', label: '告警规则', title: '本机告警规则', description: '查看与管理本机规则', group: '管理', component: defineAsyncComponent(() => import('../../components/AlertRulesPanel.vue')) },
  { id: 'nodes', scope: 'global', label: '节点管理', title: '节点管理', description: '节点连接、切换与离线清理', group: '管理', available: c => c.hub, component: defineAsyncComponent(() => import('./NodeInventory.vue')) },
  { id: 'hub', scope: 'global', label: 'Hub', title: 'Hub：Space / Room / claim', description: '节点分组与接入', group: '管理', available: c => c.hub, component: defineAsyncComponent(() => import('../../components/HubPanel.vue')) },
  { id: 'cloud', scope: 'global', label: 'Cloud', title: 'Cloud 控制台', description: '云节点控制台', group: '管理', available: c => c.hub, component: defineAsyncComponent(() => import('../../components/CloudPanel.vue')) },
  { id: 'templates', scope: 'global', label: '模板', title: 'Zabbix 模板 / 主机组 / 资产', description: '主机组与资产模板', group: '管理', available: c => c.hub, component: defineAsyncComponent(() => import('../../components/TemplatesPanel.vue')) },
  { id: 'plugins', scope: 'local', label: '插件管理', title: '本机插件管理', description: '插件目录、运行状态与启停', group: '管理', available: c => pluginAccess(c.platform, c.admin ? 'admin' : 'viewer', c.authenticated).read, component: defineAsyncComponent(() => import('../plugins/PluginsWorkspace.vue')) },
  { id: 'config', scope: 'local', label: '配置', title: '配置：本机与节点', description: '采集器、设备与节点配置', group: '管理', available: c => c.admin, component: defineAsyncComponent(() => import('../../components/ConfigPanel.vue')) },
]

export function resolveWorkspace(value: string | null): WorkspaceId {
  return modules.find(module => module.id === value)?.id ?? 'operations'
}
