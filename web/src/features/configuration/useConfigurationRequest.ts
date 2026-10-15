import { onScopeDispose, ref } from 'vue'

/** One cancellable read lane. Results may only update state while still current. */
export function useConfigurationRequest() {
  const loading = ref(false)
  const reloading = ref(false)
  let active: AbortController | undefined
  let detach: (() => void) | undefined
  let disposed = false

  function cancel() {
    active?.abort()
    detach?.()
    active = undefined
    detach = undefined
    loading.value = reloading.value = false
  }

  function start(force = false, parent?: AbortSignal) {
    cancel()
    if (disposed) return
    const controller = active = new AbortController()
    const abort = () => controller.abort()
    parent?.addEventListener('abort', abort, { once: true })
    if (parent?.aborted) abort()
    detach = () => parent?.removeEventListener('abort', abort)
    loading.value = true
    reloading.value = force
    return {
      signal: controller.signal,
      current: () => !disposed && active === controller && !controller.signal.aborted,
      finish() {
        if (active !== controller) return
        detach?.()
        detach = undefined
        active = undefined
        loading.value = reloading.value = false
      },
    }
  }

  onScopeDispose(() => { disposed = true; cancel() })
  return { loading, reloading, start, cancel }
}
