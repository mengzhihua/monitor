import { onScopeDispose, ref } from 'vue'

/** Coordinate the two editors without owning either editor's data or revision. */
export function useConfigurationFeedback() {
  const busy = ref(false)
  const error = ref('')
  const message = ref('')
  let disposed = false
  let active: object | undefined

  function clear() { error.value = ''; message.value = '' }
  function beginMutation() {
    if (disposed || busy.value) return
    const operation = active = {}
    busy.value = true
    clear()
    return {
      current: () => !disposed && active === operation,
      finish() {
        if (active !== operation) return
        active = undefined
        busy.value = false
      },
    }
  }

  onScopeDispose(() => { disposed = true; active = undefined })
  return { busy, error, message, clear, beginMutation }
}

export type ConfigurationFeedback = ReturnType<typeof useConfigurationFeedback>
