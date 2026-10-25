import assert from 'node:assert/strict'
import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { tmpdir } from 'node:os'
import puppeteer from 'puppeteer-core'

const baseUrl = process.env.AIVORY_AUTH_LAYOUT_TEST_URL || 'http://127.0.0.1:5174'
const actionsOnly = process.argv.includes('--actions-only')
const variantsOnly = actionsOnly || process.argv.includes('--variants-only')
const executablePath = process.env.CHROME_PATH || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium',
].find(existsSync)
assert.ok(executablePath, 'Set CHROME_PATH to a Chrome/Chromium executable')
const outputDir = path.join(tmpdir(), 'aivory-auth-layout')
mkdirSync(outputDir, { recursive: true })

let needsSetup = false
let passkeyEnabled = false
let authenticated = false
let authorizeFailed = false
const user = { id: 'layout-user', email: 'layout@example.test', name: 'Layout User', role: 'user', status: 'active', has_password: true }
const translations = Object.fromEntries(['en', 'zh', 'zh-Hant', 'ja', 'fr'].map((language) => [language, JSON.parse(readFileSync(new URL(`../../src/i18n/locales/${language}/auth.json`, import.meta.url), 'utf8'))]))
let providers = [{ id: 'github', kind: 'github', name: 'GitHub' }]
const policy = () => ({
  password_login_enabled: true, passkey_login_enabled: passkeyEnabled, entry_mode: 'login_page',
  oauth_auto_provision_enabled: true, oauth_initial_password_policy: 'required',
  default_provider: null, providers,
})
const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage()
const errors = []
page.on('pageerror', (error) => errors.push(String(error)))
await page.setRequestInterception(true)
page.on('request', (request) => {
  const url = new URL(request.url())
  if (!url.pathname.startsWith('/api/')) return void request.continue()
  const fixtures = {
    '/api/auth/session': { authenticated, user: authenticated ? user : null, access_token: authenticated ? 'layout-token' : '', request_signing_key: 'layout-key', auth_policy: policy() },
    '/api/public/auth-policy': policy(),
    '/api/public/oauth-providers': providers,
    '/api/public/needs-setup': { needs_setup: needsSetup },
    '/api/public/signup-open': { open: true, captcha_required: false, login_captcha_required: false },
    '/api/auth/forgot-password': { ok: true, retry_after: 60 },
    '/api/announcement': { enabled: false, bar_enabled: false },
    '/api/me': user,
    '/api/auth/desktop/authorize': { expires_at: Math.floor(Date.now() / 1000) + 300, client: { user_agent: 'Mozilla/5.0 (Macintosh) AivoryDesktop/2.5.1-beta.13', ip: '203.0.113.10', location: 'SG' } },
  }
  if (authorizeFailed && url.pathname === '/api/auth/desktop/authorize' && request.method() === 'POST') {
    return void request.respond({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'authorization_failed' }) })
  }
  void request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(fixtures[url.pathname] ?? {}) })
})
await page.evaluateOnNewDocument(() => {
  if (!localStorage.getItem('aivory.lang')) localStorage.setItem('aivory.lang', 'en')
  localStorage.setItem('aivory.theme', 'light')
  if (localStorage.getItem('aivory.layout.desktop') === 'true') {
    window.aivoryDesktop = {
      getInfo: async () => ({ version: '2.5.1-beta.13', platform: 'darwin' }),
      loginInBrowser: async () => ({ status: 'denied' }),
      cancelBrowserLogin: async () => ({ status: 'cancelled' }),
      checkUpdates: async () => ({ status: 'current' }),
    }
  }
})

async function check(label, screenshot = false) {
  await page.waitForFunction(() => {
    const panel = document.querySelector('.login-panel')
    return panel && panel.dataset.density && panel.getBoundingClientRect().height > 0
  })
  await page.evaluate(() => document.fonts.ready)
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  const result = await page.evaluate(() => {
    const panel = document.querySelector('.login-panel')
    const content = document.querySelector('.login-content')
    const panelRect = panel.getBoundingClientRect()
    const contentRect = content.getBoundingClientRect()
    const doc = document.scrollingElement
    return {
      document: [doc.scrollWidth, doc.clientWidth, doc.scrollHeight, doc.clientHeight],
      panel: [panel.scrollWidth, panel.clientWidth, panel.scrollHeight, panel.clientHeight],
      bounds: [contentRect.top, contentRect.bottom, panelRect.top, panelRect.bottom],
      panelScrollTop: panel.scrollTop,
      allowsPanelScroll: Boolean(content.querySelector('.login-oauth-row, .login-passkey')),
      contentWidth: contentRect.width,
      overflowingButtons: [...content.querySelectorAll('button')].filter((button) => button.getBoundingClientRect().height > 0 && (button.scrollWidth > button.clientWidth + 1 || button.scrollHeight > button.clientHeight + 1)).map((button) => button.textContent.trim()),
    }
  })
  const [dw, vw, dh, vh] = result.document
  const [pw, pcw, ph, pch] = result.panel
  const [top, bottom, panelTop, panelBottom] = result.bounds
  try {
    assert.ok(dw <= vw + 1 && dh <= vh + 1, `${label}: document scrolls ${JSON.stringify(result)}`)
    assert.ok(pw <= pcw + 1, `${label}: form scrolls horizontally ${JSON.stringify(result)}`)
    if (ph > pch + 1) {
      assert.ok(result.allowsPanelScroll, `${label}: unexpected form scrolling ${JSON.stringify(result)}`)
      assert.ok(top + result.panelScrollTop >= panelTop - 1 && bottom + result.panelScrollTop <= panelTop + ph + 1, `${label}: scrolling must reach the entire form ${JSON.stringify(result)}`)
    } else {
      assert.ok(top >= panelTop - 1 && bottom <= panelBottom + 1, `${label}: form is clipped ${JSON.stringify(result)}`)
    }
    assert.deepEqual(errors, [], `${label}: rendering errors`)
    assert.deepEqual(result.overflowingButtons, [], `${label}: button labels must fit`)
  } catch (error) {
    await page.screenshot({ path: path.join(outputDir, 'failure.png') })
    throw error
  }
  if (screenshot) await page.screenshot({ path: path.join(outputDir, `${label.replace(/[^a-z0-9-]/gi, '-')}.png`) })
  return result.contentWidth
}

async function checkProviders(label) {
  const layout = await page.$eval('.login-oauth-row', (row) => {
    const bounds = row.getBoundingClientRect()
    const buttons = [...row.querySelectorAll('.login-provider-button')]
    const boxes = buttons.map((button) => button.getBoundingClientRect())
    const rows = [...new Set(boxes.map((box) => Math.round(box.top)))].map((top) => boxes.filter((box) => Math.round(box.top) === top))
    return {
      iconsOnly: row.dataset.iconsOnly === 'true',
      rows: new Set(boxes.map((box) => Math.round(box.top))).size,
      width: bounds.width,
      gap: parseFloat(getComputedStyle(row).columnGap),
      count: buttons.length,
      fits: boxes.every((box) => box.width >= 44 && box.height >= 44 && box.left >= bounds.left - 1 && box.right <= bounds.right + 1),
      names: buttons.map((button) => button.getAttribute('aria-label')),
      counts: rows.map((row) => row.length),
      centered: rows.every((row) => Math.abs((row[0].left + row.at(-1).right) / 2 - (bounds.left + bounds.right) / 2) < 1),
      equalWidths: Math.max(...boxes.map((box) => box.width)) - Math.min(...boxes.map((box) => box.width)) < 1,
    }
  })
  assert.equal(await page.$('.login-provider-picker'), null, `${label}: providers must remain directly accessible`)
  assert.equal(layout.count, providers.length, `${label}: no providers may disappear`)
  assert.deepEqual(layout.names, providers.map((provider) => provider.name), `${label}: icons retain accessible names`)
  assert.ok(layout.fits, `${label}: provider buttons must fit and preserve 44px targets`)
  assert.ok(Math.max(...layout.counts) - Math.min(...layout.counts) <= 1, `${label}: distribute providers evenly between rows`)
  assert.ok(layout.centered, `${label}: center every provider row`)
  if (layout.iconsOnly) assert.ok(layout.equalWidths, `${label}: keep icon buttons the same width between rows`)
  if (!layout.iconsOnly || providers.length * 44 + (providers.length - 1) * layout.gap <= layout.width) {
    assert.equal(layout.rows, 1, `${label}: wrap only when icons cannot fit on one line`)
  } else {
    assert.ok(layout.rows > 1, `${label}: icons must wrap instead of shrinking or overflowing`)
  }
  if (providers.length === 2 && providers.every((provider) => provider.name.startsWith('Provider'))) {
    assert.equal(layout.iconsOnly, false, `${label}: keep names when they fit`)
  }
  return layout
}

async function visit(route) {
  await page.goto(`${baseUrl}${route}`, { waitUntil: 'networkidle0' })
  await page.waitForSelector('.login-content')
}

async function checkError(label) {
  try {
    await page.waitForFunction(() => [...document.querySelectorAll('.login-error')].some((element) => element.getBoundingClientRect().height > 0 && element.textContent.trim()), { timeout: 3000 })
  } catch (error) {
    await page.screenshot({ path: path.join(outputDir, 'failure.png') })
    throw new Error(`${label}: validation must show a visible error`, { cause: error })
  }
  await check(label)
}

try {
  const viewports = variantsOnly ? [] : [
    { width: 1440, height: 900 }, { width: 1280, height: 720 },
    { width: 1024, height: 500 }, { width: 844, height: 390 },
    { width: 390, height: 844 }, { width: 390, height: 560 },
    { width: 320, height: 480 }, { width: 390, height: 360 }, { width: 320, height: 360 },
  ]
  for (const viewport of viewports) {
    await page.setViewport(viewport)
    const suffix = `${viewport.width}x${viewport.height}`
    await visit('/login')
    const loginWidth = await check(`login-${suffix}`, true)
    await page.click('.login-submit')
    await checkError(`login-errors-${suffix}`)
    await visit('/register')
    assert.equal(await check(`register-${suffix}`, true), loginWidth, 'Login and registration widths must match')
    await page.click('.login-submit')
    await checkError(`register-errors-${suffix}`)
    await page.type('#register-name', 'Layout User')
    await page.type('#register-email', 'layout@example.test')
    if (await page.$eval('#register-pw', (input) => input.getBoundingClientRect().height === 0)) {
      await page.click('.login-submit')
      await page.waitForFunction(() => document.querySelector('#register-pw').getBoundingClientRect().height > 0)
      await check(`register-password-${suffix}`, viewport.width === 390 && viewport.height === 844)
      await page.click('.login-submit')
      await checkError(`register-password-errors-${suffix}`)
      await page.click('.login-step-actions button:first-child')
      assert.equal(await page.$eval('#register-name', (input) => input.value), 'Layout User', 'Going back must preserve form values')
    }
    await visit('/forgot-password')
    assert.equal(await check(`forgot-${suffix}`), loginWidth, 'Password recovery width must match login')
    await page.type('#forgot-email', 'layout@example.test')
    await page.click('.login-submit')
    await page.waitForSelector('.login-delivery-hint')
    await check(`reset-sent-${suffix}`, true)
    await visit(`/reset-password?lang=en#token=${'a'.repeat(43)}`)
    await page.waitForSelector('#new-pw')
    await check(`reset-${suffix}`, true)
    await page.click('.login-submit')
    await checkError(`reset-errors-${suffix}`)
    await page.type('#new-pw', 'password123')
    if (await page.$eval('#confirm-pw', (input) => input.getBoundingClientRect().height === 0)) {
      await page.click('.login-submit')
      await page.waitForFunction(() => document.querySelector('#confirm-pw').getBoundingClientRect().height > 0)
      await check(`reset-confirm-${suffix}`, true)
      await page.click('.login-submit')
      await checkError(`reset-confirm-errors-${suffix}`)
    }
    await page.type('#confirm-pw', 'password123')
    await page.click('.login-submit')
    await page.waitForSelector('.login-success-icon')
    await check(`reset-done-${suffix}`)
    await visit('/register?verify_email=layout%40example.test')
    await page.waitForSelector('#register-code')
    await check(`verify-${suffix}`)
    await visit('/login?twofa=1')
    await page.waitForSelector('#code')
    await check(`twofa-${suffix}`)
    needsSetup = true
    await visit('/setup')
    await check(`setup-${suffix}`)
    await page.click('.login-submit')
    await checkError(`setup-errors-${suffix}`)
    await page.type('#setup-name', 'Layout Admin')
    await page.type('#setup-email', 'admin@example.test')
    if (await page.$eval('#setup-pw', (input) => input.getBoundingClientRect().height === 0)) {
      await page.click('.login-submit')
      await page.waitForFunction(() => document.querySelector('#setup-pw').getBoundingClientRect().height > 0)
      await check(`setup-password-${suffix}`)
      await page.click('.login-submit')
      await checkError(`setup-password-errors-${suffix}`)
    }
    needsSetup = false
    console.log(`PASS: authentication forms and validation fit ${suffix}`)
  }
  await visit('/login')
  for (const route of actionsOnly ? [] : ['/login', '/register']) {
    await page.setViewport({ width: 1440, height: 900 })
    await visit(route)
    await page.click('.login-submit')
    await page.setViewport({ width: 320, height: 360 })
    await checkError(`${route.slice(1)}-resized-errors`)
  }
  for (const route of actionsOnly ? [] : ['/login', '/register', '/forgot-password']) {
    await page.setViewport({ width: 390, height: 844 })
    await visit(route)
    await page.evaluate(() => {
      Object.defineProperty(window.visualViewport, 'height', { value: 340, configurable: true })
      window.visualViewport.dispatchEvent(new Event('resize'))
    })
    await page.waitForFunction(() => document.getElementById('root').getBoundingClientRect().height === 340)
    await page.focus('.login-input')
    await check(`${route.slice(1)}-visual-keyboard`, true)
    await page.click('.login-submit')
    await checkError(`${route.slice(1)}-visual-keyboard-errors`)
    await page.evaluate(() => {
      Reflect.deleteProperty(window.visualViewport, 'height')
      window.visualViewport.dispatchEvent(new Event('resize'))
    })
  }
  if (!actionsOnly) console.log('PASS: validation survives resizing and forms fit above the visual keyboard')
  const variantViewports = actionsOnly ? [] : variantsOnly ? [{ width: 320, height: 360 }] : [{ width: 390, height: 560 }, { width: 320, height: 360 }]
  for (const viewport of variantViewports) {
    await page.setViewport(viewport)
    for (const language of ['zh', 'zh-Hant', 'ja', 'fr']) {
      await page.evaluate((code) => localStorage.setItem('aivory.lang', code), language)
      for (const route of ['/login', '/register', '/forgot-password', '/register?verify_email=layout%40example.test']) {
        await visit(route)
        if (route === '/login') assert.equal(await page.$eval('label[for="email"]', (label) => label.textContent), translations[language].login.emailLabel)
        await check(`${route.slice(1)}-${language}-${viewport.height}`, true)
        await page.click('.login-submit')
        await checkError(`${route.slice(1)}-${language}-errors-${viewport.height}`)
      }
      await visit('/forgot-password')
      await page.type('#forgot-email', user.email)
      await page.click('.login-submit')
      await page.waitForSelector('.login-delivery-hint')
      await check(`reset-sent-${language}-${viewport.height}`)
      await visit(`/reset-password?lang=${language}#token=${'a'.repeat(43)}`)
      await page.waitForSelector('#new-pw')
      await check(`reset-${language}-${viewport.height}`)
      await page.click('.login-submit')
      await checkError(`reset-${language}-errors-${viewport.height}`)
      if (await page.$eval('#confirm-pw', (input) => input.getBoundingClientRect().height === 0)) {
        await page.type('#new-pw', 'password123')
        await page.click('.login-submit')
        await page.waitForFunction(() => document.querySelector('#confirm-pw').getBoundingClientRect().height > 0)
        await check(`reset-confirm-${language}-${viewport.height}`)
        await page.click('.login-submit')
        await checkError(`reset-confirm-${language}-errors-${viewport.height}`)
      }
      await visit('/login?twofa=1')
      await page.waitForSelector('#code')
      await check(`twofa-${language}-${viewport.height}`)
      await page.click('.login-submit')
      await checkError(`twofa-${language}-errors-${viewport.height}`)
    }
    console.log(`PASS: five-language form states fit ${viewport.width}x${viewport.height}`)
  }
  passkeyEnabled = true
  for (const viewport of [{ width: 1440, height: 900 }, { width: 1280, height: 720 }, { width: 320, height: 360 }]) {
    await page.setViewport(viewport)
    for (const count of [2, 4, 6, 7, 8, 10, 13]) {
      providers = Array.from({ length: count }, (_, index) => ({ id: `provider-${index}`, kind: 'oidc', name: `Provider ${index + 1}` }))
      for (const route of ['/login', '/register']) {
        const label = `${route.slice(1)}-${count}-providers-${viewport.height}`
        await visit(route)
        await check(label, true)
        await checkProviders(label)
        if (route === '/login') assert.match(await page.$eval('.login-passkey', (button) => button.textContent), /.+/)
        await page.click('.login-submit')
        await checkError(`${label}-errors`)
      }
    }
  }
  providers = [{ id: 'google', kind: 'google', name: 'Google' }, { id: 'github', kind: 'github', name: 'GitHub' }, { id: 'apple', kind: 'apple', name: 'Apple' }]
  await page.setViewport({ width: 1440, height: 900 })
  await visit('/login')
  assert.equal((await checkProviders('wide labels')).iconsOnly, false)
  // Resize the actual row to cross the text and icon thresholds, then restore
  // it. This also catches label measurements that oscillate in icon mode.
  for (const width of [190, 140, 420]) {
    await page.$eval('.login-content', (content, width) => { content.style.maxWidth = `${width}px` }, width)
    await page.waitForFunction(({ iconsOnly, rows }) => {
      const row = document.querySelector('.login-oauth-row')
      const tops = [...(row?.querySelectorAll('.login-provider-button') ?? [])].map((button) => Math.round(button.getBoundingClientRect().top))
      return row?.dataset.iconsOnly === String(iconsOnly) && new Set(tops).size === rows
    }, {}, { iconsOnly: width < 420, rows: width === 140 ? 2 : 1 })
    const layout = await checkProviders(`resized row ${width}`)
    assert.equal(layout.rows, width === 140 ? 2 : 1)
  }
  await page.focus('.login-provider-button')
  await page.keyboard.press('Tab')
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('aria-label')), 'GitHub')

  providers = [{ id: 'enterprise', kind: 'oidc', name: 'Enterprise single sign-on with a very long organization name across international departments and regional subsidiaries', icon: '🔐' }]
  await visit('/login')
  assert.equal((await checkProviders('long enterprise name')).iconsOnly, true)
  await page.focus('.login-provider-button')
  await page.waitForSelector('[role="tooltip"]')
  assert.equal(await page.$eval('[role="tooltip"]', (tooltip) => tooltip.textContent), providers[0].name)
  await check('long enterprise name', true)
  console.log('PASS: provider names, icons, wrapping, resize recovery and keyboard access')
  await page.evaluate(() => localStorage.setItem('aivory.layout.desktop', 'true'))
  for (const language of ['en', 'zh', 'zh-Hant', 'ja', 'fr']) {
    await page.evaluate((code) => localStorage.setItem('aivory.lang', code), language)
    await visit('/login')
    assert.equal(await page.$('.login-providers'), null, 'Desktop must not offer third-party login')
    assert.ok(await page.$('button:has(svg.lucide-external-link)'), 'Desktop browser login must stay available')
    await check(`desktop-${language}`, true)
    await page.click('.login-submit')
    await checkError(`desktop-${language}-errors`)
  }
  console.log('PASS: desktop login fits in all five languages')
  await page.evaluate(() => localStorage.removeItem('aivory.layout.desktop'))
  authenticated = true
  for (const language of ['en', 'zh', 'zh-Hant', 'ja', 'fr']) {
    await page.evaluate((code) => localStorage.setItem('aivory.lang', code), language)
    await visit('/desktop/authorize?request_id=layout-request')
    await page.waitForSelector('.login-authorization-actions')
    await check(`authorize-${language}`, true)
    authorizeFailed = true
    await page.click('.login-submit')
    await page.waitForSelector('.login-error')
    await checkError(`authorize-${language}-errors`)
    authorizeFailed = false
  }
  console.log(`PASS: localized forms and multiple providers; screenshots in ${outputDir}`)
} finally {
  await browser.close()
}
