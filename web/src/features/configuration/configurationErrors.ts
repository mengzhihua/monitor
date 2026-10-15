import { ApiError } from '../../api/transport'

export const configurationAuthError = '没有管理权限或登录已失效，请重新登录。'
export const configurationConflictError = '配置已在别处被修改，草稿已保留。请核对最新配置后重新编辑。'

export function isConfigurationAuthError(error: unknown) {
  return error instanceof ApiError && [401, 403].includes(error.status)
}

/** Extract the backend's explanation without exposing the transport wrapper. */
export function configurationErrorDetail(error: unknown): string {
  const raw = error instanceof Error ? error.message : String(error)
  if (!(error instanceof ApiError)) return raw
  const body = raw.replace(/^\S+: \d+ /, '')
  try {
    const parsed = JSON.parse(body) as { error?: unknown; message?: unknown }
    const text = parsed?.error ?? parsed?.message
    if (typeof text === 'string' && text) return text
  } catch { /* The server can also return a plain text explanation. */ }
  return body || raw
}

export function configurationSaveError(error: unknown) {
  if (isConfigurationAuthError(error)) return configurationAuthError
  if (error instanceof ApiError && error.status === 400) return `配置无效，未保存：${configurationErrorDetail(error)}`
  return String(error)
}
