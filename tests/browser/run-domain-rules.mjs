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
const output = process.env.AIVORY_DOMAIN_SCREENSHOTS || path.join(tmpdir(), 'aivory-domain-rules')
await mkdir(output, { recursive: true })
const cacheDir = await mkdtemp(path.join(tmpdir(), 'aivory-domain-cache-'))
const server = await createServer({ cacheDir, server: { host: '127.0.0.1', port: 5205, strictPort: true }, logLevel: 'error' })
const workspace = { id: 'ws1', name: '测试团队', icon_url: '/workspace-logo.png', description: '这里不应出现工作空间介绍' }
const otherWorkspace = { id: 'ws2', name: '另一个团队' }
const existingRule = {
  domain: 'existing.example', domains: ['existing.example'], workspace_id: workspace.id, workspace_name: workspace.name,
  icon_url: workspace.icon_url, description: workspace.description, lock_personal: true,
  email_verification_required: true, initial_group_id: '', initial_group_name: '', enabled: true,
  subscription_purchase_disabled: false, member_count: 3,
}
let spaces = []
let domains = []
const writes = []
const errors = []
let rejectWorkspaceChange = false
let browser
try {
  await server.listen()
  browser = await puppeteer.launch({ executablePath, headless: true })
  const page = await browser.newPage()
  page.on('pageerror', (error) => errors.push(String(error)))
  await page.setRequestInterception(true)
  page.on('request', async (request) => {
    const route = new URL(request.url()).pathname
    if (!route.startsWith('/api/')) return request.continue()
    if (route.startsWith('/api/admin/domains') && ['POST', 'PATCH'].includes(request.method())) {
      writes.push({ method: request.method(), body: JSON.parse(request.postData()) })
      if (rejectWorkspaceChange) {
        rejectWorkspaceChange = false
        return request.respond({ status: 409, contentType: 'application/json', body: '{"error":"domain_workspace_has_members"}' })
      }
      return request.respond({ status: 200, contentType: 'application/json', body: '{"ok":true}' })
    }
    const fixtures = {
      '/api/admin/domains': { domains }, '/api/admin/workspaces': { workspaces: spaces },
      '/api/admin/user-groups': [], '/api/workspaces': { workspaces: [] }, '/api/me/domain-data': {},
    }
    if (route === '/api/events') return request.respond({ status: 200, contentType: 'text/event-stream', body: '' })
    return request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(fixtures[route] ?? {}) })
  })
  async function clickButton(text) {
    const handle = await page.evaluateHandle((text) => Array.from(document.querySelectorAll('button')).find((button) => button.textContent.trim() === text), text)
    assert.ok(handle.asElement(), `Missing button: ${text}`)
    await handle.asElement().click()
    await handle.dispose()
  }
  async function open(theme = 'light') {
    await page.goto(`http://127.0.0.1:5205/tests/browser/admin-tables-harness.html?view=domains&theme=${theme}&lang=zh`, { waitUntil: 'networkidle0' })
    await clickButton('添加域规则')
    await page.waitForSelector('#domain-names')
    await page.waitForFunction(() => document.querySelector('[role="dialog"]')?.getAnimations().every((animation) => animation.playState !== 'running'))
  }
  async function edit(theme = 'light') {
    await page.goto(`http://127.0.0.1:5205/tests/browser/admin-tables-harness.html?view=domains&theme=${theme}&lang=zh`, { waitUntil: 'networkidle0' })
    await page.click('button[aria-label="编辑规则"]')
    await page.waitForSelector('#domain-names')
    await page.waitForFunction(() => document.querySelector('[role="dialog"]')?.getAnimations().every((animation) => animation.playState !== 'running'))
  }
  async function choose(name) {
    await page.click('[aria-labelledby="domain-workspace-label"]')
    await page.waitForSelector('[role="option"]')
    const handle = await page.evaluateHandle((name) => Array.from(document.querySelectorAll('[role="option"]')).find((option) => option.textContent.includes(name)), name)
    assert.ok(handle.asElement(), `Missing workspace option: ${name}`)
    await handle.asElement().click()
    await handle.dispose()
    await page.waitForSelector('[role="listbox"]', { hidden: true })
  }
  const isLocked = () => page.$eval('#domain-lock', (button) => button.getAttribute('aria-checked') === 'true')
  const lockDisabled = () => page.$eval('#domain-lock', (button) => button.disabled)
  const screenshot = (name) => page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true })
  async function save() {
    const before = writes.length
    await clickButton('保存')
    await page.waitForFunction(() => !document.querySelector('#domain-names'))
    assert.equal(writes.length, before + 1)
    assert.ok(!('icon_url' in writes.at(-1).body) && !('description' in writes.at(-1).body), 'Domain editor must not edit workspace profiles')
    return writes.at(-1)
  }

  await page.setViewport({ width: 1280, height: 900 })
  await open()
  await page.type('#domain-names', 'personal.example')
  assert.equal(await lockDisabled(), true)
  assert.equal(await isLocked(), false)
  await screenshot('desktop-unassigned')
  assert.deepEqual(Object.fromEntries(Object.entries((await save()).body).filter(([key]) => ['workspace_id', 'lock_personal'].includes(key))), { workspace_id: '', lock_personal: false })

  spaces = [workspace]
  await open()
  await page.type('#domain-names', 'team.example')
  await choose(workspace.name)
  assert.equal(await lockDisabled(), false)
  await page.click('#domain-lock')
  assert.equal(await isLocked(), true)
  assert.equal(await page.$$eval('[role="dialog"] input[type="file"], [role="dialog"] img', (items) => items.length), 0)
  assert.equal(await page.$$eval('[role="dialog"] textarea', (items) => items.length), 1)
  assert.equal(await page.$eval('[role="dialog"]', (dialog) => dialog.textContent.includes('这里不应出现工作空间介绍')), false)
  await screenshot('desktop-selected-workspace')
  await choose('不加入工作空间')
  assert.equal(await lockDisabled(), true)
  assert.equal(await isLocked(), false)
  await choose(workspace.name)
  assert.equal(await isLocked(), false, 'Clearing the workspace must reset the lock')
  await save()

  domains = [existingRule]
  await edit()
  assert.equal(await page.$eval('[aria-labelledby="domain-workspace-label"]', (button) => button.disabled), true)
  assert.match(await page.$eval('#domain-workspace-hint', (hint) => hint.textContent), /已有用户/)
  assert.equal(await isLocked(), true)
  await page.$eval('#domain-names', (input) => { input.select() })
  await page.type('#domain-names', 'changed.example')
  const update = await save()
  assert.equal(update.method, 'PATCH')
  assert.equal(update.body.domain, existingRule.domain)
  assert.deepEqual(update.body.domains, ['changed.example'])
  assert.equal(update.body.workspace_id, workspace.id)
  assert.equal(update.body.lock_personal, true)

  spaces = [workspace, otherWorkspace]
  domains = [{ ...existingRule, member_count: 0, workspace_id: '', workspace_name: '', lock_personal: false }]
  await edit()
  assert.equal(await page.$eval('[aria-labelledby="domain-workspace-label"]', (button) => button.disabled), false)
  assert.equal(await lockDisabled(), true)
  assert.match(await page.$eval('[role="dialog"]', (dialog) => dialog.textContent), /已有用户的用户组保持不变/)
  await choose(workspace.name)
  await page.click('#domain-lock')
  await screenshot('desktop-edit-empty-rule')
  let changed = await save()
  assert.equal(changed.method, 'PATCH')
  assert.equal(changed.body.workspace_id, workspace.id)
  assert.equal(changed.body.lock_personal, true)

  domains = [{ ...existingRule, member_count: 0 }]
  await edit()
  await choose(otherWorkspace.name)
  changed = await save()
  assert.equal(changed.body.workspace_id, otherWorkspace.id)

  await edit()
  await choose('不加入工作空间')
  assert.equal(await lockDisabled(), true)
  assert.equal(await isLocked(), false)
  changed = await save()
  assert.equal(changed.body.workspace_id, '')
  assert.equal(changed.body.lock_personal, false)

  domains = [{ ...existingRule, workspace_id: '', workspace_name: '', lock_personal: false }]
  await edit()
  assert.equal(await page.$eval('[aria-labelledby="domain-workspace-label"]', (button) => button.disabled), true)
  assert.equal(await lockDisabled(), true)
  assert.match(await page.$eval('#domain-workspace-hint', (hint) => hint.textContent), /已有用户/)
  await save()

  domains = [{ ...existingRule, member_count: 0 }]
  await edit()
  await choose(otherWorkspace.name)
  // Another administrator cleared the target before the first user joined.
  // Recover the current server target, rather than the editor's old snapshot.
  domains = [{ ...existingRule, workspace_id: '', workspace_name: '', lock_personal: false }]
  rejectWorkspaceChange = true
  await clickButton('保存')
  await page.waitForSelector('[role="alert"]')
  assert.equal(await page.$eval('[aria-labelledby="domain-workspace-label"]', (button) => button.disabled), true)
  assert.match(await page.$eval('[aria-labelledby="domain-workspace-label"]', (button) => button.textContent), /不加入工作空间/)
  assert.equal(await lockDisabled(), true)
  assert.match(await page.$eval('[role="alert"]', (alert) => alert.textContent), /已有用户/)
  changed = await save()
  assert.equal(changed.body.workspace_id, '')
  assert.equal(changed.body.lock_personal, false)

  domains = []
  spaces = []
  await page.setViewport({ width: 390, height: 844, isMobile: true, hasTouch: true })
  for (const theme of ['light', 'dark']) {
    await open(theme)
    await page.type('#domain-names', `${theme}.example`)
    assert.equal(await lockDisabled(), true)
    await screenshot(`mobile-${theme}`)
    assert.ok(await page.$eval('[role="dialog"]', (dialog) => {
      const box = dialog.getBoundingClientRect()
      return box.left >= -1 && box.right <= window.innerWidth + 1 && dialog.scrollWidth <= box.width + 1
    }), `${theme}: domain editor must fit the mobile viewport`)
    await save()
  }
  assert.deepEqual(errors, [], 'Browser runtime errors')
  console.log(`PASS: 10 domain-rule scenarios; screenshots: ${output}`)
} finally {
  await browser?.close()
  await server.close()
  await rm(cacheDir, { recursive: true, force: true })
}
