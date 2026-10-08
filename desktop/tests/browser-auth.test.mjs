import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import auth from '../browser-auth.cjs'

test('browser authorization uses only the configured base URL, PKCE, and its desktop session', async () => {
  let challenge
  let opened
  let authorized = false
  let polls = 0
  const requestId = 'r'.repeat(43)
  const flow = new auth.BrowserAuth({ baseUrl: 'https://private.example:8443/',
    fetch: async (url, options) => {
      assert.equal(new URL(url).origin, 'https://private.example:8443')
      assert.equal(options.credentials, 'include')
      assert.equal(options.redirect, 'error')
      const body = JSON.parse(options.body)
      if (url.endsWith('/start')) {
        challenge = body.challenge
        return { ok: true, json: async () => ({ request_id: requestId }) }
      }
      polls++
      assert.equal(body.request_id, requestId)
      assert.equal(createHash('sha256').update(body.verifier).digest('base64url'), challenge)
      return { ok: true, json: async () => ({ status: 'authorized' }) }
    }, openBrowser: async (url) => { opened = new URL(url) },
    onAuthorized: async () => { authorized = true },
  })
  assert.deepEqual(await flow.start(), { status: 'authorized' })
  assert.equal(opened.origin, 'https://private.example:8443')
  assert.equal(opened.pathname, '/desktop/authorize')
  assert.equal(opened.searchParams.get('request_id'), requestId)
  assert.equal(opened.searchParams.has('verifier'), false)
  assert.equal(polls, 1)
  assert.equal(authorized, true)
})

test('duplicate browser attempts are coalesced and cancellation stops polling', async () => {
  let requests = 0
  let opened
  const didOpen = new Promise((resolve) => { opened = resolve })
  const flow = new auth.BrowserAuth({ baseUrl: 'https://example.test',
    fetch: async () => { requests++; return { ok: true, json: async () => ({ request_id: 'a'.repeat(43) }) } },
    openBrowser: async () => { opened() }, onAuthorized: () => { assert.fail('cancelled flow must not sign in') },
  })
  const running = flow.start()
  await didOpen
  assert.deepEqual(await flow.start(), { status: 'busy' })
  flow.cancel()
  assert.deepEqual(await running, { status: 'cancelled' })
  assert.equal(requests, 1)
})
