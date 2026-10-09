import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'
import windowChrome from '../../desktop/window-chrome.cjs'

// Use real administrator pages and their shared Dialog/Sheet components. The
// native smoke test separately verifies how Electron applies these styles and
// changes the fullscreen attribute; this checks the resulting portal geometry.
const chrome = process.env.CHROME_PATH ?? [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium',
].find(existsSync)
if (!chrome) throw new Error('Set CHROME_PATH to a Chrome or Chromium executable')
const output = '/tmp/aivory-desktop-drawers'
await mkdir(output, { recursive: true })
const copy = JSON.parse(readFileSync(new URL('../../src/i18n/locales/en/admin.json', import.meta.url)))
const now = Math.floor(Date.now() / 1000)
const channel = { id: 'c1', name: 'Production', base_url: 'https://api.example.test/v1', has_api_key: true, enabled: true, sort_order: 0, updated_at: now }
const model = { id: 'm1', label: 'Example model', protocol: 'openai.responses', channel_id: 'c1', kind: 'chat', request_id: 'example-model', enabled: true, sort_order: 0, param_controls: [], tool_mode: 'native', price_input: 0, price_output: 0 }
const fixtures = {
  '/admin/channels': [channel], '/admin/channels/health': {},
  '/admin/channels/c1/models': [{ channel_id: 'c1', request_id: 'example-model', kind: 'chat', label: 'Example model', enabled: true }],
  '/admin/channels/c1/health': { channel_id: 'c1', disabled_until: 0, models: [] },
  '/admin/models': [model],
  '/admin/skills': [{ id: 's1', name: 'Example skill', enabled: true, icon: '', instructions: 'Explain clearly.', description: 'A skill', assets: [], sort_order: 0 }],
  '/admin/audit-logs': { logs: [{ id: 'audit-1', type: 'models', actor_user_id: 'u1', actor_name: 'Administrator', actor_role: 'admin', action: 'admin.models.update', target_type: 'model', target_id: 'm1', target_name: 'Example model', result: 'success', source: 'admin', occurred_at_ms: now * 1000, created_at: now, changes: {}, metadata: {} }], total: 1, page: 1, page_size: 50 },
  '/admin/usage': { records: [{ id: 1, user_id: 'u1', user_name: 'Administrator', model_id: 'm1', channel_id: 'c1', created_at: now, input_tokens: 12, output_tokens: 30, cost: 0, credits: 0, status: 'error', purpose: 'chat', latency_ms: 1200, error_message: 'Upstream error' }], total: 1, total_cost: 0, page: 1, page_size: 50 },
}
const failures = []
const server = await createServer({ server: { port: 5195, strictPort: false }, logLevel: 'error' })
let browser

try {
  await server.listen()
  const base = server.resolvedUrls.local[0]
  browser = await puppeteer.launch({ executablePath: chrome, headless: true })
  const page = await browser.newPage()
  page.on('pageerror', error => failures.push(String(error)))
  await page.setRequestInterception(true)
  page.on('request', request => {
    const url = new URL(request.url())
    if (!url.pathname.startsWith('/api/')) return request.continue()
    const body = fixtures[url.pathname.slice(4)]
    if (body === undefined || request.method() !== 'GET') failures.push(`Unexpected API request: ${request.method()} ${url.pathname}`)
    return request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(body ?? []) })
  })

  const settled = () => page.waitForFunction(() => [...document.querySelectorAll('[role="dialog"]')].every(element => element.getAnimations().every(animation => animation.playState !== 'running')))
  async function close() {
    await page.keyboard.press('Escape')
    await page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
  }
  async function clickText(text) {
    const button = await page.evaluateHandle(text => [...document.querySelectorAll('main button')].find(element => element.textContent.trim() === text), text)
    assert.ok(button.asElement(), `Missing button: ${text}`)
    await button.asElement().click()
    await button.dispose()
  }
  async function checkDrawer(view, inset) {
    await page.waitForSelector('[role="dialog"]', { visible: true })
    await settled()
    const facts = await page.evaluate(() => {
      const drawer = document.querySelector('[role="dialog"]')
      const rect = drawer.getBoundingClientRect()
      const style = getComputedStyle(drawer)
      return { top: rect.top, right: rect.right, bottom: rect.bottom, width: rect.width,
        viewportWidth: innerWidth, viewportHeight: innerHeight, translate: style.translate,
        scrollWidth: drawer.scrollWidth, clientWidth: drawer.clientWidth,
        topTarget: document.elementFromPoint(8, 8)?.id }
    })
    assert.ok(Math.abs(facts.top - inset) < 1, `${view}: incorrect drawer top: ${JSON.stringify(facts)}`)
    assert.ok(Math.abs(facts.right - facts.viewportWidth) < 1, `${view}: drawer is not anchored to the right`)
    assert.ok(Math.abs(facts.bottom - facts.viewportHeight) < 1, `${view}: drawer exceeds the bottom`)
    assert.ok(facts.width <= facts.viewportWidth, `${view}: drawer exceeds the viewport width`)
    assert.ok(facts.scrollWidth <= facts.clientWidth + 1, `${view}: drawer content overflows horizontally`)
    assert.notEqual(facts.topTarget, 'aivory-window-drag', `${view}: title area covers the overlay`)
    // Long forms scroll inside the drawer without moving its anchored surface.
    await page.$eval('[role="dialog"]', element => { element.scrollTop = element.scrollHeight })
    assert.equal(await page.$eval('[role="dialog"]', element => Math.round(element.getBoundingClientRect().top)), inset)
  }

  for (const theme of ['light', 'dark']) {
    for (const width of [1440, 390]) {
      await page.setViewport({ width, height: width === 390 ? 780 : 900 })
      for (const mode of ['web', 'window', 'fullscreen']) {
        const inset = mode === 'window' ? 36 : 0
        for (const view of ['channels', 'models', 'skills', 'audit', 'usage']) {
          await page.goto(`${base}tests/browser/admin-tables-harness.html?view=${view}&lang=en&theme=${theme}`, { waitUntil: 'networkidle0' })
          await page.waitForSelector('table.admin-data-table')
          if (mode !== 'web') {
            await page.addStyleTag({ content: windowChrome.windowChromeCSS })
            await page.evaluate(fullscreen => {
              document.documentElement.toggleAttribute('data-aivory-fullscreen', fullscreen)
              const drag = document.createElement('div')
              drag.id = 'aivory-window-drag'
              document.body.append(drag)
            }, mode === 'fullscreen')
          }
          if (view === 'channels' || view === 'models') await page.click(`[data-admin-tour="${view}-create"]`)
          else if (view === 'skills') await clickText(copy.skills.new)
          else await page.click('tbody tr[role="button"]')
          await checkDrawer(view, inset)
          if (view === 'channels' && mode === 'window') {
            await page.evaluate(() => document.documentElement.setAttribute('data-aivory-fullscreen', ''))
            await checkDrawer('open drawer entering fullscreen', 0)
            await page.evaluate(() => document.documentElement.removeAttribute('data-aivory-fullscreen'))
            await checkDrawer('open drawer leaving fullscreen', 36)
          }
          if (width === 1440 && mode === 'window') await page.screenshot({ path: `${output}/${view}-${theme}.png` })
          await close()
          if (view === 'channels') {
            // The create and edit forms must use identical drawer positioning.
            const editSelector = `button[aria-label="${copy.common.edit}: Production"]`
            await page.waitForSelector(editSelector, { visible: true })
            await page.click(editSelector)
            await checkDrawer('channel edit', inset)
            await close()
            // Centered confirmations must retain their modal placement.
            const removeSelector = `button[aria-label="${copy.common.remove}: Production"]`
            await page.waitForSelector(removeSelector, { visible: true })
            await page.click(removeSelector)
            await page.waitForSelector('[data-dialog-presentation="modal"]', { visible: true })
            await settled()
            const modal = await page.$eval('[role="dialog"]', element => {
              const rect = element.getBoundingClientRect()
              return { centerX: rect.left + rect.width / 2, centerY: rect.top + rect.height / 2, width: innerWidth, height: innerHeight }
            })
            assert.ok(Math.abs(modal.centerX - modal.width / 2) < 1, 'Confirmation is not horizontally centered')
            assert.ok(Math.abs(modal.centerY - (modal.height + inset) / 2) < 1, 'Confirmation is not vertically centered in the content area')
            await close()
          }
        }
        console.log(`PASS: real administrator drawers, error/audit details, modal placement and title overlays (${theme}, ${width}px, ${mode})`)
      }
    }
  }
  assert.deepEqual(failures, [])
  console.log(`Screenshots: ${output}`)
} finally {
  await browser?.close()
  await server.close()
}
