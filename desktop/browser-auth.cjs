const { randomBytes, createHash } = require('node:crypto')
const { setTimeout: delay } = require('node:timers/promises')

class BrowserAuth {
  constructor({ baseUrl, fetch, openBrowser, onAuthorized }) {
    Object.assign(this, { baseUrl, fetch, openBrowser, onAuthorized })
  }

  async cancel() {
    this.controller?.abort()
    // Wait for any token exchange to settle before a password login can write
    // its session cookies. A cancelled attempt must never replace that login.
    await this.running
  }

  start() {
    if (this.controller) return Promise.resolve({ status: 'busy' })
    this.running = this.run()
    return this.running
  }

  async run() {
    const controller = new AbortController()
    this.controller = controller
    const timeout = setTimeout(() => controller.abort(new Error('expired')), 5 * 60 * 1000)
    const verifier = randomBytes(32).toString('base64url')
    const challenge = createHash('sha256').update(verifier).digest('base64url')
    const request = async (path, body) => {
      const response = await this.fetch(new URL(path, this.baseUrl).href, {
        method: 'POST', credentials: 'include', cache: 'no-store', redirect: 'error',
        headers: { 'Content-Type': 'application/json', Origin: new URL(this.baseUrl).origin },
        body: JSON.stringify(body),
        signal: AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]),
      })
      if (!response.ok) throw new Error(response.status === 404 && path.endsWith('/token') ? 'expired' : 'failed')
      const result = await response.json()
      controller.signal.throwIfAborted()
      return result
    }
    try {
      const started = await request('/api/auth/desktop/start', { challenge })
      if (!/^[A-Za-z0-9_-]{43}$/.test(started.request_id)) throw new Error('failed')
      const url = new URL('/desktop/authorize', this.baseUrl)
      url.searchParams.set('request_id', started.request_id)
      await this.openBrowser(url.href)
      while (!controller.signal.aborted) {
        await delay(2000, undefined, { signal: controller.signal })
        const result = await request('/api/auth/desktop/token', { request_id: started.request_id, verifier })
        if (result.status === 'pending') continue
        if (result.status === 'authorized') {
          await this.onAuthorized()
          return { status: 'authorized' }
        }
        if (result.status === 'denied') return { status: 'denied' }
        throw new Error('failed')
      }
      return { status: 'cancelled' }
    } catch (error) {
      return { status: controller.signal.aborted
        ? (controller.signal.reason?.message === 'expired' ? 'expired' : 'cancelled')
        : (error.message === 'expired' ? 'expired' : 'failed') }
    } finally {
      clearTimeout(timeout)
      if (this.controller === controller) this.controller = undefined
    }
  }
}

module.exports = { BrowserAuth }
