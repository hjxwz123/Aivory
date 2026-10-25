import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const executablePath = process.env.CHROME_PATH || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium',
].find(existsSync)
assert.ok(executablePath, 'Set CHROME_PATH to a Chrome/Chromium executable')
const output = path.join(tmpdir(), 'aivory-reply-animation-settings')
await mkdir(output, { recursive: true })
const cacheDir = await mkdtemp(path.join(tmpdir(), 'aivory-animation-settings-cache-'))
const server = await createServer({ cacheDir, server: { host: '127.0.0.1', port: 5207, strictPort: true }, logLevel: 'error' })
const variants = ['paper-plane', 'origami', 'ink', 'sprout', 'orbit', 'prism']
const translations = Object.fromEntries(['zh', 'zh-Hant', 'en', 'fr', 'ja'].map((lang) => [lang, {
  settings: JSON.parse(readFileSync(new URL(`../../src/i18n/locales/${lang}/settings.json`, import.meta.url))),
  welcome: JSON.parse(readFileSync(new URL(`../../src/i18n/locales/${lang}/welcome.json`, import.meta.url))),
}]))
let profile = { id: 'wait-user', name: 'Test', email: 'wait@example.test', role: 'admin', status: 'active', has_password: true, settings: { onboarded: true, reply_animation: 'paper-plane' } }
let failSave = false
const patches = []
const errors = []
let browser
let checks = 0
try {
  await server.listen()
  browser = await puppeteer.launch({ executablePath, headless: true })
  const page = await browser.newPage()
  page.setDefaultTimeout(30_000)
  page.on('pageerror', (error) => errors.push(String(error)))
  await page.setRequestInterception(true)
  page.on('request', (request) => {
    const route = new URL(request.url()).pathname
    if (!route.startsWith('/api/')) return request.continue()
    const respond = (body, status = 200) => request.respond({ status, contentType: 'application/json', body: JSON.stringify(body) })
    if (route === '/api/me/settings' && request.method() === 'PATCH') {
      const patch = JSON.parse(request.postData())
      patches.push(patch)
      if (failSave) return respond({ error: 'save_failed' }, 500)
      profile = { ...profile, settings: { ...profile.settings, ...patch } }
      return respond(profile.settings)
    }
    if (route === '/api/me') return respond(profile)
    return respond({})
  })
  async function open(view, theme = 'light', lang = 'zh', animation = 'paper-plane') {
    profile = { ...profile, settings: { onboarded: view !== 'welcome', reply_animation: animation } }
    await page.goto(`http://127.0.0.1:5207/tests/browser/reply-wait-harness.html?view=${view}&theme=${theme}&lang=${lang}&animation=${animation}`, { waitUntil: 'networkidle0' })
    await page.waitForFunction(() => Boolean(window.__REPLY_WAIT__))
    if (view === 'settings' || view === 'gallery') await page.waitForSelector('[data-reply-animation-picker]')
    if (view === 'welcome') await page.waitForSelector('[data-welcome-step="language"]')
    await page.evaluate(() => document.fonts.ready)
  }
  async function choose(variant) {
    const label = `[data-reply-animation-picker] label:has(input[value="${variant}"])`
    await page.$eval(label, (node) => node.scrollIntoView({ block: 'center' }))
    await page.click(label)
  }
  async function saved(variant) {
    await page.waitForFunction((variant) => {
      const state = window.__REPLY_WAIT__.settings.getState()
      return !state.replyAnimationPending && state.appearance.replyAnimation === variant && window.__REPLY_WAIT__.auth.getState().user.settings.reply_animation === variant
    }, {}, variant)
    assert.equal(await page.$eval('[data-reply-animation-picker] input:checked', (node) => node.value), variant)
  }
  async function bounded() {
    const geometry = await page.$eval('[data-reply-animation-picker]', (picker) => ({
      width: innerWidth,
      document: document.documentElement.scrollWidth,
      controls: [...picker.querySelectorAll('label')].map((node) => {
        const rect = node.getBoundingClientRect()
        return { left: rect.left, right: rect.right, width: rect.width, scroll: node.scrollWidth, client: node.clientWidth }
      }),
    }))
    assert.ok(geometry.document <= geometry.width + 1, JSON.stringify(geometry))
    assert.ok(geometry.controls.every((rect) => rect.width >= 44 && rect.left >= -1 && rect.right <= geometry.width + 1 && rect.scroll <= rect.client + 1), JSON.stringify(geometry))
  }
  async function clickText(label) {
    const clicked = await page.evaluate((label) => {
      const button = [...document.querySelectorAll('button')].find((node) => node.textContent.trim() === label)
      button?.click()
      return Boolean(button)
    }, label)
    assert.ok(clicked, `Missing button: ${label}`)
  }
  async function animationStep(lang = 'zh') {
    for (let step = 0; step < 4; step++) await clickText(translations[lang].welcome.actions.next)
    await page.waitForSelector('[data-welcome-step="replyAnimation"]')
    await page.waitForSelector('[data-reply-animation-picker]')
  }
  async function screenshot(name) {
    // Wait for smooth scrolling and the welcome heading's focus transition.
    await new Promise((resolve) => setTimeout(resolve, 800))
    await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true })
  }

  await page.setViewport({ width: 1440, height: 1000 })
  await open('settings')
  assert.equal(await page.$eval('[data-reply-animation-picker] input:checked', (node) => node.value), 'paper-plane')
  assert.deepEqual(await page.$$eval('[data-reply-animation-picker] [data-reply-animation]', (nodes) => nodes.map((node) => node.dataset.replyAnimation)), variants)
  await bounded()
  for (const variant of [...variants.slice(1), variants[0]]) {
    await choose(variant)
    await saved(variant)
    assert.equal(patches.at(-1).reply_animation, variant)
    checks++
  }
  await page.focus('[data-reply-animation-picker] input[value="paper-plane"]')
  await page.keyboard.press('ArrowRight')
  await saved('origami')
  checks++
  await choose('prism')
  await saved('prism')
  await page.reload({ waitUntil: 'networkidle0' })
  await saved('prism')
  assert.equal(await page.evaluate(() => JSON.parse(localStorage.getItem('aivory.settings')).appearance.replyAnimation), 'prism')
  checks++
  failSave = true
  await choose('ink')
  await saved('prism')
  failSave = false
  checks++

  await choose('paper-plane')
  await saved('paper-plane')
  await page.$eval('[data-reply-animation-picker]', (node) => {
    const pane = document.querySelector('[data-settings-pane]')
    const section = node.closest('section')
    pane.scrollBy({ top: section.getBoundingClientRect().top - pane.getBoundingClientRect().top - 120, behavior: 'instant' })
  })
  await screenshot('settings-desktop-light')

  for (const theme of ['light', 'dark']) {
    for (const lang of Object.keys(translations)) {
      await page.setViewport({ width: 320, height: 740, isMobile: true, hasTouch: true })
      await open('settings', theme, lang)
      await bounded()
      const labels = await page.$$eval('[data-reply-animation-picker] label', (nodes) => nodes.map((node) => node.textContent.trim()))
      for (const variant of variants) assert.ok(labels.some((label) => label.includes(translations[lang].settings.appearance.replyAnimation.options[variant])))
      if (lang === 'zh') {
        await page.$eval('[data-reply-animation-picker]', (node) => node.scrollIntoView({ block: 'center' }))
        await screenshot(`settings-mobile-${theme}`)
      }
      checks++
      await open('welcome', theme, lang)
      await animationStep(lang)
      await bounded()
      if (lang === 'zh') await screenshot(`welcome-mobile-${theme}`)
      checks++
    }
  }

  await page.setViewport({ width: 1440, height: 1000 })
  await open('welcome')
  await animationStep()
  await choose('orbit')
  assert.equal(patches.at(-1).reply_animation, 'paper-plane', 'Onboarding choices remain drafts')
  await clickText(translations.zh.welcome.actions.back)
  await clickText(translations.zh.welcome.actions.next)
  assert.equal(await page.$eval('[data-reply-animation-picker] input:checked', (node) => node.value), 'orbit')
  // A background profile refresh must not discard the wizard draft.
  await page.evaluate(() => {
    const { auth } = window.__REPLY_WAIT__
    auth.getState().setUser({ ...auth.getState().user, settings: { reply_animation: 'paper-plane' } })
  })
  assert.equal(await page.$eval('[data-reply-animation-picker] input:checked', (node) => node.value), 'orbit')
  await screenshot('welcome-desktop-light')
  await clickText(translations.zh.welcome.actions.next)
  await page.waitForSelector('[data-welcome-step="memory"]')
  await clickText(translations.zh.welcome.actions.start)
  await page.waitForSelector('[data-welcome-ready]')
  assert.equal(profile.settings.reply_animation, 'orbit')
  assert.equal(await page.evaluate(() => window.__REPLY_WAIT__.settings.getState().appearance.replyAnimation), 'orbit')
  checks++

  await open('welcome', 'light', 'zh', 'ink')
  await animationStep()
  await choose('prism')
  await clickText(translations.zh.welcome.actions.skip)
  await page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
  assert.equal(profile.settings.reply_animation, 'ink')
  assert.equal(await page.evaluate(() => window.__REPLY_WAIT__.settings.getState().appearance.replyAnimation), 'ink')
  assert.equal(patches.at(-1).reply_animation, undefined)
  checks++

  await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
  await open('gallery', 'dark')
  const animations = await page.$$eval('[data-reply-animation] *', (nodes) => nodes.map((node) => getComputedStyle(node).animationName))
  assert.ok(animations.every((name) => name === 'none'))
  checks++

  assert.deepEqual(errors, [])
  console.log(`Reply animation settings checks passed (${checks} scenarios). Screenshots: ${output}`)
} finally {
  await browser?.close()
  await server.close()
  await rm(cacheDir, { recursive: true, force: true })
}
