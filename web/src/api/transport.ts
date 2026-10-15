import { auth } from './auth'

const base = ''

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

/** All HTTP clients share the same authentication and response-error handling. */
async function request(path: string, init: RequestInit, includePathInError = true): Promise<Response> {
  const headers = new Headers(init.headers)
  const token = auth.token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  const response = await fetch(base + path, { ...init, headers })
  if (!response.ok) {
    const prefix = includePathInError ? `${path}: ` : ''
    throw new ApiError(response.status, `${prefix}${response.status} ${await response.text()}`)
  }
  return response
}

export async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await request(path, { signal })
  return response.json() as Promise<T>
}

export async function send<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = {}
  let init: RequestInit = { method, headers, signal }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init = { ...init, body: JSON.stringify(body) }
  }
  const r = await request(path, init)
  if (r.status === 204) return undefined as T
  const text = await r.text()
  return (text ? JSON.parse(text) : undefined) as T
}

export async function post<T>(path: string, body: unknown): Promise<T> {
  return send<T>('POST', path, body)
}

/** Downloads retain their legacy error text; callers own the payload format. */
export function download(path: string, signal?: AbortSignal): Promise<Response> {
  return request(path, { signal }, false)
}
