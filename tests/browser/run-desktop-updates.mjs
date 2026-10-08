import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import puppeteer from 'puppeteer-core'

const executablePath = process.env.CHROME_PATH || ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium'].find(existsSync)
assert.ok(executablePath, 'Chrome is required')
const baseUrl = process.env.AIVORY_LAYOUT_TEST_URL || 'http://127.0.0.1:5173'
const user = { id: 'desktop-update-admin', email: 'admin@example.test', name: 'Admin', role: 'admin', status: 'active', has_password: true,
  settings: { onboarded: true, admin_onboarding_v1: 'completed', language: 'zh', theme: 'light' } }
const policy = { password_login_enabled: true, passkey_login_enabled: true, entry_mode: 'login_page', providers: [], oauth_initial_password_policy: 'required' }
let config = { enabled: false, version: '2.5.1-beta.6', downloads: { macos_arm64: 'https://downloads.example.test/old.dmg' } }
let download = { enabled: false, url: '' }
let latest = '2.5.1-beta.7'
let saved
const officialPackages = {
  macos_arm64: 'https://github.com/hjxwz123/Aivory/releases/download/v2.5.1-beta.7/Aivory-2.5.1-beta.7-mac-arm64.dmg',
  windows_x64: 'https://github.com/hjxwz123/Aivory/releases/download/v2.5.1-beta.7/Aivory-2.5.1-beta.7-win-x64.exe',
}
const browser = await puppeteer.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
const page = await browser.newPage()
const errors = []
page.on('pageerror', error => errors.push(String(error)))
await page.setRequestInterception(true)
page.on('request', request => {
  const pathname = new URL(request.url()).pathname
  if (!pathname.startsWith('/api/')) { void request.continue(); return }
  let response = {}
  if (pathname === '/api/auth/session') response = { authenticated: true, user, access_token: 'test', request_signing_key: 'test', auth_policy: policy }
  else if (pathname === '/api/me') response = user
  else if (pathname === '/api/admin/settings') {
    if (request.method() === 'PATCH') { saved = JSON.parse(request.postData()); config = saved.desktop_update; download = saved.desktop_download }
    response = { desktop_update: config, desktop_download: download }
  } else if (pathname.startsWith('/api/admin/desktop-update')) response = { config, latest_version: latest, update_available: config.version !== latest, releases: [{ version: '2.5.1-beta.7', downloads: officialPackages }] }
  else if (pathname === '/api/admin/onboarding') response = { status: 'completed', steps: [] }
  else if (pathname === '/api/public/needs-setup') response = { needs_setup: false }
  else if (pathname === '/api/public/auth-policy') response = policy
  else if (pathname === '/api/public/signup-open') response = { open: true }
  else if (pathname === '/api/announcement') response = { enabled: false, bar_enabled: false }
  else if (pathname === '/api/workspaces') response = { workspaces: [] }
  else if (pathname === '/api/admin/system-update') response = { current_version: '2.5.1-beta.6', update_available: false, configured: false }
  else if (['/api/projects', '/api/skills', '/api/me/skills', '/api/me/prompts'].includes(pathname)) response = []
  void request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(response) })
})
await page.evaluateOnNewDocument(() => { localStorage.setItem('aivory.lang', 'zh'); localStorage.setItem('aivory.theme', 'light') })

async function openSettings() {
  await page.goto(`${baseUrl}/admin/settings/desktop`)
  await page.waitForSelector('#desktop-version')
}
async function save() {
  saved = undefined
  await page.$eval('button:has(svg.lucide-save)', button => button.click())
  await page.waitForFunction(() => !document.querySelector('button:has(svg.lucide-save)')?.hasAttribute('aria-busy'))
  assert.ok(saved)
  assert.deepEqual(Object.keys(saved), ['desktop_update', 'desktop_download'])
}
try {
  await page.setViewport({ width: 1280, height: 850 })
  await openSettings()
  await page.waitForFunction(() => document.body.textContent.includes('最新版本：v2.5.1-beta.7'))
  await page.evaluate(() => [...document.querySelectorAll('button')].find(button => button.textContent === '使用此版本').click())
  assert.equal(await page.$eval('#desktop-version', input => input.value), latest)
  assert.equal(await page.$eval('#desktop-macos_arm64', input => input.value), '')
  assert.equal(await page.$eval('#desktop-published', control => control.getAttribute('aria-checked')), 'false')
  await page.type('#desktop-windows_x64', 'https://downloads.example.test/deployment-win.exe')
  await page.type('#desktop-macos_arm64', 'https://downloads.example.test/deployment-arm64.dmg')
  await page.click('#desktop-published')
  await save()
  assert.equal(config.enabled, true)
  assert.equal(config.downloads.macos_arm64, 'https://downloads.example.test/deployment-arm64.dmg')
  await openSettings()
  assert.equal(await page.$eval('#desktop-published', control => control.getAttribute('aria-checked')), 'true')
  assert.equal(await page.$eval('#desktop-windows_x64', input => input.value), 'https://downloads.example.test/deployment-win.exe')
  console.log('PASS: changing release clears old URLs, operator publishes its own packages, saved configuration survives reload')

  await page.click('#desktop-source')
  await page.waitForSelector('[role="option"]')
  await page.evaluate(() => [...document.querySelectorAll('[role="option"]')].find(option => option.textContent.includes('官方安装包')).click())
  await page.waitForFunction(() => !document.querySelector('[role="listbox"]'))
  assert.equal(await page.$eval('#desktop-published', control => control.getAttribute('aria-checked')), 'false')
  assert.equal(await page.$eval('#desktop-macos_arm64', input => input.value), officialPackages.macos_arm64)
  assert.equal(await page.$eval('#desktop-macos_arm64', input => input.readOnly), true)
  await page.click('#desktop-published')
  await page.waitForFunction(() => document.getElementById('desktop-published').getAttribute('aria-checked') === 'true')
  await save()
  assert.equal(config.source, 'official')
  assert.equal(config.enabled, true)
  assert.deepEqual(config.downloads, officialPackages)
  await openSettings()
  assert.equal(await page.$eval('#desktop-published', control => control.getAttribute('aria-checked')), 'true')
  console.log('PASS: official installers are selected per platform, snapshotted at publication, and retained on reload')
  await page.type('#desktop-download-url', 'https://downloads.example.test/apps#desktop')
  await page.click('#desktop-download-visible')
  await save()
  assert.deepEqual(download, { enabled: true, url: 'https://downloads.example.test/apps#desktop' })
  await openSettings()
  assert.equal(await page.$eval('#desktop-download-visible', control => control.getAttribute('aria-checked')), 'true')
  assert.equal(await page.$eval('#desktop-download-url', input => input.value), download.url)
  console.log('PASS: administrator can save a download landing page and toggle both web entry points')

  for (const lang of ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
    user.settings.language = lang
    for (const theme of ['light', 'dark']) {
      user.settings.theme = theme
      for (const viewport of [{ width: 1280, height: 850 }, { width: 390, height: 640 }]) {
        await page.setViewport(viewport)
        await openSettings()
        assert.equal(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1 && document.scrollingElement.scrollHeight <= innerHeight + 1), true, `${lang}/${theme}/${viewport.width}: viewport overflow`)
        if (lang === 'zh' && viewport.width === 1280) await page.screenshot({ path: `/tmp/aivory-desktop-update-admin-${theme}.png` })
      }
    }
  }
  console.log('PASS: desktop-update settings fit desktop/mobile in all five languages and both themes')
  user.settings.language = 'zh'
  latest = '2.5.1-beta.8'
  await page.goto(`${baseUrl}/admin/settings/logging`)
  await page.waitForSelector('a[href="/admin/settings/desktop"]')
  await page.waitForFunction(() => document.body.textContent.includes('发现桌面版新版本 v2.5.1-beta.8。'))
  assert.equal(await page.evaluate(() => document.scrollingElement.scrollHeight <= innerHeight + 1), true)
  await openSettings()
  await page.click('#desktop-published')
  await save()
  assert.equal(config.enabled, false)
  assert.deepEqual(errors, [])
  console.log('PASS: admin notice links to installer settings and updates can be withdrawn')
} finally { await browser.close() }
