import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const chrome = process.env.CHROME_PATH ?? [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
].find(existsSync)
if (!chrome) throw new Error('Set CHROME_PATH to a Chrome or Chromium executable')

const output = '/tmp/aivory-admin-model-settings'
await mkdir(output, { recursive: true })
const channels = [
  { id: 'c1', name: 'Production OpenAI', type: 'openai', api_format: 'responses', enabled: true, headers: {}, has_api_key: true },
  { id: 'c2', name: 'Decision provider', type: 'typesafe', enabled: true, headers: {}, has_api_key: true },
  { id: 'c3', name: 'Secondary OpenAI', type: 'openai', api_format: 'responses', enabled: true, headers: {}, has_api_key: true },
  { id: 'c4', name: 'Backup OpenAI', type: 'openai', api_format: 'responses', enabled: true, headers: {}, has_api_key: true },
]
const models = ['chat', 'image', 'embedding', 'decision'].map((kind) => ({
  id: `m-${kind}`, channel_id: kind === 'decision' ? 'c2' : 'c1', label: `Test ${kind}`,
  kind, request_id: `test-${kind}`, description: 'Model description', icon: '', enabled: true,
  param_controls: [], extra_params: {}, official_tools: [], tags: [], skills: [],
  tool_mode: 'native', stream: true, vision: true, fallback_ttft_sec: 0,
  auto_disable_errors: 0, auto_disable_timeouts: 0, auto_disable_minutes: 0,
}))
const savedBindings = new Map()
let legacyBindingsModel = ''
const bindings = (id) => savedBindings.get(id) ?? ({
  regular: [{ id: `b-${id}`, model_id: id, channel_id: models.find((model) => model.id === id).channel_id,
    priority: 1, weight: 100, role: 'regular', channel_enabled: true, disabled_until: 0 }],
  fallback: [],
})
const channelModels = new Map(channels.map((channel) => [channel.id, [{
  id: `cm-${channel.id}`, channel_id: channel.id, request_id: 'test-chat', label: 'Test chat',
  description: '', kind: 'chat', enabled: true, source: 'manual', updated_at: 0,
}]]))
const fixtures = {
  '/admin/channels': channels,
  '/admin/channels/health': {},
  '/admin/models': models,
  '/admin/model-tags': [{ id: 't1', name: 'Reasoning', sort_order: 0 }],
  '/admin/skills': [{ id: 's1', name: 'Research', description: 'Research workflow', enabled: true }],
  '/admin/tools/builtins': [{ name: 'aivory_web_search', label: 'Web search', enabled: true }],
  '/admin/mcp': [],
  '/admin/settings': {},
  '/admin/user-groups': [{ id: 'g1', name: 'Team' }],
}
const errors = []
const mutations = []
const quotaFetches = []
const server = await createServer({ server: { port: 5199, strictPort: false }, logLevel: 'error' })
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
    let body = fixtures[path]
    const modelRoute = path.match(/^\/admin\/models\/(m-\w+)(?:\/(channels|quotas|skills))?$/)
    if (modelRoute) {
      const [, id, resource] = modelRoute
      body = resource === 'channels' ? bindings(id) : resource === 'quotas' ? [] : { ok: true }
      if (resource === 'channels' && request.method() === 'GET' && id === legacyBindingsModel) body = { regular: null, fallback: null }
      if (resource === 'channels' && request.method() === 'PUT') {
        const value = JSON.parse(request.postData())
        body = Object.fromEntries(['regular', 'fallback'].map((role) => [role, value[role].map((binding, index) => ({
          ...binding, id: `b-${id}-${role}-${index}`, model_id: id, role,
          channel_enabled: channels.find((channel) => channel.id === binding.channel_id).enabled,
          disabled_until: 0, consecutive_errors: 0, consecutive_timeouts: 0, updated_at: 0,
        }))]))
        savedBindings.set(id, body)
      }
      if (resource === 'quotas' && request.method() === 'GET') quotaFetches.push(id)
      if (request.method() === 'PATCH' && !resource) {
        const model = models.find((item) => item.id === id)
        Object.assign(model, JSON.parse(request.postData()))
        body = model
      }
    }
    if (path === '/admin/channels/capabilities') {
      const requestID = url.searchParams.get('request_id')
      body = channels.filter((channel) => requestID === 'test-decision' ? channel.id === 'c2'
        : requestID === 'changed-chat' ? ['c3', 'c4'].includes(channel.id) : channel.type === 'openai')
    }
    const channelRoute = path.match(/^\/admin\/channels\/(c[\w-]+)\/(models|health)$/)
    if (channelRoute) {
      const [, id, resource] = channelRoute
      if (resource === 'models' && request.method() === 'PUT') channelModels.set(id, JSON.parse(request.postData()))
      body = resource === 'models' ? channelModels.get(id) ?? [] : { channel: channels.find((channel) => channel.id === id), models: [] }
    }
    if (path === '/admin/channels' && request.method() === 'POST') {
      body = { ...JSON.parse(request.postData()), id: 'c-created', has_api_key: true }
    }
    if (/^\/admin\/channels\/c\w+$/.test(path) && request.method() === 'PATCH') {
      body = { ...channels.find((channel) => channel.id === path.split('/').at(-1)), ...JSON.parse(request.postData()) }
    }
    if (request.method() !== 'GET') mutations.push({ path, method: request.method(), body: JSON.parse(request.postData() || '{}') })
    if (body === undefined) {
      errors.push(`Missing fixture: ${request.method()} ${path}`)
      body = []
    }
    await request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
  })

  const visible = (selector) => page.$eval(selector, (element) => element.checkVisibility())
  const tabCount = () => page.$$eval('[role="tabpanel"]', (panels) => panels.filter((panel) => panel.checkVisibility()).length)
  const selectTab = async (label) => {
    const tab = await page.evaluateHandle((text) => [...document.querySelectorAll('[role="tab"]')].find((item) => item.textContent.trim() === text), label)
    assert.ok(tab.asElement(), `Tab not found: ${label}`)
    await tab.asElement().evaluate((element) => element.scrollIntoView({ behavior: 'instant', block: 'center', inline: 'center' }))
    await tab.asElement().click()
    await tab.dispose()
    await page.waitForFunction((text) => document.querySelector('[role="tab"][aria-selected="true"]')?.textContent.trim() === text, { timeout: 5000 }, label).catch(async (error) => {
      await page.screenshot({ path: `${output}/failed-tab.png`, fullPage: true })
      const facts = await page.evaluate((text) => {
        const target = [...document.querySelectorAll('[role="tab"]')].find((item) => item.textContent.trim() === text)
        const rect = target.getBoundingClientRect()
        return { active: document.querySelector('[role="tab"][aria-selected="true"]')?.textContent, open: document.querySelector('details').open, rect: rect.toJSON(), hit: document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)?.outerHTML.slice(0, 250) }
      }, label)
      throw new Error(`${label}: ${error.message}; ${JSON.stringify(facts)}`)
    })
    assert.equal(await tabCount(), 1)
  }
  const fill = async (selector, value) => {
    await page.$eval(selector, (element) => {
      element.scrollIntoView({ behavior: 'instant', block: 'center' })
      element.focus()
      element.select()
    })
    await page.keyboard.type(value)
  }
  const addBinding = async (role, label) => {
    await page.evaluate((text, group) => {
      const section = document.querySelector('#binding-regular-0')?.closest('section')
        ?? [...document.querySelectorAll('section')].find((element) => element.querySelector('button')?.textContent.trim() === text)
      const buttons = [...section.querySelectorAll('button')].filter((button) => button.textContent.trim() === text)
      buttons[group === 'regular' ? 0 : 1].click()
    }, label, role)
    await page.waitForSelector(`#binding-${role}-0`, { visible: true })
  }
  const assertHint = async (selector, text) => {
    await page.$eval(selector, (element) => { element.scrollIntoView({ block: 'center' }); element.focus() })
    await page.waitForFunction((expected) => [...document.querySelectorAll('[data-radix-popper-content-wrapper]')].some((element) => element.textContent.includes(expected)), {}, text)
    const rect = await page.evaluate(() => document.querySelector('[data-radix-popper-content-wrapper]').getBoundingClientRect().toJSON())
    assert.ok(rect.left >= 0 && rect.right <= page.viewport().width, 'Hint escaped the viewport')
    await page.keyboard.press('Escape')
    await page.$eval(selector, (element) => element.blur())
    await page.click(selector)
    await page.waitForFunction((expected) => [...document.querySelectorAll('[data-radix-popper-content-wrapper]')].some((element) => element.textContent.includes(expected)), {}, text)
    assert.ok((await page.evaluate(() => document.body.innerText)).includes(text), 'Clicking the hint did not open its content')
    await page.keyboard.press('Escape')
    await page.$eval(selector, (element) => element.blur())
  }

  const removeLabel = JSON.parse(readFileSync('src/i18n/locales/zh/admin.json', 'utf8')).models.channels.remove
  const baseBinding = bindings('m-chat').regular[0]
  for (const width of [1440, 390]) {
    await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
    for (const variant of ['single', 'disabled', 'multiple']) {
      const disabled = variant !== 'single'
      const makeBinding = (role, channelID, index) => ({
        ...baseBinding, id: `align-${role}-${index}`, role, channel_id: channelID,
        channel_enabled: !disabled, channel_auto_disabled_until: disabled ? Math.floor(Date.now() / 1000) + 300 : 0,
        disabled_until: disabled ? Math.floor(Date.now() / 1000) + 300 : 0,
      })
      savedBindings.set('m-chat', {
        regular: (variant === 'multiple' ? ['c1', 'c3'] : ['c1']).map((id, index) => makeBinding('regular', id, index)),
        fallback: (variant === 'multiple' ? ['c3', 'c4'] : ['c4']).map((id, index) => makeBinding('fallback', id, index)),
      })
      await page.goto(`${base}tests/browser/admin-tables-harness.html?view=model-edit&model=m-chat&theme=${width === 390 ? 'dark' : 'light'}&lang=zh`, { waitUntil: 'networkidle0' })
      await page.waitForSelector('#binding-fallback-0', { visible: true })
      const alignment = await page.$$eval('[id^="binding-"]', (selectors, label) => selectors.map((selector) => {
        const row = selector.closest('.grid')
        const remove = [...row.querySelectorAll('button')].find((button) => button.getAttribute('aria-label') === label)
        const center = (element) => { const rect = element.getBoundingClientRect(); return rect.top + rect.height / 2 }
        const status = row.querySelector('.col-span-full')
        return {
          id: selector.id, offset: Math.abs(center(selector) - center(remove)),
          numericOffsets: [...row.querySelectorAll('input')].map((input) => Math.abs(center(selector) - center(input))),
          statusBelow: !status || status.getBoundingClientRect().top >= selector.getBoundingClientRect().bottom,
        }
      }), removeLabel)
      for (const row of alignment) {
        assert.ok(row.offset <= 1, `${width}/${variant}/${row.id}: remove button is offset by ${row.offset}px`)
        assert.ok(row.statusBelow, `${width}/${variant}/${row.id}: status overlaps channel controls`)
        if (width >= 640) assert.ok(row.numericOffsets.every((offset) => offset <= 1), `${row.id}: priority/weight controls are misaligned`)
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0, `${width}/${variant}: page overflow`)
      const section = await page.evaluateHandle(() => document.querySelector('#binding-regular-0').closest('section'))
      await section.asElement().screenshot({ path: `${output}/binding-alignment-${variant}-${width}.png` })
      await section.dispose()
    }
  }
  savedBindings.delete('m-chat')
  console.log('Channel remove-button alignment passed: regular/fallback, single/multiple/disabled, desktop/mobile')

  for (const locale of process.env.ADMIN_SETTINGS_LOCALES?.split(',').filter(Boolean) ?? ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
    const modelLocale = JSON.parse(readFileSync(`src/i18n/locales/${locale}/admin.json`, 'utf8')).models
    const labels = modelLocale.advancedTabs
    for (const [width, theme] of [[1440, 'light'], [390, 'dark']]) {
      await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
      await page.goto(`${base}tests/browser/admin-tables-harness.html?view=model-edit&model=m-chat&theme=${theme}&lang=${locale}`, { waitUntil: 'networkidle0' })
      await page.waitForSelector('#m-icon', { visible: true })
      assert.equal(await visible('#m-desc'), true)
      assert.equal(await visible('#m-ttft'), false)
      assert.equal(await tabCount(), 0)
      assert.equal(await page.$('#m-ch'), null, 'The duplicate basic channel selector is still present')
      await addBinding('regular', modelLocale.channels.add)
      await page.waitForSelector('#priority-regular-0', { visible: true })
      await addBinding('fallback', modelLocale.channels.add)
      await addBinding('fallback', modelLocale.channels.add)
      await page.waitForSelector('#priority-fallback-0', { visible: true })
      assert.equal(await page.$('#weight-fallback-0'), null)
      assert.ok(!(await page.evaluate(() => document.body.innerText)).includes(modelLocale.channels.priorityHint), 'Routing guidance is displayed as a permanent paragraph')
      await assertHint(`button[aria-label="${modelLocale.channels.priorityHintLabel}"]`, modelLocale.channels.priorityHint)
      await assertHint(`button[aria-label="${modelLocale.channels.cacheHintLabel}"]`, modelLocale.channels.cacheHint)
      await page.click('details > summary')
      await page.waitForSelector('[role="tab"]', { visible: true })
      assert.equal(await tabCount(), 1)
      assert.equal(await visible('#m-ttft'), true)
      assert.equal(await visible('#m-extra-params'), false)
      assert.equal(await visible('#m-pi'), false)
      const actual = await page.$$eval('[role="tab"]', (tabs) => tabs.map((tab) => tab.textContent.trim()))
      assert.deepEqual(actual, ['policy', 'tools', 'generation', 'tags', 'permissions', 'pricing'].map((key) => labels[key]))
      for (const label of actual) {
        await selectTab(label)
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0, `${locale}/${width}: page overflow`)
      }
      await selectTab(labels.policy)
      if (locale === 'zh') {
        await page.evaluate(() => window.scrollTo(0, 0))
        await page.screenshot({ path: `${output}/model-${width}-${theme}.png`, fullPage: true })
      }
    }
    console.log(`Model settings layouts passed: ${locale}`)
  }

  await page.setViewport({ width: 1440, height: 960 })
  legacyBindingsModel = 'm-embedding'
  await page.goto(`${base}tests/browser/admin-tables-harness.html?view=model-edit&model=m-embedding&theme=light&lang=zh`, { waitUntil: 'networkidle0' })
  assert.ok((await page.$eval('#binding-regular-0', (element) => element.textContent)).includes('Production OpenAI'), 'A legacy model lost its existing channel')
  legacyBindingsModel = ''
  const originalChat = structuredClone(models[0])
  await page.goto(`${base}tests/browser/admin-tables-harness.html?view=model-edit&model=m-chat&theme=light&lang=zh`, { waitUntil: 'networkidle0' })
  await fill('#m-req', 'changed-chat')
  assert.equal(await page.$('#binding-regular-0'), null, 'Changing request_id kept stale bindings')
  await page.waitForFunction(() => [...document.querySelectorAll('button')].some((button) => button.textContent.trim() === '添加渠道' && !button.disabled))
  await addBinding('regular', '添加渠道')
  assert.ok((await page.$eval('#binding-regular-0', (element) => element.textContent)).includes('Secondary OpenAI'))
  await addBinding('regular', '添加渠道')
  await fill('#priority-regular-0', '2')
  await fill('#weight-regular-0', '30')
  await fill('#weight-regular-1', '70')
  const beforeRoutingSave = mutations.length
  await page.evaluate(() => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '保存').click())
  await page.waitForFunction(() => ![...document.querySelectorAll('button')].some((button) => button.getAttribute('aria-busy') === 'true'))
  const routingChanges = mutations.slice(beforeRoutingSave)
  assert.equal(routingChanges.find((item) => item.method === 'PATCH').body.channel_id, 'c3', 'The legacy field did not follow the selected regular channel')
  assert.deepEqual(routingChanges.find((item) => item.method === 'PUT').body.regular, [
    { channel_id: 'c3', priority: 2, weight: 30 }, { channel_id: 'c4', priority: 1, weight: 70 },
  ])
  await fill('#m-req', 'test-decision')
  await page.waitForFunction(() => [...document.querySelectorAll('button')].some((button) => button.textContent.trim() === '添加渠道' && !button.disabled))
  await addBinding('regular', '添加渠道')
  assert.ok((await page.$eval('#binding-regular-0', (element) => element.textContent)).includes('Decision provider'))
  assert.ok((await page.$eval('#m-kind', (element) => element.textContent)).includes('决策'), 'The model kind did not follow the TypeSafe channel')
  models[0] = originalChat
  savedBindings.delete('m-chat')

  await page.setViewport({ width: 1440, height: 960 })
  await page.goto(`${base}tests/browser/admin-tables-harness.html?view=model-edit&model=m-chat&theme=light&lang=zh`, { waitUntil: 'networkidle0' })
  await fill('#m-desc', 'Updated model description')
  await fill('#m-icon', 'M')
  await page.click('details > summary')
  const quotasBefore = quotaFetches.length
  await selectTab('权限与配额')
  await page.click('button[role="switch"][aria-label="Team"]')
  await fill('input[aria-label="Team: 重置周期"]', '9')
  await selectTab('生成设置')
  await fill('#m-extra-params', '{"temperature":0.3}')
  const rawLabel = JSON.parse(readFileSync('src/i18n/locales/zh/admin.json', 'utf8')).models.pc.showRaw
  await page.evaluate((text) => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === text).click(), rawLabel)
  const rawSelector = '[role="tabpanel"][data-state="active"] textarea:not([id])'
  await fill(rawSelector, '[invalid draft')
  await selectTab('计费')
  await fill('#m-pi', '2.25')
  await selectTab('生成设置')
  assert.equal(await page.$eval('#m-extra-params', (input) => input.value), '{"temperature":0.3}')
  assert.equal(await visible(rawSelector), true, 'The raw parameter editor lost its local open state')
  assert.equal(await page.$eval(rawSelector, (input) => input.value), '[invalid draft')
  await fill(rawSelector, '[]')
  await selectTab('权限与配额')
  assert.equal(await page.$eval('input[aria-label="Team: 重置周期"]', (input) => input.value), '9')
  assert.equal(quotaFetches.length, quotasBefore, 'Tab switching refetched and reset quotas')
  await selectTab('生成设置')
  await fill('#m-extra-params', '{bad json')
  await selectTab('计费')
  await page.click('details > summary')
  const before = mutations.length
  await page.evaluate(() => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '保存').click())
  await page.waitForFunction(() => document.querySelector('details').open && document.querySelector('[role="tab"][aria-selected="true"]')?.textContent.trim() === '生成设置', { timeout: 5000 }).catch(async (error) => {
    const facts = await page.evaluate(() => ({ open: document.querySelector('details').open, active: document.querySelector('[role="tab"][aria-selected="true"]')?.textContent, label: document.querySelector('#m-label').value, request: document.querySelector('#m-req').value, extra: document.querySelector('#m-extra-params').value, text: document.body.innerText.slice(-1200) }))
    throw new Error(`Error reveal: ${error.message}; ${JSON.stringify(facts)}; mutations: ${JSON.stringify(mutations)}`)
  })
  assert.equal(await visible('#m-extra-params'), true)
  assert.equal(mutations.length, before, 'Invalid configuration was submitted')
  await fill('#m-extra-params', '{"temperature":0.3}')
  const beforeValidSave = mutations.length
  await page.evaluate(() => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '保存').click())
  await page.waitForFunction(() => ![...document.querySelectorAll('button')].some((button) => button.getAttribute('aria-busy') === 'true'))
  const payload = mutations.slice(beforeValidSave).find((item) => item.method === 'PATCH' && item.path === '/admin/models/m-chat')?.body
  assert.ok(payload, 'Model edits were not saved')
  assert.deepEqual(payload.extra_params, { temperature: 0.3 })
  assert.equal(payload.price_input, 2.25)
  assert.equal(payload.description, 'Updated model description')
  assert.equal(payload.icon, 'M')

  await selectTab('工具与技能')
  const toolLabel = JSON.parse(readFileSync('src/i18n/locales/zh/admin.json', 'utf8')).models.fields.addOfficialTool
  await page.evaluate((text) => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === text).click(), toolLabel)
  await selectTab('计费')
  await page.click('details > summary')
  const beforeToolError = mutations.length
  await page.evaluate(() => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === '保存').click())
  await page.waitForFunction(() => document.querySelector('details').open && document.querySelector('[role="tab"][aria-selected="true"]')?.textContent.trim() === '工具与技能')
  assert.equal(mutations.length, beforeToolError)

  for (const [kind, expected] of [
    ['image', ['policy', 'generation', 'tags', 'permissions', 'pricing']],
    ['embedding', ['policy', 'generation', 'tags', 'pricing']],
    ['decision', ['policy', 'tags', 'pricing']],
  ]) {
    const labels = JSON.parse(readFileSync('src/i18n/locales/zh/admin.json', 'utf8')).models.advancedTabs
    await page.goto(`${base}tests/browser/admin-tables-harness.html?view=model-edit&model=m-${kind}&theme=light`, { waitUntil: 'networkidle0' })
    await page.click('details > summary')
    assert.deepEqual(await page.$$eval('[role="tab"]', (tabs) => tabs.map((tab) => tab.textContent.trim())), expected.map((key) => labels[key]))
    if (kind === 'image') assert.equal(await visible('#m-imgto'), true)
    if (kind === 'embedding') {
      await selectTab(labels.generation)
      assert.equal(await visible('#m-dim'), true)
    }
  }
  await page.focus('details > summary')
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => !document.querySelector('details').open)
  assert.equal(await tabCount(), 0)
  await page.keyboard.press('Space')
  await page.waitForFunction(() => document.querySelector('details').open)
  assert.equal(await tabCount(), 1)

  for (const locale of process.env.ADMIN_SETTINGS_LOCALES?.split(',').filter(Boolean) ?? ['zh', 'zh-Hant', 'en', 'ja', 'fr']) {
  const channelLocale = JSON.parse(readFileSync(`src/i18n/locales/${locale}/admin.json`, 'utf8')).channels
  for (const [width, theme] of [[1440, 'light'], [390, 'dark']]) {
    await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
    await page.goto(`${base}tests/browser/admin-tables-harness.html?view=channels&theme=${theme}&lang=${locale}`, { waitUntil: 'networkidle0' })
    await page.evaluate((text) => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === text).click(), channelLocale.new)
    await page.waitForSelector('#ch-name', { visible: true })
    assert.equal(await visible('#ch-headers'), false)
    await page.click('details > summary')
    assert.equal(await visible('#ch-headers'), true)
    assert.equal(await visible('#ch-disable-errors'), false)
    await fill('#ch-headers', '{"A":"a"}')
    await selectTab(channelLocale.advancedTabs.reliability)
    assert.equal(await visible('#ch-headers'), false)
    await fill('#ch-disable-errors', '3')
    await selectTab(channelLocale.advancedTabs.headers)
    assert.equal(await page.$eval('#ch-headers', (input) => input.value), '{"A":"a"}')
    await fill('#ch-headers', '{invalid json')
    await selectTab(channelLocale.advancedTabs.reliability)
    assert.equal(await page.$eval('#ch-disable-errors', (input) => input.value), '3')
    await page.click('details > summary')
    const beforeChannelError = mutations.length
    await page.evaluate((text) => [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === text).click(), channelLocale.modelAdd.createChannel)
    await page.waitForFunction((text) => document.querySelector('details').open && document.querySelector('[role="tab"][aria-selected="true"]')?.textContent.trim() === text, {}, channelLocale.advancedTabs.headers)
    assert.equal(await page.$eval('#ch-headers', (input) => input.getAttribute('aria-invalid')), 'true')
    assert.equal(mutations.length, beforeChannelError)
    await fill('#ch-headers', '{"A":"a"}')
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0)
    await page.click('details > summary')
    await page.$eval('[role="dialog"] .overflow-y-auto', (element) => { element.scrollTop = 0 })
    const newDimensions = await page.$eval('[role="dialog"]', (element) => ({ width: element.offsetWidth, height: element.offsetHeight }))
    const newFormClasses = await page.$eval('[role="dialog"] form', (element) => element.className)
    if (locale === 'zh') await page.screenshot({ path: `${output}/channel-new-${width}-${theme}.png`, fullPage: true })
    await fill('#ch-name', 'New channel')
    await fill('[role="dialog"] form input', 'manual-model')
    await page.evaluate(() => document.querySelector('[role="dialog"] form').requestSubmit())
    assert.ok((await page.$eval('[role="dialog"]', (element) => element.innerText)).includes('manual-model'))
    const beforeNewChannel = mutations.length
    await page.evaluate(() => document.querySelector('[role="dialog"] > :last-child button:last-child').click())
    await page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
    const createdChanges = mutations.slice(beforeNewChannel)
    assert.equal(createdChanges.find((item) => item.method === 'POST').body.name, 'New channel')
    assert.deepEqual(createdChanges.find((item) => item.method === 'POST').body.headers, { A: 'a' })
    assert.equal(createdChanges.find((item) => item.method === 'PUT').body[0].request_id, 'manual-model')
    channelModels.set('c1', [{ id: 'cm-c1', channel_id: 'c1', request_id: 'test-chat', label: 'Test chat', description: '', kind: 'chat', enabled: true, source: 'manual', updated_at: 0 }])
    await page.evaluate(() => [...document.querySelectorAll('button')].find((button) => button.getAttribute('aria-label')?.endsWith(': Production OpenAI')).click())
    await page.waitForSelector('#ch-name', { visible: true })
    await page.waitForFunction(() => document.querySelector('[role="dialog"]').innerText.includes('Test chat'))
    assert.equal(await visible('#ch-headers'), false)
    const editDimensions = await page.$eval('[role="dialog"]', (element) => ({ width: element.offsetWidth, height: element.offsetHeight }))
    assert.deepEqual(editDimensions, newDimensions, `${locale}/${width}: channel editor dimensions differ`)
    assert.equal(await page.$eval('[role="dialog"] form', (element) => element.className), newFormClasses)
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0)
    if (locale === 'zh') await page.screenshot({ path: `${output}/channel-edit-${width}-${theme}.png`, fullPage: true })
    await page.evaluate((label) => [...document.querySelectorAll('[role="dialog"] button')].find((button) => button.getAttribute('aria-label') === label).click(), channelLocale.modelAdd.removeModel.replace('{{name}}', 'Test chat'))
    await fill('[role="dialog"] form input', 'replacement-model')
    await page.evaluate(() => document.querySelector('[role="dialog"] form').requestSubmit())
    const beforeEditChannel = mutations.length
    await page.evaluate(() => document.querySelector('[role="dialog"] > :last-child button:last-child').click())
    await page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
    const editedChanges = mutations.slice(beforeEditChannel)
    assert.equal(editedChanges.find((item) => item.method === 'PATCH').body.api_key, '', 'An edit should preserve the original API key')
    assert.deepEqual(editedChanges.find((item) => item.method === 'PUT').body.map((model) => model.request_id), ['replacement-model'])
  }
  console.log(`Channel creation and editing checks passed: ${locale}`)
  }
  assert.deepEqual(errors, [])
  console.log(`Admin model settings browser checks passed; screenshots: ${output}`)
} finally {
  await browser?.close()
  await server.close()
}
