import assert from 'node:assert/strict'
import { existsSync, realpathSync } from 'node:fs'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const executablePath = [
  process.env.CHROME_PATH,
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/chromium',
].find((candidate) => candidate && existsSync(candidate))
if (!executablePath) throw new Error('Set CHROME_PATH to a local Chrome or Edge executable')
const output = resolve(process.env.MOBILE_LAYOUT_OUTPUT || join(tmpdir(), 'aivory-mobile-layout-results-20261011'))
const selected = process.argv[2]
const temporary = await mkdtemp(join(tmpdir(), 'aivory-mobile-layout-'))
await mkdir(output, { recursive: true })
const results = []
const errors = []
let server
let browser
let page
let origin

const settle = () => page.evaluate(async () => {
  await document.fonts.ready
  await new Promise((done) => setTimeout(done, 350))
})
const rectangle = (selector) => page.$eval(selector, (element) => {
  const r = element.getBoundingClientRect()
  return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
})
const visible = (rect, bounds, label) => {
  assert.ok(rect.width > 0 && rect.height > 0, `${label}: empty ${JSON.stringify(rect)}`)
  assert.ok(rect.left >= bounds.left - 1 && rect.right <= bounds.right + 1 && rect.top >= bounds.top - 1 && rect.bottom <= bounds.bottom + 1,
    `${label}: ${JSON.stringify(rect)} outside ${JSON.stringify(bounds)}`)
}

async function record(name, run) {
  if (selected && !name.startsWith(selected)) return
  try {
    const measurements = await run()
    await page.screenshot({ path: join(output, `${name}.png`) })
    results.push({ name, passed: true, measurements })
    console.log('PASS', name)
  } catch (error) {
    await page.screenshot({ path: join(output, `${name}-failed.png`) }).catch(() => {})
    results.push({ name, passed: false, error: String(error) })
    throw error
  } finally {
    await writeFile(join(output, selected ? `results-${selected}.json` : 'results.json'), JSON.stringify({ browser: await browser.version(), results, errors }, null, 2))
  }
}

async function open(mode, viewport, file) {
  const mobile = viewport.width < 1024 && (viewport.width < 640 || viewport.height <= 520)
  await page.setViewport({ ...viewport, isMobile: mobile, hasTouch: mobile })
  const params = new URLSearchParams({ mode, ...(file ? { file } : {}) })
  await page.goto(`${origin}/tests/browser/mobile-layout-harness.html?${params}`, { waitUntil: 'domcontentloaded', timeout: 180000 })
  for (let attempt = 0; attempt < 4; attempt++) {
    try {
      await page.waitForFunction(() => window.__MOBILE_LAYOUT__?.ready)
      await settle()
      return
    } catch (error) {
      if (attempt === 3 || !String(error).includes('Execution context was destroyed')) throw error
    }
  }
}

async function bounds() {
  return page.evaluate(() => {
    const css = getComputedStyle(document.documentElement)
    const left = parseFloat(css.getPropertyValue('--surface-viewport-left')) || 0
    const top = parseFloat(css.getPropertyValue('--surface-viewport-top')) || 0
    const width = parseFloat(css.getPropertyValue('--surface-viewport-width')) || innerWidth
    const height = parseFloat(css.getPropertyValue('--surface-viewport-height')) || innerHeight
    return { left, top, width, height, right: left + width, bottom: top + height }
  })
}

async function configurePage(target) {
  target.setDefaultTimeout(60000)
  await target.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
  target.on('pageerror', (error) => errors.push(String(error)))
  target.on('console', (message) => {
    if (message.type() === 'error') console.error('[browser]', message.text())
  })
  target.on('requestfailed', (request) => {
    if (request.url().startsWith(origin)) console.error('[request failed]', request.url(), request.failure()?.errorText)
  })
  await target.setRequestInterception(true)
  target.on('request', async (request) => {
    const url = new URL(request.url())
    if (url.origin === origin && url.pathname.startsWith('/api/')) {
      const fixtures = await target.evaluate(() => window.__MOBILE_LAYOUT__?.fixtures).catch(() => null)
      let body = []
      if (url.pathname === '/api/me/settings') body = JSON.parse(request.postData() || '{}')
      else if (url.pathname === '/api/me/password/set') body = { ok: true }
      else if (url.pathname === '/api/artifacts/mobile-layout-image') {
        await request.respond({ status: 200, contentType: 'image/png', body: Buffer.from((fixtures?.imageDataUrl || '').split(',')[1] || '', 'base64') })
        return
      }
      else if (url.pathname === '/api/conversations') body = { conversations: fixtures?.archivedConversations || [], has_more: false, limit: 21, offset: 0 }
      else if (url.pathname === '/api/workspaces/mobile-layout-workspace/members') body = { members: fixtures?.workspaceMembers || [] }
      else if (url.pathname === '/api/me/ppt/templates') {
        if (new URL(target.url()).searchParams.get('file') === 'invalid') {
          await request.respond({ status: 502, contentType: 'application/json', body: JSON.stringify({ error: '本地模拟模板加载错误。'.repeat(30) }) })
          return
        }
        body = { templates: Array.from({ length: 24 }, (_, index) => ({ id: `template-${index + 1}`, name: `Layout template ${index + 1}`, category: 'Local fixture' })), page: 1, size: 24, has_more: false }
      }
      else if (url.pathname === '/api/me/skill-commands') body = fixtures?.skills || []
      else if (url.pathname === '/api/me/prompts') body = fixtures?.prompts || []
      else if (url.pathname === '/api/kbs') body = fixtures?.knowledgeBases || []
      await request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
    } else if (url.origin === origin || ['data:', 'blob:'].includes(url.protocol)) {
      await request.continue()
    } else {
      await request.abort()
    }
  })
}

try {
  server = await createServer({
    root: process.cwd(), cacheDir: join(temporary, 'vite-cache'),
    optimizeDeps: {
      entries: ['tests/browser/mobile-layout-harness.html'],
      include: ['docx-preview', 'read-excel-file/browser', 'read-excel-file/web-worker', '@aiden0z/pptx-renderer', 'shiki/core', 'shiki/engine/javascript', 'mathlive'],
    },
    server: { host: '127.0.0.1', port: 0, fs: { allow: [process.cwd(), realpathSync(resolve('node_modules'))] } },
    logLevel: 'warn',
  })
  await server.listen()
  origin = `http://127.0.0.1:${server.httpServer.address().port}`
  browser = await puppeteer.launch({
    executablePath, headless: true, userDataDir: join(temporary, 'browser-profile'),
    args: ['--no-sandbox', '--disable-background-networking', '--disable-component-update', '--disable-sync', '--disable-breakpad', '--disable-crash-reporter', '--no-first-run'],
  })
  page = await browser.newPage()
  await configurePage(page)

  for (const mode of ['sheet', 'password', 'command', 'fullscreen']) {
    for (const viewport of [{ width: 320, height: 568 }, { width: 390, height: 844 }, { width: 844, height: 390 }, { width: 1280, height: 900 }]) {
      await record(`${mode}-${viewport.width}x${viewport.height}`, async () => {
        await open(mode, viewport)
        const limit = await bounds()
        const dialog = await rectangle('[role="dialog"]')
        visible(dialog, limit, mode)
        if (mode === 'password') {
          const submit = await rectangle('button[type="submit"]')
          visible(submit, dialog, 'password submit')
          assert.ok(submit.height >= 44)
          const focused = await page.evaluate(() => document.activeElement?.getAttribute('role') || document.activeElement?.id)
          assert.equal(focused, viewport.width < 640 || viewport.height <= 520 ? 'dialog' : 'set-pw-new')
          const body = '[data-password-gate] [data-slot="dialog-body"]'
          await page.$eval(body, (el) => { el.scrollTop = el.scrollHeight })
          visible(await rectangle('#set-pw-confirm'), await rectangle(body), 'password confirmation')
        }
        if (mode === 'command') {
          await page.$eval('[cmdk-list]', (el) => { el.scrollTop = el.scrollHeight })
          const last = await page.$$eval('[cmdk-item]', (items) => {
            const r = items.at(-1).getBoundingClientRect()
            return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
          })
          visible(last, await rectangle('[cmdk-list]'), 'last search item')
        }
        return { dialog, limit }
      })
    }
  }

  for (const viewport of [{ width: 320, height: 568 }, { width: 390, height: 844 }, { width: 844, height: 390 }, { width: 1280, height: 900 }]) {
    await record(`select-${viewport.width}x${viewport.height}`, async () => {
      await open('select', viewport)
      await page.click('[data-slot="select-trigger"]')
      await page.waitForSelector('[role="listbox"] [role="option"]')
      await settle()
      const mobile = viewport.width < 640 || viewport.height <= 520
      const surfaceSelector = mobile ? '[data-mobile-choice]' : '[role="listbox"]'
      const surface = await rectangle(surfaceSelector)
      visible(surface, await bounds(), 'choice panel')
      if (mobile) {
        assert.ok(await page.$('[data-radix-popper-content-wrapper]:has(> [data-mobile-choice])'), 'mobile choices should use their dedicated bottom panel')
        await page.screenshot({ path: join(output, `select-${viewport.width}x${viewport.height}-open.png`) })
        await page.$eval('[data-mobile-choice] [data-radix-select-viewport]', (el) => { el.scrollTop = el.scrollHeight })
        const last = await rectangle('[role="option"]:last-child')
        visible(last, await rectangle('[data-mobile-choice] [data-radix-select-viewport]'), 'last mobile choice')
        await page.click('[role="option"]:last-child')
        assert.equal(await page.evaluate(() => window.__MOBILE_LAYOUT__.selectedChoice), 'choice-24')
      } else {
        await page.click('[role="option"]:last-child')
        assert.equal(await page.evaluate(() => window.__MOBILE_LAYOUT__.selectedChoice), 'choice-24')
      }
      return { surface, mobile }
    })
  }

  for (const mode of ['sheet', 'password', 'command', 'fullscreen']) {
    await record(`${mode}-keyboard-offset-safe`, async () => {
      await open(mode, { width: 390, height: 844 })
      await page.evaluate(() => {
        window.__MOBILE_LAYOUT__.setSafeInsets({ top: 47, bottom: 34, left: 10, right: 10 })
        window.__MOBILE_LAYOUT__.setViewport({ top: 100, height: 320 })
      })
      await settle()
      const limit = await bounds()
      assert.equal(limit.top, 100)
      assert.equal(limit.bottom, 420)
      const dialog = await rectangle('[role="dialog"]')
      visible(dialog, limit, mode)
      if (mode === 'password') visible(await rectangle('button[type="submit"]'), dialog, 'keyboard submit')
      return { dialog, limit, simulated: true }
    })
  }

  await record('password-tiny-scroll', async () => {
    await open('password', { width: 390, height: 180 })
    await page.$eval('[role="dialog"]', (el) => { el.scrollTop = el.scrollHeight })
    const dialog = await rectangle('[role="dialog"]')
    visible(await rectangle('button[type="submit"]'), dialog, 'tiny submit')
    return { dialog }
  })

  for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 180 }]) {
    await record(`filters-${viewport.width}x${viewport.height}`, async () => {
      await open('filters', viewport)
      await page.click('#root button')
      await page.waitForSelector('[role="dialog"]')
      await settle()
      const dialog = await rectangle('[role="dialog"]')
      visible(dialog, await bounds(), 'filters')
      await page.$eval('[role="dialog"]', (el) => { el.scrollTop = el.scrollHeight })
      const last = await page.$$eval('[role="dialog"] [role="combobox"]', (items) => {
        const r = items.at(-1).getBoundingClientRect()
        return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
      })
      visible(last, dialog, 'filter sort')
      return { dialog, last }
    })
  }

  for (const mode of ['settings', 'conversations', 'mask', 'formula']) {
    for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 390 }]) {
      await record(`${mode}-${viewport.width}x${viewport.height}`, async () => {
        await open(mode, viewport)
        const dialog = await rectangle('[role="dialog"]')
        visible(dialog, await bounds(), mode)
        return { dialog }
      })
    }
    await record(`${mode}-keyboard-offset`, async () => {
      await open(mode, { width: 390, height: 844 })
      await page.evaluate(() => window.__MOBILE_LAYOUT__.setViewport({ top: 80, height: 300 }))
      await settle()
      const dialog = await rectangle('[role="dialog"]')
      visible(dialog, await bounds(), mode)
      return { dialog, simulated: true }
    })
  }

  for (const mode of ['settings', 'mask']) {
    await record(`${mode}-tiny-scroll`, async () => {
      await open(mode, { width: 390, height: 844 })
      await page.evaluate(() => window.__MOBILE_LAYOUT__.setViewport({ height: 180 }))
      await settle()
      const dialog = await rectangle('[role="dialog"]')
      visible(dialog, await bounds(), mode)
      await page.$eval('[role="dialog"]', (el) => { el.scrollTop = el.scrollHeight })
      if (mode === 'settings') {
        assert.equal(await page.$eval('[role="dialog"]', (el) => getComputedStyle(el).overflowY), 'auto')
        assert.ok(await page.$eval('[role="dialog"]', (el) => el.scrollTop > 0), 'tiny settings must scroll')
      } else {
        const last = await page.$$eval('[role="dialog"] [data-slot="dialog-footer"] button', (buttons) => {
          const r = buttons.at(-1).getBoundingClientRect()
          return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
        })
        visible(last, dialog, 'tiny image mask submit')
      }
      return { dialog, simulated: true }
    })
  }

  await record('mask-short-landscape-stage', async () => {
    await open('mask', { width: 844, height: 390 })
    await page.evaluate(() => window.__MOBILE_LAYOUT__.setViewport({ top: 40, height: 300 }))
    await settle()
    assert.equal(await page.$eval('[role="dialog"]', (el) => el.dataset.imageMaskCompact), 'true')
    await page.$eval('[role="dialog"] canvas', (el) => el.scrollIntoView({ block: 'center' }))
    await settle()
    const canvas = await rectangle('[role="dialog"] canvas')
    assert.ok(canvas.height >= 80, 'short landscape canvas must remain usable')
    visible(canvas, await rectangle('[role="dialog"]'), 'short landscape canvas')
    await page.$eval('[role="dialog"]', (el) => { el.scrollTop = el.scrollHeight })
    const submit = await page.$$eval('[data-slot="dialog-footer"] button', (buttons) => {
      const r = buttons.at(-1).getBoundingClientRect()
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
    })
    visible(submit, await rectangle('[role="dialog"]'), 'short landscape submit')
    return { canvas, submit, simulated: true }
  })

  for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 390 }, { width: 390, height: 180 }, { width: 844, height: 300 }]) {
    await record(`workspace-${viewport.width}x${viewport.height}`, async () => {
      await open('workspace', viewport)
      const root = '[data-workspace-management-dialog]'
      const pane = '[data-workspace-management-scroll]'
      const dialog = await rectangle(root)
      visible(dialog, await bounds(), 'workspace')
      if (viewport.height < 520) {
        assert.equal(await page.$eval(root, (el) => getComputedStyle(el).overflowY), 'auto')
        assert.equal(await page.$eval('[role="tablist"]', (el) => el.getAttribute('aria-orientation')), 'horizontal')
        await page.$eval(`${pane} li:last-child`, (el) => el.scrollIntoView({ block: 'end' }))
      } else {
        await page.$eval(pane, (el) => { el.scrollTop = el.scrollHeight })
      }
      await settle()
      const last = await rectangle(`${pane} li:last-child`)
      visible(last, viewport.height < 520 ? await rectangle(root) : await rectangle(pane), 'last workspace member')
      if (viewport.height < 520) await page.$eval(root, (el) => { el.scrollTop = el.scrollHeight })
      const close = await page.$$eval(`${root} [data-slot="dialog-footer"] button`, (buttons) => {
        const r = buttons.at(-1).getBoundingClientRect()
        return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
      })
      visible(close, await rectangle(root), 'workspace footer close')
      return { dialog, last, close }
    })
  }

  for (const file of ['ready', 'invalid']) {
    await record(`template-tiny-${file}`, async () => {
      await open('template', { width: 320, height: 180 }, file)
      await settle()
      const picker = '[data-template-picker]'
      assert.equal(await page.$eval(picker, (el) => el.dataset.templateScroll), 'content')
      if (file === 'invalid') {
        await page.$eval(picker, (el) => { el.scrollTop = el.scrollHeight })
        await page.$eval('[data-template-error] button', (el) => el.scrollIntoView({ block: 'center' }))
        visible(await rectangle('[data-template-error] button'), await rectangle(picker), 'template retry')
      } else {
        await page.$eval(picker, (el) => { el.scrollTop = el.scrollHeight })
        await page.$$eval('[data-template-list] button', (buttons) => buttons.at(-1).querySelector('span').scrollIntoView({ block: 'center' }))
        const last = await page.$$eval('[data-template-list] button', (buttons) => {
          const r = buttons.at(-1).querySelector('span').getBoundingClientRect()
          return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
        })
        visible(last, await rectangle(picker), 'last template title')
        visible(last, await rectangle('[role="dialog"]'), 'last template title in dialog')
      }
      await page.$eval('[role="dialog"]', (el) => { el.scrollTop = el.scrollHeight })
      visible(await rectangle('[data-slot="dialog-footer"] button'), await rectangle('[role="dialog"]'), 'template confirm')
      return { picker: await rectangle(picker), simulated: true }
    })
  }

  await record('fullscreen-zoom-retains-layout', async () => {
    await open('fullscreen', { width: 390, height: 844 })
    const before = await rectangle('[role="dialog"]')
    await page.evaluate(() => window.__MOBILE_LAYOUT__.setViewport({ top: 100, left: 60, height: 320, width: 200, scale: 2 }))
    await settle()
    const after = await rectangle('[role="dialog"]')
    assert.deepEqual(after, before, 'pinch zoom must preserve surface layout dimensions')
    return { before, after, simulated: true }
  })

  await record('welcome-body-and-celebration', async () => {
    await open('welcome', { width: 844, height: 390 })
    visible(await rectangle('[role="dialog"]'), await bounds(), 'welcome wizard')
    for (let step = 0; step < 5; step++) {
      await page.waitForFunction(() => document.querySelector('[data-welcome-ready]') || document.querySelector('[role="dialog"][data-state="open"] [data-slot="dialog-footer"] button:last-child'))
      if (await page.$('[data-welcome-ready]')) break
      await page.$eval('[role="dialog"] [data-slot="dialog-footer"] button:last-child', (button) => button.click())
      await settle()
    }
    await page.waitForSelector('[data-welcome-ready]')
    const dialog = await rectangle('[data-welcome-ready]')
    visible(dialog, await bounds(), 'welcome celebration')
    visible(await rectangle('[data-welcome-ready] [data-slot="dialog-footer"] button'), dialog, 'welcome CTA')
    return { dialog }
  })

  for (const input of ['/', '@']) {
    await record(`composer-${input === '/' ? 'slash' : 'mention'}-scroll-keyboard`, async () => {
      await open('composer', { width: 390, height: 844 })
      await page.click('[contenteditable="true"]')
      await page.keyboard.down('Meta')
      await page.keyboard.press('A')
      await page.keyboard.up('Meta')
      await page.keyboard.type(input)
      await page.waitForSelector('[data-command-placement]')
      await page.waitForFunction(() => document.querySelectorAll('[data-command-index]').length > 3)
      await settle()
      const menuSelector = '[data-command-placement]'
      const before = await rectangle(menuSelector)
      visible(before, await bounds(), 'composer suggestions')
      const manualScroll = await page.$eval(menuSelector, (menu) => {
        menu.scrollTop = menu.scrollHeight
        return menu.scrollTop
      })
      await settle()
      assert.equal(await page.$eval(menuSelector, (menu) => menu.scrollTop), manualScroll, 'manual menu scroll must not jump back to selected item')
      await page.evaluate(() => window.__MOBILE_LAYOUT__.setViewport({ height: 320, top: 40 }))
      await settle()
      const after = await rectangle(menuSelector)
      visible(after, await bounds(), 'keyboard suggestions')
      return { before, after, manualScroll, simulated: true }
    })
  }

  for (const file of ['docx', 'pptx', 'xlsx', 'long-xlsx', 'text', 'invalid']) {
    for (const viewport of [{ width: 390, height: 844 }, { width: 844, height: 390 }]) {
      await record(`office-${file}-${viewport.width}x${viewport.height}`, async () => {
        await open('office', viewport, file)
        await page.waitForFunction(() => window.__MOBILE_LAYOUT__.officeReady)
        await settle()
        const measured = await page.evaluate(() => window.__MOBILE_LAYOUT__.scrollOfficeToEnd())
        visible(measured.scroller, measured.clip, `${file} scrollport`)
        assert.ok(measured.hiddenScrollerBottom <= 1, JSON.stringify(measured))
        if (file === 'docx' || file === 'pptx' || file === 'text') {
          assert.ok(measured.finalContent.bottom <= measured.scroller.bottom + 1, `${file} final content remains below its visible scrollport`)
        }
        if (file === 'long-xlsx') {
          assert.ok(measured.tableLastRow.top >= measured.scroller.top - 1 && measured.tableLastRow.bottom <= measured.scroller.bottom + 1, 'last spreadsheet row is vertically visible')
          const cell = await page.$eval('[role="tabpanel"]', (el) => {
            el.scrollLeft = el.scrollWidth
            const last = el.querySelector('tbody tr:last-child td:last-child')
            const r = last.getBoundingClientRect()
            return { text: last.textContent, left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
          })
          assert.ok(cell.text.includes('END-COLUMN-H'))
          visible(cell, measured.scroller, 'last spreadsheet column')
        }
        return measured
      })
    }
  }
  assert.deepEqual(errors, [])
  console.log(`Completed ${results.length} local frontend browser scenarios. Artifacts: ${output}`)
} finally {
  await browser?.close()
  await server?.close()
  await rm(temporary, { recursive: true, force: true })
}
