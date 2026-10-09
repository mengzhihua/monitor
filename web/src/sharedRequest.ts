/** Share one in-flight read; cancellation prevents an obsolete result applying. */
export function sharedRequest<T>(read: (signal: AbortSignal) => Promise<T>, apply: (value: T) => void) {
  let current: { controller: AbortController; done: Promise<void> } | undefined
  return {
    run(signal?: AbortSignal): Promise<void> {
      if (signal?.aborted) return Promise.resolve()
      if (current && !current.controller.signal.aborted) return current.done
      const controller = new AbortController()
      const abort = () => controller.abort()
      signal?.addEventListener('abort', abort, { once: true })
      const done = read(controller.signal).then(value => {
        if (!controller.signal.aborted) apply(value)
      }).catch(() => { /* a later refresh retries transient failures */ }).finally(() => {
        signal?.removeEventListener('abort', abort)
        if (current?.controller === controller) current = undefined
      })
      current = { controller, done }
      return done
    },
    cancel() {
      current?.controller.abort()
      current = undefined
    },
  }
}
