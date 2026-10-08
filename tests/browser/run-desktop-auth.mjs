import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import path from 'node:path'
import { tmpdir } from 'node:os'
import puppeteer from 'puppeteer-core'

const baseUrl = process.env.AIVORY_LAYOUT_TEST_URL || 'http://127.0.0.1:5173'
const executablePath = process.env.CHROME_PATH || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium',
].find(existsSync)
assert.ok(executablePath, 'Chrome is required')
const requestId = 'a'.repeat(43)
const authUrl = `${baseUrl}/desktop/authorize?request_id=${requestId}`
const user = { id: 'desktop-user', email: 'desktop@example.test', name: 'Desktop', role: 'user',
  status: 'active', has_password: true, settings: { language: 'zh', theme: 'light', onboarded: true } }
const policy = { password_login_enabled: true, passkey_login_enabled: false, entry_mode: 'login_page', providers: [], oauth_initial_password_policy: 'required' }
let authenticated = false
let authorized = null
let loginRequires2FA = false
let oauthStarts = 0
const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage()
const errors = []
page.on('pageerror', (error) => errors.push(String(error)))
await page.setRequestInterception(true)
page.on('request', (request) => {
  const url = new URL(request.url())
  if (!url.pathname.startsWith('/api/')) { void request.continue(); return }
  if (/\/api\/auth\/oauth\/[^/]+\/start$/.test(url.pathname)) oauthStarts++
  let response = {}
  if (url.pathname === '/api/auth/session') response = { authenticated, user: authenticated ? user : null,
    access_token: authenticated ? 'desktop-token' : '', request_signing_key: 'desktop-key', auth_policy: policy }
  else if (url.pathname === '/api/public/needs-setup') response = { needs_setup: false }
  else if (url.pathname === '/api/public/auth-policy') response = policy
  else if (url.pathname === '/api/public/signup-open') response = { open: true, captcha_required: false, login_captcha_required: false }
  else if (url.pathname === '/api/auth/login' || url.pathname === '/api/auth/login/2fa') {
    if (loginRequires2FA && url.pathname.endsWith('/login')) response = { totp_required: true, ticket: 'desktop-2fa' }
    else { authenticated = true; response = { user, access_token: 'desktop-token', request_signing_key: 'desktop-key' } }
  } else if (url.pathname === '/api/me/password/set') {
    user.has_password = true
    response = { user, access_token: 'desktop-token', request_signing_key: 'desktop-key' }
  } else if (url.pathname === '/api/auth/desktop/authorize') {
    if (request.method() === 'POST') { authorized = JSON.parse(request.postData()).approve; response = { ok: true } }
    else response = { expires_at: Math.floor(Date.now() / 1000) + 300 }
  } else if (url.pathname === '/api/public/legal-config') response = { contact_email: 'support@example.test' }
  else if (url.pathname === '/api/me') response = user
  else if (['/api/public/oauth-providers', '/api/workspaces', '/api/projects', '/api/skills', '/api/me/skills', '/api/me/prompts'].includes(url.pathname)) response = []
  void request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(response) })
})
await page.evaluateOnNewDocument(() => { localStorage.setItem('aivory.lang', 'zh'); localStorage.setItem('aivory.theme', 'light') })

try {
  for (const twoFactor of [false, true]) {
    authenticated = false
    authorized = null
    loginRequires2FA = twoFactor
    await page.goto(authUrl)
    await page.waitForSelector('#email')
    assert.equal(new URL(page.url()).pathname, '/login')
    await page.type('#email', user.email)
    await page.type('#pw', 'password123')
    await page.click('button[type="submit"]')
    if (twoFactor) { await page.waitForSelector('#code'); await page.type('#code', '123456'); await page.click('button[type="submit"]') }
    await page.waitForFunction(() => location.pathname === '/desktop/authorize')
    await page.waitForFunction(() => document.body.innerText.includes('授权桌面版登录'))
    assert.ok((await page.$eval('.login-content', (el) => el.innerText)).includes(user.email))
    assert.equal(authorized, null, 'Authorization must require an explicit confirmation')
    await page.$eval('.login-submit', (el) => el.click())
    await page.waitForFunction(() => document.body.innerText.includes('桌面版已授权'))
    assert.equal(authorized, true)
    assert.equal(await page.evaluate(() => sessionStorage.getItem('aivory.desktop.authorization')), null)
    console.log(`PASS: browser ${twoFactor ? 'two-factor' : 'password'} login returns to explicit desktop consent`)
  }

  // OAuth providers return to the website root; the tab-scoped pending request
  // must bring an authenticated callback back to consent.
  authenticated = false
  await page.goto(authUrl)
  await page.waitForSelector('#email')
  authenticated = true
  await page.goto(baseUrl)
  await page.waitForFunction(() => location.pathname === '/desktop/authorize')
  await page.waitForFunction(() => document.body.innerText.includes('授权桌面版登录'))
  console.log('PASS: an OAuth return to the site root resumes pending desktop consent')

  user.has_password = false
  await page.goto(authUrl)
  await page.waitForSelector('#set-pw-new')
  await page.type('#set-pw-new', 'password123')
  await page.type('#set-pw-confirm', 'password123')
  await page.click('[role="dialog"] button[type="submit"]')
  await page.waitForFunction(() => !document.querySelector('#set-pw-new'))
  await page.waitForSelector('.login-submit')
  console.log('PASS: required initial password setup completes before desktop authorization')

  for (const lang of ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
    for (const viewport of [{ width: 320, height: 480 }, { width: 1024, height: 500 }]) {
      await page.evaluate((language) => localStorage.setItem('aivory.lang', language), lang)
      user.settings.language = lang
      await page.setViewport(viewport)
      await page.goto(authUrl)
      await page.waitForSelector('.login-submit')
      await page.waitForFunction(() => document.querySelector('.login-submit')?.textContent?.length > 3)
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true)
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollHeight <= innerHeight), true)
    }
  }
  await page.screenshot({ path: path.join(tmpdir(), 'aivory-desktop-authorization.png') })
  authorized = null
  await page.$eval('.login-submit + button', (el) => el.click())
  await page.waitForFunction(() => document.querySelector('.login-back'))
  assert.equal(authorized, false)
  assert.deepEqual(errors, [])
  console.log('PASS: cancellation and five-language consent layout at narrow and short viewports')

  authenticated = false
  policy.providers = [{ id: 'github-fixture', name: 'GitHub fixture', kind: 'github', icon: '' }]
  policy.passkey_login_enabled = true
  policy.oauth_auto_provision_enabled = true
  policy.default_provider = policy.providers[0]
  await page.goto(`${baseUrl}/login`)
  await page.waitForFunction(() => document.querySelector('.login-providers')?.textContent.includes('GitHub fixture'))
  assert.ok(await page.$('#login-title.login-title'), 'Web login retains its visible welcome heading')
  assert.ok(await page.$('.login-content .login-intro'), 'Web login retains its welcome subtitle')
  console.log('PASS: ordinary web login retains its third-party providers')
  await page.evaluateOnNewDocument(() => {
    window.aivoryDesktop = {
      getInfo: async () => ({ version: '2.5.1-beta.6', platform: 'darwin' }),
      loginInBrowser: async () => ({ status: 'denied' }),
      cancelBrowserLogin: async () => ({ status: 'cancelled' }),
      checkUpdates: async () => ({ status: 'current' }),
    }
  })
  for (const lang of ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
    await page.evaluate((language) => localStorage.setItem('aivory.lang', language), lang)
    await page.setViewport({ width: 320, height: 480 })
    await page.goto(`${baseUrl}/login`)
    await page.waitForSelector('#email')
    const browserButton = await page.$('button:has(svg.lucide-external-link)')
    assert.ok(browserButton, 'Desktop browser login action must be visible')
    assert.equal(await page.$('.login-providers'), null, 'Desktop login must not offer OAuth or passkeys')
    assert.equal(await page.$('#login-title.login-title'), null, 'Desktop login must hide the form welcome heading')
    assert.equal(await page.$('.login-content .login-intro'), null, 'Desktop login must hide the form welcome subtitle')
    assert.ok(await page.$('#login-title.sr-only'), 'Desktop login retains an accessible section heading')
    assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth), true)
    assert.equal(await page.evaluate(() => document.scrollingElement.scrollHeight <= innerHeight), true)
    const overflow = await browserButton.evaluate((button) => button.scrollWidth > button.clientWidth + 1)
    assert.equal(overflow, false, `Browser login button overflows in ${lang}`)
  }
  assert.deepEqual(errors, [])
  console.log('PASS: desktop hides the form welcome copy and browser login fits narrow windows in all five languages')
  for (const mode of ['login_page', 'provider_picker', 'auto_redirect']) {
    policy.entry_mode = mode
    await page.goto(`${baseUrl}/login`)
    await page.waitForSelector('#email')
    assert.equal(new URL(page.url()).pathname, '/login')
    assert.ok(await page.$('button:has(svg.lucide-external-link)'))
    assert.equal(await page.$('.login-providers'), null)
  }
  assert.equal(oauthStarts, 0, 'Desktop must never automatically start provider login')
  await page.goto(`${baseUrl}/register`)
  await page.waitForSelector('#register-email')
  assert.equal(await page.$('.login-providers'), null, 'Desktop registration must not offer provider login')
  policy.password_login_enabled = false
  await page.goto(`${baseUrl}/login`)
  await page.waitForSelector('button:has(svg.lucide-external-link)')
  assert.equal(await page.$('#email'), null)
  assert.equal(await page.$('.login-providers'), null)
  assert.equal(await page.$('.login-notice'), null)
  assert.deepEqual(errors, [])
  console.log('PASS: desktop offers only browser/password login in every entry mode and respects disabled password policy')

  policy.password_login_enabled = true
  policy.entry_mode = 'login_page'
  loginRequires2FA = true
  await page.goto(`${baseUrl}/login`)
  await page.waitForSelector('#email')
  await page.type('#email', user.email)
  await page.type('#pw', 'password123')
  await page.click('button[type="submit"]')
  await page.waitForSelector('#code')
  assert.ok(await page.$('#login-title.login-title'), 'Desktop two-factor verification retains its instructional heading')
  assert.ok(await page.$('.login-content .login-intro'), 'Desktop two-factor verification retains its instructions')
  assert.deepEqual(errors, [])
  console.log('PASS: desktop two-factor verification retains its required instructions')
} finally { await browser.close() }
