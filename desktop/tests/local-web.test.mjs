import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, mkdir, writeFile, readFile, symlink, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import localWeb from '../local-web.cjs'

const { APP_URL, createLocalHandler } = localWeb
const baseUrl = 'https://server.example/'

test('only the packaged HTTPS origin is a local app URL; blob previews and API servers receive no bridge', () => {
  assert.equal(localWeb.localAppUrl(APP_URL), true)
  assert.equal(localWeb.localAppUrl('blob:' + APP_URL + 'preview'), false)
  assert.equal(localWeb.localAppUrl(baseUrl), false)
  assert.equal(localWeb.localAppUrl('file:///index.html'), false)
})

test('local routes/assets stay bundled, and missing assets/traversal cannot expose files', async (t) => {
  const dir = await mkdtemp(path.join(tmpdir(), 'aivory-local-web-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  const webDir = path.join(dir, 'web')
  await mkdir(path.join(webDir, 'assets'), { recursive: true })
  await writeFile(path.join(webDir, 'index.html'), '<html>Local app</html>')
  await writeFile(path.join(webDir, 'assets/app.js'), 'console.log("bundled")')
  await writeFile(path.join(dir, 'secret'), 'outside')
  await symlink(path.join(dir, 'secret'), path.join(webDir, 'secret.txt'))
  const handler = createLocalHandler({ webDir, baseUrl,
    fetch: () => assert.fail('Static files must not request the server'),
    fileFetch: async url => new Response(await readFile(fileURLToPath(url))),
  })
  for (const route of ['', 'admin/models', 'chat/a-long-id']) {
    const response = await handler(new Request(APP_URL + route))
    assert.equal(await response.text(), '<html>Local app</html>')
    assert.match(response.headers.get('content-type'), /text\/html/)
  }
  const script = await handler(new Request(APP_URL + 'assets/app.js'))
  assert.match(script.headers.get('content-type'), /text\/javascript/)
  assert.match(await script.text(), /bundled/)
  for (const route of ['assets/missing.js', 'missing.svg', '%2e%2e%2fsecret', '%5csecret', 'secret.txt', '%00', '%zz']) {
    assert.equal((await handler(new Request(APP_URL + route))).status, 404, route)
  }
  assert.equal(await (await handler(new Request(APP_URL, { method: 'HEAD' }))).text(), '')
  assert.equal((await handler(new Request(APP_URL, { method: 'POST', body: 'no' }))).status, 405)
})

test('API transport preserves signed paths, uploads and headers while keeping cookies on the server', async () => {
  let failure = false
  const handler = createLocalHandler({ baseUrl, onFailure: () => { failure = true },
    fetch: async (url, options) => {
      assert.equal(url, baseUrl + 'api/upload?id=a%2Fb')
      assert.equal(options.headers.get('origin'), 'https://server.example')
      assert.equal(options.headers.get('cookie'), null)
      assert.equal(options.headers.get('authorization'), 'Bearer access')
      assert.equal(options.headers.get('x-signature'), 'signature')
      assert.equal(options.credentials, 'include')
      assert.equal(await new Response(options.body).text(), 'uploaded-body')
      return new Response('ok', { headers: { 'set-cookie': 'secret=1; HttpOnly', 'content-encoding': 'gzip', 'content-length': '900' } })
    },
  })
  const response = await handler(new Request(APP_URL + 'api/upload?id=a%2Fb', {
    method: 'POST', body: 'uploaded-body', headers: { cookie: 'local=untrusted', origin: new URL(APP_URL).origin,
      authorization: 'Bearer access', 'x-signature': 'signature' },
  }))
  assert.equal(await response.text(), 'ok')
  for (const name of ['set-cookie', 'content-encoding', 'content-length']) assert.equal(response.headers.get(name), null)
  assert.equal(failure, false)
})

test('streaming forwards the first chunk without buffering and cancellation reaches the upstream', async () => {
  let cancelled = false
  const handler = createLocalHandler({ baseUrl, fetch: async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode('first')) },
    cancel() { cancelled = true },
  })) })
  const response = await handler(new Request(APP_URL + 'api/stream'))
  const reader = response.body.getReader()
  assert.equal(new TextDecoder().decode((await reader.read()).value), 'first')
  await reader.cancel()
  assert.equal(cancelled, true)
})

test('API redirects remain local and aborted requests do not show the offline UI', async () => {
  const controller = new AbortController()
  let failures = 0
  const handler = createLocalHandler({ baseUrl, onFailure: () => { failures++ }, fetch: async (_url, options) => {
    if (options.signal.aborted) throw new Error('cancelled')
    return new Response(null, { status: 302, headers: { location: '/api/download/next?key=123' } })
  } })
  const response = await handler(new Request(APP_URL + 'api/download'))
  assert.equal(response.headers.get('location'), APP_URL + 'api/download/next?key=123')
  controller.abort()
  await assert.rejects(handler(new Request(APP_URL + 'api/download', { signal: controller.signal })))
  assert.equal(failures, 0)
})
