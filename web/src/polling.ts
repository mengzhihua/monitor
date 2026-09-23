import { onBeforeUnmount, onMounted, watch } from 'vue'
import { pageVisible } from './visibility'

/** Poll only in the foreground and wait for completion before scheduling again. */
export function usePolling(task: (signal: AbortSignal) => Promise<void>, delay: number) {
  let controller: AbortController | undefined
  let timer: number | undefined
  let disposed = false
  function cancel() {
    clearTimeout(timer)
    controller?.abort()
    controller = undefined
  }
  async function refresh() {
    cancel()
    if (disposed || !pageVisible.value) return
    const current = controller = new AbortController()
    try {
      await task(current.signal)
    } finally {
      if (controller === current && !disposed && pageVisible.value) {
        controller = undefined
        timer = window.setTimeout(() => { void refresh() }, delay)
      }
    }
  }
  watch(pageVisible, (visible) => { if (visible) void refresh(); else cancel() })
  onMounted(() => { void refresh() })
  onBeforeUnmount(() => { disposed = true; cancel() })
  return refresh
}
