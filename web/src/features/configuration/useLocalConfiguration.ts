import { computed, onMounted, ref, shallowRef, toRaw } from 'vue'
import type { AgentVisual, ManageConfig } from '../../api/configuration/types'
import { configurationApi as api } from '../../api/configuration/client'
import { ApiError } from '../../api/transport'
import { configurationAuthError, configurationConflictError, configurationErrorDetail, configurationSaveError, isConfigurationAuthError } from './configurationErrors'
import type { ConfigurationFeedback } from './useConfigurationFeedback'
import { useConfigurationRequest } from './useConfigurationRequest'

export function useLocalConfiguration(canManage: () => boolean, feedback: ConfigurationFeedback) {
  const request = useConfigurationRequest()
  const local = shallowRef<ManageConfig | null>(null)
  const baseline = shallowRef<ManageConfig | null>(null)
  const draft = ref('')
  const editor = ref<'yaml' | 'form'>('yaml')
  const form = ref<AgentVisual | null>(null)
  const formError = ref('')
  let base = ''
  let updated = 0
  let formSnapshot = ''
  let preferForm = true

  function dirty() { return draft.value !== base || (!!form.value && JSON.stringify(form.value) !== formSnapshot) }
  const conflict = computed(() => !!local.value && !!baseline.value && (local.value.updated !== baseline.value.updated || local.value.yaml !== baseline.value.yaml))

  function apply(config: ManageConfig, force = false) {
    local.value = config
    if (!force && dirty()) return // The save revision always belongs to the retained draft.
    baseline.value = config
    draft.value = base = config.yaml
    updated = config.updated
    formError.value = config.form_error || ''
    form.value = config.form ? structuredClone(toRaw(config.form)) : null
    formSnapshot = form.value ? JSON.stringify(form.value) : ''
    if (preferForm && form.value && !formError.value) { editor.value = 'form'; preferForm = false }
  }

  async function load(force = false) {
    const read = request.start(force)
    if (!read) return
    try {
      const config = await api.manageConfig(read.signal)
      if (read.current()) apply(config, force)
    } catch (error) {
      if (read.current()) feedback.error.value = isConfigurationAuthError(error) ? configurationAuthError : `读取本机配置失败：${configurationErrorDetail(error)}`
    } finally { read.finish() }
  }

  function switchEditor(next: 'yaml' | 'form') {
    if (next === editor.value || feedback.busy.value || request.reloading.value) return
    if (dirty()) {
      if (!confirm('切换编辑方式将放弃未保存修改。请先保存或复制草稿，确定切换？')) return
      if (local.value) apply(local.value, true)
    }
    editor.value = next
  }

  async function reload() {
    if (feedback.busy.value || request.loading.value) return
    if (dirty() && !confirm('载入最新配置将放弃当前草稿。请先复制需要保留的内容，确定继续？')) return
    feedback.clear()
    await load(true)
  }

  async function save() {
    if (!canManage() || request.loading.value || conflict.value || !local.value || local.value.writable === false) return
    const mutation = feedback.beginMutation()
    if (!mutation) return
    try {
      const next = editor.value === 'form' && form.value
        ? await api.putManageForm(structuredClone(toRaw(form.value)), updated)
        : await api.putManageConfig(draft.value, updated)
      if (!mutation.current()) return
      apply(next, true)
      feedback.message.value = '配置已保存并校验通过。重启服务后生效。'
    } catch (error) {
      if (!mutation.current()) return
      if (error instanceof ApiError && error.status === 409) {
        await load()
        if (mutation.current()) feedback.error.value = configurationConflictError
      } else feedback.error.value = configurationSaveError(error)
    } finally { mutation.finish() }
  }

  async function rollback() {
    if (!canManage() || feedback.busy.value || request.loading.value || !local.value?.backup) return
    if (!confirm('恢复上一份已保存的配置？当前文件会变成新的备份。')) return
    const mutation = feedback.beginMutation()
    if (!mutation) return
    try {
      const restored = await api.rollbackManageConfig()
      if (!mutation.current()) return
      apply(restored, true)
      feedback.message.value = '已恢复上一份配置。重启服务后生效。'
    } catch (error) {
      if (!mutation.current()) return
      feedback.error.value = error instanceof ApiError && error.status === 404 ? '没有可恢复的备份。' : isConfigurationAuthError(error) ? configurationAuthError : String(error)
    } finally { mutation.finish() }
  }

  async function restart() {
    if (!canManage() || feedback.busy.value) return
    if (!confirm('重启将短暂中断本机监控与 Web 服务（数秒），服务将自动重新启动。确定继续？')) return
    const mutation = feedback.beginMutation()
    if (!mutation) return
    try {
      await api.restartService()
      if (mutation.current()) feedback.message.value = '重启指令已发出，页面将在服务恢复后自动重连。'
    } catch (error) {
      if (mutation.current()) feedback.error.value = isConfigurationAuthError(error) ? configurationAuthError : String(error)
    } finally { mutation.finish() }
  }

  onMounted(() => { void load() })
  return { local, draft, editor, form, formError, conflict, dirty, switchEditor, reload, save, rollback, restart, loading: request.loading, reloading: request.reloading }
}
