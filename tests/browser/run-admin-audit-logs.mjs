import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const chrome = process.env.CHROME_PATH ?? [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome', '/usr/bin/chromium',
].find(existsSync)
if (!chrome) throw new Error('Set CHROME_PATH to a Chrome or Chromium executable')
const output = '/tmp/aivory-admin-audit-logs'
await mkdir(output, { recursive: true })
const time = Date.now() - 60_000
const makeLog = (id, type = 'users') => ({
  id, type, actor_user_id: 'admin-1', actor_name: 'Audit Administrator', actor_role: 'admin',
  action: 'admin.users.update', target_type: 'user', target_id: 'user-2', target_name: 'Test user',
  result: 'success', created_at: Math.floor(time / 1000), occurred_at_ms: time,
  request_id: 'request-42', client_ip: '127.0.0.1', metadata: { changed: 'role' },
})
let records = []
let failDeletion = false
const mutations = []
const requests = []
const errors = []
const server = await createServer({ server: { port: 5197, strictPort: false }, logLevel: 'error' })
let browser

try {
  await server.listen()
  const base = server.resolvedUrls.local[0]
  browser = await puppeteer.launch({ executablePath: chrome, headless: true })
  const page = await browser.newPage()
  page.on('pageerror', (error) => errors.push(String(error)))
  await page.setRequestInterception(true)
  page.on('request', async (request) => {
    const url = new URL(request.url())
    if (!url.pathname.startsWith('/api/')) return request.continue()
    const path = url.pathname.slice(4)
    let status = 200
    let body
    const matches = (log) => (!url.searchParams.get('type') || url.searchParams.get('type') === log.type)
      && (!url.searchParams.get('q') || `${log.id} ${log.actor_name} ${log.target_id}`.toLowerCase().includes(url.searchParams.get('q').toLowerCase()))
      && (!url.searchParams.get('until') || log.occurred_at_ms <= Date.parse(url.searchParams.get('until')))
    if (path === '/admin/audit-logs' && request.method() === 'GET') {
      const filtered = records.filter(matches)
      const currentPage = Number(url.searchParams.get('page') || 1)
      const pageSize = Number(url.searchParams.get('page_size') || 50)
      body = { logs: filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize), total: filtered.length, page: currentPage, page_size: pageSize }
      requests.push({ page: currentPage, type: url.searchParams.get('type') })
    } else if (path.startsWith('/admin/audit-logs') && request.method() === 'DELETE') {
      mutations.push({ path, query: Object.fromEntries(url.searchParams) })
      if (failDeletion) {
        failDeletion = false
        status = 500
        body = { error: 'Test deletion failure' }
      } else {
        const id = path.slice('/admin/audit-logs/'.length)
        const keep = path === '/admin/audit-logs' ? (log) => !matches(log) : (log) => log.id !== decodeURIComponent(id)
        const remaining = records.filter(keep)
        body = path === '/admin/audit-logs' ? { deleted: records.length - remaining.length } : { ok: true }
        records = [...remaining, { ...makeLog(`deletion-${mutations.length}`, 'logs'), action: 'admin.logs.delete', occurred_at_ms: Date.now() }]
      }
    } else {
      errors.push(`Missing fixture: ${request.method()} ${path}`)
      body = []
    }
    await request.respond({ status, contentType: 'application/json', body: JSON.stringify(body) })
  })

  const dialog = () => page.$('[role="dialog"]')
  const clickCommand = async (text) => {
    await waitForAnimations()
    const button = await page.evaluateHandle((label) => [...document.querySelectorAll('[role="dialog"] button')].find((element) => element.textContent.trim() === label), text)
    assert.ok(button.asElement(), `Missing dialog command: ${text}`)
    await button.asElement().evaluate((element) => element.scrollIntoView({ block: 'center' }))
    await button.asElement().click().catch(async (error) => {
      await page.screenshot({ path: `${output}/failed-command.png`, fullPage: true })
      const rect = await button.asElement().evaluate((element) => element.getBoundingClientRect().toJSON())
      throw new Error(`${text}: ${error.message}; ${JSON.stringify(rect)}`)
    })
    await button.dispose()
  }
  const closeDialogs = () => page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
  const waitForAnimations = () => page.waitForFunction(() => [...document.querySelectorAll('[role="dialog"]')].every((element) => element.getAnimations({ subtree: true }).every((animation) => animation.playState === 'finished')))
  const checkViewport = async () => assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0)

  for (const locale of process.env.ADMIN_AUDIT_LOCALES?.split(',').filter(Boolean) ?? ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
    const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}/admin.json`, 'utf8'))
    const labels = translations.logs
    for (const [width, theme] of [[1440, 'light'], [390, 'dark']]) {
      records = [makeLog('workspace-log', 'workspace'), makeLog('admin-log'), makeLog('channel-log', 'channels')]
      await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
      await page.goto(`${base}tests/browser/admin-tables-harness.html?view=audit&theme=${theme}&lang=${locale}`, { waitUntil: 'networkidle0' })
      await page.waitForSelector('tbody tr', { visible: true })
      await checkViewport()
      assert.ok(!(await page.$eval('tbody', (element) => element.innerText)).includes('127.0.0.1'))
      await page.click('tbody tr:first-child')
      await page.waitForSelector('[role="dialog"]', { visible: true })
      const beforeCancel = mutations.length
      await clickCommand(labels.deleteRow)
      await page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length === 2)
      await clickCommand(translations.common.cancel)
      await page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length === 1)
      assert.equal(mutations.length, beforeCancel)
      assert.ok((await page.$eval('[role="dialog"]', (element) => element.innerText)).includes('workspace-log'))
      await clickCommand(labels.deleteRow)
      await page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length === 2)
      await clickCommand(translations.common.delete)
      await closeDialogs()
      await page.waitForSelector('tbody tr', { visible: true })
      assert.equal(mutations.at(-1).path, '/admin/audit-logs/workspace-log')
      assert.ok(!(await page.$eval('tbody', (element) => element.innerText)).includes('workspace-log'))

      const deleteLabel = labels.deleteEntry.replace('{{id}}', 'admin-log')
      await page.$eval('.admin-table-scroll', (element) => { element.scrollLeft = element.scrollWidth })
      await page.focus(`button[aria-label="${deleteLabel}"]`)
      await page.keyboard.press('Space')
      await page.waitForSelector('[role="dialog"]', { visible: true })
      assert.equal(await page.$$eval('[role="dialog"]', (elements) => elements.length), 1, 'The row delete command opened a detail drawer')
      failDeletion = true
      await clickCommand(translations.common.delete)
      await page.waitForFunction(() => !document.querySelector('[role="dialog"] button[aria-busy="true"]'))
      assert.ok(records.some((log) => log.id === 'admin-log'), 'A failed deletion removed the log')
      assert.ok(await dialog(), 'A failed deletion closed the confirmation')
      await clickCommand(translations.common.cancel)
      await closeDialogs()

      await page.click(`button[aria-label="${labels.deleteFiltered}"]`)
      await page.waitForSelector('[role="dialog"]', { visible: true })
      await waitForAnimations()
      await checkViewport()
      assert.ok(!(await page.$eval('[role="dialog"]', (element) => element.innerText)).includes('logs.deleteConfirm'))
      await page.screenshot({ path: `${output}/delete-${locale}-${width}-${theme}.png`, fullPage: true })
      const openedAt = Date.now()
      records.push({ ...makeLog('arrived-after-confirmation'), occurred_at_ms: openedAt + 1000 })
      await clickCommand(translations.common.delete)
      await closeDialogs()
      await page.waitForSelector('tbody tr', { visible: true })
      assert.equal(mutations.at(-1).path, '/admin/audit-logs')
      assert.ok(mutations.at(-1).query.until, 'Bulk deletion did not freeze its time boundary')
      assert.ok(records.some((log) => log.id === 'arrived-after-confirmation'), 'Bulk deletion removed a newly arriving event')
      assert.ok(records.some((log) => log.type === 'logs'))
    }
    console.log(`Audit deletion layouts and interactions passed: ${locale}`)
  }

  records = [...Array.from({ length: 51 }, (_, index) => makeLog(`user-log-${index + 1}`)), makeLog('channel-unmatched', 'channels')]
  await page.setViewport({ width: 1440, height: 960, isMobile: false, hasTouch: false })
  await page.goto(`${base}tests/browser/admin-tables-harness.html?view=audit&theme=light&lang=zh`, { waitUntil: 'networkidle0' })
  await page.click('button[aria-label="按类型筛选"]')
  const filteredResponse = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return url.pathname === '/api/admin/audit-logs' && url.searchParams.get('type') === 'users'
  })
  const option = await page.evaluateHandle(() => [...document.querySelectorAll('[role="option"]')].find((element) => element.textContent.trim() === '用户'))
  await option.asElement().click()
  await option.dispose()
  await filteredResponse
  await page.waitForFunction(() => document.querySelectorAll('tbody tr').length === 50 && document.querySelector('button[aria-label="删除筛选结果"]')?.disabled === false)
  const next = await page.evaluateHandle(() => [...document.querySelectorAll('button')].find((element) => element.getAttribute('aria-label')?.includes('下一页')))
  await next.asElement().click()
  await next.dispose()
  await page.waitForFunction(() => document.querySelectorAll('tbody tr').length === 1 && document.querySelector('tbody').innerText.includes('user-log-51'))
  await page.focus('button[aria-label="删除审计日志 user-log-51"]')
  await page.keyboard.press('Enter')
  await page.waitForSelector('[role="dialog"]', { visible: true })
  await clickCommand('删除')
  await closeDialogs()
  await page.waitForFunction(() => document.querySelectorAll('tbody tr').length === 50)
  assert.equal(requests.at(-1).page, 1, 'Deleting the last row of a page did not recover pagination')
  await page.click('button[aria-label="删除筛选结果"]')
  await page.waitForSelector('[role="dialog"]', { visible: true })
  await clickCommand('删除')
  await closeDialogs()
  await page.waitForFunction(() => !document.querySelector('tbody tr'))
  assert.equal(mutations.at(-1).query.type, 'users')
  assert.ok(records.some((log) => log.id === 'channel-unmatched'), 'Type-filter deletion removed an unrelated event')
  assert.deepEqual(errors, [])
  console.log(`Audit deletion browser checks passed; screenshots: ${output}`)
} finally {
  await browser?.close()
  await server.close()
}
