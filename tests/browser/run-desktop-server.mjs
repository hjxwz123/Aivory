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
const temp = await mkdtemp(path.join(tmpdir(), 'aivory-desktop-server-'))
const appDirectory = path.join(temp, 'app')
const profile = path.join(temp, 'profile')
let browser
let child
let logs = ''
const agents = []
const fixture = createServer((req, res) => {
  agents.push({ url: req.url, agent: req.headers['user-agent'] })
  if (req.url === '/api/client') {
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ agent: req.headers['user-agent'], cookie: req.headers.cookie || '' }))
    return
  }
  if (req.url === '/api/public/desktop-update') { res.end('{"enabled":false}'); return }
  res.setHeader('Set-Cookie', 'configured_session=true; HttpOnly; SameSite=Lax; Path=/; Max-Age=3600')
  res.setHeader('Content-Type', 'text/html')
  res.end('<html lang="en"><body><h1 id="fixture">Server</h1><textarea id="draft"></textarea></body></html>')
})
const otherFixture = createServer((req, res) => {
  res.setHeader('Content-Type', 'application/json')
  res.end(JSON.stringify({ cookie: req.headers.cookie || '' }))
})
async function listen(server) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return server.address().port
}
async function launch(urlSuffix) {
  const probe = createServer()
  const debugPort = await listen(probe)
  await new Promise((resolve) => probe.close(resolve))
  const env = { ...process.env }
  delete env.ELECTRON_RUN_AS_NODE
  child = spawn(electron, [appDirectory, `--user-data-dir=${profile}`, `--remote-debugging-port=${debugPort}`], { env, stdio: ['ignore', 'pipe', 'pipe'] })
  for (const pipe of [child.stdout, child.stderr]) pipe.on('data', (chunk) => { logs += chunk.toString() })
  let endpoint
  for (let attempt = 0; attempt < 200; attempt++) {
    if (child.exitCode !== null) throw new Error(logs)
    try { endpoint = (await (await fetch(`http://127.0.0.1:${debugPort}/json/version`)).json()).webSocketDebuggerUrl } catch {}
    if (endpoint) break
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  assert.ok(endpoint, logs)
  browser = await puppeteer.connect({ browserWSEndpoint: endpoint, defaultViewport: null })
  const page = await (await browser.waitForTarget((target) => target.url().endsWith(urlSuffix))).page()
  await page.waitForSelector(urlSuffix === '/server.html' ? '#server-form' : '#fixture')
  return page
}
async function stop() {
  const proc = child
  if (browser) { await browser.close().catch(() => browser.disconnect()); browser = undefined }
  if (proc?.exitCode === null) {
    const timeout = setTimeout(() => proc.kill('SIGKILL'), 5000)
    await once(proc, 'exit')
    clearTimeout(timeout)
  }
  child = undefined
}
async function openSettings(page) {
  const target = browser.waitForTarget((target) => target.url().endsWith('/server.html'))
  await page.evaluate(() => window.serverSmoke.native('settings'))
  const settings = await (await target).page()
  await settings.waitForSelector('#server-form')
  await settings.waitForFunction(() => document.getElementById('server-url').value !== '')
  return settings
}
try {
  const baseUrl = `http://127.0.0.1:${await listen(fixture)}/`
  const otherUrl = `http://127.0.0.1:${await listen(otherFixture)}/`
  process.env.AIVORY_DESKTOP_BASE_URL = ''
  await prepareApp(appDirectory)
  assert.equal(JSON.parse(await readFile(path.join(appDirectory, 'config.json'))).baseUrl, '')
  await writeFile(path.join(appDirectory, 'test-bootstrap.cjs'), `
    const { app, Menu, ipcMain, nativeTheme } = require('electron')
    const { getMessages } = require('./locales.cjs')
    ipcMain.handle('server-smoke:native', (_event, action) => {
      if (action === 'settings') {
        const find = (items) => { for (const item of items) {
          if (item.label === getMessages(app.getLocale()).serverSettings) return item
          const nested = item.submenu && find(item.submenu.items)
          if (nested) return nested
        } }
        find(Menu.getApplicationMenu().items).click()
      }
      if (action === 'dark') nativeTheme.themeSource = 'dark'
      if (action === 'light') nativeTheme.themeSource = 'light'
      return global.__serverTray
    })
    require('./main.cjs')
  `)
  const main = await readFile(path.join(appDirectory, 'main.cjs'), 'utf8')
  await writeFile(path.join(appDirectory, 'main.cjs'), main.replace('tray = new Tray(icon)', 'tray = new Tray(icon); global.__serverTray = { exists: !tray.isDestroyed(), template: icon.isTemplateImage(), width: icon.getSize().width }'))
  for (const name of ['preload.cjs', 'server-preload.cjs']) {
    const filename = path.join(appDirectory, name)
    await writeFile(filename, await readFile(filename, 'utf8') + `\ncontextBridge.exposeInMainWorld('serverSmoke', { native: (action) => ipcRenderer.invoke('server-smoke:native', action), forbiddenSave: (url) => ipcRenderer.invoke('desktop:server-save', url) })`)
  }
  const pkg = JSON.parse(await readFile(path.join(appDirectory, 'package.json')))
  pkg.main = 'test-bootstrap.cjs'
  await writeFile(path.join(appDirectory, 'package.json'), JSON.stringify(pkg))
  const setup = await launch('/server.html')
  await setup.waitForFunction(() => Boolean(document.getElementById('title').textContent))
  assert.equal(await setup.$eval('#server-url', (input) => input.value), '')
  const tray = await setup.evaluate(() => window.serverSmoke.native('info'))
  assert.equal(tray.exists, true)
  if (process.platform === 'darwin') { assert.equal(tray.template, true); assert.equal(tray.width, 22) }
  for (const theme of ['light', 'dark']) {
    await setup.evaluate((mode) => window.serverSmoke.native(mode), theme)
    await setup.reload()
    await setup.waitForFunction((dark) => document.documentElement.classList.contains('dark') === dark, {}, theme === 'dark')
    for (const viewport of [{ width: 560, height: 430 }, { width: 360, height: 280 }, { width: 320, height: 240 }]) {
      await setup.setViewport(viewport)
      assert.equal(await setup.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth && document.scrollingElement.scrollHeight <= innerHeight), true)
      assert.equal(await setup.evaluate(() => {
        const main = document.querySelector('main'); main.scrollTop = main.scrollHeight
        return document.getElementById('save').getBoundingClientRect().bottom <= innerHeight
      }), true)
      if (viewport.width === 560) await setup.screenshot({ path: `/tmp/aivory-desktop-server-${theme}.png` })
    }
  }
  await setup.evaluate(() => { document.getElementById('server-url').value = 'https://example.com/api' })
  await setup.$eval('#save', (button) => button.click())
  await setup.waitForFunction(() => document.getElementById('server-url').getAttribute('aria-invalid') === 'true')
  const target = browser.waitForTarget((target) => target.url() === baseUrl)
  await setup.evaluate((url) => { document.getElementById('server-url').value = url }, baseUrl)
  await setup.$eval('#save', (button) => button.click())
  let page = await (await target).page()
  await page.waitForSelector('#fixture')
  assert.equal(JSON.parse(await readFile(path.join(profile, 'server.json'))).baseUrl, baseUrl)
  assert.equal(await page.evaluate(() => typeof window.desktopServer), 'undefined')
  assert.equal(await page.evaluate((url) => window.serverSmoke.forbiddenSave(url).then(() => false, () => true), otherUrl), true)
  assert.match(await page.evaluate(() => navigator.userAgent), /AivoryDesktop\/2\.5\.1-beta\.6/)
  assert.match(await page.evaluate(() => fetch('/api/client').then((response) => response.json()).then((client) => client.agent)), /AivoryDesktop\/2\.5\.1-beta\.6/)
  assert.match(agents.find((request) => request.url === '/')?.agent, /AivoryDesktop\//)
  console.log('PASS: empty build URL shows responsive setup; tray icon and renderer/request app identity are present; remote configuration IPC is rejected')
  await page.evaluate(() => { document.getElementById('draft').value = 'Keep this draft' })
  const settings = await openSettings(page)
  assert.equal(await page.$eval('#draft', (input) => input.value), 'Keep this draft')
  await settings.$eval('#save', (button) => button.click())
  await new Promise((resolve) => setTimeout(resolve, 150))
  assert.equal(await page.$eval('#draft', (input) => input.value), 'Keep this draft')
  await stop()
  await writeFile(path.join(appDirectory, 'config.json'), JSON.stringify({ baseUrl: 'https://invalid-default.example/' }))
  page = await launch(`:${new URL(baseUrl).port}/`)
  assert.match(await page.evaluate(() => fetch('/api/client').then((r) => r.json()).then((client) => client.cookie)), /configured_session=true/)
  await stop()
  await writeFile(path.join(appDirectory, 'config.json'), '{"baseUrl":""}')
  page = await launch(`:${new URL(baseUrl).port}/`)
  console.log('PASS: restart and generic/different-default updates reuse saved URL and cookies; opening settings preserves the draft')
  const changing = await openSettings(page)
  const switched = browser.waitForTarget((target) => target.url() === otherUrl)
  await changing.evaluate((url) => { document.getElementById('server-url').value = url }, otherUrl)
  await changing.$eval('#save', (button) => button.click())
  const second = await (await switched).page()
  await second.waitForFunction(() => document.body.textContent.includes('cookie'))
  assert.equal(JSON.parse(await second.$eval('body', (body) => body.textContent)).cookie, '')
  assert.equal(JSON.parse(await readFile(path.join(profile, 'server.json'))).baseUrl, otherUrl)
  assert.equal(page.isClosed(), true)
  console.log('PASS: changing server destroys the old renderer and isolates login cookies')
} catch (error) { console.error(logs); throw error }
finally {
  await stop()
  for (const server of [fixture, otherFixture]) {
    server.closeAllConnections()
    await new Promise((resolve) => server.close(resolve))
  }
  await rm(temp, { recursive: true, force: true })
}
