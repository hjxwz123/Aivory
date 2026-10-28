import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const executablePath = process.env.CHROME_PATH || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium',
].find(existsSync)
assert.ok(executablePath, 'Set CHROME_PATH to a Chrome/Chromium executable')
const output = process.env.AIVORY_REPLY_WAIT_SCREENSHOTS || path.join(tmpdir(), 'aivory-reply-wait')
await mkdir(output, { recursive: true })
const cacheDir = await mkdtemp(path.join(tmpdir(), 'aivory-reply-wait-cache-'))
const server = await createServer({ cacheDir, server: { host: '127.0.0.1', port: 5206, strictPort: true }, logLevel: 'error' })
const errors = []
let browser
let checks = 0
try {
  await server.listen()
  browser = await puppeteer.launch({ executablePath, headless: true })
  const page = await browser.newPage()
  page.setDefaultNavigationTimeout(60_000)
  page.on('pageerror', (error) => errors.push(String(error)))
  await page.setRequestInterception(true)
  page.on('request', (request) => {
    const route = new URL(request.url()).pathname
    if (!route.startsWith('/api/')) return request.continue()
    if (route === '/api/events') return request.respond({ status: 200, contentType: 'text/event-stream', body: '' })
    const fixtures = {
      '/api/me/skills': [], '/api/me/prompts': [], '/api/me/mcps': [], '/api/me/skill-commands': [],
      '/api/kbs': [], '/api/projects': [], '/api/model-tags': [],
      '/api/workspaces': { workspaces: [] }, '/api/me/domain-data': {},
      '/api/sandbox/availability': { available: false },
      '/api/me/credits': { permanent: 100, available: 100, timed: { balance: 0, grants: [] } },
    }
    return request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(fixtures[route] ?? {}) })
  })
  async function open(view = 'main', theme = 'light', lang = 'zh', animation = 'paper-plane') {
    await page.goto(`http://127.0.0.1:5206/tests/browser/reply-wait-harness.html?view=${view}&theme=${theme}&lang=${lang}&animation=${animation}`, { waitUntil: 'networkidle0' })
    await page.waitForFunction(() => Boolean(window.__REPLY_WAIT__))
    if (view === 'main') await page.evaluate(() => window.__REPLY_WAIT__.send())
    await page.waitForSelector('[data-reply-pending]')
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    await page.waitForFunction(() => document.getAnimations().every((animation) => !Number.isFinite(animation.effect?.getTiming().iterations) || animation.playState !== 'running'))
  }
  async function phase(name, age = 0) {
    await page.evaluate((name, age) => window.__REPLY_WAIT__.phase(name, age), name, age)
  }
  async function noPending() {
    await page.waitForFunction(() => !document.querySelector('[data-reply-pending]'))
  }
  async function bounded() {
    assert.ok(await page.evaluate(() => {
      const pending = document.querySelector('[data-reply-pending]')
      const rect = pending.getBoundingClientRect()
      return rect.width > 100 && rect.left >= -1 && rect.right <= innerWidth + 1 && pending.scrollWidth <= pending.clientWidth + 1 && document.documentElement.scrollWidth <= innerWidth + 1
    }), 'Waiting feedback must fit the viewport')
  }
  async function screenshot(name) {
    await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true })
  }
  async function animationOnly() {
    const state = await page.$eval('[data-reply-pending]', (node) => {
      const copy = node.cloneNode(true)
      copy.querySelectorAll('.sr-only, [aria-hidden="true"]').forEach((child) => child.remove())
      const accessible = node.querySelector('.sr-only')
      return {
        visibleText: copy.textContent.trim(),
        accessibleText: accessible?.textContent.trim(),
        labelWidth: accessible?.getBoundingClientRect().width,
        role: node.getAttribute('role'),
      }
    })
    assert.equal(state.visibleText, '', 'Waiting feedback should be driven by animation')
    assert.ok(state.accessibleText && !state.accessibleText.includes('replyPending.'))
    assert.equal(state.labelWidth, 1, 'The status description should only be exposed to screen readers')
    assert.equal(state.role, 'status')
  }

  async function followsAccent(selector) {
    const colors = await page.$eval(selector, (node) => {
      const probe = document.createElement('span')
      probe.style.color = 'var(--color-accent)'
      document.body.append(probe)
      const result = { animation: getComputedStyle(node).color, accent: getComputedStyle(probe).color }
      probe.remove()
      return result
    })
    assert.equal(colors.animation, colors.accent, 'Animation should follow the selected site accent')
  }

  async function liveReasoning(view) {
    await phase('reasoning')
    await page.waitForSelector('[data-reasoning-preview]')
    assert.equal(await page.$('[data-answer-cursor]'), null, 'Reasoning should occupy one line without an empty answer cursor')
    assert.equal(await page.$eval('[data-reasoning-toggle]', (node) => node.getAttribute('aria-expanded')), 'false')
    assert.ok(await page.$eval('[data-reasoning-current]', (node) => node.textContent.includes('交通方便')))
    assert.ok(!await page.$eval('[data-reasoning-toggle]', (node) => node.textContent.includes('思考中')))
    await followsAccent('[data-reply-activity]')
    const spinner = await page.$eval('[data-reply-activity] > span', (node) => {
      const style = getComputedStyle(node)
      const rect = node.getBoundingClientRect()
      return { width: rect.width, height: rect.height, radius: style.borderRadius, animation: style.animationName }
    })
    assert.ok(Math.abs(spinner.width - spinner.height) < 1 && spinner.radius === '50%')
    assert.ok(spinner.animation.includes('reply-activity-spin'))
    // Tokens keep arriving faster than the display cadence; they must not starve it.
    await page.evaluate(async () => {
      for (let index = 0; index < 25; index++) {
        window.__REPLY_WAIT__.reason(`先比较路线。\n\n正在核对最新的列车时刻 ${index}`)
        await new Promise((resolve) => setTimeout(resolve, 60))
      }
    })
    await page.waitForFunction(() => document.querySelector('[data-reasoning-current]')?.textContent === '正在核对最新的列车时刻 24')
    const line = await page.$eval('[data-reasoning-preview]', (node) => {
      const style = getComputedStyle(node)
      const current = node.querySelector('[data-reasoning-current]')
      return { height: node.getBoundingClientRect().height, lineHeight: parseFloat(style.lineHeight), whiteSpace: getComputedStyle(current).whiteSpace, transition: getComputedStyle(current).animationName }
    })
    assert.ok(line.height <= line.lineHeight + 1)
    assert.equal(line.whiteSpace, 'nowrap')
    assert.ok(line.transition.includes('reasoning-line-in'))
    if (view === 'main') await screenshot('reasoning-single-line-desktop')
    await page.click('[data-reasoning-toggle]')
    assert.equal(await page.$eval('[data-reasoning-content]', (node) => node.hidden), false)
    await page.evaluate(() => window.__REPLY_WAIT__.reason('检查剩余交通安排。'))
    assert.equal(await page.$eval('[data-reasoning-toggle]', (node) => node.getAttribute('aria-expanded')), 'true')
    await phase('answer')
    await page.waitForFunction(() => document.querySelector('[data-reasoning-content]')?.hidden)
  }

  async function longQuestion(view, theme, width) {
    await page.setViewport({ width, height: 900, isMobile: width < 640, hasTouch: width < 640 })
    await open(view, theme)
    const text = Array.from({ length: 28 }, (_, index) => `${index + 1}. 请比较交通、预算与下雨时的替代安排，保留完整的问题内容。`).join('\n')
    await page.evaluate((text) => window.__REPLY_WAIT__.send(text), text)
    const selector = '[data-user-message-toggle]'
    await page.waitForSelector(selector, { timeout: 8000 }).catch(async (error) => {
      console.error('Long question layout:', { view, theme, width }, await page.$$eval('[data-user-message-content]', (nodes) => nodes.map((node) => ({
        textLength: node.textContent.length,
        height: node.getBoundingClientRect().height,
        lineHeight: getComputedStyle(node).lineHeight,
        lines: getComputedStyle(node).getPropertyValue('--user-message-preview-lines'),
        contentHeight: node.querySelector('[data-user-message-viewport] > div')?.getBoundingClientRect().height,
      }))))
      throw error
    })
    assert.equal(await page.$eval(selector, (node) => node.getAttribute('aria-expanded')), 'false')
    const folded = await page.$eval(selector, (node) => {
      const viewport = document.getElementById(node.getAttribute('aria-controls'))
      return { height: viewport.clientHeight, scroll: viewport.scrollHeight, lines: parseFloat(getComputedStyle(viewport).lineHeight), buttonHeight: node.getBoundingClientRect().height }
    })
    assert.ok(folded.scroll > folded.height * 2)
    assert.ok(folded.height <= folded.lines * (width < 640 ? 6 : 8) + 1)
    if (width < 640) assert.ok(folded.buttonHeight >= 44)
    if (view === 'main') {
      await page.$eval(selector, (node) => node.closest('[data-user-message-content]').scrollIntoView({ block: 'center' }))
      await screenshot(`question-collapsed-${theme}-${width}`)
    }
    await page.click(selector)
    assert.equal(await page.$eval(selector, (node) => node.getAttribute('aria-expanded')), 'true')
    assert.equal(await page.$eval(selector, (node) => document.getElementById(node.getAttribute('aria-controls')).textContent), text)
    assert.ok(await page.$eval(selector, (node) => {
      const viewport = document.getElementById(node.getAttribute('aria-controls'))
      return viewport.clientHeight >= viewport.scrollHeight - 1
    }))
    await page.click(selector)
    assert.equal(await page.$eval(selector, (node) => node.getAttribute('aria-expanded')), 'false')
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1))
    // A new short question must not inherit the old message's disclosure state.
    await page.evaluate(() => window.__REPLY_WAIT__.send('给我一个简短的建议。'))
    await page.waitForFunction(() => !document.querySelector('[data-user-message-toggle]'))
  }

  await page.setViewport({ width: 1280, height: 900 })
  await open()
  // Exercise the real composer: stop the fixture turn, type and send a new one.
  await page.click('button[aria-label="停止生成"]')
  await noPending()
  await page.waitForSelector('[contenteditable="true"]')
  await page.type('[contenteditable="true"]', '帮我重新安排一个更轻松的两天行程。')
  await page.click('button[aria-label="发送"]')
  await page.waitForSelector('[data-reply-pending]')
  assert.equal(await page.$$eval('[data-reply-pending]', (nodes) => nodes.length), 1)
  await animationOnly()
  assert.equal(await page.$$('.thinking-shimmer').then((nodes) => nodes.length), 0)
  await bounded()
  await screenshot('desktop-light')
  const initial = await page.$$eval('[data-reply-pending] [data-motion-part]', (nodes) => nodes.map((node) => getComputedStyle(node).transform))
  await new Promise((resolve) => setTimeout(resolve, 180))
  const later = await page.$$eval('[data-reply-pending] [data-motion-part]', (nodes) => nodes.map((node) => getComputedStyle(node).transform))
  assert.notDeepEqual(initial, later, 'The activity rhythm must visibly animate')
  checks++

  for (const age of [15_000, 35_000]) {
    await phase('waiting', age)
    await page.waitForSelector('[data-reply-pending]')
    await animationOnly()
  }
  checks++

  for (const name of ['reasoning', 'tool', 'answer', 'stopped', 'error', 'image', 'research']) {
    await phase('waiting')
    await page.waitForSelector('[data-reply-pending]')
    await phase(name)
    await noPending()
    if (name === 'reasoning' || name === 'tool') await page.waitForSelector('[data-reply-activity]')
    if (name === 'answer') assert.ok((await page.$eval('body', (body) => body.textContent)).includes('两天的轻松行程'))
    if (name === 'stopped') assert.ok((await page.$eval('body', (body) => body.textContent)).includes('已停止生成'))
    if (name === 'error') await page.waitForSelector('[role="alert"]')
    if (name === 'image') await page.waitForSelector('[data-image-generating="generating"]')
    checks++
  }

  for (const view of ['private', 'thread']) {
    await open(view)
    await bounded()
    await screenshot(`${view}-light`)
    await phase('reasoning')
    await noPending()
    await page.waitForSelector('[data-reply-activity]')
    await phase('answer')
    await noPending()
    checks++
  }

  for (const view of ['main', 'private', 'thread']) {
    await open(view)
    await liveReasoning(view)
    checks++
  }

  for (const theme of ['light', 'dark']) {
    for (const view of ['main', 'private', 'thread']) {
      await longQuestion(view, theme, 390)
      checks++
    }
  }
  await longQuestion('main', 'light', 1280)
  checks++

  await page.evaluate(() => window.__REPLY_WAIT__.settings.getState().setAppearance({ userMessageMarkdown: true }))
  await page.evaluate(() => window.__REPLY_WAIT__.send('# 行程需求\n\n' + '- 请保留所有交通和预算安排。\n'.repeat(24)))
  await page.waitForSelector('[data-user-message-toggle]')
  await page.click('[data-user-message-toggle]')
  assert.equal(await page.$eval('[data-user-message-viewport]:has(.prose-user)', (node) => node.dataset.collapsed), 'false')
  checks++

  for (const theme of ['light', 'dark']) {
    await page.setViewport({ width: 390, height: 844, isMobile: true, hasTouch: true })
    await open('main', theme)
    await bounded()
    await phase('waiting', 35_000)
    await animationOnly()
    await bounded()
    await screenshot(`mobile-${theme}`)
    checks++
    for (const view of ['private', 'thread']) {
      await open(view, theme)
      await bounded()
      if (theme === 'dark') await screenshot(`mobile-${view}-${theme}`)
      await phase('answer')
      await noPending()
      checks++
    }
  }

  await page.setViewport({ width: 320, height: 740, isMobile: true, hasTouch: true })
  for (const lang of ['zh', 'zh-Hant', 'en', 'fr', 'ja']) {
    await open('main', 'light', lang)
    await phase('waiting', 35_000)
    await animationOnly()
    await bounded()
    assert.ok(!await page.$eval('[data-reply-pending]', (node) => node.textContent.includes('replyPending.')))
    checks++
  }

  for (const variant of ['paper-plane', 'origami', 'ink', 'sprout', 'orbit', 'prism']) {
    await open('main', 'dark', 'zh', variant)
    await page.waitForSelector(`[data-reply-pending] [data-reply-animation="${variant}"]`)
    await animationOnly()
    await bounded()
    await followsAccent('[data-reply-animation]')
    await page.evaluate(() => document.documentElement.style.setProperty('--color-accent', 'rgb(201, 67, 125)'))
    await followsAccent('[data-reply-animation]')
    const state = () => page.$$eval('[data-reply-pending] [data-motion-part]', (nodes) => nodes.map((node) => {
      const style = getComputedStyle(node)
      return [style.transform, style.strokeDashoffset]
    }))
    const before = await state()
    await new Promise((resolve) => setTimeout(resolve, 240))
    assert.notDeepEqual(before, await state(), `${variant} must animate in real chat`)
    checks++

    await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
    const animations = await page.$$eval('[data-reply-pending] [data-reply-animation] *', (nodes) => nodes.map((node) => getComputedStyle(node).animationName))
    assert.ok(animations.every((name) => name === 'none'), `${variant}: ${animations.join(', ')}`)
    await animationOnly()
    if (variant === 'paper-plane') await screenshot('reduced-motion')
    await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'no-preference' }])
    checks++
  }

  assert.deepEqual(errors, [])
  console.log(`Reply wait browser checks passed (${checks} scenarios). Screenshots: ${output}`)
} finally {
  await browser?.close()
  await server.close()
  await rm(cacheDir, { recursive: true, force: true })
}
