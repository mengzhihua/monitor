/** Bound the whole operation, including body decoding, and release listeners/timers. */
export class RequestTimeoutError extends Error {
  constructor() { super('request deadline exceeded'); this.name = 'RequestTimeoutError' }
}
export async function withDeadline<T>(run: (signal: AbortSignal) => Promise<T>, parent: AbortSignal | null | undefined, milliseconds: number): Promise<T> {
  if (parent?.aborted) throw parent.reason ?? new DOMException('Canceled', 'AbortError')
  const controller = new AbortController()
  let rejectAbort: (reason: unknown) => void = () => {}
  const stopped = new Promise<never>((_, reject) => { rejectAbort = reject })
  const cancel = () => {
    const reason = parent?.reason ?? new DOMException('Canceled', 'AbortError')
    controller.abort(reason); rejectAbort(reason)
  }
  parent?.addEventListener('abort', cancel, { once: true })
  const timer = setTimeout(() => {
    const reason = new RequestTimeoutError()
    controller.abort(reason); rejectAbort(reason)
  }, milliseconds)
  try { return await Promise.race([run(controller.signal), stopped]) }
  finally { clearTimeout(timer); parent?.removeEventListener('abort', cancel) }
}
