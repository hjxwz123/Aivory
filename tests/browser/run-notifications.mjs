import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir, readFile } from 'node:fs/promises'
import puppeteer from 'puppeteer-core'

const executablePath = process.env.CHROME_PATH || ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium'].find(existsSync)
const baseUrl = process.env.AIVORY_LAYOUT_TEST_URL || 'http://127.0.0.1:5173'
const screenshots = '/tmp/aivory-notifications-review'
await mkdir(screenshots, { recursive: true })
const user = { id: 'notice-user', email: 'fixture@example.test', name: 'Fixture', role: 'user', status: 'active', has_password: true, settings: { onboarded: true, language: 'zh', theme: 'light' } }
const policy = { password_login_enabled: true, passkey_login_enabled: true, entry_mode: 'login_page', providers: [], oauth_initial_password_policy: 'required' }
const model = { id: 'fixture-model', label: 'Model', kind: 'chat', request_id: 'fixture-model', channel_id: 'fixture-channel', enabled: true, stream: true, tags: [] }
let notifications = [
  { id: 'notice-new', title: 'New release', body: '<p>Latest notification content.</p><img src="x" onerror="window.notificationUnsafe=true">', enabled: true, version: 'new-v1', created_at: 1728000200, updated_at: 1728000200 },
  { id: 'notice-old', title: 'Earlier update', body: '<p>Earlier notification content.</p>', enabled: true, version: 'old-v1', created_at: 1728000100, updated_at: 1728000100 },
  { id: 'notice-unviewed', title: 'Historical notice', body: '<p>Unread history.</p>', enabled: true, version: 'unviewed-v1', created_at: 1728000050, updated_at: 1728000050 },
  { id: 'notice-draft', title: 'Private draft', body: 'Hidden content', enabled: false, version: 'draft-v1', created_at: 1728000000, updated_at: 1728000000 },
]
const states = new Map()
let mutation = 0
let failDismiss = false
let popup = { enabled: true, title: 'Campaign', body: '<p>Promotion</p>', remember_dismiss: false, require_read: false, updated_at: 100, button_text: 'Explore offer', button_url: 'https://example.test/offer' }
const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage()
const errors = []
page.on('pageerror', (error) => errors.push(String(error)))
await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
await page.setRequestInterception(true)
page.on('request', (request) => {
  const url = new URL(request.url())
  const pathname = url.pathname
  if (!pathname.startsWith('/api/')) { void request.continue(); return }
  let status = 200
  let response
  const match = pathname.match(/^\/api\/(admin\/)?notifications(?:\/([^/]+))?(\/read)?$/)
  if (match) {
    const admin = Boolean(match[1])
    const id = match[2]
    const item = notifications.find((entry) => entry.id === id)
    const body = request.postData() ? JSON.parse(request.postData()) : {}
    if (match[3]) {
      if (body.dismiss && failDismiss) { failDismiss = false; status = 500; response = { error: 'Failed to save dismissal' } }
      else if (!item || item.version !== body.version) { status = 409; response = { error: 'changed' } }
      else {
        const key = `${user.id}:${id}`
        const state = states.get(key) ?? {}
        if (body.read !== false) state.read = body.version
        if (body.dismiss) state.dismissed = body.version
        states.set(key, state)
        response = { ok: true }
      }
    } else if (request.method() === 'POST' || request.method() === 'PUT') {
      const saved = { ...item, ...body, id: id || `created-${++mutation}`, version: `edit-${++mutation}`, created_at: item?.created_at ?? 1728000300, updated_at: 1728000300 + mutation }
      notifications = [saved, ...notifications.filter((entry) => entry.id !== saved.id)]
      response = saved
    } else if (request.method() === 'DELETE') {
      notifications = notifications.filter((entry) => entry.id !== id)
      response = { ok: true }
    } else if (id) response = item ?? { error: 'not found' }
    else {
      const all = notifications.filter((entry) => (admin || entry.enabled) && entry.title.toLowerCase().includes((url.searchParams.get('search') ?? '').toLowerCase()))
      const offset = Number(url.searchParams.get('offset') ?? 0)
      response = { total: all.length, notifications: all.slice(offset, offset + 50).map(({ body: _body, ...entry }) => {
        const state = states.get(`${user.id}:${entry.id}`) ?? {}
        return { ...entry, unread: state.read !== entry.version, should_popup: state.dismissed !== entry.version }
      }) }
    }
  } else {
    const fixtures = {
      '/api/auth/session': { authenticated: true, user, access_token: 'fixture', request_signing_key: 'fixture', auth_policy: policy },
      '/api/me': user, '/api/me/settings': user.settings, '/api/public/auth-policy': policy,
      '/api/public/needs-setup': { needs_setup: false }, '/api/public/signup-open': { open: true },
      '/api/announcement': popup, '/api/workspaces': { workspaces: [] },
      '/api/models': { models: [model], default_id: model.id }, '/api/image-models': { models: [], default_id: '' },
      '/api/conversations': { conversations: [], has_more: false }, '/api/library/catalog': { skills: [], prompts: [], mcp: [] },
      '/api/me/credits': { permanent: 100, available: 100, timed: { balance: 0, grants: [] } },
      '/api/me/credit-adjustment-notifications': { notifications: [] },
      '/api/public/desktop-download': { enabled: false }, '/api/admin/settings': { announcement: popup },
      '/api/admin/onboarding': { status: 'completed', deployment_profile: 'personal', required: [], optional: [], full_optional: [] },
    }
    const arrays = ['/api/projects', '/api/skills', '/api/me/skills', '/api/me/prompts', '/api/me/mcps', '/api/model-tags', '/api/kbs', '/api/public/oauth-providers']
    response = fixtures[pathname] ?? (arrays.includes(pathname) ? [] : {})
  }
  void request.respond({ status, contentType: 'application/json', body: JSON.stringify(response) })
})
const center = '[data-notification-center][data-state="open"]'
async function closeCenter() {
  assert.equal(await page.$$eval(`${center} input[type="checkbox"], ${center} button[aria-label="Close"], ${center} button[aria-label="关闭"]`, (elements) => elements.length), 0)
  await page.click(`${center} [data-notification-dismiss]`)
  await page.waitForFunction(() => !document.querySelector('[data-notification-center]'))
}
async function menu() {
  const handles = await page.$$('button[aria-haspopup="menu"]')
  let opened = false
  for (const handle of handles) {
    const available = await handle.evaluate((button) => button.getBoundingClientRect().width > 0 && (button.textContent.includes('Fixture') || button.textContent.trim() === 'F'))
    if (available) { await handle.click(); opened = true; break }
  }
  assert.equal(opened, true)
  await page.waitForSelector('[role="menu"]')
}
async function openManually(label) {
  await menu()
  assert.equal(await page.$eval('[role="menu"]', (element) => element.innerText.includes('帮助')), false)
  await page.evaluate((label) => [...document.querySelectorAll('[role="menuitem"]')].find((entry) => entry.textContent.trim() === label)?.click(), label)
  await page.waitForSelector(center)
}
async function bounds() {
  return page.evaluate(() => {
    const el = document.querySelector('[data-notification-center]') ?? document.querySelector('[data-slot="sheet-content"]')
    const rect = el?.getBoundingClientRect()
    return { pageFits: document.scrollingElement.scrollWidth <= innerWidth && document.scrollingElement.scrollHeight <= innerHeight, modalFits: !rect || (rect.left >= -1 && rect.top >= -1 && rect.right <= innerWidth + 1 && rect.bottom <= innerHeight + 1) }
  })
}
try {
  await page.setViewport({ width: 1280, height: 800 })
  await page.goto(`${baseUrl}/chat`)
  const shown = new Set()
  for (let i = 0; i < 2; i++) {
    await page.waitForSelector('[role="dialog"][data-state="open"]')
    assert.equal(await page.$$eval('[role="dialog"][data-state="open"]', (entries) => entries.length), 1)
    if (await page.$(center)) {
      shown.add('notification')
      await page.waitForFunction(() => document.querySelector('[data-notification-center] article')?.innerText.includes('Latest notification content.'))
      assert.equal(await page.evaluate(() => window.notificationUnsafe), undefined)
      assert.equal(await page.$eval(center, (element) => element.innerText.includes('Private draft')), false)
      await page.screenshot({ path: `${screenshots}/notifications-desktop.png` })
      await closeCenter()
    } else {
      shown.add('promotion')
      assert.equal(await page.$$eval('[role="dialog"] button', (buttons) => buttons.some((button) => button.textContent === 'Explore offer')), true)
      await page.$eval('[role="dialog"] button[aria-label]', (button) => button.click())
      await page.waitForFunction(() => ![...document.querySelectorAll('[role="dialog"]')].some((element) => element.innerText.includes('Campaign')))
    }
  }
  assert.equal(shown.size, 2)
  console.log('PASS: startup dialogs are serialized; custom promotional CTA, sanitized content, and published-only history work')
  popup.enabled = false
  assert.equal(states.get('notice-user:notice-unviewed').read, undefined)
  assert.equal(states.get('notice-user:notice-unviewed').dismissed, 'unviewed-v1')
  await page.reload()
  await page.waitForSelector('[contenteditable="true"]')
  assert.equal(await page.$(center), null)
  await openManually('通知')
  await page.$eval(`${center} nav button:nth-of-type(2)`, (button) => button.click())
  await page.waitForFunction(() => document.querySelector('[data-notification-center] article')?.innerText.includes('Earlier notification content.'))
  await closeCenter()
  notifications[0].version = 'new-v2'
  notifications[0].body = '<p>Updated publication.</p>'
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await page.waitForSelector(center)
  await page.waitForFunction(() => document.querySelector('[data-notification-center] article')?.innerText.includes('Updated publication.'))
  failDismiss = true
  await page.click(`${center} [data-notification-dismiss]`)
  await page.waitForSelector(`${center} [role="alert"]`)
  assert.ok(await page.$(center), 'Failed dismissal closed the notification center')
  assert.equal(states.get('notice-user:notice-new').dismissed, 'new-v1')
  await closeCenter()
  assert.equal(states.get('notice-user:notice-new').dismissed, 'new-v2')
  console.log('PASS: one dismissal button suppresses the current publication, preserves unread history, retries failed saves, and lets edited notifications prompt again')
  for (const language of ['en', 'zh', 'zh-Hant', 'ja', 'fr']) {
    const common = JSON.parse(await readFile(new URL(`../../src/i18n/locales/${language}/common.json`, import.meta.url), 'utf8'))
    user.settings.language = language
    user.settings.theme = language === 'zh' ? 'dark' : 'light'
    for (const viewport of [{ width: 1280, height: 800 }, { width: 320, height: 480 }]) {
      await page.setViewport(viewport)
      await page.goto(`${baseUrl}/chat`)
      await page.waitForSelector('[contenteditable="true"]')
      await openManually(common.notifications.title)
      await page.waitForSelector(`${center} article h2`)
      assert.deepEqual(await bounds(), { pageFits: true, modalFits: true })
      assert.equal(await page.$eval(center, (element) => element.innerText.includes('notifications.')), false)
      await page.screenshot({ path: `${screenshots}/notifications-${language}-${viewport.width}.png` })
      await closeCenter()
    }
  }
  console.log('PASS: five languages, dark theme, desktop and short mobile modals fit without page scrolling')
  user.id = 'notice-admin'; user.role = 'admin'; user.settings.language = 'zh'; user.settings.theme = 'light'
  for (const item of notifications) states.set(`${user.id}:${item.id}`, { dismissed: item.version })
  await page.setViewport({ width: 1280, height: 800 })
  await page.goto(`${baseUrl}/admin/notifications`)
  await page.waitForSelector('[data-admin-list-actions] button', { visible: true })
  await page.waitForSelector('.admin-data-table', { visible: true })
  await page.screenshot({ path: `${screenshots}/admin-notifications.png` })
  await page.$eval('[data-admin-list-actions] button', (button) => button.click())
  await page.waitForSelector('#notification-title')
  await page.type('#notification-title', 'Browser test notification')
  await page.type('#notification-body', '<p>Published in drawer</p>')
  await page.click('[data-slot="sheet-content"] button[type="submit"]')
  await page.waitForFunction(() => !document.querySelector('[data-slot="sheet-content"]'))
  await page.waitForFunction(() => document.querySelector('.admin-data-table')?.innerText.includes('Browser test notification'))
  await page.evaluate(() => [...document.querySelectorAll('.admin-data-table button')].find((button) => button.textContent === 'Browser test notification').click())
  await page.waitForFunction(() => document.querySelector('#notification-title')?.value === 'Browser test notification')
  await page.$eval('#notification-title', (input) => { input.value = ''; input.dispatchEvent(new Event('input', { bubbles: true })) })
  await page.click('#notification-title', { clickCount: 3 })
  await page.keyboard.press('Backspace')
  await page.type('#notification-title', 'Edited in browser')
  await page.click('[data-slot="sheet-content"] button[type="submit"]')
  await page.waitForFunction(() => !document.querySelector('[data-slot="sheet-content"]'))
  await page.waitForFunction(() => document.querySelector('.admin-data-table')?.innerText.includes('Edited in browser'))
  await page.evaluate(() => [...document.querySelectorAll('.admin-data-table button')].find((button) => button.textContent === 'Edited in browser').click())
  await page.waitForSelector('#notification-title')
  await page.setViewport({ width: 320, height: 480 })
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  await page.evaluate(() => document.querySelectorAll('[class*="group/toast"] button[aria-label]').forEach((button) => button.click()))
  await page.waitForFunction(() => !document.querySelector('[class*="group/toast"]'))
  // Editor actions follow the form instead of using a pinned footer.
  await page.$eval('[data-slot="sheet-content"] button[type="submit"]', (button) => button.scrollIntoView({ block: 'center' }))
  await page.waitForFunction(() => { const r = document.querySelector('[data-slot="sheet-content"] button[type="submit"]').getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight })
  await page.screenshot({ path: `${screenshots}/admin-notification-drawer-mobile.png` })
  assert.deepEqual(await bounds(), { pageFits: true, modalFits: true })
  assert.equal(await page.$eval('[data-slot="sheet-content"] button[type="submit"]', (button) => { const r = button.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight }), true)
  await page.evaluate(() => [...document.querySelectorAll('[data-slot="sheet-content"] button')].find((button) => button.textContent === '删除').click())
  await page.evaluate(() => [...document.querySelectorAll('[data-slot="sheet-content"] button')].find((button) => button.textContent === '确认删除').click())
  await page.waitForFunction(() => !document.querySelector('[data-slot="sheet-content"]'))
  assert.equal(notifications.some((item) => item.title === 'Edited in browser'), false)
  assert.deepEqual(errors, [])
  console.log(`PASS: admin create/edit/delete works in the side drawer; review screenshots saved to ${screenshots}`)
} finally { await browser.close() }
