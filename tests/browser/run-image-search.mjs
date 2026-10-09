import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const executablePath = [process.env.CHROME_PATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium'].find(path => path && existsSync(path))
if (!executablePath) throw new Error('Set CHROME_PATH')
const server = await createServer({ server: { host: '127.0.0.1', port: 0 }, logLevel: 'warn' })
const requests = []
const errors = []
let failOriginal = false
let browser
try {
  await server.listen()
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`
  browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox', '--disable-background-networking'] })
  const page = await browser.newPage()
  page.setDefaultTimeout(30_000)
  page.on('pageerror', error => errors.push(String(error)))
  await page.setRequestInterception(true)
  page.on('request', request => {
    const url = new URL(request.url())
    if (url.hostname === 'images.example.test') {
      requests.push({ path: url.pathname, headers: request.headers() })
      if (url.pathname.startsWith('/missing') || url.pathname === '/fallback-thumb.svg' || (failOriginal && url.pathname === '/portrait.svg')) {
        request.respond({ status: 404, contentType: 'text/plain', body: 'Missing' })
      } else {
        const [width, height] = url.pathname.startsWith('/portrait') ? [240, 400] : [640, 360]
        request.respond({ status: 200, contentType: 'image/svg+xml', body: `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}"><rect width="100%" height="100%" fill="#b8c7c3"/><text x="15" y="45" font-family="sans-serif" font-size="20" fill="#152522">${url.pathname.slice(1)}</text></svg>` })
      }
      return
    }
    if (url.pathname.startsWith('/api/')) {
      request.respond({ status: 200, contentType: 'application/json', body: '{}' })
      return
    }
    request.continue()
  })
  await page.setCacheEnabled(false)
  const gallery = '[data-image-search-gallery]'
  async function settled() {
    await page.waitForFunction(selector => {
      const grid = document.querySelector(selector)
      return grid && grid.querySelectorAll('img').length === 4 && [...grid.querySelectorAll('img')].every(img => img.complete && img.naturalWidth > 0) && grid.querySelector('[role="status"]')
    }, {}, gallery)
  }
  const output = '/tmp/aivory-image-search-check'
  await mkdir(output, { recursive: true })
  for (const [width, locale, dark] of [[1024, 'en', false], [375, 'zh', false], [320, 'en', true]]) {
    await page.setViewport({ width, height: 900 })
    await page.goto(`${origin}/tests/browser/image-search-harness.html?locale=${locale}${dark ? '&dark' : ''}`)
    await settled()
    assert.equal(await page.$$eval(`${gallery} li`, items => items.length), 5)
    assert.equal(await page.$eval('[aria-expanded]', el => el.getAttribute('aria-expanded')), 'false')
    assert.equal(await page.$(`${gallery} + [data-image-search-gallery]`), null)
    assert.equal(await page.$('[data-case="ordinary"] [data-image-search-gallery]'), null)
    assert.equal(await page.$eval('html', el => el.scrollWidth <= el.clientWidth), true, `page overflow at ${width}`)
    assert.equal(await page.$$eval(`${gallery} img`, images => images.every(img => {
      const bounds = img.getBoundingClientRect()
      const parent = img.parentElement.getBoundingClientRect()
      return bounds.width <= parent.width + 1 && bounds.height <= parent.height + 1 && getComputedStyle(img).objectFit === 'contain'
    })), true)
    assert.match(await page.$eval(`${gallery} li:nth-child(4)`, el => el.textContent), locale === 'zh' ? /图片无法加载/ : /Image unavailable/)
    await page.screenshot({ path: `${output}/${width}-${locale}${dark ? '-dark' : ''}.png`, fullPage: true })
  }
  // Keyboard activation opens a real Radix preview; ESC closes it and restores focus.
  await page.focus(`${gallery} li:first-child button`)
  await page.keyboard.press('Enter')
  await page.waitForSelector('[role="dialog"] img')
  assert.equal(await page.$eval('[role="dialog"] img', img => img.getAttribute('src')), 'https://images.example.test/landscape.svg')
  assert.equal(await page.$eval('[role="dialog"] a', link => link.href), 'https://source.example.test/gallery')
  await page.keyboard.press('Escape')
  await page.waitForSelector('[role="dialog"]', { hidden: true })
  await page.waitForFunction(selector => document.querySelector(`${selector} li:first-child button`) === document.activeElement && getComputedStyle(document.body).pointerEvents !== 'none', {}, gallery)
  // A failed full-size preview falls back to a working thumbnail.
  failOriginal = true
  await page.waitForFunction(() => document.activeElement?.closest('[data-image-search-gallery]') && !document.querySelector('[data-state="closed"][role="dialog"]'))
  await page.click(`${gallery} li:nth-child(2) button`)
  await page.waitForFunction(() => {
    const img = document.querySelector('[role="dialog"] img')
    return img?.getAttribute('src') === 'https://images.example.test/portrait-thumb.svg' && img.complete && img.naturalWidth > 0
  })
  await page.keyboard.press('Escape')
  await page.waitForSelector('[role="dialog"]', { hidden: true })
  failOriginal = false
  // The bundled desktop frontend uses the same external media URLs.
  await page.evaluateOnNewDocument(() => { window.aivoryDesktop = { serverBaseUrl: 'https://api.example.test', getInfo: async () => ({ version: 'test', platform: 'darwin' }) } })
  await page.goto(`${origin}/tests/browser/image-search-harness.html`)
  await settled()
  assert.equal(await page.$eval(`${gallery} img`, img => img.src), 'https://images.example.test/landscape-thumb.svg')
  assert.ok(requests.some(request => request.path === '/fallback.svg'))
  assert.ok(requests.every(request => !request.path.includes('metadata-only') && !request.path.includes('ordinary-thumbnail')), 'ordinary metadata images were downloaded without being displayed')
  assert.ok(requests.every(request => !request.headers.referer), 'external images leaked a referrer')
  assert.deepEqual(errors, [])
  console.log(`Image search browser checks passed (desktop, mobile, dark mode, keyboard preview, loading fallback); screenshots: ${output}`)
} finally {
  await browser?.close()
  await server.close()
}
