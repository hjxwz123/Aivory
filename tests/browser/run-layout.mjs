import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import puppeteer from 'puppeteer-core'

const baseUrl = process.env.AIVORY_LAYOUT_TEST_URL || 'http://127.0.0.1:5173'
const executablePath = process.env.CHROME_PATH || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
].find(existsSync)
assert.ok(executablePath, 'Set CHROME_PATH to a Chrome/Chromium executable')
const policy = {
  password_login_enabled: true, passkey_login_enabled: true, entry_mode: 'login_page',
  default_provider: null, oauth_initial_password_policy: 'required', providers: [],
}
const user = {
  id: 'layout-user', email: 'layout@example.test', name: 'Layout', role: 'admin', status: 'active',
  has_password: true, created_at: 1,
  settings: { onboarded: true, admin_onboarding_v1: 'completed', language: 'zh', theme: 'light' },
}
const model = {
  id: 'layout-model', label: 'GPT', kind: 'chat', request_id: 'gpt-layout', channel_id: 'channel-0',
  description: '', icon: '', enabled: true, stream: true, tool_mode: 'native', tags: [],
}
const channels = Array.from({ length: 40 }, (_, i) => ({
  id: `channel-${i}`, name: `Channel ${i}`, type: 'openai', api_format: 'chat',
  base_url: 'https://upstream.example.test/v1', enabled: true, has_api_key: true, sort_order: i,
}))
const messages = Array.from({ length: 30 }, (_, i) => ({
  id: `message-${i}`, role: i % 2 ? 'assistant' : 'user', created_at: 1,
  blocks: [{ kind: 'text', text: `Message ${i}: ${'A long shared conversation remains readable. '.repeat(10)}` }],
}))
let authenticated = false
function responseFor(route) {
  if (route === '/auth/session') return {
    authenticated, user: authenticated ? user : null, access_token: 'layout-token',
    request_signing_key: 'layout-key', auth_policy: policy,
  }
  const fixtures = {
    '/public/auth-policy': policy, '/public/needs-setup': { needs_setup: false },
    '/public/signup-open': { open: true, captcha_required: false, login_captcha_required: false },
    '/public/legal-config': { contact_email: 'support@example.test', privacy_text: '', terms_text: '' },
    '/public/user-groups': [], '/user-groups': [], '/credit-packages': [],
    '/payments/methods': [], '/payments/orders': { orders: [], total: 0 },
    '/public/shared/layout': { title: 'Layout conversation', messages, created_at: 1 },
    '/me': user, '/me/settings': user.settings,
    '/me/credits': { permanent: 100, available: 100, timed: { balance: 0, grants: [] } },
    '/me/credit-adjustment-notifications': { notifications: [] },
    '/workspaces': { workspaces: [] }, '/models': { models: [model], default_id: model.id },
    '/image-models': { models: [], default_id: '' }, '/model-tags': [],
    '/conversations': { conversations: [], has_more: false },
    '/projects': [], '/skills': [], '/me/skills': [], '/me/prompts': [], '/me/mcps': [],
    '/library/catalog': { skills: [], prompts: [], mcp: [] },
    '/kbs': [], '/me/storage': { used_bytes: 0, quota_bytes: 100000 },
    '/me/files': { files: [], total: 0 },
    '/workspaces/join/layout': { name: 'Layout workspace', id: 'workspace-layout' },
    '/admin/channels': channels, '/admin/channels/health': { channels: {} },
    '/admin/models': [model], '/admin/settings': {},
    '/admin/users': { users: [user], total: 1 }, '/admin/user-groups': [],
    '/admin/files': { files: [], total: 0 },
    '/admin/onboarding': { status: 'completed', steps: [] },
    '/announcement': { enabled: false, bar_enabled: false },
  }
  return fixtures[route] ?? {}
}

const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage()
const errors = []
page.on('pageerror', (error) => errors.push(String(error)))
await page.setRequestInterception(true)
page.on('request', (request) => {
  const url = new URL(request.url())
  if (url.pathname.startsWith('/api/')) {
    void request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(responseFor(url.pathname.slice(4))) })
  } else {
    void request.continue()
  }
})
await page.evaluateOnNewDocument(() => {
  localStorage.setItem('aivory.lang', 'zh')
  localStorage.setItem('aivory.theme', 'light')
})

async function facts() {
  return page.evaluate(() => {
    const doc = document.scrollingElement
    return {
      documentHeight: doc.scrollHeight, viewportHeight: doc.clientHeight,
      documentWidth: doc.scrollWidth, viewportWidth: doc.clientWidth,
      rootHeight: document.getElementById('root').getBoundingClientRect().height,
      panes: [...document.querySelectorAll('.app-viewport, main, .app-scroll')].map((pane) => ({
        tag: pane.tagName, className: pane.className, rect: pane.getBoundingClientRect().toJSON(),
        overflow: getComputedStyle(pane).overflowY, display: getComputedStyle(pane).display,
        minHeight: getComputedStyle(pane).minHeight,
      })),
    }
  })
}
async function checkDocument(label) {
  const result = await facts()
  if (result.documentHeight > result.viewportHeight + 1) {
    await page.screenshot({ path: path.join(tmpdir(), 'aivory-layout-overflow.png') })
  }
  assert.ok(result.documentHeight <= result.viewportHeight + 1, `${label}: document overflows ${JSON.stringify(result)}`)
  assert.ok(result.documentWidth <= result.viewportWidth + 1, `${label}: horizontal overflow ${JSON.stringify(result)}`)
  assert.deepEqual(errors, [], `${label}: rendering errors`)
}
async function checkScrollable(selector, label) {
  const result = await page.$eval(selector, (pane) => {
    pane.scrollTop = pane.scrollHeight
    const box = pane.getBoundingClientRect()
    return { scrolled: pane.scrollTop > 0, bottom: box.bottom, height: innerHeight }
  })
  assert.ok(result.scrolled, `${label}: long content must remain scrollable`)
  assert.ok(result.bottom <= result.height + 1, `${label}: scroll container exceeds viewport`)
  await checkDocument(label)
}

try {
  for (const viewport of [
    { width: 1440, height: 900 }, { width: 1024, height: 500 },
    { width: 390, height: 844 }, { width: 844, height: 390 },
    { width: 320, height: 480 },
  ]) {
    await page.setViewport(viewport)
    authenticated = false
    for (const route of ['/login', '/register', '/forgot-password', '/privacy', '/terms', '/share/layout', '/welcome']) {
      await page.goto(`${baseUrl}${route}`, { waitUntil: 'networkidle0' })
      await page.waitForSelector(route.startsWith('/share') ? '#shared-conversation-title' : route === '/welcome' ? '[data-page-scroll]' : 'main')
      await checkDocument(`${route} ${viewport.width}x${viewport.height}`)
      if (['/login', '/register', '/forgot-password'].includes(route)) {
        const width = await page.$eval('.login-panel', (pane) => ({ scroll: pane.scrollWidth, client: pane.clientWidth }))
        assert.ok(width.scroll <= width.client + 1, `${route}: form has horizontal overflow ${JSON.stringify(width)}`)
        if (route === '/login') {
          await page.focus('.login-input')
          const focusedWidth = await page.$eval('.login-panel', (pane) => ({ scroll: pane.scrollWidth, client: pane.clientWidth }))
          assert.ok(focusedWidth.scroll <= focusedWidth.client + 1, 'Focused fields must not create horizontal scrolling')
        }
      }
      if (route.startsWith('/share') || ['/privacy', '/terms', '/welcome'].includes(route)) {
        await checkScrollable('[data-page-scroll]', route)
      }
      if (route === '/register' && viewport.height <= 500) await checkScrollable('.login-panel', 'register form')
      if (route === '/register' && viewport.width === 844) {
        await page.screenshot({ path: path.join(tmpdir(), 'aivory-layout-register-landscape.png') })
      }
    }
    authenticated = true
    for (const route of ['/', '/projects', '/files', '/skills', '/kb', '/workspace/join/layout', '/missing', '/admin/channels', '/admin/models', '/admin/users', '/admin/files', '/admin/settings/model-policy']) {
      await page.goto(`${baseUrl}${route}`, { waitUntil: 'networkidle0' })
      await page.waitForSelector(route === '/' ? '[contenteditable="true"]' : 'main')
      await checkDocument(`${route} ${viewport.width}x${viewport.height}`)
      if (route === '/admin/channels') await checkScrollable('main > .overflow-y-auto', 'admin channels')
      if (route === '/' && viewport.width === 390) {
        await page.screenshot({ path: path.join(tmpdir(), 'aivory-layout-chat-phone.png') })
        // A keyboard-sized viewport keeps the composer reachable in its pane.
        await page.setViewport({ width: 390, height: 360 })
        await page.waitForFunction(() => document.getElementById('root').getBoundingClientRect().height === 360)
        await checkDocument('chat with keyboard-sized viewport')
        await page.$eval('[contenteditable="true"]', (editor) => editor.scrollIntoView({ block: 'nearest' }))
        await checkDocument('composer focus')
        await page.setViewport(viewport)
        await page.evaluate(() => {
          Object.defineProperty(window.visualViewport, 'height', { value: 340, configurable: true })
          window.visualViewport.dispatchEvent(new Event('resize'))
        })
        await page.waitForFunction(() => document.getElementById('root').getBoundingClientRect().height === 340)
        await page.$eval('[contenteditable="true"]', (editor) => editor.scrollIntoView({ block: 'nearest' }))
        await checkDocument('visual viewport changes without a layout viewport resize')
        assert.ok(await page.$eval('[contenteditable="true"]', (editor) => editor.getBoundingClientRect().bottom <= 340), 'Composer must fit above the visual keyboard')
        await page.evaluate(() => {
          Reflect.deleteProperty(window.visualViewport, 'height')
          window.visualViewport.dispatchEvent(new Event('resize'))
        })
        await page.waitForFunction(() => document.getElementById('root').getBoundingClientRect().height === innerHeight)
      }
    }
    console.log(`PASS: public, authentication, chat and admin layouts at ${viewport.width}x${viewport.height}`)
  }
  authenticated = false
  await page.setViewport({ width: 390, height: 560 })
  for (const language of ['en', 'zh-Hant', 'ja', 'fr']) {
    await page.evaluate((code) => localStorage.setItem('aivory.lang', code), language)
    for (const route of ['/login', '/register']) {
      await page.goto(`${baseUrl}${route}`, { waitUntil: 'networkidle0' })
      await page.waitForSelector('.login-panel')
      await checkDocument(`${route} ${language}`)
      assert.equal(await page.$eval('.login-panel', (pane) => pane.scrollWidth <= pane.clientWidth), true, `${route} ${language}: horizontal form overflow`)
    }
  }
  console.log('PASS: login and registration forms fit in all five languages, including focused inputs')
} catch (error) {
  console.error('Layout verification failed at', page.url(), errors)
  await page.screenshot({ path: path.join(tmpdir(), 'aivory-layout-failure.png') }).catch(() => {})
  throw error
} finally {
  await browser.close()
}
