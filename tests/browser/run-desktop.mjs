import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { once } from 'node:events'
import puppeteer from 'puppeteer-core'
import { prepareApp } from '../../desktop/prepare.mjs'

const requireDesktop = createRequire(new URL('../../desktop/package.json', import.meta.url))
const electron = requireDesktop('electron')
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
  } else if (req.url === '/api/session') {
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ hasSession }))
  } else if (req.url === '/api/stream') {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' })
    res.write('data: first-visible-content\n\n')
    const timer = setTimeout(() => res.end('data: done\n\n'), 1500)
    res.on('close', () => clearTimeout(timer))
  } else if (req.url === '/oauth/start') {
    res.writeHead(302, { Location: `${oauthUrl}/authorize` })
    res.end()
  } else {
    if (!hasSession) res.setHeader('Set-Cookie', 'desktop_session=smoke; HttpOnly; SameSite=Lax; Path=/; Max-Age=3600')
    res.setHeader('Content-Type', 'text/html; charset=utf-8')
    res.end(`<!doctype html><html><head><title>Aivory Desktop Fixture</title></head>
      <body data-had-session="${hasSession}"><h1 id="smoke">Aivory desktop</h1>
      <a id="preview" href="/share/example" target="_blank" rel="noopener">Preview</a>
      <a id="blocked" href="file:///etc/passwd">Blocked navigation</a>
      <textarea id="draft"></textarea></body></html>`)
  }
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
    ? target.url().endsWith('/offline.html') : target.url().startsWith(baseUrl), { timeout: 20000 })
  const page = await target.page()
  await page.waitForSelector(expectOffline ? '#title' : '#smoke')
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
  const deadline = Date.now() + 3000
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
  await prepareApp(appDirectory)
  await writeFile(path.join(appDirectory, 'smoke-bootstrap.cjs'), `
    const { app, BrowserWindow, ipcMain, shell, net, dialog } = require('electron')
    shell.openExternal = async (url) => {
      console.log('EXTERNAL_URL:' + url)
    }
    let updatePrompts = 0
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
        enabled: true, version: '2.5.1-beta.7', downloads: {
          [({darwin:'macos',win32:'windows',linux:'linux'}[process.platform]) + '_' + process.arch]: 'https://downloads.example.test/Aivory-installer',
        },
      }) }
    }
    ipcMain.handle('smoke:native', (event, action) => {
      const window = BrowserWindow.fromWebContents(event.sender)
      if (action === 'close') window.close()
      if (action === 'restore') { window.restore(); window.show() }
      if (action === 'focus') { app.focus({ steal: true }); window.show(); window.focus() }
      if (action === 'quit') setTimeout(() => app.quit(), 100)
      return { minimized: window.isMinimized(), destroyed: window.isDestroyed(), visible: window.isVisible(), minimizable: window.isMinimizable(), fullscreen: window.isFullScreen(), prompts: updatePrompts }
    })
    app.on('browser-window-created', (_event, window) => {
      window.on('close', () => console.log('NATIVE_CLOSE_REQUEST'))
      window.on('minimize', () => console.log('NATIVE_MINIMIZED'))
      window.on('restore', () => console.log('NATIVE_RESTORED'))
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
  assert.equal((await page.evaluate(() => window.aivoryDesktop.getInfo())).version, '2.5.1-beta.6')
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
  assert.match(logs, /UPDATE_PROMPT:.*2\.5\.1-beta\.7/)
  console.log('PASS: native release checking prompts for a newer matching installer')
  const returnedHome = page.waitForNavigation({ waitUntil: 'domcontentloaded' })
  await page.evaluate(() => { void window.aivoryDesktop.loginInBrowser() })
  await returnedHome
  await page.waitForSelector('#smoke')
  assert.ok(logs.includes('EXTERNAL_URL:' + baseUrl + '/desktop/authorize?request_id=' + browserRequestId))
  assert.equal(await page.evaluate(() => fetch('/api/browser-session').then((r) => r.json()).then((r) => r.authorized)), true)
  assert.equal(await page.evaluate(() => document.cookie.includes('browser_authorized')), false)
  assert.match(requestAgents.get('/api/auth/desktop/start'), /AivoryDesktop\//)
  assert.match(requestAgents.get('/api/auth/desktop/token'), /AivoryDesktop\//)
  console.log('PASS: browser login uses the baked URL and the exchange populates HttpOnly desktop session cookies')

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

  const popupTarget = browser.waitForTarget((target) => target.url() === `${baseUrl}/share/example`)
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
  await offline.waitForFunction(() => !document.getElementById('retry').disabled)
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
  await offline.waitForFunction(() => document.body.dataset.visible === 'false')
  assert.equal(await page.$eval('#draft', (element) => element.value), 'Unsent draft stays intact')
  console.log('PASS: localized offline UI inherits the theme and restores the page without losing drafts')

  await page.goto(`${baseUrl}/oauth/start`, { waitUntil: 'domcontentloaded' })
  assert.equal(page.url(), `${baseUrl}/oauth/return`)
  assert.equal(await page.evaluate(() => fetch('/api/session').then((res) => res.json()).then((result) => result.hasSession)), true)
  console.log('PASS: full-page authentication redirects return with the same session')

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
  const recoveredTarget = await browser.waitForTarget((target) => target.url().startsWith(baseUrl))
  const recovered = await recoveredTarget.page()
  await recovered.waitForSelector('#smoke')
  await startupOffline.waitForFunction(() => document.body.dataset.visible === 'false')
  console.log('PASS: unreachable startup shows local UI and retry loads the configured server')
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
  await Promise.all([fixture, oauth].map((server) => new Promise((resolve) => {
    server.closeAllConnections()
    server.close(resolve)
  })))
  await rm(temp, { recursive: true, force: true })
}
