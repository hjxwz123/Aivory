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
const output = '/tmp/aivory-admin-user-drawers'
await mkdir(output, { recursive: true })
const now = Math.floor(Date.now() / 1000)
const users = Array.from({ length: 21 }, (_, index) => ({
  id: `u${index + 1}`, name: index === 20 ? 'A user with a long name for drawer layout verification' : `Person ${index + 1}`,
  email: `person${index + 1}@example.com`, role: index === 0 ? 'admin' : 'user', status: 'active',
  group_id: 'g1', created_at: now, last_seen_at: now, settings: {},
}))
const conversations = (id) => [{
  id: `conv-${id}`, user_id: id, title: `Conversation ${id}`, model_id: 'gpt-5', provider: 'openai',
  updated_at: now, archived: true, starred: true,
}]
const memories = (id) => [{
  id: `memory-${id}`, memory_text: `Memory belonging to ${id}`, slot: 'response_style', value: 'detailed',
  status: 'ACTIVE', created_at: now, updated_at: now,
}]
const logins = (id) => Array.from({ length: 51 }, (_, index) => ({
  id: `login-${id}-${index + 1}`, login_at: now - index * 3600, ip: `2001:db8::${index + 1}`,
  location: `Location ${id} ${index + 1}`, method: index % 2 ? 'oauth_2fa' : 'password',
  user_agent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/148.0.0.0 Safari/537.36',
}))
const errors = []
const requests = []
const deletions = []
let deletedConversations = new Set()
let failNext = ''
let emptyKind = ''
let deferredRequest = null
let deferNext = ''
const server = await createServer({ server: { port: 5196, strictPort: false }, logLevel: 'error' })
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
    requests.push({ path, method: request.method(), query: Object.fromEntries(url.searchParams) })
    let status = 200
    let body
    if (path === '/admin/users') {
      const search = url.searchParams.get('search')?.toLowerCase() || ''
      const filtered = users.filter((user) => `${user.name} ${user.email}`.toLowerCase().includes(search))
      const offset = Number(url.searchParams.get('offset') || 0)
      const limit = Number(url.searchParams.get('limit') || 20)
      body = { users: filtered.slice(offset, offset + limit), total: filtered.length }
    } else if (path === '/admin/user-groups') {
      body = [{ id: 'g1', name: 'Team', is_default: true }]
    } else if (/^\/admin\/users\/u\d+$/.test(path)) {
      body = users.find((user) => path.endsWith(`/${user.id}`))
    } else if (/^\/admin\/users\/u\d+\/(conversations|memories|login-history)$/.test(path)) {
      const [, , , id, kind] = path.split('/')
      if (failNext === kind) {
        failNext = ''
        status = 500
        body = { error: `Failed to load ${kind}` }
      } else if (kind === 'login-history') {
        const rows = emptyKind === kind ? [] : logins(id)
        const offset = Number(url.searchParams.get('offset') || 0)
        const limit = Number(url.searchParams.get('limit') || 50)
        body = { items: rows.slice(offset, offset + limit), total: rows.length }
      } else {
        body = emptyKind === kind ? [] : kind === 'memories' ? memories(id) : conversations(id).filter((row) => !deletedConversations.has(row.id))
      }
      if (deferNext === kind) {
        deferNext = ''
        deferredRequest = () => request.respond({ status, contentType: 'application/json', body: JSON.stringify(body) })
        return
      }
    } else if (/^\/admin\/conversations\/conv-u\d+$/.test(path)) {
      const cid = path.split('/').at(-1)
      if (request.method() === 'DELETE') {
        deletions.push(cid)
        deletedConversations.add(cid)
        body = { ok: true }
      } else {
        body = conversations(cid.slice(5))[0]
      }
    } else if (path.endsWith('/messages')) {
      body = []
    } else if (path === '/models' || path === '/image-models') {
      body = { models: [], default_id: '' }
    } else if (path === '/model-tags') {
      body = []
    } else {
      errors.push(`Missing fixture: ${request.method()} ${path}`)
      body = []
    }
    await request.respond({ status, contentType: 'application/json', body: JSON.stringify(body) })
  })

  const waitForAnimations = () => page.waitForFunction(() => [...document.querySelectorAll('[role="dialog"]')].every((element) => element.getAnimations().every((animation) => animation.playState === 'finished')))
  const open = async (label, more) => {
    const trigger = await page.$(`main tbody tr:first-child button[aria-label="${more}"]`)
    await trigger.evaluate((element) => element.scrollIntoView({ block: 'center', inline: 'center' }))
    await trigger.click()
    await trigger.dispose()
    await page.waitForSelector('[role="menuitem"]', { visible: true })
    const item = await page.evaluateHandle((label) => [...document.querySelectorAll('[role="menuitem"]')].find((element) => element.textContent.trim() === label), label)
    assert.ok(item.asElement(), `Missing user menu item: ${label}`)
    await item.asElement().click()
    await item.dispose()
    await page.waitForSelector('[role="dialog"]', { visible: true })
    await waitForAnimations()
  }
  const close = async () => {
    await page.keyboard.press('Escape')
    await page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
  }
  const clickDialogCommand = async (label) => {
    await waitForAnimations()
    const command = await page.evaluateHandle((label) => [...document.querySelectorAll('[role="dialog"] button')].find((element) => element.textContent.trim() === label), label)
    assert.ok(command.asElement(), `Missing command: ${label}`)
    await command.asElement().evaluate((element) => element.scrollIntoView({ block: 'center' }))
    await command.asElement().click()
    await command.dispose()
  }
  const waitForRows = () => page.waitForSelector('[role="dialog"] tbody tr', { visible: true })
  const loadUsers = (locale = 'en', theme = 'light') => page.goto(`${base}tests/browser/admin-tables-harness.html?view=users&theme=${theme}&lang=${locale}`, { waitUntil: 'networkidle0' })
  const entries = [
    ['conversations', 'viewConversations', 5, 'Conversation'],
    ['login-history', 'viewLoginHistory', 5, 'Location'],
    ['memories', 'viewMemories', 4, 'Memory belonging to'],
  ]

  for (const locale of process.env.ADMIN_USER_LOCALES?.split(',').filter(Boolean) ?? ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
    const admin = JSON.parse(readFileSync(`src/i18n/locales/${locale}/admin.json`, 'utf8'))
    const common = JSON.parse(readFileSync(`src/i18n/locales/${locale}/common.json`, 'utf8'))
    for (const [width, theme] of [[1440, 'light'], [390, 'dark']]) {
      await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
      await loadUsers(locale, theme)
      await page.type('main input', 'person')
      await page.waitForFunction(() => document.querySelectorAll('main tbody tr').length === 20)
      // Wait for the debounced search before navigating away from page one.
      await page.waitForFunction(() => document.querySelector('main input')?.value === 'person')
      await page.waitForResponse((response) => {
        const url = new URL(response.url())
        return url.pathname === '/api/admin/users' && url.searchParams.get('search') === 'person'
      })
      await page.click(`main button[aria-label="${common.pagination.next}"]`)
      await page.waitForFunction(() => document.querySelectorAll('main tbody tr').length === 1 && document.querySelector('main tbody').textContent.includes('person21@example.com'))
      for (const [kind, key, columns, rowText] of entries) {
        const before = requests.length
        await open(admin.users[key], admin.users.more)
        await waitForRows()
        const facts = await page.$eval('[role="dialog"]', (element) => {
          const box = element.getBoundingClientRect()
          const frame = element.querySelector('.admin-table-scroll')
          const heading = element.querySelector('h2')
          const closeButton = element.querySelector('button')
          return {
            right: box.right, left: box.left, width: box.width, height: box.height,
            columns: element.querySelectorAll('thead th').length, text: element.innerText,
            scrollable: frame.scrollWidth > frame.clientWidth,
            headerOverlap: heading.getBoundingClientRect().right > closeButton.getBoundingClientRect().left,
          }
        })
        assert.ok(Math.abs(facts.right - width) <= 1, `${kind}: drawer is not at the right edge`)
        assert.ok(facts.left >= 0 && facts.height === 960, `${kind}: drawer exceeds viewport`)
        assert.equal(facts.columns, columns, `${kind}: missing columns`)
        assert.ok(facts.text.includes(`${rowText} u21`), `${kind}: wrong user's records`)
        assert.ok(!facts.text.includes(admin.users.backToUsers), `${kind}: standalone page header leaked into drawer`)
        assert.equal(facts.headerOverlap, false, `${kind}: title overlaps close control`)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0)
        assert.equal(requests.slice(before).filter((request) => /^\/admin\/users\/u\d+$/.test(request.path)).length, 0, 'Drawer duplicated the user-detail request')
        if (width === 390) {
          assert.ok(facts.scrollable, `${kind}: full columns should scroll horizontally`)
          assert.ok(await page.$eval('[role="dialog"] .admin-table-scroll', (frame) => {
            frame.scrollLeft = frame.scrollWidth
            const lastColumn = frame.querySelector('thead th:last-child').getBoundingClientRect()
            return frame.scrollLeft > 0 && lastColumn.right <= frame.getBoundingClientRect().right + 1
          }))
          await page.$eval('[role="dialog"] .admin-table-scroll', (frame) => { frame.scrollLeft = 0 })
        }
        if (kind === 'login-history') {
          await page.click(`[role="dialog"] button[aria-label="${common.pagination.next}"]`)
          await page.waitForFunction(() => document.querySelector('[role="dialog"] tbody')?.textContent.includes('Location u21 51'))
          assert.equal(requests.filter((request) => request.path.endsWith('/login-history')).at(-1).query.offset, '50')
        }
        if (kind === 'memories') assert.equal(await page.$$eval('[role="dialog"] tbody button', (elements) => elements.length), 0, 'Memory viewer gained mutation controls')
        await page.screenshot({ path: `${output}/${kind}-${locale}-${width}-${theme}.png`, fullPage: true })
        await close()
        assert.equal(await page.$eval('main input', (input) => input.value), 'person')
        assert.ok((await page.$eval('main tbody', (body) => body.textContent)).includes('person21@example.com'), 'Closing drawer lost user pagination')
      }
    }
    console.log(`User drawers, table scrolling, pagination and i18n passed: ${locale}`)
  }

  const admin = JSON.parse(readFileSync('src/i18n/locales/en/admin.json', 'utf8'))
  const common = JSON.parse(readFileSync('src/i18n/locales/en/common.json', 'utf8'))
  await page.setViewport({ width: 1440, height: 960, isMobile: false, hasTouch: false })
  await loadUsers()
  for (const [kind, key] of entries) {
    failNext = kind
    await open(admin.users[key], admin.users.more)
    await page.waitForSelector('[role="dialog"] [role="alert"]')
    await clickDialogCommand(kind === 'conversations' ? common.actions.tryAgain : admin.users[kind === 'memories' ? 'memoriesRetry' : 'loginHistoryRetry'])
    await waitForRows()
    await close()
    emptyKind = kind
    await open(admin.users[key], admin.users.more)
    const emptyKey = kind === 'conversations' ? 'noConversations' : kind === 'memories' ? 'noMemories' : 'noLoginHistory'
    await page.waitForFunction((text) => document.querySelector('[role="dialog"]')?.textContent.includes(text), {}, admin.users[emptyKey])
    await close()
    emptyKind = ''
  }

  await open(admin.users.viewConversations, admin.users.more)
  await waitForRows()
  await page.click(`[role="dialog"] button[aria-label="${admin.users.deleteConversation}"]`)
  await page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length === 2)
  await clickDialogCommand(common.actions.cancel)
  await page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length === 1)
  assert.equal(deletions.length, 0)
  await page.click(`[role="dialog"] button[aria-label="${admin.users.deleteConversation}"]`)
  await page.waitForFunction(() => document.querySelectorAll('[role="dialog"]').length === 2)
  await clickDialogCommand(common.actions.delete)
  await page.waitForFunction((text) => document.querySelectorAll('[role="dialog"]').length === 1 && document.querySelector('[role="dialog"]').textContent.includes(text), {}, admin.users.noConversations)
  assert.deepEqual(deletions, ['conv-u1'])
  await close()
  deletedConversations = new Set()

  deferNext = 'memories'
  await open(admin.users.viewMemories, admin.users.more)
  await page.waitForFunction(() => !document.querySelector('[role="dialog"] tbody'))
  assert.ok(deferredRequest, 'Expected pending user-memory request')
  await close()
  await page.click(`main button[aria-label="${common.pagination.next}"]`)
  await page.waitForFunction(() => document.querySelector('main tbody')?.textContent.includes('person21@example.com'))
  await open(admin.users.viewMemories, admin.users.more)
  await waitForRows()
  await deferredRequest()
  deferredRequest = null
  await page.waitForFunction(() => document.querySelector('[role="dialog"] tbody')?.textContent.includes('Memory belonging to u21'))
  assert.ok(!(await page.$eval('[role="dialog"]', (element) => element.textContent)).includes('Memory belonging to u1'))
  await close()

  await open(admin.users.viewConversations, admin.users.more)
  await waitForRows()
  const href = await page.$eval('[role="dialog"] tbody a', (element) => element.getAttribute('href'))
  assert.equal(href, '/admin/users/u21/conversations/conv-u21')
  await page.click('[role="dialog"] tbody a')
  await page.waitForFunction(() => !document.querySelector('[role="dialog"]') && document.querySelector('main h1')?.textContent === 'Conversation u21')
  assert.ok(requests.some((request) => request.path === '/admin/conversations/conv-u21/messages' && request.query.mode === 'tree'), 'Conversation details did not request the full context tree')

  for (const view of ['conversations', 'logins', 'memories']) {
    await page.goto(`${base}tests/browser/admin-tables-harness.html?view=${view}&theme=light&lang=en`, { waitUntil: 'networkidle0' })
    await page.waitForSelector('main table', { visible: true })
    assert.ok(await page.$('main h1'), `${view}: legacy standalone page lost its header`)
    assert.equal(await page.$('[role="dialog"]'), null)
  }
  assert.deepEqual(errors, [])
  console.log(`User drawer browser checks passed; screenshots: ${output}`)
} finally {
  if (deferredRequest) await deferredRequest().catch(() => {})
  await browser?.close()
  await server.close()
}
