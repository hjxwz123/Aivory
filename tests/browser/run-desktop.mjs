import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, readFile, writeFile, rm, mkdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { once } from 'node:events'
import puppeteer from 'puppeteer-core'
import { prepareApp } from '../../desktop/prepare.mjs'
import localWeb from '../../desktop/local-web.cjs'
const { APP_URL } = localWeb

const requireDesktop = createRequire(new URL('../../desktop/package.json', import.meta.url))
const currentVersion = JSON.parse(await readFile(new URL('../../package.json', import.meta.url), 'utf8')).version
const updateVersion = currentVersion.split('-')[0].replace(/(\d+)$/, (patch) => String(Number(patch) + 1)) + '-beta.1'
const electron = requireDesktop('electron')
const { WebSocketServer } = requireDesktop('ws')
const temp = await mkdtemp(path.join(tmpdir(), 'aivory-desktop-smoke-'))
const appDirectory = path.join(temp, 'app')
let browser
let child
let logs = ''
let baseUrl
let oauthUrl
let refuseConnections = false
let browserLoginChallenge
const requestAgents = new Map()
const browserRequestId = 's'.repeat(43)
let abortedStream = false
let voiceHeaders
let paymentBody

const fixture = createServer((req, res) => {
  requestAgents.set(req.url, req.headers['user-agent'])
  if (refuseConnections) { req.socket.destroy(); return }
  const hasSession = req.headers.cookie?.includes('desktop_session=smoke') || false
  if (req.url === '/api/auth/desktop/start') {
    let raw = ''
    req.on('data', (chunk) => { raw += chunk })
    req.on('end', () => {
      browserLoginChallenge = JSON.parse(raw).challenge
      res.setHeader('Content-Type', 'application/json')
      res.end(JSON.stringify({ request_id: browserRequestId }))
    })
  } else if (req.url === '/api/auth/desktop/token') {
    let raw = ''
    req.on('data', (chunk) => { raw += chunk })
    req.on('end', async () => {
      const { verifier, request_id } = JSON.parse(raw)
      const { createHash } = await import('node:crypto')
      assert.equal(createHash('sha256').update(verifier).digest('base64url'), browserLoginChallenge)
      assert.equal(request_id, browserRequestId)
      res.setHeader('Set-Cookie', 'browser_authorized=true; HttpOnly; SameSite=Lax; Path=/; Max-Age=3600')
      res.setHeader('Content-Type', 'application/json')
      res.end(JSON.stringify({ status: 'authorized' }))
    })
  } else if (req.url === '/api/browser-session') {
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ authorized: req.headers.cookie?.includes('browser_authorized=true') || false }))
  } else if (req.url === '/api/init') {
    if (!hasSession) res.setHeader('Set-Cookie', 'desktop_session=smoke; HttpOnly; SameSite=Lax; Path=/; Max-Age=3600')
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ hasSession }))
  } else if (req.url === '/api/session') {
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ hasSession }))
  } else if (req.url === '/api/stream') {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' })
    res.write('data: first-visible-content\n\n')
    const timer = setTimeout(() => res.end('data: done\n\n'), 1500)
    res.on('close', () => clearTimeout(timer))
  } else if (req.url === '/api/echo') {
    const chunks = []
    req.on('data', chunk => chunks.push(chunk))
    req.on('end', () => {
      res.setHeader('Content-Type', 'application/json')
      res.end(JSON.stringify({ body: Buffer.concat(chunks).toString(), origin: req.headers.origin,
        cookie: req.headers.cookie, contentType: req.headers['content-type'], signature: req.headers['x-signature'] }))
    })
  } else if (req.url === '/api/compressed') {
    import('node:zlib').then(({ gzipSync }) => {
      res.writeHead(200, { 'Content-Encoding': 'gzip', 'Content-Type': 'application/json' })
      res.end(gzipSync(JSON.stringify({ compressed: true, hasSession })))
    })
  } else if (req.url === '/api/long-stream') {
    res.writeHead(200, { 'Content-Type': 'text/event-stream' })
    res.write('data: started\n\n')
    const timer = setInterval(() => res.write(': heartbeat\n\n'), 100)
    res.on('close', () => { clearInterval(timer); abortedStream = true })
  } else if (req.url.startsWith('/pay/checkout')) {
    const chunks = []
    req.on('data', chunk => chunks.push(chunk))
    req.on('end', () => {
      paymentBody = Buffer.concat(chunks).toString()
      res.setHeader('Content-Type', 'text/html')
      res.end('<html><body><a id="return" href="/subscription?payment_order=test-order">Finish checkout</a></body></html>')
    })
  } else if (req.url === '/oauth/start') {
    res.writeHead(302, { Location: `${oauthUrl}/authorize` })
    res.end()
  } else {
    if (!hasSession) res.setHeader('Set-Cookie', 'desktop_session=smoke; HttpOnly; SameSite=Lax; Path=/; Max-Age=3600')
    res.setHeader('Content-Type', 'text/html; charset=utf-8')
    res.end(`<!doctype html><html><head><title>Aivory Desktop Fixture</title><style>html,body { margin:0; height:100%; } :root { --color-bg:#ffffff; } #root { height:100dvh; min-height:0; display:flow-root; }</style></head>
      <body data-had-session="${hasSession}"><div id="root"><h1 id="smoke">Aivory desktop</h1>
      <a id="preview" href="/share/example" target="_blank" rel="noopener">Preview</a>
      <a id="blocked" href="file:///etc/passwd">Blocked navigation</a>
      <textarea id="draft"></textarea></div></body></html>`)
  }
})
const voiceServer = new WebSocketServer({ noServer: true })
fixture.on('upgrade', (req, socket, head) => {
  if (req.url !== '/api/audio/stream') { socket.destroy(); return }
  voiceHeaders = req.headers
  voiceServer.handleUpgrade(req, socket, head, client => {
    client.send(JSON.stringify({ type: 'ready' }))
    client.on('message', (data, binary) => client.send(JSON.stringify({ binary, bytes: data.length, text: binary ? '' : data.toString() })))
  })
})
const oauth = createServer((_req, res) => {
  res.writeHead(302, { Location: `${baseUrl}/oauth/return` })
  res.end()
})

async function listen(server) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return server.address().port
}

async function launch(expectOffline = false) {
  const portProbe = createServer()
  const debugPort = await listen(portProbe)
  await new Promise((resolve) => portProbe.close(resolve))
  const env = { ...process.env, AIVORY_DESKTOP_BASE_URL: 'https://invalid-runtime-override.example' }
  delete env.ELECTRON_RUN_AS_NODE
  child = spawn(electron, [
    appDirectory,
    `--user-data-dir=${path.join(temp, 'profile')}`,
    `--remote-debugging-port=${debugPort}`,
    '--disable-background-timer-throttling',
    '--disable-renderer-backgrounding',
  ], { env, stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout.on('data', (chunk) => { logs += chunk.toString() })
  child.stderr.on('data', (chunk) => { logs += chunk.toString() })
  let endpoint
  const deadline = Date.now() + 20000
  while (!endpoint && Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`Electron exited early: ${logs}`)
    try {
      const response = await fetch(`http://127.0.0.1:${debugPort}/json/version`)
      endpoint = (await response.json()).webSocketDebuggerUrl
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100))
    }
  }
  assert.ok(endpoint, `Electron debugger did not start: ${logs}`)
  browser = await puppeteer.connect({ browserWSEndpoint: endpoint, defaultViewport: null })
  const target = await browser.waitForTarget((target) => expectOffline
    ? target.url().endsWith('/offline.html') : target.url().startsWith(APP_URL), { timeout: 20000 })
  const page = await target.page()
  await page.waitForSelector(expectOffline ? '#title' : '#smoke')
  if (!expectOffline) await page.waitForFunction(() => document.body.dataset.ready === 'true')
  return page
}

async function stop() {
  const processToClose = child
  const exited = processToClose && processToClose.exitCode === null ? once(processToClose, 'exit') : Promise.resolve()
  if (browser) {
    await browser.close().catch(() => browser?.disconnect())
    browser = undefined
  }
  if (processToClose?.exitCode === null) {
    const timer = setTimeout(() => processToClose.kill('SIGKILL'), 5000)
    await exited
    clearTimeout(timer)
  }
  child = undefined
}

async function waitWindowState(page, minimized) {
  const deadline = Date.now() + 15000
  let state
  do {
    state = await page.evaluate(() => window.desktopSmoke.native('state'))
    const minimizedOrHidden = state.minimized || (process.platform === 'darwin' && !state.visible)
    if (minimizedOrHidden === minimized) return state
    await new Promise((resolve) => setTimeout(resolve, 100))
  } while (Date.now() < deadline)
  assert.fail(`Native window state: ${JSON.stringify(state)}`)
}

try {
  baseUrl = `http://127.0.0.1:${await listen(fixture)}`
  oauthUrl = `http://localhost:${await listen(oauth)}`
  process.env.AIVORY_DESKTOP_BASE_URL = baseUrl
  const webDir = path.join(temp, 'web')
  await mkdir(webDir)
  await writeFile(path.join(webDir, 'index.html'), `<!doctype html><html><head><title>Packaged local frontend</title><style>html,body { margin:0; height:100%; } :root { --color-bg:#ffffff; } #root { height:100dvh; min-height:0; display:flow-root; }</style></head>
    <body><div id="root"><h1 id="smoke">Aivory desktop — bundled UI</h1>
    <a id="preview" href="/share/example" target="_blank" rel="noopener">Preview</a>
    <a id="blocked" href="file:///etc/passwd">Blocked navigation</a>
    <a id="external" href="${baseUrl}/oauth/start">Website</a>
    <textarea id="draft"></textarea></div><script>
    async function init() { const session = await fetch('/api/init').then(r=>r.json()); document.body.dataset.hadSession=String(session.hasSession); document.body.dataset.ready='true' }
    init().catch(()=>{}); window.addEventListener('aivory:desktop-reconnected', ()=>init().catch(()=>{}));
    </script></body></html>`)
  await prepareApp(appDirectory, { webDir })
  await writeFile(path.join(appDirectory, 'smoke-bootstrap.cjs'), `
    const { app, BrowserWindow, ipcMain, shell, net, dialog } = require('electron')
    shell.openExternal = async (url) => {
      console.log('EXTERNAL_URL:' + url)
    }
    let updatePrompts = 0
    const windowActions = new WeakMap()
    dialog.showMessageBox = async (_window, options) => {
      console.log('UPDATE_PROMPT:' + options.message)
      updatePrompts++
      return { response: 1 }
    }
    const originalNetFetch = net.fetch.bind(net)
    net.fetch = async (url, options) => {
      if (new URL(url).pathname !== '/api/public/desktop-update') return originalNetFetch(url, options)
      if (url !== '${baseUrl}/api/public/desktop-update') throw new Error('Unexpected update origin')
      return { ok: true, json: async () => ({
        enabled: true, version: '${updateVersion}', downloads: {
          [({darwin:'macos',win32:'windows',linux:'linux'}[process.platform]) + '_' + process.arch]: 'https://downloads.example.test/Aivory-installer',
        },
      }) }
    }
    ipcMain.handle('smoke:native', (event, action) => {
      const window = BrowserWindow.fromWebContents(event.sender)
      if (action === 'close') window.close()
      if (action === 'restore') app.emit('activate')
      if (action === 'close-and-restore') { window.close(); app.emit('activate') }
      if (action === 'focus') { app.focus({ steal: true }); window.show(); window.focus() }
      if (action === 'fullscreen') window.setFullScreen(true)
      if (action === 'windowed') window.setFullScreen(false)
      if (action === 'quit') setTimeout(() => app.quit(), 100)
      return { minimized: window.isMinimized(), destroyed: window.isDestroyed(), visible: window.isVisible(), minimizable: window.isMinimizable(), fullscreen: window.isFullScreen(), prompts: updatePrompts, actions: windowActions.get(window) }
    })
    app.on('browser-window-created', (_event, window) => {
      const actions = []
      windowActions.set(window, actions)
      for (const method of ['minimize', 'hide']) {
        const original = window[method].bind(window)
        window[method] = () => {
          actions.push({ action: method, fullscreen: window.isFullScreen() })
          return original()
        }
      }
      window.on('close', () => console.log('NATIVE_CLOSE_REQUEST'))
      window.on('minimize', () => console.log('NATIVE_MINIMIZED'))
      window.on('restore', () => console.log('NATIVE_RESTORED'))
      window.on('enter-full-screen', () => console.log('NATIVE_FULLSCREEN_ENTER'))
      window.on('leave-full-screen', () => { console.log('NATIVE_FULLSCREEN_LEAVE'); actions.push({ action: 'leave-fullscreen' }) })
      window.webContents.on('leave-html-full-screen', () => actions.push({ action: 'leave-html-fullscreen' }))
      window.webContents.on('did-finish-load', () => {
        console.log('NATIVE_WINDOW_TITLE:' + JSON.stringify(window.getTitle()))
      })
    })
    require('./main.cjs')
  `)
  const preloadPath = path.join(appDirectory, 'preload.cjs')
  await writeFile(preloadPath, (await readFile(preloadPath, 'utf8')) + `
    contextBridge.exposeInMainWorld('desktopSmoke', {
      native: (action) => ipcRenderer.invoke('smoke:native', action),
    })
  `)
  const testPackage = JSON.parse(await readFile(path.join(appDirectory, 'package.json'), 'utf8'))
  testPackage.main = 'smoke-bootstrap.cjs'
  await writeFile(path.join(appDirectory, 'package.json'), JSON.stringify(testPackage))
  const config = JSON.parse(await readFile(path.join(appDirectory, 'config.json'), 'utf8'))
  assert.equal(config.baseUrl, `${baseUrl}/`)

  const page = await launch()
  assert.ok(page.url().startsWith(APP_URL))
  assert.equal(requestAgents.has('/'), false, 'Server frontend must never be requested')
  assert.equal(await page.evaluate(() => window.aivoryDesktop.serverBaseUrl), `${baseUrl}/`)
  assert.deepEqual(await page.evaluate(() => ({
    require: typeof window.require,
    process: typeof window.process,
    electron: typeof window.electron,
  })), { require: 'undefined', process: 'undefined', electron: 'undefined' })
  assert.equal(await page.evaluate(() => fetch('/api/session').then((res) => res.json()).then((result) => result.hasSession)), true)
  assert.equal(await page.evaluate(() => document.cookie.includes('desktop_session')), false)
  console.log('PASS: packaged URL, same-origin API cookies, and renderer isolation')
  assert.match(logs, /NATIVE_WINDOW_TITLE:""/)
  assert.doesNotMatch(logs, /NATIVE_WINDOW_TITLE:"[^"]/)
  console.log('PASS: the native title bar does not display the website title')
  assert.equal((await page.evaluate(() => window.aivoryDesktop.getInfo())).version, currentVersion)
  if (process.platform === 'darwin') {
    await page.waitForSelector('#aivory-window-drag')
    assert.equal(await page.$eval('#aivory-window-drag', (element) => getComputedStyle(element).webkitAppRegion), 'drag')
    assert.equal(await page.$eval('#root', (element) => Math.round(element.getBoundingClientRect().top)), 36)
    await page.evaluate(() => document.documentElement.style.setProperty('--color-bg', '#111113'))
    assert.equal(await page.$eval('#aivory-window-drag', (element) => getComputedStyle(element).backgroundColor), 'rgb(17, 17, 19)')
    assert.equal(await page.evaluate(() => document.scrollingElement.scrollHeight <= innerHeight), true)
    console.log('PASS: Mac window chrome is draggable, follows the page theme, and reserves space without page scrolling')

    await page.evaluate(() => {
      document.documentElement.style.setProperty('--color-sidebar-bg', '#202024')
      const sidebar = document.createElement('aside')
      sidebar.dataset.windowSidebar = ''
      sidebar.style.width = '280px'
      document.getElementById('root').prepend(sidebar)
    })
    await page.waitForFunction(() => document.documentElement.style.getPropertyValue('--aivory-chrome-sidebar-width') === '280px')
    assert.match(await page.$eval('#aivory-window-drag', (element) => getComputedStyle(element).backgroundImage), /rgb\(32, 32, 36\).*280px/)
    await page.$eval('aside', (element) => { element.style.width = '320px' })
    await page.waitForFunction(() => document.documentElement.style.getPropertyValue('--aivory-chrome-sidebar-width') === '320px')
    await page.$eval('aside', (element) => element.remove())
    await page.waitForFunction(() => document.documentElement.style.getPropertyValue('--aivory-chrome-sidebar-width') === '0px')
    console.log('PASS: Mac title area follows the sidebar surface, resizing and removal')

    const checkFullscreenLayout = async (fullscreen) => {
      await page.waitForFunction((fullscreen) => document.documentElement.hasAttribute('data-aivory-fullscreen') === fullscreen, { timeout: 15000 }, fullscreen)
      await page.waitForFunction((inset) => {
        const rect = document.getElementById('root').getBoundingClientRect()
        return Math.abs(rect.top - inset) < 1 && Math.abs(rect.bottom - innerHeight) < 1
          && document.scrollingElement.scrollHeight <= innerHeight
      }, { timeout: 15000 }, fullscreen ? 0 : 36)
      assert.equal(await page.$eval('#aivory-window-drag', (element) => getComputedStyle(element).display), fullscreen ? 'none' : 'block')
    }
    await page.evaluate(() => window.desktopSmoke.native('focus'))
    // Let macOS finish showing/activating the initial window before asking it
    // to animate into a new fullscreen Space.
    await new Promise(resolve => setTimeout(resolve, 1000))
    await page.evaluate(() => window.desktopSmoke.native('fullscreen'))
    await checkFullscreenLayout(true)
    assert.equal((await page.evaluate(() => window.desktopSmoke.native('state'))).fullscreen, true)
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.waitForSelector('#smoke')
    await checkFullscreenLayout(true)
    await page.evaluate(() => window.desktopSmoke.native('windowed'))
    await checkFullscreenLayout(false)
    console.log('PASS: native Mac fullscreen and reload remove the title inset; leaving restores it without page scrolling')

    await page.evaluate(() => {
      const trigger = document.createElement('button')
      trigger.id = 'html-fullscreen'
      trigger.textContent = 'Fullscreen'
      trigger.onclick = () => document.documentElement.requestFullscreen()
      document.getElementById('root').append(trigger)
    })
    await page.click('#html-fullscreen')
    await page.waitForFunction(() => Boolean(document.fullscreenElement))
    await checkFullscreenLayout(true)
    await page.evaluate(() => document.exitFullscreen())
    await checkFullscreenLayout(false)
    await page.$eval('#html-fullscreen', (element) => element.remove())
    console.log('PASS: HTML fullscreen uses the same zero-inset layout and restores the ordinary window')

    await page.evaluate(() => { document.getElementById('draft').value = 'Fullscreen draft stays intact' })
    for (const mode of ['native', 'html']) {
      await page.evaluate(() => window.desktopSmoke.native('focus'))
      await new Promise(resolve => setTimeout(resolve, 1000))
      const before = (await page.evaluate(() => window.desktopSmoke.native('state'))).actions.length
      if (mode === 'native') await page.evaluate(() => window.desktopSmoke.native('fullscreen'))
      else {
        await page.evaluate(() => {
          const button = document.createElement('button')
          button.id = 'close-html-fullscreen'
          button.textContent = 'Fullscreen'
          button.onclick = () => document.documentElement.requestFullscreen()
          document.getElementById('root').append(button)
        })
        await page.click('#close-html-fullscreen')
        await page.waitForFunction(() => Boolean(document.fullscreenElement))
      }
      await checkFullscreenLayout(true)
      await page.evaluate(() => window.desktopSmoke.native('close'))
      const state = await waitWindowState(page, true)
      assert.equal(state.fullscreen, false, 'Closing must vacate the Mac fullscreen Space')
      const actions = state.actions.slice(before)
      const leave = actions.findIndex(event => event.action === (mode === 'native' ? 'leave-fullscreen' : 'leave-html-fullscreen'))
      const minimized = actions.findIndex(event => event.action === 'minimize')
      assert.ok(leave >= 0 && minimized > leave, JSON.stringify(actions))
      assert.ok(actions.filter(event => ['minimize', 'hide'].includes(event.action)).every(event => !event.fullscreen), 'Never minimize or hide in fullscreen')
      await page.evaluate(() => window.desktopSmoke.native('restore'))
      await waitWindowState(page, false)
      await checkFullscreenLayout(false)
      await page.waitForFunction(() => !document.fullscreenElement)
      assert.equal(await page.$eval('#draft', element => element.value), 'Fullscreen draft stays intact')
      await page.screenshot({ path: path.join(tmpdir(), 'aivory-desktop-close-' + mode + '.png') })
      if (mode === 'html') await page.$eval('#close-html-fullscreen', element => element.remove())
    }
    console.log('PASS: closing native/HTML fullscreen exits the Space before minimize/hide; Dock activation restores the original page and draft')
    await page.evaluate(() => window.desktopSmoke.native('close-and-restore'))
    await new Promise(resolve => setTimeout(resolve, 800))
    await waitWindowState(page, false)
    console.log('PASS: immediate Dock activation cancels pending close actions')
  }
  await page.evaluate(() => { document.getElementById('draft').value = 'Draft survives window close' })
  await page.evaluate(() => window.desktopSmoke.native('focus'))
  await new Promise((resolve) => setTimeout(resolve, 500))
  await page.evaluate(() => window.desktopSmoke.native('close'))
  await waitWindowState(page, true)
  assert.equal(await page.$eval('#draft', (element) => element.value), 'Draft survives window close')
  assert.equal(child.exitCode, null)
  await page.evaluate(() => window.desktopSmoke.native('restore'))
  await waitWindowState(page, false)
  console.log('PASS: closing minimizes the window, preserves its draft, and permits restore')
  assert.equal((await page.evaluate(() => window.aivoryDesktop.checkUpdates())).status, 'available')
  assert.ok(logs.includes('UPDATE_PROMPT:') && logs.includes(updateVersion))
  console.log('PASS: native release checking prompts for a newer matching installer')
  const authorization = await page.evaluate(async () => {
    window.authorizationDocument = crypto.randomUUID()
    const documentId = window.authorizationDocument
    let emitted = false
    window.addEventListener('aivory:desktop-authorized', () => { emitted = true }, { once: true })
    const result = await window.aivoryDesktop.loginInBrowser()
    return { result, emitted, sameDocument: window.authorizationDocument === documentId }
  })
  assert.equal(authorization.result.status, 'authorized')
  assert.equal(authorization.emitted, true)
  assert.equal(authorization.sameDocument, true)
  assert.equal(await page.$eval('#draft', (element) => element.value), 'Draft survives window close')
  await page.waitForSelector('#smoke')
  assert.ok(logs.includes('EXTERNAL_URL:' + baseUrl + '/desktop/authorize?request_id=' + browserRequestId))
  assert.equal(await page.evaluate(() => fetch('/api/browser-session').then((r) => r.json()).then((r) => r.authorized)), true)
  assert.equal(await page.evaluate(() => document.cookie.includes('browser_authorized')), false)
  assert.match(requestAgents.get('/api/auth/desktop/start'), /AivoryDesktop\//)
  assert.match(requestAgents.get('/api/auth/desktop/token'), /AivoryDesktop\//)
  console.log('PASS: browser login uses the selected server, retains the document/draft, emits authorization and populates HttpOnly desktop cookies')

  const stream = await page.evaluate(async () => {
    const response = await fetch('/api/stream')
    const reader = response.body.getReader()
    const first = await reader.read()
    const firstText = new TextDecoder().decode(first.value)
    const remaining = await reader.read()
    return { firstText, remainingText: new TextDecoder().decode(remaining.value) }
  })
  assert.match(stream.firstText, /first-visible-content/)
  assert.doesNotMatch(stream.firstText, /done/)
  assert.match(stream.remainingText, /done/)
  console.log('PASS: streaming content arrives before response completion')

  const echoed = await page.evaluate(async () => {
    const response = await fetch('/api/echo', { method: 'POST', headers: { 'Content-Type': 'application/json', 'x-signature': 'signed-payload' }, body: JSON.stringify({ text: '测试 local UI' }) })
    return response.json()
  })
  assert.equal(echoed.body, JSON.stringify({ text: '测试 local UI' }))
  assert.equal(echoed.origin, baseUrl)
  assert.equal(echoed.signature, 'signed-payload')
  assert.match(echoed.cookie, /desktop_session=smoke/)
  const uploaded = await page.evaluate(() => new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    const body = new FormData()
    body.append('file', new Blob(['Upload bytes from the local frontend']), 'test.txt')
    xhr.open('POST', '/api/echo')
    xhr.onload = () => resolve(JSON.parse(xhr.responseText))
    xhr.onerror = () => reject(new Error('upload failed'))
    xhr.send(body)
  }))
  assert.match(uploaded.contentType, /multipart\/form-data; boundary=/)
  assert.match(uploaded.body, /Upload bytes from the local frontend/)
  assert.deepEqual(await page.evaluate(() => fetch('/api/compressed').then(r=>r.json())), { compressed: true, hasSession: true })
  await page.evaluate(async () => {
    const controller = new AbortController()
    const id = crypto.randomUUID()
    window.aivoryDesktop.startApiRequest(id)
    controller.signal.addEventListener('abort', () => window.aivoryDesktop.abortApiRequest(id), { once: true })
    const response = await fetch('/api/long-stream', { signal: controller.signal, headers: { 'x-aivory-desktop-request': id } })
    await response.body.getReader().read()
    controller.abort()
  })
  for (let i=0; i<30 && !abortedStream; i++) await new Promise(resolve => setTimeout(resolve, 100))
  assert.equal(abortedStream, true, 'Cancelling local streams must stop the server request')
  console.log('PASS: signed POSTs, multipart uploads, compressed authenticated responses and upstream stream cancellation')

  const voice = await page.evaluate(() => new Promise((resolve, reject) => {
    const replies = []
    const timer = setTimeout(() => { connection.close(); reject(new Error('Voice timeout')) }, 5000)
    const connection = window.aivoryDesktop.connectAudioSocket(event => {
      if (event.type === 'error') { clearTimeout(timer); reject(new Error('Voice transport failed')) }
      if (event.type !== 'message') return
      const message = JSON.parse(event.data)
      if (message.type === 'ready') { connection.send(new Uint8Array([1,2,3,4]).buffer); connection.send('{"type":"end"}') }
      else {
        replies.push(message)
        if (replies.length === 2) { clearTimeout(timer); connection.close(); resolve(replies) }
      }
    })
  }))
  assert.deepEqual(voice, [{ binary: true, bytes: 4, text: '' }, { binary: false, bytes: 14, text: '{"type":"end"}' }])
  assert.equal(voiceHeaders.origin, baseUrl)
  assert.match(voiceHeaders.cookie, /desktop_session=smoke/)
  assert.match(voiceHeaders['user-agent'], /AivoryDesktop\//)
  console.log('PASS: native voice WebSocket forwards authenticated binary/text frames to an HTTP API server')

  for (const type of ['redirect', 'form_post']) {
    const checkoutTarget = browser.waitForTarget(target => target.url() === baseUrl + '/pay/checkout?type=' + type)
    await page.evaluate(async ({ url, type }) => {
      window.paymentReturn = new Promise(resolve => window.addEventListener('aivory:desktop-payment-return', event => resolve(event.detail), { once: true }))
      await window.aivoryDesktop.openPayment({ type, url, fields: { order: 'test-order', value: 'a&b 中文' } })
    }, { url: baseUrl + '/pay/checkout?type=' + type, type })
    const checkout = await (await checkoutTarget).page()
    assert.equal(await checkout.evaluate(() => typeof window.aivoryDesktop), 'undefined')
    assert.equal(await checkout.evaluate(() => typeof window.require), 'undefined')
    if (type === 'form_post') assert.equal(paymentBody, 'order=test-order&value=a%26b+%E4%B8%AD%E6%96%87')
    await checkout.click('#return')
    assert.equal(await page.evaluate(() => window.paymentReturn), '/subscription?payment_order=test-order')
    assert.equal(page.url(), APP_URL)
  }
  console.log('PASS: payment redirects/form POSTs use isolated checkout windows and return to the local app')

  const popupTarget = browser.waitForTarget((target) => target.url() === `${APP_URL}share/example`)
  await page.$eval('#preview', (element) => element.click())
  const preview = await (await popupTarget).page()
  assert.equal(await preview.evaluate(() => fetch('/api/session').then((res) => res.json()).then((result) => result.hasSession)), true)
  assert.equal(await preview.evaluate(() => typeof window.require), 'undefined')
  assert.equal(await preview.evaluate(() => window.aivoryDesktop.getInfo().then(() => false, () => true)), true)
  await preview.close()
  const beforeNavigation = page.url()
  await page.$eval('#blocked', (element) => element.click())
  await new Promise((resolve) => setTimeout(resolve, 200))
  assert.equal(page.url(), beforeNavigation)
  console.log('PASS: preview windows retain the session; local-file navigation is blocked')

  await page.evaluate(() => {
    document.getElementById('draft').value = 'Unsent draft stays intact'
    document.documentElement.dataset.theme = 'dark'
    document.documentElement.dataset.accent = 'moss'
    document.documentElement.lang = 'zh'
  })
  const offlineTarget = browser.waitForTarget((target) => target.url().endsWith('/offline.html'))
  await page.setOfflineMode(true)
  const offline = await (await offlineTarget).page()
  await offline.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
  await offline.mouse.move(0, 0)
  await offline.waitForFunction(() => document.body.dataset.visible === 'true')
  await offline.waitForFunction(() => document.getElementById('title').textContent === '网络连接已断开')
  assert.equal(await offline.evaluate((url) => document.body.innerText.includes(url), baseUrl), false)
  assert.equal(await offline.evaluate(() => document.documentElement.classList.contains('dark')), true)
  assert.equal(await offline.evaluate(() => document.documentElement.dataset.accent), 'moss')
  await offline.waitForFunction(() => {
    const probe = document.createElement('span')
    probe.style.backgroundColor = document.getElementById('retry').matches(':hover') ? 'var(--color-accent-hover)' : 'var(--color-accent)'
    document.body.append(probe)
    const expected = getComputedStyle(probe).backgroundColor
    probe.remove()
    return getComputedStyle(document.getElementById('retry')).backgroundColor === expected
  })
  assert.equal(await page.evaluate(() => typeof window.desktopStatus), 'undefined')
  await offline.$eval('#retry', (element) => element.click())
  await offline.waitForFunction(() => !document.getElementById('retry').disabled, { polling: 100 })
  assert.equal(await offline.evaluate(() => document.body.dataset.visible), 'true')
  await offline.screenshot({ path: path.join(tmpdir(), 'aivory-desktop-offline-dark.png') })
  await page.evaluate(() => {
    document.documentElement.dataset.theme = 'light'
    document.documentElement.dataset.accent = 'mono'
    document.documentElement.lang = 'fr'
  })
  await offline.waitForFunction(() => document.documentElement.lang === 'fr')
  await offline.mouse.move(0, 0)
  await offline.waitForFunction(() => {
    const probe = document.createElement('span')
    probe.style.backgroundColor = document.getElementById('retry').matches(':hover') ? 'var(--color-accent-hover)' : 'var(--color-accent)'
    document.body.append(probe)
    const expected = getComputedStyle(probe).backgroundColor
    probe.remove()
    return getComputedStyle(document.getElementById('retry')).backgroundColor === expected
  })
  await offline.setViewport({ width: 390, height: 844 })
  assert.equal(await offline.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true)
  await offline.screenshot({ path: path.join(tmpdir(), 'aivory-desktop-offline-light-narrow.png') })
  for (const viewport of [{ width: 844, height: 390 }, { width: 320, height: 240 }, { width: 1280, height: 300 }]) {
    await offline.setViewport(viewport)
    assert.equal(await offline.evaluate(() => document.scrollingElement.scrollHeight <= innerHeight), true)
    assert.equal(await offline.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true)
    const reachable = await offline.evaluate(() => {
      const main = document.querySelector('main')
      main.scrollTop = main.scrollHeight
      const action = document.getElementById('open-browser').getBoundingClientRect()
      return action.bottom <= innerHeight && action.top >= main.getBoundingClientRect().top
    })
    assert.equal(reachable, true, `Offline actions must be reachable at ${viewport.width}x${viewport.height}`)
  }
  console.log('PASS: offline content adapts to short windows and stays accessible without document scrolling')
  await page.setOfflineMode(false)
  // Detached native views stop animation frames; poll their state with a timer.
  await offline.waitForFunction(() => document.body.dataset.visible === 'false', { polling: 100 })
  assert.equal(await page.$eval('#draft', (element) => element.value), 'Unsent draft stays intact')
  console.log('PASS: localized offline UI inherits the theme and restores the page without losing drafts')

  const localBefore = page.url()
  await page.$eval('#external', (element) => element.click())
  await new Promise((resolve) => setTimeout(resolve, 100))
  assert.equal(page.url(), localBefore)
  assert.ok(logs.includes('EXTERNAL_URL:' + baseUrl + '/oauth/start'))
  assert.equal(await page.evaluate(() => fetch('/api/session').then((res) => res.json()).then((result) => result.hasSession)), true)
  console.log('PASS: server website links use the system browser and preserve the local application')

  await stop()
  const restarted = await launch()
  assert.equal(await restarted.evaluate(() => document.body.dataset.hadSession), 'true')
  console.log('PASS: login cookie persists after a full Electron restart')
  await stop()
  refuseConnections = true
  const startupOffline = await launch(true)
  await startupOffline.waitForFunction(() => document.body.dataset.visible === 'true')
  assert.match(await startupOffline.$eval('#title', (element) => element.textContent), /Aivory/)
  await startupOffline.screenshot({ path: path.join(tmpdir(), 'aivory-desktop-server-unavailable.png') })
  refuseConnections = false
  await startupOffline.$eval('#retry', (element) => element.click())
  const recoveredTarget = await browser.waitForTarget((target) => target.url().startsWith(APP_URL))
  const recovered = await recoveredTarget.page()
  await recovered.waitForSelector('#smoke')
  await startupOffline.waitForFunction(() => document.body.dataset.visible === 'false', { polling: 100 })
  await recovered.waitForFunction(() => document.body.dataset.ready === 'true')
  console.log('PASS: unreachable startup retains the local frontend and retry reconnects its API')
  const appExited = once(child, 'exit')
  await recovered.evaluate(() => window.desktopSmoke.native('quit'))
  await Promise.race([appExited, new Promise((_, reject) => setTimeout(() => reject(new Error('Explicit quit did not stop Electron')), 5000))])
  assert.equal(child.exitCode, 0)
  console.log('PASS: explicit Quit exits Electron instead of minimizing')
} catch (error) {
  console.error(logs)
  throw error
} finally {
  await stop()
  for (const client of voiceServer.clients) client.terminate()
  voiceServer.close()
  await Promise.all([fixture, oauth].map((server) => new Promise((resolve) => {
    server.closeAllConnections()
    server.close(resolve)
  })))
  await rm(temp, { recursive: true, force: true })
}
