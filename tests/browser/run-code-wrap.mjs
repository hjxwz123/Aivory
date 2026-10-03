import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { realpathSync } from 'node:fs'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const candidates = [
  process.env.CHROME_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
]
const executablePath = candidates.find((candidate) => candidate && existsSync(candidate))
if (!executablePath) throw new Error('Set CHROME_PATH to an existing Chrome or Edge executable')
const temporary = await mkdtemp(join(tmpdir(), 'aivory-code-wrap-browser-'))
let server
let browser

try {
  server = await createServer({
    root: process.cwd(),
    cacheDir: join(temporary, 'vite-cache'),
    optimizeDeps: { entries: ['tests/browser/code-wrap-harness.html'], include: ['shiki/core', 'shiki/engine/javascript'] },
    server: { host: '127.0.0.1', port: 5196, strictPort: true, fs: { allow: [process.cwd(), realpathSync(resolve('node_modules'))] } },
    logLevel: 'warn',
  })
  await server.listen()
  const address = server.httpServer.address()
  if (!address || typeof address === 'string') throw new Error('Missing test server port')
  browser = await puppeteer.launch({
    executablePath,
    headless: true,
    userDataDir: join(temporary, 'browser-profile'),
    args: ['--no-sandbox', '--disable-background-networking', '--disable-component-update', '--disable-sync', '--disable-breakpad', '--disable-crash-reporter', '--no-first-run'],
  })
  const page = await browser.newPage()
  page.setDefaultTimeout(60_000)
  await page.setViewport({ width: 1280, height: 900 })
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(String(error)))
  await page.evaluateOnNewDocument(() => {
    Reflect.set(window, '__COPIED_CODE__', null)
    Reflect.set(window, '__PREVIEW_RENDERED__', false)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (value) => { Reflect.set(window, '__COPIED_CODE__', value) } } })
    window.addEventListener('message', (event) => {
      if (event.data?.codeWrapPreview) Reflect.set(window, '__PREVIEW_RENDERED__', true)
    })
  })
  await page.goto(`http://127.0.0.1:${address.port}/tests/browser/code-wrap-harness.html`, { waitUntil: 'domcontentloaded', timeout: 180_000 })
  await page.waitForFunction(() => Reflect.get(window, '__CODE_WRAP__') && document.querySelectorAll('[data-code-body]').length === 6)

  const main = '[data-case="main"]'
  const toggle = `${main} button[aria-pressed]`
  const geometry = () => page.$eval(`${main} [data-code-body]`, (element) => ({
    whiteSpace: getComputedStyle(element).whiteSpace,
    scrollWidth: element.scrollWidth,
    clientWidth: element.clientWidth,
  }))
  await page.waitForFunction(() => getComputedStyle(document.querySelector('[data-case="main"] [data-code-body]')).whiteSpace === 'pre')
  const unwrapped = await geometry()
  assert.ok(unwrapped.scrollWidth > unwrapped.clientWidth)
  await page.click(toggle)
  await page.waitForFunction(() => document.querySelector('[data-case="main"] [data-code-body]').dataset.wrap === 'true')
  const wrapped = await geometry()
  assert.equal(wrapped.whiteSpace, 'pre-wrap')
  assert.ok(wrapped.scrollWidth <= wrapped.clientWidth + 1)
  assert.equal(await page.$eval('[data-case="second"] [data-code-body]', (element) => element.dataset.wrap), 'false')
  console.log('PASS independent wrapping and unbroken URL/CJK geometry')

  await page.click(toggle)
  await page.click('[data-case="settings"] button[aria-label="Wrap code blocks by default"]')
  await page.waitForFunction(() => Reflect.get(window, '__CODE_WRAP__').settings.getState().appearance.codeBlockWrap)
  assert.equal(await page.$eval(`${main} [data-code-body]`, (element) => element.dataset.wrap), 'false')
  assert.ok(await page.$$eval('[data-case="second"] [data-code-body], [data-case="nested"] [data-code-body], [data-case="private"] [data-code-body]', (elements) => elements.every((element) => element.dataset.wrap === 'true')))
  await page.click(`${main} button[aria-label="Use default wrapping"]`)
  await page.waitForFunction(() => document.querySelector('[data-case="main"] [data-code-body]').dataset.wrap === 'true')
  await page.click(`${main} button[aria-label="Copy"]`)
  assert.equal(await page.evaluate(() => Reflect.get(window, '__COPIED_CODE__')), await page.evaluate(() => Reflect.get(window, '__CODE_WRAP__').original))
  console.log('PASS appearance default, explicit false priority, reset and exact clipboard code')

  await page.click(toggle)
  await page.evaluate(() => Reflect.get(window, '__CODE_WRAP__').append())
  await page.waitForFunction(() => document.querySelector('[data-case="main"] [data-code-body]').textContent.endsWith(Reflect.get(window, '__CODE_WRAP__').appended))
  await page.evaluate(() => Reflect.get(window, '__CODE_WRAP__').finish())
  await page.waitForFunction(() => document.querySelector('[data-case="main"] code span[style*="--shiki-"]'))
  assert.equal(await page.$eval(`${main} [data-code-body]`, (element) => element.dataset.wrap), 'false')
  await page.focus(toggle)
  await page.keyboard.press('Space')
  await page.waitForFunction(() => document.querySelector('[data-case="main"] [data-code-body]').dataset.wrap === 'true')
  console.log('PASS streaming/final Shiki upgrade keeps selection and keyboard toggles')

  await page.click('[data-case="html"] button[aria-label="Preview"]')
  await page.waitForFunction(() => Reflect.get(window, '__PREVIEW_RENDERED__'))
  await page.evaluate(() => Reflect.get(window, '__CODE_WRAP__').artifacts.getState().close())
  console.log('PASS original HTML preview still executes in its sandbox')

  await page.evaluate(() => { document.querySelector('[data-case="settings"]').style.display = 'none' })
  await page.click('[data-case="html"] button[aria-pressed]')
  await page.click('[data-case="html"] button[aria-pressed]')
  await page.setViewport({ width: 320, height: 800 })
  await page.evaluate(() => Reflect.get(window, '__CODE_WRAP__').resize(280))
  await page.waitForFunction(() => document.querySelector('[data-case="blocks"]').getBoundingClientRect().width <= 280)
  const layout = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    document: document.documentElement.scrollWidth,
    bodies: Array.from(document.querySelectorAll('[data-code-body]')).map((element) => ({ wrap: element.dataset.wrap, client: element.clientWidth, scroll: element.scrollWidth })),
    toolbars: Array.from(document.querySelectorAll('[data-code-toolbar]')).map((element) => ({ client: element.clientWidth, scroll: element.scrollWidth })),
  }))
  assert.ok(layout.document <= layout.viewport + 1, JSON.stringify(layout))
  assert.ok(layout.bodies.every((body) => body.wrap === 'true' && body.scroll <= body.client + 1), JSON.stringify(layout))
  assert.ok(layout.toolbars.every((toolbar) => toolbar.scroll <= toolbar.client + 1), JSON.stringify(layout))
  assert.equal(await page.$$eval('[data-case="private"] button', (buttons) => buttons.length), 1)
  assert.deepEqual(pageErrors, [])
  console.log('PASS 320px mobile / 280px panel layout, toolbar fit and private display-only controls')
} finally {
  await browser?.close()
  await server?.close()
  await rm(temporary, { recursive: true, force: true })
}
