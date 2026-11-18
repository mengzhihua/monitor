import { auth } from './auth'
import { RequestTimeoutError, withDeadline } from './deadline'

const base = ''
export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) { super(message); this.status = status }
}

/** The deadline encloses fetch, error bodies and success decoding. Never retries writes. */
async function request<T>(path: string, init: RequestInit, decode: (response: Response) => Promise<T>, timeoutMs: number, includePathInError = true): Promise<T> {
  const headers = new Headers(init.headers)
  const token = auth.token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  const mutation = !['GET', 'HEAD'].includes(init.method || 'GET')
  try {
    return await withDeadline(async signal => {
      const response = await fetch(base + path, { ...init, headers, signal })
      if (!response.ok) {
        const prefix = includePathInError ? `${path}: ` : ''
        throw new ApiError(response.status, `${prefix}${response.status} ${await response.text()}`)
      }
      return decode(response)
    }, init.signal, timeoutMs)
  } catch (error) {
    // Caller cancellation is not a failure and must retain its original identity.
    if (init.signal?.aborted) throw error
    const uncertain = mutation ? '操作结果尚未确认，请先重新读取状态核对，避免重复提交。' : '请检查连接后重试。'
    if (error instanceof RequestTimeoutError) throw new ApiError(0, `请求超时。${uncertain}`)
    if (error instanceof TypeError) throw new ApiError(0, `无法连接服务器或响应中断。${uncertain}`)
    if (error instanceof SyntaxError) throw new ApiError(0, `服务器响应无法解析。${uncertain}`)
    throw error
  }
}

export function get<T>(path: string, signal?: AbortSignal, timeoutMs = 30_000): Promise<T> {
  return request(path, { signal }, response => response.json() as Promise<T>, timeoutMs)
}

export function send<T>(method: string, path: string, body?: unknown, signal?: AbortSignal, timeoutMs = 60_000): Promise<T> {
  const headers: Record<string, string> = {}
  const init: RequestInit = { method, headers, signal }
  if (body !== undefined) { headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body) }
  return request(path, init, async response => {
    if (response.status === 204) return undefined as T
    const text = await response.text()
    return (text ? JSON.parse(text) : undefined) as T
  }, timeoutMs)
}
export function post<T>(path: string, body: unknown): Promise<T> { return send<T>('POST', path, body) }

/** These bounded report downloads are buffered before returning so a stalled body cannot hang the UI. */
export function download(path: string, signal?: AbortSignal): Promise<Response> {
  return request(path, { signal }, async response => new Response(await response.blob(), {
    status: response.status, statusText: response.statusText, headers: response.headers,
  }), 120_000, false)
}
