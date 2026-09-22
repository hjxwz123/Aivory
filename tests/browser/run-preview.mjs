/**
 * Reproduce the reported blank-HTML-preview bug in real headless Chrome.
 *
 * Reported symptom: after the assistant finishes writing HTML, opening the
 * preview shows a BLANK frame, and pressing the panel's refresh button makes the
 * content appear. That means the frame held the right srcDoc but never painted
 * it — otherwise a refresh would not have helped.
 *
 * Each scenario drives the REAL ArtifactPanel through the same store calls the
 * app uses, then waits for the sandboxed frame to post its marker back. No
 * marker = the frame never executed its document = blank preview.
 *
 * Usage: node tests/browser/run-preview.mjs
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

const PORT = 5198

/**
 * Host the harness is loaded from.
 *
 * Defaults to localhost, which is a "potentially trustworthy origin" and so is
 * EXEMPT from the preview document's `upgrade-insecure-requests` CSP. Loading
 * over the machine's LAN IP instead reproduces a plain-http self-hosted
 * deployment, where that exemption does not apply.
 */
const ORIGIN_HOST = process.env.PREVIEW_HOST ?? 'localhost'

/** The artifact's own script posts this back, proving the document executed. */
function artifact(marker, body) {
  return `<!doctype html><html><head><meta charset="utf-8"></head><body><script>parent.postMessage({previewMarker:${JSON.stringify(marker)}},'*')<\/script><h1>${body}</h1></body></html>`
}

/**
 * Reports the computed background of a Tailwind utility back to the parent.
 *
 * This is the only way to check that `buildHtmlPreviewDocument`'s injected
 * Tailwind runtime actually compiled: the frame is opaque-origin, so the test
 * cannot read its stylesheet directly. `bg-red-600` must resolve to rgb(220,38,38)
 * when the runtime ran, and stay transparent when it did not.
 */
const TAILWIND_PROBE = `<!doctype html><html><head><meta charset="utf-8"></head><body class="bg-red-600"><script>
setTimeout(function () {
  parent.postMessage({
    previewMarker: 'tailwind',
    background: getComputedStyle(document.body).backgroundColor,
    width: document.body.getBoundingClientRect().width
  }, '*')
}, 2500)
<\/script><h1 class="text-white">Tailwind</h1></body></html>`

const STREAM_CHUNKS = [
  artifact('skipped-too-short', 'x'),
  '<!doctype html><html><head>',
  '<!doctype html><html><head><meta charset="utf-8"></head>',
  '<!doctype html><html><head><meta charset="utf-8"></head><body>',
  artifact('stream-final', 'Streamed content'),
]

const SCENARIOS = [
  {
    name: 'open a finished artifact (the reported flow)',
    async run(page) {
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-finished',
          html,
          shareable: false,
        })
      }, artifact('finished', 'Finished content'))
    },
    expect: 'finished',
  },
  {
    name: 'streaming via autoOpenPreview, then reopen the same block',
    async run(page) {
      // Faithful replay of code-block.tsx: autoOpenPreview on the first chunks,
      // then syncHtml for the rest — the panel auto-opens on the FIRST chunk.
      for (const chunk of STREAM_CHUNKS) {
        await page.evaluate((html) => window.__AUTO_OPEN__('block-stream', html), chunk)
        await new Promise((resolve) => setTimeout(resolve, 60))
      }
      // The user closes the panel while it streams, then clicks preview after
      // the assistant has finished.
      await page.evaluate(() => window.__ARTIFACT_STORE__.getState().close())
      await new Promise((resolve) => setTimeout(resolve, 1500))
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-stream',
          html,
          shareable: false,
        })
      }, STREAM_CHUNKS[STREAM_CHUNKS.length - 1])
    },
    expect: 'stream-final',
  },
  {
    name: 'close the panel and reopen the same block',
    async run(page) {
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-reopen',
          html,
          shareable: false,
        })
      }, artifact('reopen-1', 'First open'))
      await new Promise((resolve) => setTimeout(resolve, 1200))
      await page.evaluate(() => window.__ARTIFACT_STORE__.getState().close())
      await new Promise((resolve) => setTimeout(resolve, 1500))
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-reopen',
          html,
          shareable: false,
        })
      }, artifact('reopen-2', 'Second open'))
    },
    expect: 'reopen-2',
  },
  {
    name: 'streaming with the panel left OPEN (auto-open, never closed)',
    async run(page) {
      for (const chunk of STREAM_CHUNKS) {
        await page.evaluate((html) => window.__AUTO_OPEN__('block-open', html), chunk)
        await new Promise((resolve) => setTimeout(resolve, 120))
      }
    },
    expect: 'stream-final',
  },
  {
    name: 'click preview again while the same block is already open',
    async run(page) {
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-reclick',
          html,
          shareable: false,
        })
      }, artifact('reclick-first', 'First'))
      await new Promise((resolve) => setTimeout(resolve, 1500))
      // The user presses the preview button a second time, after the block has
      // re-rendered with new markup.
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-reclick',
          html,
          shareable: false,
        })
      }, artifact('reclick-second', 'Second'))
    },
    expect: 'reclick-second',
  },
  {
    name: 'switch to a different html block',
    async run(page) {
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-a',
          html,
          shareable: false,
        })
      }, artifact('block-a', 'A'))
      await new Promise((resolve) => setTimeout(resolve, 1500))
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-b',
          html,
          shareable: false,
        })
      }, artifact('block-b', 'B'))
    },
    expect: 'block-b',
  },
  {
    name: 'auto-open then syncHtml without ever closing (pure streaming)',
    async run(page) {
      await page.evaluate((html) => window.__AUTO_OPEN__('block-sync', html), STREAM_CHUNKS[1])
      await new Promise((resolve) => setTimeout(resolve, 400))
      for (const chunk of STREAM_CHUNKS.slice(2)) {
        await page.evaluate(
          (html) => window.__ARTIFACT_STORE__.getState().syncHtml('block-sync', html, false),
          chunk,
        )
        await new Promise((resolve) => setTimeout(resolve, 200))
      }
    },
    expect: 'stream-final',
  },
  {
    // Real assistant HTML leans on Tailwind, which `buildHtmlPreviewDocument`
    // injects. An artifact that renders unstyled is not blank, but an artifact
    // whose layout depends on Tailwind can look blank — and a refresh would
    // "fix" it if the runtime only works on a second load.
    name: 'injected Tailwind runtime actually compiles',
    waitAfter: 6000,
    async run(page) {
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-tailwind',
          html,
          shareable: false,
        })
      }, TAILWIND_PROBE)
    },
    expect: 'tailwind',
    verify(entry) {
      // Tailwind v4 emits oklch, v3 emitted rgb — accept either, and only
      // require that the utility applied SOMETHING rather than nothing.
      const background = String(entry?.background ?? '')
      if (background && background !== 'rgba(0, 0, 0, 0)' && background !== 'transparent') return null
      return `Tailwind did not compile: body background is ${JSON.stringify(background)}`
    },
  },
  {
    // Below 1024px ChatSidePanel renders a Radix Sheet instead of the desktop
    // inline aside, and Sheet content mounts through a portal only while open —
    // a different mount order for the frame.
    name: 'narrow viewport (Sheet branch) opens and renders',
    viewport: { width: 800, height: 900 },
    async run(page) {
      await page.evaluate((html) => {
        window.__ARTIFACT_STORE__.getState().openArtifact({
          type: 'html',
          sourceKey: 'block-sheet',
          html,
          shareable: false,
        })
      }, artifact('sheet-open', 'Sheet branch'))
    },
    expect: 'sheet-open',
  },
]

function findChrome() {
  const found = CHROME_CANDIDATES.find((candidate) => existsSync(candidate))
  if (!found) throw new Error('no Chrome/Edge binary found')
  return found
}

async function run() {
  const server = await createServer({
    root: process.cwd(),
    server: { port: PORT, strictPort: true },
    logLevel: 'warn',
  })
  await server.listen()

  const browser = await puppeteer.launch({
    executablePath: findChrome(),
    headless: true,
    args: ['--no-sandbox', '--disable-gpu', '--window-size=1500,1000'],
  })

  let failed = 0
  try {
    for (const scenario of SCENARIOS) {
      const page = await browser.newPage()
      // The preview is a desktop-only inline split panel below 1024px; scenarios
      // that need the Sheet branch override this.
      const viewport = scenario.viewport ?? { width: 1500, height: 1000 }
      await page.setViewport(viewport)
      page.setDefaultNavigationTimeout(180_000)
      page.setDefaultTimeout(60_000)

      const errors = []
      page.on('pageerror', (error) => errors.push(String(error)))
      page.on('console', (message) => {
        if (message.type() === 'error') errors.push(message.text())
      })

      await page.goto(`http://${ORIGIN_HOST}:${PORT}/tests/browser/preview-harness.html`, {
        waitUntil: 'domcontentloaded',
      })
      await page.waitForFunction(() => Boolean(window.__PREVIEW__), { timeout: 60_000 })
      await new Promise((resolve) => setTimeout(resolve, 1500))

      await scenario.run(page)
      // Well past the 350ms trailing debounce and any remount animation.
      await new Promise((resolve) => setTimeout(resolve, scenario.waitAfter ?? 3000))

      const probe = await page.evaluate(() => window.__PREVIEW__)
      const markers = probe.renders.map((entry) => String(entry.previewMarker ?? ''))
      const matched = probe.renders.find((entry) => entry.previewMarker === scenario.expect)
      const rendered = markers.includes(scenario.expect)
      const verifyProblem = rendered && scenario.verify ? scenario.verify(matched) : null
      // A frame that ran its document but has no box is the "blank preview":
      // the content is there and invisible.
      const frameBox = probe.frameRect
      const collapsed = !frameBox || frameBox.width < 50 || frameBox.height < 50

      if (rendered && !collapsed && !verifyProblem) {
        console.log(
          `ok   ${scenario.name}  (srcDoc=${probe.srcDocLength}B, frame=${Math.round(frameBox.width)}×${Math.round(frameBox.height)}, markers=${markers.length})`,
        )
      } else {
        failed += 1
        console.log(`FAIL ${scenario.name}`)
        if (!rendered) {
          console.log(`       expected marker "${scenario.expect}"`)
          console.log(`       got markers ${JSON.stringify(markers)}`)
        }
        if (verifyProblem) console.log(`       ${verifyProblem}`)
        if (collapsed) {
          console.log(`       frame box collapsed: ${JSON.stringify(probe.frameRect)}`)
          console.log(`       panel box: ${JSON.stringify(probe.panelRect)}`)
        }
        console.log(`       srcDoc length on frame: ${probe.srcDocLength}`)
        console.log(`       panel open: ${probe.panelOpen}`)
      }
      if (errors.length) console.log(`       console errors: ${errors.slice(0, 2).join(' | ').slice(0, 200)}`)
      await page.close()
    }
  } finally {
    await browser.close()
    await server.close()
  }

  console.log(failed === 0 ? '\npreview renders in every scenario' : `\n${failed} scenario(s) FAILED`)
  return failed === 0 ? 0 : 1
}

run()
  .then((code) => process.exit(code))
  .catch((error) => {
    console.error(error)
    process.exit(2)
  })
