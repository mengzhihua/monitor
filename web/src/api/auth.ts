const TOKEN_KEY = 'monitor.token'

/**
 * Access token for agents started with `web.token`. Accepted once via
 * `/?token=...` (then removed from the URL so it is not kept in history) or
 * entered in the UI; kept in sessionStorage and sent as a Bearer header.
 */
let memoryToken = ''
let memoryOnly = false
const listeners = new Set<() => void>()
function notify() { for (const listener of [...listeners]) if (listeners.has(listener)) listener() }
function storedTokenChanged(event: StorageEvent) {
  if (event.key !== TOKEN_KEY && event.key !== null) return
  try { if (event.storageArea === sessionStorage) void auth.token }
  catch { /* Storage can become unavailable while a window is open. */ }
}
export const auth = {
  get token(): string {
    if (!memoryOnly) {
      let changed = false
      try {
        const stored = sessionStorage.getItem(TOKEN_KEY) ?? ''
        if (stored !== memoryToken) {
          memoryToken = stored
          changed = true
        }
      }
      catch { memoryOnly = true }
      if (changed) notify()
    }
    return memoryToken
  },
  set token(t: string) {
    const changed = memoryToken !== t
    memoryToken = t
    try {
      if (t) sessionStorage.setItem(TOKEN_KEY, t)
      else sessionStorage.removeItem(TOKEN_KEY)
    } catch { memoryOnly = true }
    // Publish only after both caches agree, so listeners may safely read token.
    if (changed) notify()
  },
  /** Observe application logins and session storage updates without a UI dependency. */
  onChange(listener: () => void): () => void {
    void this.token
    const first = listeners.size === 0
    listeners.add(listener)
    if (first && typeof window !== 'undefined') window.addEventListener('storage', storedTokenChanged)
    return () => {
      if (!listeners.delete(listener)) return
      if (!listeners.size && typeof window !== 'undefined') window.removeEventListener('storage', storedTokenChanged)
    }
  },
  /** Import `?token=` from the page URL and scrub it from the address bar. */
  fromURL() {
    const u = new URL(location.href)
    const t = u.searchParams.get('token')
    if (t === null) return
    this.token = t
    u.searchParams.delete('token')
    history.replaceState(history.state, '', u.pathname + u.search + u.hash)
  },
}
