/** Keep desktop native requests cancellable; web fetch behavior is unchanged. */
export async function apiFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const bridge = typeof window !== 'undefined' ? window.aivoryDesktop : undefined
  const signal = init.signal
  if (!bridge?.startApiRequest || !bridge.abortApiRequest || !signal) return fetch(input, init)
  const target = new URL(input, window.location.href)
  if (target.origin !== window.location.origin || !target.pathname.startsWith('/api/')) return fetch(input, init)
  signal.throwIfAborted()
  const id = crypto.randomUUID()
  const abort = () => bridge.abortApiRequest!(id)
  const cleanup = () => signal.removeEventListener('abort', abort)
  bridge.startApiRequest(id)
  signal.addEventListener('abort', abort, { once: true })
  const headers = new Headers(init.headers)
  headers.set('x-aivory-desktop-request', id)
  try {
    const response = await fetch(input, { ...init, headers })
    if (!response.body) { cleanup(); return response }
    const reader = response.body.getReader()
    const body = new ReadableStream<Uint8Array>({
      async pull(controller) {
        try {
          const { done, value } = await reader.read()
          if (done) { cleanup(); controller.close() }
          else controller.enqueue(value)
        } catch (error) { cleanup(); controller.error(error) }
      },
      cancel(reason) { abort(); cleanup(); return reader.cancel(reason) },
    }, { highWaterMark: 0 })
    const result = new Response(body, { status: response.status, statusText: response.statusText, headers: response.headers })
    for (const key of ['url', 'redirected', 'type'] as const) Object.defineProperty(result, key, { value: response[key] })
    return result
  } catch (error) { abort(); cleanup(); throw error }
}
