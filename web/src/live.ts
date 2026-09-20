import type { AlarmLogEntry, LiveAlarmMsg, LiveMsg } from './api'
import { api } from './api'

type Handler = (m: LiveMsg) => void

/** Single WebSocket shared by all charts; reconnects with backoff. */
class Live {
  private ws: WebSocket | null = null
  private handlers = new Map<string, Set<Handler>>()
  private retry = 1000
  private wanted = new Set<string>()
  private sendTimer: number | undefined

  connected = false
  onState: ((up: boolean) => void) | null = null
  onAlarm: ((e: AlarmLogEntry) => void) | null = null

  start() {
    if (this.ws) return
    const ws = new WebSocket(api.liveURL([...this.wanted]), api.liveProtocols())
    this.ws = ws
    ws.onopen = () => { this.retry = 1000; this.connected = true; this.onState?.(true); this.flush() }
    ws.onmessage = (e) => {
      const m = JSON.parse(e.data) as LiveMsg | LiveAlarmMsg
      if ('alarm' in m) { this.onAlarm?.(m.alarm); return }
      this.handlers.get(m.chart)?.forEach((h) => h(m))
    }
    ws.onclose = () => {
      this.ws = null; this.connected = false; this.onState?.(false)
      setTimeout(() => this.start(), this.retry)
      this.retry = Math.min(this.retry * 2, 15000)
    }
    ws.onerror = () => ws.close()
  }

  /** Drop the current socket (e.g. after credentials change) and reconnect now. */
  restart() {
    const ws = this.ws
    if (!ws) { this.start(); return }
    ws.onclose = () => { this.ws = null; this.connected = false; this.onState?.(false); this.start() }
    ws.close()
  }

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
      if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ charts: [...this.wanted] }))
    }, 50)
  }
}

export const live = new Live()
