import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import puppeteer from 'puppeteer-core'

const executablePath = process.env.CHROME_PATH || ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium'].find(existsSync)
assert.ok(executablePath, 'Chrome is required')
const baseUrl = process.env.AIVORY_LAYOUT_TEST_URL || 'http://127.0.0.1:5173'
const webAgent = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/150.0.0.0 Safari/537.36'
const appAgent = `${webAgent} AivoryDesktop/2.5.1-beta.6`
const user = { id: 'fixture', email: 'test@example.test', name: 'Test', role: 'admin', status: 'active', has_password: true, created_at: 1720000000,
  settings: { onboarded: true, admin_onboarding_v1: 'completed', language: 'zh', theme: 'light' } }
const policy = { password_login_enabled: true, passkey_login_enabled: true, entry_mode: 'login_page', providers: [], oauth_initial_password_policy: 'required' }
const model = { id: 'fixture-model', label: 'Model', kind: 'chat', request_id: 'fixture-model', channel_id: 'fixture-channel', enabled: true, stream: true, tags: [] }
const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage()
const errors = []
page.on('pageerror', (error) => errors.push(String(error)))
await page.setRequestInterception(true)
page.on('request', (request) => {
  const pathname = new URL(request.url()).pathname
  if (!pathname.startsWith('/api/')) { void request.continue(); return }
  const fixtures = {
    '/api/auth/session': { authenticated: true, user, access_token: 'fixture', request_signing_key: 'fixture', auth_policy: policy },
    '/api/me': user, '/api/admin/users/fixture': user, '/api/me/settings': user.settings,
    '/api/admin/users/fixture/login-history': { total: 2, items: [appAgent, webAgent].map((agent, index) => ({ id: `login-${index}`, user_id: user.id, login_at: 1720000000, ip: '127.0.0.1', location: '', user_agent: agent, method: index ? 'password' : 'desktop_browser' })) },
    '/api/admin/user-feedback': { total: 1, limit: 50, offset: 0, items: [{ id: 'feedback-app', user_id: user.id, user_email: user.email, user_name: user.name, message_id: '', conversation_id: '', conversation_title: '', description: 'App record fixture', page_path: '/chat', user_agent: appAgent, viewport_width: 1200, viewport_height: 800, has_screenshot: false, screenshot_size: 0, created_at: 1720000000 }] },
    '/api/admin/audit-logs': { total: 1, page: 1, page_size: 50, logs: [{ id: 'audit-app', actor_user_id: user.id, actor_name: user.name, action: 'settings.update', type: 'settings', target_type: 'settings', target_id: 'fixture', created_at: 1720000000, result: 'success', user_agent: appAgent, metadata: {}, changes: {} }] },
    '/api/auth/sessions': { current: 'app-session', sessions: [appAgent, webAgent].map((agent, index) => ({ id: index ? 'web-session' : 'app-session', ip: '127.0.0.1', user_agent: agent, location: '', created_at: 1720000000, last_seen: 1720000000 })) },
    '/api/admin/onboarding': { status: 'completed', steps: [] }, '/api/public/needs-setup': { needs_setup: false },
    '/api/public/auth-policy': policy, '/api/public/signup-open': { open: true }, '/api/announcement': { enabled: false, bar_enabled: false },
    '/api/workspaces': { workspaces: [] }, '/api/admin/settings': {},
    '/api/admin/system-update': { current_version: '2.5.1-beta.6', update_available: false, configured: false },
    '/api/admin/desktop-update': { config: { enabled: false, version: '', downloads: {} }, update_available: false },
    '/api/models': { models: [model], default_id: model.id }, '/api/image-models': { models: [], default_id: '' },
    '/api/conversations': { conversations: [], has_more: false }, '/api/library/catalog': { skills: [], prompts: [], mcp: [] },
    '/api/me/credits': { permanent: 100, available: 100, timed: { balance: 0, grants: [] } },
    '/api/me/credit-adjustment-notifications': { notifications: [] },
  }
  const arrays = ['/api/projects', '/api/skills', '/api/me/skills', '/api/me/prompts', '/api/me/mcps', '/api/model-tags', '/api/kbs', '/api/me/passkeys', '/api/me/identities', '/api/public/oauth-providers']
  void request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(fixtures[pathname] ?? (arrays.includes(pathname) ? [] : {})) })
})
try {
  await page.setViewport({ width: 1280, height: 850 })
  await page.evaluateOnNewDocument(() => localStorage.setItem('aivory.lang', 'zh'))
  await page.goto(`${baseUrl}/admin/users/fixture/login-history`)
  await page.waitForFunction(() => document.body.textContent.includes('App 版') && document.body.textContent.includes('Chrome · macOS'))
  console.log('PASS: login history distinguishes App and regular Chrome records')
  await page.goto(`${baseUrl}/admin/feedback`)
  await page.waitForSelector('td[data-column="description"] button')
  await page.click('td[data-column="description"] button')
  await page.waitForFunction(() => document.querySelector('[role="dialog"]')?.textContent.includes('App 版 2.5.1-beta.6 · macOS'))
  console.log('PASS: feedback drawer browser information identifies App')
  await page.goto(`${baseUrl}/admin/logs/audit`)
  await page.waitForSelector('tr[role="button"]')
  await page.click('tr[role="button"]')
  await page.waitForFunction(() => document.querySelector('[role="dialog"]')?.textContent.includes('App 版 2.5.1-beta.6 · macOS'))
  console.log('PASS: audit drawer uses the localized App label')
  await page.goto(`${baseUrl}/chat`)
  await page.waitForSelector('[contenteditable="true"]')
  await page.click('button[aria-label="账户"]')
  await page.waitForSelector('[role="menuitem"]')
  await page.evaluate(() => [...document.querySelectorAll('[role="menuitem"]')].find((item) => item.querySelector('svg.lucide-settings')).click())
  await page.waitForFunction(() => document.querySelector('[role="dialog"]')?.textContent.includes('App 版 · macOS'))
  assert.ok((await page.$eval('[role="dialog"]', (dialog) => dialog.textContent)).includes('Chrome · macOS'))
  console.log('PASS: active sessions distinguish App and ordinary browsers')
  assert.deepEqual(errors, [])
} catch (error) {
  console.error('Page:', page.url(), 'Errors:', errors)
  console.error((await page.$eval('body', (body) => body.textContent)).slice(-3000))
  throw error
} finally { await browser.close() }
