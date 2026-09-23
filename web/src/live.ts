import type { AlarmLogEntry, LiveAlarmMsg, LiveMsg } from './api'
import { api, selection } from './api'

type Handler = (m: LiveMsg) => void

/** Single WebSocket shared by all charts; reconnects with backoff. */
class Live {
  private ws: WebSocket | null = null
  private handlers = new Map<string, Set<Handler>>()
  private retry = 1000
  private retryTimer: number | undefined
  private enabled = false
  private wanted = new Set<string>()
  private sendTimer: number | undefined

  connected = false
  onState: ((up: boolean) => void) | null = null
  onAlarm: ((e: AlarmLogEntry) => void) | null = null

  constructor() {
    window.addEventListener('offline', () => this.dropSocket())
    window.addEventListener('online', () => { if (this.enabled) this.start() })
  }

  // The server interprets [] as "all charts". One blank ID maps to an empty
  // subscription while leaving the socket open for alarm events.
  private subscriptions() { return this.wanted.size ? [...this.wanted] : [' '] }

  start() {
    this.enabled = true
    if (this.ws || !navigator.onLine) return
    clearTimeout(this.retryTimer)
    const ws = new WebSocket(api.liveURL(this.subscriptions()), api.liveProtocols())
    this.ws = ws
    ws.onopen = () => {
      if (this.ws !== ws) return
      this.retry = 1000; this.connected = true; this.onState?.(true); this.flush()
    }
    ws.onmessage = (e) => {
      if (this.ws !== ws) return
      let m: LiveMsg | LiveAlarmMsg
      try { m = JSON.parse(e.data) } catch { return }
      if ((m.node ?? '') !== selection.node) return
      if ('alarm' in m) { this.onAlarm?.(m.alarm); return }
      this.handlers.get(m.chart)?.forEach((h) => h(m))
    }
    ws.onclose = () => {
      if (this.ws !== ws) return
      this.ws = null; this.connected = false; this.onState?.(false)
      if (this.enabled && navigator.onLine) {
        this.retryTimer = window.setTimeout(() => this.start(), this.retry)
        this.retry = Math.min(this.retry * 2, 15000)
      }
    }
    ws.onerror = () => ws.close()
  }

  private dropSocket() {
    clearTimeout(this.retryTimer)
    clearTimeout(this.sendTimer)
    const old = this.ws
    this.ws = null
    this.connected = false
    this.onState?.(false)
    old?.close()
  }

  stop() { this.enabled = false; this.dropSocket() }

  /** Replace immediately; an offline TCP close handshake can take 30 seconds. */
  restart() { this.dropSocket(); this.start() }

  subscribe(chart: string, h: Handler) {
    let set = this.handlers.get(chart)
    if (!set) { set = new Set(); this.handlers.set(chart, set) }
    set.add(h)
    this.wanted.add(chart)
    this.flush()
    return () => {
      set!.delete(h)
      if (set!.size === 0) { this.handlers.delete(chart); this.wanted.delete(chart); this.flush() }
    }
  }

  private flush() {
    // Debounce subscription updates: many charts mount at once.
    clearTimeout(this.sendTimer)
    this.sendTimer = window.setTimeout(() => {
      if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ charts: this.subscriptions() }))
    }, 50)
  }
}

export const live = new Live()
