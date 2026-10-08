import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'
const executablePath = [process.env.CHROME_PATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium'].find(p => p && existsSync(p))
if (!executablePath) throw new Error('Set CHROME_PATH')
const server = await createServer({ server: { host: '127.0.0.1', port: 0 }, logLevel: 'warn' })
let probes = 0
server.middlewares.use((req, res, next) => {
  if (req.url?.startsWith('/__gui_probe')) { probes++; res.end('probe'); return }
  if (!req.url?.startsWith('/api/')) { next(); return }
  res.setHeader('content-type', 'application/json')
  if (req.url.startsWith('/api/me/skill-commands')) res.end(JSON.stringify([{ id: 'catalog:sk_visual', name: 'generative-ui', description: 'Visual answers', source_skill_id: 'sk_visual', instructions: '', can_manage: false, created_at: 0, updated_at: 0 }]))
  else if (req.url.startsWith('/api/me/skills') || req.url.startsWith('/api/me/prompts') || req.url.startsWith('/api/tools')) res.end('[]')
  else if (req.url.startsWith('/api/notifications')) res.end('{"notifications":[],"total":0}')
  else res.end('{}')
})
let browser
try {
  await server.listen()
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`
  browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox', '--disable-background-networking'] })
  const page = await browser.newPage()
  await page.setRequestInterception(true)
  page.on('request', req => {
    const url = new URL(req.url())
    if (!url.pathname.startsWith('/api/')) { req.continue(); return }
    const body = url.pathname.startsWith('/api/me/skill-commands') ? JSON.stringify([{ id: 'catalog:sk_visual', name: 'generative-ui', description: 'Visual answers', source_skill_id: 'sk_visual', instructions: '', can_manage: false, created_at: 0, updated_at: 0 }]) : url.pathname.startsWith('/api/notifications') ? '{"notifications":[],"total":0}' : '[]'
    req.respond({ status: 200, contentType: 'application/json', body })
  })
  page.setDefaultTimeout(60_000)
  const errors = []
  page.on('pageerror', e => errors.push(String(e)))
  await page.setViewport({ width: 1024, height: 900 })
  await page.goto(`${origin}/tests/browser/generative-ui-harness.html`)
  await page.waitForSelector('[data-case="presets"] [role="tab"]')
  assert.equal(await page.$$eval('[data-case="user"] [data-generative-ui], [data-case="ordinary"] [data-generative-ui]', e => e.length), 0)
  assert.match(await page.$eval('[data-case="incomplete"]', e => e.textContent), /Preparing visualization/)
  await page.click('[data-case="presets"] [role="tab"]:nth-child(2)')
  assert.match(await page.$eval('[data-case="presets"] [role="tabpanel"]:not([hidden])', e => e.textContent), /June/)
  await page.focus('[data-case="presets"] [role="tab"]:nth-child(2)')
  await page.keyboard.press('ArrowLeft')
  assert.equal(await page.$eval('[data-case="presets"] [role="tab"]', e => e.getAttribute('aria-selected')), 'true')
  await page.click('[data-case="presets"] summary')
  assert.match(await page.$eval('[data-case="presets"] details[open]', e => e.textContent), /Check inputs/)
  const inner = async selector => {
    await page.waitForFunction(sel => Array.from(document.querySelectorAll('iframe')).length > 0 && Boolean(window.__GUI__), {}, selector)
    for (let i = 0; i < 100; i++) {
      for (const f of page.frames()) { try { if (await f.$(selector)) return f } catch { /* document replacing */ } }
      await new Promise(r => setTimeout(r, 50))
    }
    console.log('Missing frame diagnostics', await Promise.all(page.frames().map(async f => ({ url: f.url(), body: await f.evaluate(() => document.body.innerHTML.slice(0, 600)).catch(String) }))))
    throw new Error(`Frame missing: ${selector}`)
  }
  let content = await inner('#action')
  await content.click('#action')
  assert.equal(await content.$eval('#action', e => e.textContent), 'Updated')
  await page.evaluate(() => { window.__GUI__.setLive(true); window.__GUI__.setHTML('<p id="stream">Live content</p>') })
  await inner('#stream')
  await page.evaluate(() => { window.__GUI__.setHTML('<p id="finished">Complete content</p>'); window.__GUI__.setLive(false) })
  await inner('#finished')
  // Successful child rendering never grants parent DOM, storage, or native API.
  await page.evaluate(origin => window.__GUI__.setHTML(`<pre id="security"></pre><script>
    const results={};for(const [name,fn] of Object.entries({parent:()=>parent.document.body,storage:()=>localStorage.length,cookie:()=>document.cookie,native:()=>top.aivoryDesktop})){try{fn();results[name]='allowed'}catch{results[name]='blocked'}}
    document.getElementById('security').textContent=JSON.stringify(results);
    fetch('${origin}/__gui_probe?fetch').catch(()=>{});new WebSocket('ws://'+new URL('${origin}').host+'/__gui_probe?socket');
    </script><img src="${origin}/__gui_probe?image"><link rel="stylesheet" href="${origin}/__gui_probe?css">`), origin)
  content = await inner('#security')
  const result = JSON.parse(await content.$eval('#security', e => e.textContent))
  for (const name of ['parent', 'storage', 'cookie', 'native']) assert.equal(result[name], 'blocked', name)
  await page.evaluate(origin => window.__GUI__.setHTML(`<p id="navigation">Navigation blocked</p><script>setTimeout(()=>location.href='${origin}/__gui_probe?navigation',100)</script>`), origin)
  await inner('#navigation')
  await new Promise(r => setTimeout(r, 400))
  await page.evaluate(origin => window.__GUI__.setHTML(`<p id="form">No forms or popups</p><form action="${origin}/__gui_probe?form" method="get"><input name="q" value="private"></form><script>document.querySelector('form').submit();window.open('${origin}/__gui_probe?popup');</script>`), origin)
  await inner('#form')
  await new Promise(r => setTimeout(r, 400))
  for (const navigation of ['blob', 'data']) {
    await page.evaluate(({ origin, navigation }) => {
      const payload = `<script>fetch('${origin}/__gui_probe?${navigation}-escape').catch(()=>{})</script>`
      const encoded = JSON.stringify(payload).replace(/</g, '\\u003c')
      const target = navigation === 'blob' ? `URL.createObjectURL(new Blob([payload],{type:'text/html'}))` : `'data:text/html;base64,'+btoa(payload)`
      window.__GUI__.setHTML(`<p id="escape">${navigation}</p><script>const payload=${encoded};setTimeout(()=>location.href=${target},100)</script>`)
    }, { origin, navigation })
    await inner('#escape')
    await new Promise(r => setTimeout(r, 500))
  }
  assert.equal(probes, 0, 'generated HTML sent a network request')
  // Direct administrator command appears without any personal skill row.
  await page.click('[data-case="composer"] .tiptap')
  await page.keyboard.type('/generative-ui')
  await new Promise(r => setTimeout(r, 800))
  await page.waitForFunction(() => [...document.querySelectorAll('[cmdk-item], [role="option"], button')].some(e => e.textContent.includes('generative-ui')), { timeout: 15000 })
  const button = await page.evaluateHandle(() => [...document.querySelectorAll('[role="option"]')].find(e => e.textContent.includes('generative-ui')))
  await button.asElement().click()
  await page.keyboard.type('Visualize usage')
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => window.__SUBMITTED_SKILLS__)
  assert.deepEqual(await page.evaluate(() => window.__SUBMITTED_SKILLS__), ['catalog:sk_visual'])
  await page.evaluate(() => window.__GUI__.setHTML('<style>input{width:100%;background:var(--color-bg-muted);color:var(--color-fg)}</style><label for="n">Quantity</label><input id="n" value="10">'))
  await inner('#n')
  await mkdir('/tmp/aivory-generative-ui', { recursive: true })
  for (const width of [360, 768, 1280]) {
    await page.setViewport({ width, height: 900 })
    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; document.documentElement.classList.toggle('dark', theme === 'dark') }, theme)
      await inner('#n')
      for (const locale of ['en', 'zh', 'zh-Hant', 'ja', 'fr']) {
        await page.evaluate(locale => window.__GUI__.setLanguage(locale), locale)
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${width}/${theme}/${locale} overflows`)
      }
      await new Promise(r => setTimeout(r, 400))
      const themedFrame = await inner('#n')
      assert(await themedFrame.$eval('#n', e => e.getBoundingClientRect().width > 0))
      await page.$eval('[data-case="inline"]', e => e.scrollIntoView({ block: 'center' }))
      await new Promise(r => setTimeout(r, 100))
      await page.screenshot({ path: `/tmp/aivory-generative-ui/${width}-${theme}.png`, fullPage: true })
    }
  }
  assert.deepEqual(errors, [])
  console.log('PASS: preset interactions, private/user boundaries, streaming HTML, network/navigation/storage/native isolation, administrator slash command, 30 responsive/theme/language combinations')
} finally { await browser?.close(); await server.close() }
