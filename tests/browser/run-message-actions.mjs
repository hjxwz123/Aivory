import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const executablePath = [process.env.CHROME_PATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge', '/usr/bin/chromium'].find((candidate) => candidate && existsSync(candidate))
assert.ok(executablePath, 'Set CHROME_PATH to a local Chrome or Edge executable')
const output = process.env.AIVORY_MESSAGE_ACTIONS_OUTPUT || join(tmpdir(), 'aivory-message-actions')
await mkdir(output, { recursive: true })
const cacheDir = await mkdtemp(join(tmpdir(), 'aivory-message-actions-cache-'))
const server = await createServer({ cacheDir, server: { host: '127.0.0.1', port: 0 }, logLevel: 'error' })
const errors = []
let browser
let checks = 0
let page
try {
  await server.listen()
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`
  browser = await puppeteer.launch({ executablePath, headless: true })
  page = await browser.newPage()
  page.setDefaultNavigationTimeout(60000)
  page.on('pageerror', (error) => errors.push(String(error)))
  await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }])
  await page.evaluateOnNewDocument(() => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: async (text) => { window.__COPIED_MESSAGE__ = text } }, configurable: true })
  })
  await page.setRequestInterception(true)
  page.on('request', (request) => {
    if (!new URL(request.url()).pathname.startsWith('/api/')) return request.continue()
    return request.respond({ status: 200, contentType: 'application/json', body: '{}' })
  })

  async function configure(patch) {
    await page.evaluate((patch) => window.__MESSAGE_ACTIONS__.configure(patch), patch)
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  }
  async function branchText() {
    return page.$eval('[data-message-branch]', (node) => node.textContent)
  }
  async function inline() {
    const measurements = await page.$eval('[data-message-footer]', (node) => {
      const rect = (selector) => {
        const r = node.querySelector(selector).getBoundingClientRect()
        return { left: r.left, right: r.right, top: r.top, height: r.height }
      }
      return {
        branch: rect('[data-message-branch]'), actions: rect('[data-message-actions]'),
        copy: rect('button[aria-label="复制"]'),
        footerHeight: node.getBoundingClientRect().height,
        overflow: document.documentElement.scrollWidth > innerWidth + 1,
      }
    })
    assert.equal(measurements.overflow, false, JSON.stringify(measurements))
    assert.ok(measurements.actions.left >= measurements.branch.right - 1)
    assert.ok(Math.abs(measurements.branch.top + measurements.branch.height / 2 - measurements.copy.top - measurements.copy.height / 2) <= 1, 'Branch and actions must share one row')
    return measurements
  }

  for (const theme of ['light', 'dark']) {
    for (const width of [320, 390, 1280]) {
      await page.setViewport({ width, height: 844, deviceScaleFactor: 2, isMobile: width < 640, hasTouch: width < 640 })
      await page.goto(`${origin}/tests/browser/message-actions-harness.html?theme=${theme}`, { waitUntil: 'networkidle0' })
      await page.waitForFunction(() => window.__MESSAGE_ACTIONS__?.ready)
      await page.waitForSelector('[data-message-branch]')
      await page.evaluate(() => document.fonts.ready)
      const measures = await inline()
      assert.equal(await branchText(), '4/4')
      assert.equal(await page.$eval('[data-message-branch]', (node) => getComputedStyle(node).backgroundColor), 'rgba(0, 0, 0, 0)')
      assert.equal(await page.$$eval('[data-message-footer]', (nodes) => nodes.length), 1)
      if (width === 1280) {
        await page.mouse.move(1270, 830)
        assert.equal(await page.$eval('[data-message-branch]', (node) => getComputedStyle(node).opacity), '1')
        assert.equal(await page.$eval('[data-message-actions]', (node) => getComputedStyle(node).opacity), '0')
        await page.focus('[data-message-branch] button')
        assert.equal(await page.$eval('[data-message-actions]', (node) => getComputedStyle(node).opacity), '1')
        await page.$eval('[data-message-branch] button', (node) => node.blur())
        await page.hover('[data-message-footer]')
      } else {
        assert.ok(measures.copy.height >= 44)
        assert.equal(await page.$$eval('[data-message-actions] button', (nodes) => nodes.length), 3)
        assert.equal(await page.$eval('[data-message-duration]', (node) => getComputedStyle(node).display === 'none'), width === 320)
      }
      await page.$eval('main', (node) => node.scrollIntoView({ block: 'start' }))
      const main = await page.$('main')
      await main.screenshot({ path: join(output, `${theme}-${width}.png`) })
      await page.click('[data-message-branch] button[aria-label="上一个分支"]')
      assert.equal(await branchText(), '3/4')
      await page.click('[data-message-branch] button[aria-label="下一个分支"]')
      await page.click('[data-message-branch] button[aria-label="下一个分支"]')
      assert.equal(await branchText(), '1/4', 'Last branch should wrap to the first')
      await page.click('[data-message-branch] button[aria-label="上一个分支"]')
      assert.equal(await branchText(), '4/4')
      await page.click('[data-message-actions] button[aria-label="复制"]')
      assert.ok((await page.evaluate(() => window.__COPIED_MESSAGE__)).includes('我是 快速'))
      await page.click('[data-message-actions] button[aria-label="重新生成"]')
      assert.deepEqual(await page.evaluate(() => window.__MESSAGE_ACTIONS__.events.at(-1)), { name: 'regenerate', value: 'branch-4' })
      checks++

      if (width < 640) {
        await page.click('[data-message-actions] button[aria-label="更多"]')
        await page.waitForSelector('[role="dialog"]')
        assert.ok(await page.$eval('[role="dialog"]', (node) => node.textContent.includes('1.6s') && node.textContent.includes('1.25')))
        assert.ok(await page.$('[role="dialog"] a[href="/subscription"]'))
        const items = await page.$$eval('[role="dialog"] button', (buttons) => buttons.map((button) => button.textContent))
        for (const action of ['编辑', '有帮助', '无帮助', '删除']) assert.ok(items.some((text) => text.includes(action)), action)
        await page.keyboard.press('Escape')
        await page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
        checks++
      }

      await configure({ readOnly: true })
      assert.equal(await page.$('[data-message-actions]'), null)
      await page.click('[data-message-branch] button[aria-label="上一个分支"]')
      assert.equal(await branchText(), '3/4')
      await configure({ readOnly: false, streaming: true })
      assert.equal(await page.$('[data-message-actions]'), null)
      assert.equal(await branchText(), '4/4')
      await page.click('[data-message-branch] button[aria-label="上一个分支"]')
      assert.equal(await branchText(), '3/4')
      await configure({ streaming: false, branchCount: 1 })
      assert.equal(await page.$('[data-message-branch]'), null)
      assert.ok(await page.$('[data-message-actions]'))
      checks++

      await configure({ branchCount: 4, role: 'user', text: '请比较这几个安排。' })
      await inline()
      assert.equal(await page.$('[data-message-actions] button[aria-label="重新生成"]'), null)
      checks++

      await configure({ role: 'assistant', branchCount: 128, text: '较多分支的回答。' })
      await inline()
      assert.equal(await branchText(), '128/128')
      if (width === 1280) {
        await configure({ columnWidth: 380 })
        await inline()
        await page.hover('[data-message-footer]')
        await page.$eval('[data-message-actions]', (node) => { node.scrollLeft = node.scrollWidth })
        await page.click('[data-message-actions] button[aria-label="删除"]')
        await page.waitForSelector('[role="dialog"]')
        await page.keyboard.press('Escape')
      }
      checks++
      console.log(`PASS message actions ${theme} ${width}px`)
    }
  }
  assert.deepEqual(errors, [])
  console.log(`Message action browser checks passed (${checks} scenarios). Screenshots: ${output}`)
} catch (error) {
  await page?.screenshot({ path: join(output, 'failure.png'), fullPage: true }).catch(() => {})
  throw error
} finally {
  await browser?.close()
  await server.close()
  await rm(cacheDir, { recursive: true, force: true })
}
