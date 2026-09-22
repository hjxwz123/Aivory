/**
 * Drive the artifact panel's file preview / editor flow and its resize divider
 * in real headless Chrome.
 *
 * `run-preview.mjs` covers the streaming HTML artifact path; this covers what
 * that harness cannot: opening a real document through the panel's `file`
 * source, switching view -> edit -> view, and dragging the divider that trades
 * conversation width for preview width.
 *
 * Usage:
 *   node tests/browser/run-panel.mjs            # assert
 *   node tests/browser/run-panel.mjs --discover # dump DOM facts, no assertions
 */
import { existsSync } from 'node:fs'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const CHROME_CANDIDATES = [
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
]

const PORT = 5197
const discover = process.argv.includes('--discover')
/** The desktop breakpoint the panel keys its inline layout off. */
const VIEWPORT = { width: 1500, height: 1000 }

function findChrome() {
  const found = CHROME_CANDIDATES.find((candidate) => existsSync(candidate))
  if (!found) throw new Error('no Chrome/Edge binary found')
  return found
}

/** Clicks the header's Edit/Preview toggle (its aria-label is localized). */
const CLICK_EDIT_TOGGLE = () => {
  const scope = document.querySelector('aside') ?? document.querySelector('[role="dialog"]') ?? document
  const button = [...scope.querySelectorAll('button')].find((node) =>
    node.querySelector('svg.lucide-pencil, svg.lucide-eye'),
  )
  if (!button) return false
  button.click()
  return true
}

async function facts(page) {
  return page.evaluate(() => {
    const box = (selector) => {
      const element = document.querySelector(selector)
      if (!element) return null
      const rect = element.getBoundingClientRect()
      return { w: Math.round(rect.width), h: Math.round(rect.height) }
    }
    const aside = document.querySelector('aside')
    const divider = document.querySelector('[role="separator"][aria-controls="chat-side-panel"]')
    return {
      panel: box('aside'),
      conversation: box('#conversation'),
      divider: box('[role="separator"][aria-controls="chat-side-panel"]'),
      dividerValue: divider?.getAttribute('aria-valuenow') ?? null,
      docxPane: box('.docx-editor'),
      docxSections: document.querySelectorAll('section.aivory-docx').length,
      editorHost: box('.cm-editor'),
      iframe: box('iframe'),
      editable: box('[contenteditable="true"]'),
      editableCount: document.querySelectorAll('[contenteditable="true"]').length,
      errorNotice: document.querySelectorAll('[role="alert"]').length,
      panelTextLength: (aside?.innerText ?? '').length,
    }
  })
}

/**
 * Console noise that says nothing about the panel: the harness page has no
 * favicon, and the markup fixture is served straight from the Vite dev server,
 * so its document tries to load `/@vite/client` from inside the opaque-origin
 * preview sandbox, which CORS blocks by design (see SandboxedHtmlFrame).
 */
const BENIGN_CONSOLE = /favicon|@vite\/client|@react-refresh|ERR_FAILED|Access to script at/i

async function connectPage(browser, { file, name, dir = 'ooxml' }) {
  const page = await browser.newPage()
  await page.setViewport(VIEWPORT)
  page.setDefaultNavigationTimeout(180_000)
  page.setDefaultTimeout(60_000)
  const errors = []
  page.on('pageerror', (error) => errors.push(String(error)))
  page.on('console', (message) => {
    if (message.type() !== 'error') return
    // A bare "Failed to load resource" carries the failing URL only in the
    // message LOCATION, so the benign favicon 404 is indistinguishable from a
    // missing asset without checking both.
    const haystack = `${message.text()} ${message.location()?.url ?? ''}`
    if (BENIGN_CONSOLE.test(haystack)) return
    errors.push(haystack.trim())
  })

  const query = new URLSearchParams({ file, name, dir }).toString()
  await page.goto(`http://localhost:${PORT}/tests/browser/panel-harness.html?${query}`, {
    waitUntil: 'domcontentloaded',
  })
  await page.waitForFunction(() => window.__PANEL__?.open === true, { timeout: 60_000 })
  await page.waitForSelector('aside', { timeout: 30_000 })
  // The panel's open animation plus the first document render.
  await new Promise((resolve) => setTimeout(resolve, 4500))
  return { page, errors }
}

async function run() {
  const server = await createServer({ root: process.cwd(), server: { port: PORT, strictPort: true }, logLevel: 'warn' })
  await server.listen()
  const browser = await puppeteer.launch({
    executablePath: findChrome(),
    headless: true,
    args: ['--no-sandbox', '--disable-gpu', '--window-size=1500,1000'],
  })

  const problems = []
  const check = (label, condition, detail) => {
    if (condition) {
      console.log(`ok   ${label}`)
      return
    }
    problems.push(`${label}${detail ? ` — ${detail}` : ''}`)
    console.log(`FAIL ${label}${detail ? `\n       ${detail}` : ''}`)
  }

  try {
    // ---------------------------------------------------------------- docx --
    {
      const { page, errors } = await connectPage(browser, { file: 'sample.docx', name: 'sample.docx' })
      const before = await facts(page)
      if (discover) console.log('docx view:', JSON.stringify(before, null, 1))
      check(
        'docx renders in the preview',
        before.docxSections > 0 && before.panelTextLength > 100 && before.errorNotice === 0,
        JSON.stringify(before),
      )

      await page.evaluate(CLICK_EDIT_TOGGLE)
      await new Promise((resolve) => setTimeout(resolve, 6000))
      const editing = await facts(page)
      if (discover) console.log('docx edit:', JSON.stringify(editing, null, 1))
      check(
        'clicking Edit mounts a document editor',
        editing.editableCount > 0 && editing.docxPane !== null && editing.errorNotice === 0,
        JSON.stringify(editing),
      )

      await page.evaluate(CLICK_EDIT_TOGGLE)
      await new Promise((resolve) => setTimeout(resolve, 5000))
      const back = await facts(page)
      if (discover) console.log('docx back to view:', JSON.stringify(back, null, 1))
      check(
        'returning to Preview restores the document',
        back.docxSections > 0 && back.editableCount === 0 && back.errorNotice === 0,
        JSON.stringify(back),
      )

      // ------------------------------------------------------------ divider --
      const dividerBefore = await page.$('[role="separator"][aria-controls="chat-side-panel"]')
      check('the divider between conversation and preview exists', dividerBefore !== null)
      if (dividerBefore) {
        const bounds = await dividerBefore.boundingBox()
        if (!bounds) {
          problems.push('divider has no box')
        } else {
          const startX = bounds.x + bounds.width / 2
          const startY = bounds.y + bounds.height / 2
          const panelBefore = (await facts(page)).panel?.w ?? 0

          // Drag left = the panel grows.
          await page.mouse.move(startX, startY)
          await page.mouse.down()
          await page.mouse.move(startX - 120, startY, { steps: 8 })
          if (discover) {
            await page.screenshot({ path: 'tests/fixtures/ooxml/out/panel-resizing.png' })
          }
          const during = await page.evaluate(() => {
            const frame = document.querySelector('.chat-side-panel-frame')
            return {
              frameResizing: frame?.getAttribute('data-resizing') ?? null,
              transition: frame ? getComputedStyle(frame).transitionProperty : null,
            }
          })
          await page.mouse.up()
          await new Promise((resolve) => setTimeout(resolve, 600))
          const grown = await facts(page)
          if (discover) console.log('after drag left:', JSON.stringify({ during, grown }, null, 1))
          check(
            'dragging the divider left widens the preview',
            (grown.panel?.w ?? 0) >= panelBefore + 80,
            `panel ${panelBefore} -> ${grown.panel?.w}`,
          )
          check(
            'dragging disables the width easing so the frame tracks the pointer',
            during.transition === 'none' && during.frameResizing === 'true',
            JSON.stringify(during),
          )
          check(
            'the conversation column keeps its reserved width',
            (grown.conversation?.w ?? 0) >= 350,
            `conversation ${grown.conversation?.w}`,
          )

          // Drag right = the panel shrinks, and cannot collapse past its floor.
          const moved = await page.$('[role="separator"][aria-controls="chat-side-panel"]')
          const movedBounds = await moved.boundingBox()
          await page.mouse.move(movedBounds.x + movedBounds.width / 2, startY)
          await page.mouse.down()
          await page.mouse.move(movedBounds.x + movedBounds.width / 2 + 900, startY, { steps: 12 })
          await page.mouse.up()
          await new Promise((resolve) => setTimeout(resolve, 600))
          const floor = await facts(page)
          check(
            'the preview cannot be dragged below its usable minimum',
            (floor.panel?.w ?? 0) >= 280 && (floor.panel?.w ?? 0) < panelBefore,
            `panel ${floor.panel?.w}`,
          )

          // Keyboard: the focused divider resizes too.
          await page.focus('[role="separator"][aria-controls="chat-side-panel"]')
          await page.keyboard.press('ArrowLeft')
          await new Promise((resolve) => setTimeout(resolve, 500))
          const keyed = await facts(page)
          check(
            'arrow keys resize from the keyboard',
            (keyed.panel?.w ?? 0) === (floor.panel?.w ?? 0) + 16,
            `panel ${floor.panel?.w} -> ${keyed.panel?.w}`,
          )

          const persisted = await page.evaluate(() => {
            const raw = localStorage.getItem('aivory.settings')
            return raw ? JSON.parse(raw).artifactPanelWidth : null
          })
          check('the chosen width persists with the other settings', typeof persisted === 'number', String(persisted))

          // A reload must restore the stored width — and must do it through the
          // CSS custom property, so the panel's open animation land on it too.
          const chosen = keyed.panel?.w ?? 0
          await page.reload({ waitUntil: 'domcontentloaded' })
          await page.waitForFunction(() => window.__PANEL__?.open === true, { timeout: 60_000 })
          await new Promise((resolve) => setTimeout(resolve, 2500))
          const reloaded = await facts(page)
          if (discover) console.log('after reload:', JSON.stringify(reloaded, null, 1))
          check(
            'a reload restores the dragged width',
            Math.abs((reloaded.panel?.w ?? 0) - chosen) <= 2,
            `before reload ${chosen}, after ${reloaded.panel?.w}`,
          )
        }
      }

      check('no console/page errors during the document flow', errors.length === 0, errors.slice(0, 3).join(' | '))
      await page.close()
    }

    // ------------------------------------------------------------ markup ----
    {
      const { page, errors } = await connectPage(browser, { file: 'sample.html', name: 'sample.html', dir: 'editors' })
      await page.evaluate(CLICK_EDIT_TOGGLE)
      await new Promise((resolve) => setTimeout(resolve, 5000))
      const editing = await facts(page)
      if (discover) console.log('html edit:', JSON.stringify(editing, null, 1))
      check(
        'markup opens the source editor beside its live preview',
        editing.editorHost !== null && (editing.iframe?.h ?? 0) > 100 && editing.errorNotice === 0,
        JSON.stringify(editing),
      )
      check('no console/page errors during the markup flow', errors.length === 0, errors.slice(0, 3).join(' | '))
      await page.close()
    }
  } finally {
    await browser.close()
    await server.close()
  }

  if (discover) return 0

  console.log(problems.length ? `\n${problems.length} check(s) FAILED` : '\npanel resize + document editing verified')
  return problems.length === 0 ? 0 : 1
}

run()
  .then((code) => process.exit(code))
  .catch((error) => {
    console.error(error)
    process.exit(2)
  })
