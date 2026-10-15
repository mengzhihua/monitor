const TOKEN_KEY = 'monitor.token'

/**
 * Access token for agents started with `web.token`. Accepted once via
 * `/?token=...` (then removed from the URL so it is not kept in history) or
 * entered in the UI; kept in sessionStorage and sent as a Bearer header.
 */
let memoryToken = ''
let memoryOnly = false
export const auth = {
  get token(): string {
    if (!memoryOnly) {
      try { memoryToken = sessionStorage.getItem(TOKEN_KEY) ?? '' }
      catch { memoryOnly = true }
    }
    return memoryToken
  },
  set token(t: string) {
    memoryToken = t
    try {
      if (t) sessionStorage.setItem(TOKEN_KEY, t)
      else sessionStorage.removeItem(TOKEN_KEY)
    } catch { memoryOnly = true }
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
