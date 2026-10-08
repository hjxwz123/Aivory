const REQUEST_HEADER = 'x-aivory-desktop-request'
const validId = (id) => typeof id === 'string' && /^[a-f0-9-]{36}$/.test(id)

// Electron's protocol Request.signal does not follow Chromium fetch aborts.
// Explicit cancellation closes native requests, including active SSE streams.
class ApiRequests {
  constructor() { this.requests = new Map(); this.owners = new WeakSet() }

  entry(id, session) {
    if (!validId(id)) return undefined
    let entry = this.requests.get(id)
    if (entry && entry.session !== session) return undefined
    if (!entry) {
      entry = { session, controller: new AbortController() }
      entry.timer = setTimeout(() => this.finish(id, entry), 30000)
      entry.timer.unref()
      this.requests.set(id, entry)
    }
    return entry
  }

  start(id, contents) {
    const entry = this.entry(id, contents.session)
    if (!entry || (entry.owner && entry.owner !== contents)) return
    entry.owner = contents
    if (!this.owners.has(contents)) {
      this.owners.add(contents)
      contents.once('destroyed', () => {
        for (const [key, request] of this.requests) {
          if (request.owner !== contents) continue
          request.controller.abort()
          this.finish(key, request)
        }
      })
    }
  }

  abort(id, contents) {
    const entry = this.requests.get(id)
    if (entry?.owner === contents) entry.controller.abort()
  }

  attach(id, session) {
    const entry = this.entry(id, session)
    if (!entry) return undefined
    clearTimeout(entry.timer)
    return { signal: entry.controller.signal, finish: () => this.finish(id, entry) }
  }

  finish(id, entry) {
    clearTimeout(entry.timer)
    if (this.requests.get(id) === entry) this.requests.delete(id)
  }
}

module.exports = { ApiRequests, REQUEST_HEADER }
