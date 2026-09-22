/**
 * Drive the editor mount harness in real headless Chrome.
 *
 * This is the only check in the repository that proves the editors actually
 * mount and work in a browser. It has already earned its keep: typecheck, the
 * unit suite and `vite build` all passed while (a) the HTML code editor threw
 * `RangeError: Invalid top rule name SingleExpression` and rendered nothing,
 * and (b) AG Grid refused to open any cell editor for want of a module.
 *
 * Usage:
 *   node tests/browser/run-editors.mjs            # assert
 *   node tests/browser/run-editors.mjs --discover # dump DOM facts, no assertions
 *
 * Chrome/Edge is driven through puppeteer-core against the locally installed
 * binary, so nothing is downloaded.
 */
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const CHROME_CANDIDATES = [
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
]

const PORT = 5199
const OUT_DIR = 'tests/fixtures/ooxml/out'
const discover = process.argv.includes('--discover')

/** Fixtures whose bytes are a ZIP; an edit must keep it a valid ZIP ("PK"). */
const ZIP_MAGIC = 'PK'

const CASES = [
  {
    editor: 'code',
    file: 'sample.html',
    name: 'sample.html',
    dir: 'editors',
    expectSelectors: ['.cm-editor', '.cm-content', '[contenteditable="true"]'],
    expectText: 'Revenue is up',
    // Type at the end of the document: CodeMirror reports the new text through
    // onChange, and the payload must stay TEXT (never a ZIP).
    async interact(page) {
      await page.click('.cm-content')
      await page.keyboard.down('Control')
      await page.keyboard.press('End')
      await page.keyboard.up('Control')
      await page.keyboard.type(' <!-- edited -->')
    },
    expectEditMagic: (magic) => magic !== ZIP_MAGIC,
  },
  {
    editor: 'sheet',
    file: 'sample.xlsx',
    name: 'sample.xlsx',
    expectSelectors: ['.ag-root', '.ag-cell'],
    expectText: 'Widgets',
    // Double-click a real cell and replace its value. The written-back package
    // must still be a ZIP, and it must differ from the input.
    async interact(page) {
      const cell = await page.evaluateHandle(() => {
        return [...document.querySelectorAll('.ag-cell')].find((node) =>
          (node.textContent ?? '').includes('Widgets'),
        )
      })
      const element = cell.asElement()
      if (!element) throw new Error('no ag-cell containing "Widgets"')
      const box = await element.boundingBox()
      if (!box) throw new Error('cell has no box')
      await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { clickCount: 2 })
      await new Promise((resolve) => setTimeout(resolve, 400))
      await page.keyboard.type('WidgetsPlus')
      await page.keyboard.press('Enter')
    },
    expectEditMagic: (magic) => magic === ZIP_MAGIC,
  },
  {
    editor: 'docx',
    file: 'sample.docx',
    name: 'sample.docx',
    expectSelectors: ['[contenteditable="true"]'],
    expectText: 'Quarterly Report',
    // The editor serializes on its own debounce; typing proves a real edit is
    // captured rather than only the mount-time snapshot.
    async interact(page) {
      await page.click('[contenteditable="true"]')
      await page.keyboard.down('Control')
      await page.keyboard.press('End')
      await page.keyboard.up('Control')
      await page.keyboard.type(' Edited in Chrome.')
    },
    expectEditMagic: (magic) => magic === ZIP_MAGIC,
  },
  {
    editor: 'pptx',
    file: 'sample.pptx',
    name: 'sample.pptx',
    expectSelectors: ['input'],
    expectText: 'Quarterly Review',
    // Slide 1's first text run is its title. Select-all is done with the
    // keyboard: a triple-click does not reliably select inside this controlled
    // React input, and typing then appends instead of replacing.
    async interact(page) {
      const first = await page.$('input')
      await first.click()
      await page.keyboard.down('Control')
      await page.keyboard.press('KeyA')
      await page.keyboard.up('Control')
      await page.keyboard.type('Q4 Review — browser')
    },
    expectEditMagic: (magic) => magic === ZIP_MAGIC,
  },
]

function findChrome() {
  const found = CHROME_CANDIDATES.find((candidate) => existsSync(candidate))
  if (!found) throw new Error('no Chrome/Edge binary found')
  return found
}

async function inspect(page) {
  return page.evaluate(() => {
    const counts = {}
    for (const selector of [
      '.cm-editor',
      '.cm-content',
      '.ag-root',
      '.ag-cell',
      'input',
      '[contenteditable="true"]',
      'canvas',
      'svg',
      'button',
    ]) {
      counts[selector] = document.querySelectorAll(selector).length
    }
    return {
      harness: window.__HARNESS__,
      counts,
      rootChildren: document.getElementById('root')?.childElementCount ?? 0,
      bodyTextSample: (document.body.innerText || '').slice(0, 600),
      htmlSample: (document.getElementById('root')?.innerHTML || '').slice(0, 500),
    }
  })
}

async function run() {
  const server = await createServer({
    root: process.cwd(),
    server: { port: PORT, strictPort: true },
    logLevel: 'warn',
  })
  await server.listen()
  const base = `http://localhost:${PORT}`

  const browser = await puppeteer.launch({
    executablePath: findChrome(),
    headless: true,
    args: ['--no-sandbox', '--disable-gpu', '--window-size=1200,900'],
  })

  const results = []
  try {
    for (const testCase of CASES) {
      const page = await browser.newPage()
      await page.setViewport({ width: 1200, height: 900 })
      // The first load pre-bundles @docx-editor.dev (≈7 MB) and ag-grid (≈20 MB),
      // which is slow the first time and cached afterwards.
      page.setDefaultNavigationTimeout(180_000)
      page.setDefaultTimeout(60_000)

      const consoleErrors = []
      const badResponses = []
      page.on('pageerror', (error) => consoleErrors.push({ text: String(error), url: '' }))
      page.on('console', (message) => {
        if (message.type() !== 'error') return
        // "Failed to load resource: 404" carries the failing URL only in the
        // message LOCATION, not its text — without this the benign favicon 404
        // is indistinguishable from a genuinely missing asset.
        consoleErrors.push({ text: message.text(), url: message.location()?.url ?? '' })
      })
      page.on('response', (response) => {
        if (response.status() >= 400) badResponses.push(`${response.status()} ${response.url()}`)
      })

      const url = `${base}/tests/browser/editor-harness.html?editor=${testCase.editor}&file=${testCase.file}&name=${testCase.name}&dir=${testCase.dir ?? 'ooxml'}`
      const record = { testCase, facts: null, consoleErrors, badResponses, interactionError: null }

      try {
        await page.goto(url, { waitUntil: 'domcontentloaded' })
        await page
          .waitForFunction(() => window.__HARNESS__ && window.__HARNESS__.phase !== 'loading', { timeout: 60_000 })
          .catch(() => {})
        // Let the lazy editor chunk resolve and paint.
        await new Promise((resolve) => setTimeout(resolve, 3000))

        if (discover) {
          console.log(`\n===== ${testCase.editor} (${testCase.file}) =====`)
          console.log(JSON.stringify(await inspect(page), null, 1))
          console.log('console errors:', consoleErrors.slice(0, 5))
          console.log('bad responses:', badResponses.slice(0, 5))
          await page.close()
          continue
        }

        const before = await inspect(page)
        try {
          await testCase.interact(page)
        } catch (error) {
          record.interactionError = error instanceof Error ? error.message : String(error)
        }
        // Serialization is debounced (up to ~1s) plus ZIP rebuild.
        await new Promise((resolve) => setTimeout(resolve, 4000))
        const after = await inspect(page)

        record.facts = after
        record.before = before

        // Persist what the BROWSER produced so verify_roundtrip.py can re-open
        // it with openpyxl / python-pptx / python-docx. This is the end-to-end
        // claim: edited in Chrome, accepted by the reference implementation.
        const base64 = await page.evaluate(() => window.__HARNESS__?.lastBase64 ?? null)
        if (base64) {
          mkdirSync(OUT_DIR, { recursive: true })
          const extension = testCase.file.split('.').pop() ?? 'bin'
          const outPath = `${OUT_DIR}/browser-${testCase.editor}.${extension}`
          writeFileSync(outPath, Buffer.from(base64, 'base64'))
          record.written = outPath
        }

        results.push(record)
      } catch (error) {
        record.interactionError = record.interactionError ?? String(error).slice(0, 200)
        results.push(record)
      }
      await page.close()
    }
  } finally {
    await browser.close()
    await server.close()
  }

  if (discover) return 0

  let failed = 0
  for (const record of results) {
    const { testCase, facts, consoleErrors, badResponses } = record
    const problems = []

    if (!facts) {
      problems.push('page never produced a result')
    } else {
      const { harness, counts } = facts
      if (!harness) problems.push('harness never initialised')
      if (harness?.phase === 'error') problems.push(`harness error: ${harness.error}`)
      if (harness?.phase !== 'mounted') problems.push(`phase=${harness?.phase}`)
      for (const selector of testCase.expectSelectors) {
        if (!counts[selector]) problems.push(`missing ${selector} (count 0)`)
      }
      if (testCase.expectText) {
        // Checked against the BEFORE snapshot: the point is that the fixture
        // rendered, and an interaction is allowed to change that text (the pptx
        // case replaces the title outright).
        const rendered = record.before?.bodyTextSample ?? ''
        if (!rendered.includes(testCase.expectText)) {
          problems.push(`body text missing "${testCase.expectText}" before editing`)
        }
      }
      if (facts.rootChildren === 0) problems.push('editor rendered nothing')

      const edits = harness?.edits ?? []
      if (edits.length === 0) {
        problems.push('editing produced no payload (onChange never fired)')
      } else {
        const last = edits[edits.length - 1]
        if (!testCase.expectEditMagic(last.magic)) {
          problems.push(`edited payload has wrong format (magic ${JSON.stringify(last.magic)})`)
        }
        if (last.bytes <= 0) problems.push('edited payload was empty')
      }
    }

    if (record.interactionError) problems.push(`interaction: ${record.interactionError}`)

    // A 404 for a real asset is a genuine problem; the harness page has no
    // favicon, which is the one benign case.
    const bad = badResponses.filter((entry) => !/favicon/i.test(entry))
    if (bad.length) problems.push(`HTTP errors: ${bad.slice(0, 2).join(', ')}`)

    const fatal = consoleErrors.filter(
      (entry) => !/favicon|React DevTools/i.test(`${entry.text} ${entry.url}`),
    )
    if (fatal.length) {
      problems.push(
        `console errors: ${fatal
          .slice(0, 2)
          .map((entry) => `${entry.text}${entry.url ? ` @ ${entry.url}` : ''}`)
          .join(' | ')
          .slice(0, 240)}`,
      )
    }

    const label = `${testCase.editor.padEnd(5)} ${testCase.file}`.padEnd(24)
    if (problems.length) {
      failed += 1
      console.log(`FAIL ${label}`)
      for (const problem of problems) console.log(`       - ${problem}`)
      console.log(`       edits=${JSON.stringify(facts?.harness?.edits ?? [])}`)
    } else {
      const last = facts.harness.edits[facts.harness.edits.length - 1]
      console.log(`ok   ${label} edits=${facts.harness.edits.length} last=${last.bytes}B/${last.magic}${record.written ? ` -> ${record.written}` : ''}`)
    }
  }

  console.log(failed === 0 ? '\nall editors mounted and produced edited bytes' : `\n${failed} editor(s) FAILED`)
  return failed === 0 ? 0 : 1
}

run()
  .then((code) => process.exit(code))
  .catch((error) => {
    console.error(error)
    process.exit(2)
  })
