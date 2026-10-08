import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createServer } from 'node:net'
import { once } from 'node:events'
import { mkdtemp, rm, mkdir } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import puppeteer from 'puppeteer-core'
import localWeb from '../../desktop/local-web.cjs'

// Launch the signed build itself, rather than the development Electron binary.
const executable = path.resolve(process.argv[2] || 'desktop/release/mac-arm64/Aivory.app/Contents/MacOS/Aivory')
const baseUrl = process.env.AIVORY_LAYOUT_TEST_URL || 'http://127.0.0.1:5173'
const temp = await mkdtemp(path.join(tmpdir(), 'aivory-package-smoke-'))
const probe = createServer()
probe.listen(0, '127.0.0.1')
await once(probe, 'listening')
const debugPort = probe.address().port
await new Promise((resolve) => probe.close(resolve))
const env = { ...process.env }
delete env.ELECTRON_RUN_AS_NODE
const child = spawn(executable, [`--user-data-dir=${temp}`, `--remote-debugging-port=${debugPort}`], { env, stdio: ['ignore', 'pipe', 'pipe'] })
let logs = ''
let browser
for (const stream of [child.stdout, child.stderr]) stream.on('data', (chunk) => { logs += chunk.toString() })
try {
  let endpoint
  const deadline = Date.now() + 20000
  while (!endpoint && Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`Packaged app exited before startup: ${logs}`)
    try { endpoint = (await (await fetch(`http://127.0.0.1:${debugPort}/json/version`)).json()).webSocketDebuggerUrl } catch {}
    if (!endpoint) await new Promise((resolve) => setTimeout(resolve, 100))
  }
  assert.ok(endpoint, 'Signed application must launch successfully')
  browser = await puppeteer.connect({ browserWSEndpoint: endpoint, defaultViewport: null })
  const setup = await (await browser.waitForTarget((target) => target.url().endsWith('/server.html'))).page()
  await setup.waitForSelector('#server-form')
  await setup.waitForSelector('#aivory-window-drag')
  assert.equal(await setup.evaluate(() => document.scrollingElement.scrollHeight <= innerHeight), true)
  assert.equal(await setup.evaluate(() => typeof window.require), 'undefined')
  await mkdir('/tmp/aivory-notifications-review', { recursive: true })
  await setup.screenshot({ path: '/tmp/aivory-notifications-review/packaged-mac-setup.png' })
  await setup.type('#server-url', baseUrl)
  await setup.click('#save')
  const page = await (await browser.waitForTarget((target) => target.url().startsWith(localWeb.APP_URL))).page()
  await page.waitForSelector('#root')
  await page.waitForSelector('.login-panel')
  await page.waitForSelector('#aivory-window-drag')
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  assert.equal(await page.evaluate(() => typeof window.require), 'undefined')
  assert.equal(await page.evaluate(() => window.aivoryDesktop.serverBaseUrl), new URL(baseUrl).origin + '/')
  assert.equal(await page.evaluate(() => navigator.serviceWorker.getRegistrations().then(r=>r.length)), 0)
  const assets = await page.evaluate(() => performance.getEntriesByType('resource')
    .map(entry => entry.name).filter(url => /\.(?:js|css|woff2?|svg|png)(?:\?|$)/.test(url)))
  assert.ok(assets.some(url => url.includes('/assets/') && url.endsWith('.js')))
  assert.ok(assets.some(url => url.endsWith('.css')))
  assert.ok(assets.every(url => new URL(url).origin === new URL(localWeb.APP_URL).origin), 'Application assets must come from the local bundle')
  assert.equal(await page.evaluate(() => document.scrollingElement.scrollHeight <= innerHeight && document.scrollingElement.scrollWidth <= innerWidth), true)
  assert.equal(await page.$eval('#root', (element) => Math.round(element.getBoundingClientRect().top)), 36)
  await page.screenshot({ path: '/tmp/aivory-notifications-review/packaged-mac-login.png' })
  console.log('PASS: signed Mac package runs its bundled frontend; API server, desktop login, and native chrome work without page scrolling or web service workers')
} finally {
  browser?.disconnect()
  if (child.exitCode === null) {
    child.kill('SIGTERM')
    const timer = setTimeout(() => child.kill('SIGKILL'), 5000)
    await once(child, 'exit')
    clearTimeout(timer)
  }
  await rm(temp, { recursive: true, force: true })
}
