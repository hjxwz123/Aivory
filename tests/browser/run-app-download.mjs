import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { createServer } from 'node:http'
import { once } from 'node:events'
import puppeteer from 'puppeteer-core'

const executablePath = process.env.CHROME_PATH || ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium'].find(existsSync)
assert.ok(executablePath, 'Chrome is required')
const baseUrl = process.env.AIVORY_LAYOUT_TEST_URL || 'http://127.0.0.1:5173'
const landing = createServer((_request, response) => {
  response.setHeader('Content-Type', 'text/html')
  response.end('<h1>App downloads</h1>')
})
landing.listen(0, '127.0.0.1')
await once(landing, 'listening')
const download = { enabled: true, url: `http://127.0.0.1:${landing.address().port}/downloads#desktop` }
const user = { id: 'app-download-fixture', email: 'fixture@example.test', name: 'Fixture', role: 'user', status: 'active', has_password: true,
  settings: { onboarded: true, language: 'zh', theme: 'light' } }
const policy = { password_login_enabled: true, passkey_login_enabled: true, entry_mode: 'login_page', providers: [], oauth_initial_password_policy: 'required' }
const model = { id: 'fixture-model', label: 'Model', kind: 'chat', request_id: 'fixture-model', channel_id: 'fixture-channel', enabled: true, stream: true, tags: [] }
let reads = 0
const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage()
await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
const errors = []
page.on('pageerror', (error) => errors.push(String(error)))
await page.setRequestInterception(true)
page.on('request', (request) => {
  const pathname = new URL(request.url()).pathname
  if (!pathname.startsWith('/api/')) { void request.continue(); return }
  const fixtures = {
    '/api/auth/session': { authenticated: true, user, access_token: 'fixture', request_signing_key: 'fixture', auth_policy: policy },
    '/api/me': user, '/api/me/settings': user.settings, '/api/public/auth-policy': policy,
    '/api/public/needs-setup': { needs_setup: false }, '/api/public/signup-open': { open: true },
    '/api/announcement': { enabled: false, bar_enabled: false }, '/api/workspaces': { workspaces: [] },
    '/api/models': { models: [model], default_id: model.id }, '/api/image-models': { models: [], default_id: '' },
    '/api/conversations': { conversations: [], has_more: false }, '/api/library/catalog': { skills: [], prompts: [], mcp: [] },
    '/api/me/credits': { permanent: 100, available: 100, timed: { balance: 0, grants: [] } },
    '/api/me/credit-adjustment-notifications': { notifications: [] },
    '/api/notifications': { notifications: [], total: 0 },
    '/api/public/desktop-download': download.enabled ? download : { enabled: false },
  }
  if (pathname === '/api/public/desktop-download') reads++
  const arrays = ['/api/projects', '/api/skills', '/api/me/skills', '/api/me/prompts', '/api/me/mcps', '/api/model-tags', '/api/kbs', '/api/public/oauth-providers']
  void request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(fixtures[pathname] ?? (arrays.includes(pathname) ? [] : {})) })
})
async function home() {
  await page.goto(`${baseUrl}/chat`)
  await page.waitForSelector('[contenteditable="true"]')
}
async function menu() {
  // The avatar trigger has an accessible account label and no SVG icon.
  await page.evaluate(() => document.activeElement?.blur())
  const trigger = await page.$('button[aria-haspopup="menu"]:has([data-slot="avatar"])')
  if (trigger) await trigger.click()
  else {
    const handles = await page.$$('button[aria-haspopup="menu"]')
    let opened = false
    for (const handle of handles) {
      const info = await handle.evaluate((button) => ({ visible: button.getBoundingClientRect().width > 0, text: button.textContent }))
      if (info.visible && (info.text.includes(user.name) || info.text.trim() === 'F')) { await handle.click(); opened = true; break }
    }
    assert.equal(opened, true, 'Account trigger must be available')
  }
  await page.waitForSelector('[role="menu"]')
}
try {
  await page.setViewport({ width: 1280, height: 850 })
  await home()
  await page.waitForSelector('[data-app-download="home"]')
  assert.equal(reads, 1, 'Home and avatar menu must share one config request')
  assert.equal(await page.$eval('[data-app-download="home"]', (link) => link.href), download.url)
  const landingTarget = browser.waitForTarget((target) => target.url() === download.url)
  await page.click('[data-app-download="home"]')
  const landingPage = await (await landingTarget).page()
  await landingPage.waitForSelector('h1')
  assert.equal(await landingPage.$eval('h1', (heading) => heading.textContent), 'App downloads')
  assert.equal(page.url(), `${baseUrl}/chat`)
  await landingPage.close()
  await menu()
  assert.equal(await page.$eval('[data-app-download="menu"]', (link) => link.href), download.url)
  const menuTarget = browser.waitForTarget((target) => target.url() === download.url)
  await page.click('[data-app-download="menu"]')
  const menuPage = await (await menuTarget).page()
  await menuPage.waitForSelector('h1')
  await menuPage.close()
  console.log('PASS: both web entry points open the configured download page without leaving the conversation')
  for (const lang of ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
    user.settings.language = lang
    for (const theme of ['light', 'dark']) {
      user.settings.theme = theme
      for (const viewport of [{ width: 1280, height: 850 }, { width: 390, height: 844 }, { width: 320, height: 480 }]) {
        await page.setViewport(viewport)
        await home()
        await page.waitForSelector('[data-app-download="home"]')
        const bounds = await page.evaluate(() => ({ documentWidth: document.scrollingElement.scrollWidth, documentHeight: document.scrollingElement.scrollHeight, width: innerWidth, height: innerHeight }))
        if (bounds.documentWidth > bounds.width + 1 || bounds.documentHeight > bounds.height + 1) await page.screenshot({ path: '/tmp/aivory-app-download-overflow.png' })
        assert.ok(bounds.documentWidth <= bounds.width + 1 && bounds.documentHeight <= bounds.height + 1, `${lang}/${theme}/${viewport.width}: ${JSON.stringify(bounds)}`)
        assert.equal(await page.$eval('[data-app-download="home"]', (link) => {
          const bounds = link.getBoundingClientRect()
          const toolbar = link.parentElement.parentElement
          const actions = toolbar.lastElementChild.getBoundingClientRect()
          return bounds.left >= 0 && bounds.top >= 0 && bounds.right + 1 <= actions.left && bounds.bottom <= innerHeight
        }), true, `${lang}/${theme}/${viewport.width}: overlapping corner controls`)
        if (lang === 'zh' && (viewport.width === 1280 || viewport.width === 390)) await page.screenshot({ path: `/tmp/aivory-app-download-${theme}-${viewport.width}.png` })
      }
    }
  }
  console.log('PASS: download entry fits all five languages, both themes, and desktop/mobile widths')
  download.enabled = false
  await home()
  assert.equal(await page.$('[data-app-download="home"]'), null)
  await menu()
  assert.equal(await page.$('[data-app-download="menu"]'), null)
  await page.keyboard.press('Escape')
  console.log('PASS: disabling download visibility hides both entry points')
  download.enabled = true
  const beforeDesktop = reads
  await page.evaluateOnNewDocument(() => {
    window.aivoryDesktop = {
      getInfo: async () => ({ version: '2.5.1-beta.6', platform: 'darwin' }),
      loginInBrowser: async () => ({ status: 'cancelled' }), cancelBrowserLogin: async () => ({ status: 'cancelled' }),
      checkUpdates: async () => ({ status: 'current' }),
    }
  })
  await home()
  assert.equal(await page.$('[data-app-download="home"]'), null)
  await menu()
  assert.equal(await page.$('[data-app-download="menu"]'), null)
  assert.equal(reads, beforeDesktop, 'Desktop clients must not request download promotion configuration')
  assert.deepEqual(errors, [])
  console.log('PASS: desktop clients hide both download entries and skip their config request')
} finally {
  await browser.close()
  landing.closeAllConnections()
  await new Promise((resolve) => landing.close(resolve))
}
