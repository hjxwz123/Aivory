import assert from 'node:assert/strict'
import { existsSync } from 'node:fs'
import { mkdir } from 'node:fs/promises'
import { createServer } from 'vite'
import puppeteer from 'puppeteer-core'

const chrome = process.env.CHROME_PATH ?? [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
].find(existsSync)
if (!chrome) throw new Error('Set CHROME_PATH to a Chrome or Chromium executable')
const output = process.env.ADMIN_TABLE_SCREENSHOTS ?? '/tmp/aivory-admin-tables'
await mkdir(output, { recursive: true })

const now = Math.floor(Date.now() / 1000)
const names = ['AIVORY', 'Rachel', 'Listener', 'lrs', 'Hyperborean']
const users = names.map((name, i) => ({ id: `u${i + 1}`, name, email: i === 4 ? 'very.long.email.address.for.layout.verification@example.com' : `${name.toLowerCase()}@example.com`, role: i < 2 ? 'admin' : 'user', status: i === 3 ? 'banned' : 'active', group_id: `g${i % 3 + 1}`, last_seen_at: now - (i ? 86400 * i : 0), created_at: now - 86400 * 30, settings: {} }))
const groups = ['Team', 'Pro', 'Lite'].map((name, i) => ({ id: `g${i + 1}`, name, description: 'Workspace membership with model access and usage limits', features: [], monthly_price_amount_minor: 1900, yearly_price_amount_minor: 19000, settlement_currency: 'USD', is_default: i === 2, sort_order: i, max_projects: 10, max_kbs: 10, credit_allowance: 1000, credit_period_seconds: 2592000, created_at: now, updated_at: now }))
const channels = ['OpenAI Production', 'Anthropic', 'Google Gemini'].map((name, i) => ({ id: `c${i + 1}`, name, type: ['openai', 'claude', 'gemini'][i], api_format: i ? '' : 'responses', base_url: `https://api.${['openai', 'anthropic', 'google'][i]}.example.com/v1/${'long-endpoint-'.repeat(8)}`, has_api_key: true, enabled: i !== 2, sort_order: i, updated_at: now }))
const models = ['GPT-5', 'Claude Sonnet', 'Gemini Pro'].map((label, i) => ({ id: `m${i + 1}`, label, channel_id: `c${i + 1}`, kind: 'chat', request_id: `${label.toLowerCase().replaceAll(' ', '-')}-production`, description: '', icon: '', enabled: true, sort_order: i, tool_mode: 'native', vision: true, stream: true, system_prompt: '', param_controls: [], price_input: 2.5, price_output: 10, price_per_image: 0, dim: 0 }))
const payments = [{ id: 'pc1', name: 'Stripe Production', provider: 'stripe', environment: 'live', enabled: true, sort_order: 0, config: {}, updated_at: now, webhook_url: 'https://example.com/api/payments/webhook/pc1' }]
const projects = [{ id: 'p1', name: 'Research workspace', description: 'Shared research documents and conversations', emoji: '', pinned: true, created_at: now, creator_id: 'u1', creator_name: 'AIVORY', creator_email: 'admin@example.com', conversation_count: 3, document_count: 2, failed_document_count: 0, processing_document_count: 0, last_activity_at: now }]
const kbs = [{ id: 'kb1', name: 'Product documentation', description: 'Documentation and reference material', embedding_model_id: 'm1', embedding_model_label: 'GPT-5', embedding_dim: 1536, created_at: now, creator_id: 'u1', creator_name: 'AIVORY', creator_email: 'admin@example.com', document_count: 3, ready_document_count: 2, failed_document_count: 1, processing_document_count: 0, last_activity_at: now }]
const documents = [{ id: 'd1', filename: 'documentation-with-a-long-filename.pdf', status: 'ready', size_bytes: 40960, chunk_count: 12, mime_type: 'application/pdf', created_at: now }]
const feedback = { id: 'f1', user_id: 'u1', user_name: 'AIVORY', user_email: 'admin@example.com', description: 'A long feedback description that should remain aligned with the table columns and open its details correctly.', conversation_title: 'Research conversation', page_path: '/chat', created_at: now, has_screenshot: false, screenshot_size: 0 }
const modelFeedback = { id: 'mf1', model_id: 'm1', model_label: 'GPT-5', updated_at: now, created_at: now, question: 'Summarize the document', response: 'The document covers the current model configuration.', rating: 'dislike', reasons: ['incorrect'], input_tokens: 120, output_tokens: 300, latency_ms: 1250, user_id: 'u1' }
const totals = { calls: 20, turns: 10, credit_charged_turns: 5, input_tokens: 400, output_tokens: 800, cache_read_tokens: 0, cache_write_tokens: 0, images_count: 0, cost: 0.5, credits: 10, turn_cost: 0.05, credit_charged_cost: 0.25, users: 2, credit_charged_users: 1, conversations: 2, workspaces: 1 }
const breakdowns = Object.fromEntries(['user', 'model', 'workspace', 'purpose', 'channel'].map((dimension) => [dimension, [{ ...totals, key: dimension === 'user' ? 'u1' : dimension === 'model' ? 'm1' : dimension, label: dimension === 'user' ? 'AIVORY' : dimension }]]))
const fixtures = {
  '/admin/audit-logs': { logs: [
    { id: 'adm_aud_42', type: 'users', actor_user_id: 'u1', actor_name: 'AIVORY', actor_role: 'admin', action: 'admin.users.role', target_type: 'user', target_id: 'u2', target_name: 'Rachel', result: 'success', severity: 'info', source: 'admin', request_id: '7f3d95c4-556b-45ac-8a0a-6c67f6d04111', client_ip: '127.0.0.1', user_agent: 'Mozilla/5.0 audit layout fixture', occurred_at_ms: now * 1000, created_at: now, duration_ms: 16, http_status: 200, method: 'POST', route: '/api/admin/users/:id/role', changes: { role: { before: 'user', after: 'admin' }, api_key: { redacted: true } }, metadata: { method: 'POST', route: '/api/admin/users/:id/role', status: 200 } },
    { id: 'adm_aud_43', type: 'authentication', actor_user_id: '', actor_name: '', action: 'auth.login', target_type: 'user', target_id: 'u2', result: 'denied', severity: 'warning', source: 'account', request_id: '7f3d95c4-556b-45ac-8a0a-6c67f6d04222', client_ip: '127.0.0.1', occurred_at_ms: now * 1000, created_at: now, http_status: 401, method: 'POST', route: '/api/auth/login', reason: 'access_denied', metadata: {} },
  ], total: 2, page: 1, page_size: 50 },
  '/admin/users': { users, total: users.length }, '/admin/users/u1': users[0],
  '/admin/analytics': { days: 30, bucket: 86400, generated_at: now, period_start: now - 30 * 86400, period_end: now, previous_period_start: now - 60 * 86400, previous_period_end: now - 30 * 86400, totals, previous_totals: totals, trend: [{ ...totals, bucket_start: now - 86400 }], previous_trend: [], breakdowns, filter_options: breakdowns },
  '/admin/models/m1/quotas': [{ model_id: 'm1', group_id: 'g1', period_seconds: 604800, limit_type: 'count', limit_value: 100 }],
  '/admin/user-groups': groups, '/admin/channels': channels, '/admin/models': models,
  '/admin/user-groups/g1/users': { users, total: users.length },
  '/models': models, '/model-tags': [], '/image-models': [],
  '/admin/settings': { settlement_currency: 'USD' },
  '/admin/prompts': [{ id: 'pr1', name: 'Research assistant', description: 'Analyze documents and cite the relevant sources.', enabled: true, content: 'Be precise.', sort_order: 0 }],
  '/admin/skills': [{ id: 's1', name: 'document_analysis', icon: 'FileText', description: 'Analyze uploaded documents and summarize key findings.', enabled: true, instructions: '', assets: [{ filename: 'analysis-template.txt', storage_path: 'skills/s1/template.txt', size_bytes: 1024 }], sort_order: 0 }],
  '/admin/model-tags': [{ id: 't1', name: 'Reasoning', sort_order: 0 }],
  '/admin/oauth-providers': [{ id: 'oa1', kind: 'google', name: 'Google', enabled: true, client_id: 'demo-client-id', has_secret: true, sort_order: 0 }],
  '/admin/payment-channels': payments,
  '/admin/payment-methods': [{ id: 'pm1', name: 'Credit card', icon: 'CreditCard', provider: 'stripe', channel_id: 'pc1', provider_method_config: {}, enabled: true, sort_order: 0 }],
  '/admin/credit-packages': [{ id: 'cp1', name: 'Extra credits', description: 'Permanent workspace credits', credits: 5000, price_amount_minor: 2500, enabled: true, sort_order: 0 }],
  '/admin/redeem-codes': [{ id: 'rc1', code: 'DEMO-1234-5678', kind: 'group', group_id: 'g1', duration_days: 30, max_uses: 10, used_count: 2, expires_at: 0, enabled: true, note: 'Support allocation', batch_name: 'October', created_by: 'u1', created_at: now }],
  '/admin/mcp': [{ id: 'mc1', name: 'Documentation search', icon: 'Blocks', description: 'Search the documentation and retrieve relevant references.', url: 'https://mcp.example.com/v1/search', enabled: true, discovered_tools: [{ name: 'search' }], last_synced_at: now, headers: {} }],
  '/admin/user-feedback': { items: [feedback], total: 1 },
  '/admin/message-feedback': { items: [modelFeedback], total: 1, summary: { total: 1, likes: 0, dislikes: 1, positive_rate: 0, coverage: 0.1, assistant_messages: 10, reasons: [] }, by_model: [{ model_id: 'm1', model_label: 'GPT-5', total: 1, likes: 0, dislikes: 1, positive_rate: 0, top_reason: 'incorrect' }] },
  '/admin/resources/knowledge-bases': { items: kbs, total: 1 }, '/admin/resources/projects': { items: projects, total: 1 },
  '/admin/resources/knowledge-bases/kb1': { item: { ...kbs[0], total_size_bytes: 40960, chunk_count: 12, embedding_model_enabled: true, shares: [{ user_id: 'u2', name: 'Rachel', email: 'rachel@example.com', role: 'read', created_at: now }] } },
  '/admin/resources/projects/p1': { item: { ...projects[0], kb_id: 'kb1', instructions: 'Research instructions', active_conversation_count: 1, archived_conversation_count: 0 } },
  '/admin/resources/projects/p1/conversations': { items: [{ id: 'conv1', title: 'Document research', creator_id: 'u1', creator_name: 'AIVORY', creator_email: 'admin@example.com', model_id: 'm1', model_label: 'GPT-5', updated_at: now, archived: false }], total: 1 },
  '/admin/users/u1/projects': projects, '/admin/users/u1/kbs': kbs, '/admin/users/u1/images': [],
  '/admin/kbs/kb1/documents': documents,
  '/admin/users/u1/conversations': [{ id: 'conv1', title: 'Document research', model_id: 'm1', updated_at: now, archived: true, starred: true }],
  '/admin/users/u1/memories': [{ id: 'mem1', memory_text: 'Prefers detailed engineering explanations.', slot: 'response_style', value: 'detailed', status: 'ACTIVE', created_at: now, updated_at: now }],
  '/admin/users/u1/login-history': { items: [{ id: 'lh1', login_at: now, ip: '2001:db8::1', location: 'Singapore', method: 'password', user_agent: 'Mozilla/5.0 (Macintosh) Chrome/148.0', device: 'Desktop' }], total: 1 },
  '/admin/html-previews': { items: [{ id: 'html1', user_id: 'u1', user_name: 'AIVORY', user_email: 'admin@example.com', created_at: now }], total: 1 },
  '/admin/workspaces': { workspaces: [{ id: 'w1', name: 'Engineering', owner_id: 'u1', owner_name: 'AIVORY', member_count: 4, created_at: now }] },
  '/admin/workspaces/w1': { workspace: { id: 'w1', name: 'Engineering', owner_id: 'u1', owner_name: 'AIVORY', created_at: now }, members: users.map((user) => ({ user_id: user.id, name: user.name, email: user.email, role: 'member', is_owner: user.id === 'u1' })), conversations: [{ id: 'conv1', title: 'Document research', user_id: 'u1', creator_name: 'AIVORY' }], projects, kbs },
  '/admin/domains': { domains: [{ domain: 'example.com', workspace_id: 'w1', workspace_name: 'Engineering', enabled: true, email_verification_required: true, initial_group_name: 'Team', lock_personal: false, member_count: 4 }] },
  '/admin/domains/example.com/users': { users: [{ user_id: 'u1', name: 'AIVORY', email: 'admin@example.com', lock_override: null, locked: false }] },
  '/admin/domains/example.com/candidates': { users: [{ user_id: 'u2', name: 'Rachel', email: 'rachel@example.com', status: 'active', personal_conversation_count: 3 }] },
  '/admin/payment-orders': { orders: [{ id: 'o1', user_id: 'u1', user_name: 'AIVORY', user_email: 'admin@example.com', provider: 'stripe', environment: 'live', status: 'pending', currency: 'USD', amount_minor: 1900, target_name: 'Team', target_type: 'group', target_id: 'g1', channel_name: 'Stripe Production', channel_id: 'pc1', created_at: now, updated_at: now }], total: 1 },
  '/admin/files': { files: [{ id: 'file1', source: 'file', filename: 'quarterly-research-report.txt', user_id: 'u1', user_email: 'admin@example.com', origin: 'conversation', size_bytes: 1024, mime_type: 'text/plain', created_at: now }], total: 1, limit: 50, offset: 0 },
  '/admin/usage': { records: [{ id: 1, user_id: 'u1', user_name: 'AIVORY', user_email: 'admin@example.com', model_id: 'm1', channel_id: 'c1', channel_name: 'OpenAI', created_at: now, input_tokens: 120, output_tokens: 300, cost: 0.003, credits: 1, status: 'success', purpose: 'chat', latency_ms: 1250 }], total: 1, total_cost: 0.003, page: 1, page_size: 50 },
}

const server = await createServer({ server: { port: 5198, strictPort: true }, logLevel: 'error' })
await server.listen()
const browser = await puppeteer.launch({ executablePath: chrome, headless: true })
const page = await browser.newPage()
await page.setRequestInterception(true)
const failures = []
const mutations = []
page.on('pageerror', (error) => failures.push(String(error)))
page.on('request', async (request) => {
  const url = new URL(request.url())
  if (!url.pathname.startsWith('/api/')) return request.continue()
  const path = url.pathname.slice(4)
  if (path === '/admin/files/content') return request.respond({ status: 200, contentType: 'text/plain', body: 'Quarterly research report\n\nTable preview fixture.' })
  let body = fixtures[path]
  if (path === '/admin/redeem-codes' && request.method() === 'GET') {
    const search = (url.searchParams.get('q') || '').trim().toLowerCase()
    const batch = url.searchParams.get('batch')
    const status = url.searchParams.get('status')
    body = body.filter((row) => (!search || [row.code, row.id, row.batch_name, row.note].some((value) => value.toLowerCase().includes(search)))
      && (!batch || row.batch_name === batch)
      && (!status || status === 'partial'))
  }
  if (request.method() !== 'GET') {
    const mutationBody = JSON.parse(request.postData() || '{}')
    mutations.push({ path, method: request.method(), body: mutationBody })
    body = { ok: true }
    if (request.method() === 'PATCH' && /^\/admin\/channels\/c\d+$/.test(path)) {
      const channel = channels.find((channel) => path.endsWith(`/${channel.id}`))
      Object.assign(channel, mutationBody)
      body = channel
    }
  }
  if (body === undefined) {
    failures.push(`Missing fixture: ${request.method()} ${path}`)
    body = []
  }
  await request.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
})

const views = process.env.ADMIN_TABLE_VIEWS?.split(',') ?? ['users', 'channels', 'models', 'prompts', 'skills', 'oauth', 'tags', 'groups', 'payment-channels', 'payment-methods', 'credits', 'redeem', 'mcp', 'feedback', 'model-feedback', 'resources', 'workspaces', 'domains', 'orders', 'previews', 'logins', 'memories', 'conversations', 'library', 'files', 'usage', 'analytics', 'quota']
try {
  for (const theme of ['dark', 'light']) {
    for (const width of theme === 'dark' ? [1440, 390] : [1440]) {
      await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
      for (const view of views) {
        const errorCount = failures.length
        await page.goto(`http://localhost:5198/tests/browser/admin-tables-harness.html?view=${view}&theme=${theme}`, { waitUntil: 'networkidle0' })
        await page.waitForSelector('table.admin-data-table', { timeout: 15000, visible: true }).catch(async (error) => {
          throw new Error(`${view}: ${error.message}; ${await page.$eval('body', (body) => body.innerText)}; ${failures.slice(errorCount).join('; ')}`)
        })
        const facts = await page.evaluate(() => {
          const table = document.querySelector('table.admin-data-table')
          const headers = [...table.querySelectorAll('thead th')]
          const rows = [...table.querySelectorAll('tbody tr')]
          const scroll = table.closest('.admin-table-scroll')
          const headerCells = headers.map((cell) => cell.getBoundingClientRect())
          const rowCells = [...rows[0].querySelectorAll(':scope > td')].map((cell) => cell.getBoundingClientRect())
          const validTables = [...document.querySelectorAll('table.admin-data-table')].filter((table) => table.getBoundingClientRect().height > 0).every((table) => {
            const cells = [...table.querySelectorAll('thead th')]
            const row = table.querySelector('tbody tr')
            const bodyCells = [...row.querySelectorAll(':scope > td')]
            const empty = bodyCells.length === 1 && bodyCells[0].colSpan === cells.length
            return cells.length > 1 && cells.every((cell) => cell.textContent.trim() && cell.scope === 'col') && (empty || cells.length === bodyCells.length && cells.every((cell, index) => Math.abs(cell.getBoundingClientRect().width - bodyCells[index].getBoundingClientRect().width) < 2))
          })
          return { headers: headers.map((cell) => cell.textContent?.trim()), columns: headers.map((cell) => cell.dataset.column), rowHeight: rows[0].getBoundingClientRect().height, tableWidth: table.getBoundingClientRect().width, bodyOverflow: document.documentElement.scrollWidth - innerWidth, scrollWidth: scroll.scrollWidth, clientWidth: scroll.clientWidth, aligned: headerCells.length === rowCells.length && headerCells.every((cell, i) => Math.abs(cell.width - rowCells[i].width) < 2), validTables, text: document.body.innerText }
        })
        assert.equal(facts.bodyOverflow, 0, `${view}: the whole page overflows`)
        assert.ok(facts.headers.length > 1 && facts.headers.every(Boolean), `${view}: missing table headers`)
        assert.ok(facts.aligned, `${view}: header and body columns are misaligned`)
        if (view === 'audit') {
          assert.ok(facts.columns.includes('result') && facts.columns.includes('id'), 'audit: result and ID missing')
          assert.equal(await page.$eval('tbody', (body) => /api_key|127\.0\.0\.1/.test(body.textContent)), false, 'audit: detail evidence leaked into list')
          await page.click('tbody tr:first-child')
          await page.waitForSelector('[role="dialog"]', { visible: true })
          await page.waitForFunction(() => [...document.querySelectorAll('[role="dialog"]')].every((dialog) => dialog.getAnimations().every((animation) => animation.playState === 'finished')))
          assert.ok(await page.$eval('[role="dialog"]', (dialog) => dialog.textContent.includes('7f3d95c4') && dialog.textContent.includes('敏感值不记录')), 'audit: drawer lacks correlation or redaction')
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0, 'audit drawer overflows')
          await page.screenshot({ path: `${output}/audit-detail-${theme}-${width}.png`, fullPage: true })
          await page.keyboard.press('Escape')
          await page.waitForFunction(() => !document.querySelector('[role="dialog"]'))
          await page.click('[aria-label="更多筛选"]')
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0, 'audit advanced filters overflow')
        }
        if (view === 'usage') {
          assert.equal(facts.headers.length, 14, 'usage: all call log columns should remain available')
          assert.ok(facts.columns.includes('first-byte') && facts.columns.includes('duration'), 'usage: timing columns should remain available')
          if (width >= 800) assert.ok(facts.scrollWidth > facts.clientWidth, 'usage: desktop log columns should scroll within the table frame')
        }
        assert.ok(facts.validTables, `${view}: a secondary table has invalid headers or columns`)
        assert.ok(facts.rowHeight > 0 && facts.rowHeight < 140, `${view}: unexpected row height ${facts.rowHeight}px`)
        assert.equal(failures.length, errorCount, `${view}: ${failures.slice(errorCount).join('; ')}`)
        assert.ok(!/common\.(order|actions|status|details)|\.fields\.name/.test(facts.text), `${view}: missing translations`)
        if (['channels', 'models', 'domains', 'workspaces', 'groups', 'redeem'].includes(view)) {
          const toolbar = await page.$eval('[data-admin-list-toolbar]', (toolbar) => {
            const input = toolbar.querySelector('input').parentElement.getBoundingClientRect()
            const buttons = [...toolbar.querySelectorAll('button')].map((button) => button.getBoundingClientRect())
            return { oneRow: buttons.every((button) => Math.abs(button.top + button.height / 2 - input.top - input.height / 2) < 2), scrollWidth: toolbar.scrollWidth, width: toolbar.clientWidth }
          })
          assert.ok(toolbar.oneRow, `${view}: toolbar commands wrapped to another line`)
          if (width >= 800) assert.ok(toolbar.scrollWidth <= toolbar.width + 1, `${view}: desktop toolbar should fit in one row`)
          if (toolbar.scrollWidth > toolbar.width) {
            assert.ok(await page.$eval('[data-admin-list-toolbar]', (toolbar) => {
              toolbar.scrollLeft = toolbar.scrollWidth
              const last = [...toolbar.querySelectorAll('[data-admin-list-actions] button')].at(-1).getBoundingClientRect()
              const visible = toolbar.scrollLeft > 0 && last.right <= toolbar.getBoundingClientRect().right + 1
              toolbar.scrollLeft = 0
              return visible
            }), `${view}: toolbar actions cannot be reached by horizontal scrolling`)
          }
        }
        if (width === 390 && facts.scrollWidth > facts.clientWidth) {
          const scrollWorks = await page.$eval('.admin-table-scroll', (frame) => {
            frame.scrollLeft = frame.scrollWidth
            const action = frame.querySelector('tbody [data-column="actions"]')
            const works = frame.scrollLeft > 0 && (!action || action.getBoundingClientRect().right <= frame.getBoundingClientRect().right + 1)
            frame.scrollLeft = 0
            return works
          })
          assert.ok(scrollWorks, `${view}: horizontal scrolling or pinned actions failed`)
        }
        if (['users', 'channels', 'models', 'mcp', 'resources', 'usage', 'files', 'domains', 'workspaces', 'groups', 'redeem', 'orders', 'analytics'].includes(view)) await page.screenshot({ path: `${output}/${view}-${theme}-${width}.png`, fullPage: true })
        console.log(`ok ${view} ${theme} ${width}px, row ${Math.round(facts.rowHeight)}px`)
      }
    }
  }

  await page.setViewport({ width: 1440, height: 960 })
  for (const [view, query, expected] of [
    ['channels', 'ANTHROPIC', 'Anthropic'], ['models', 'Claude Sonnet', 'Claude Sonnet'],
    ['domains', 'example.com', 'example.com'], ['workspaces', 'AIVORY', 'Engineering'],
    ['groups', 'Lite', 'Lite'], ['redeem', 'support', 'DEMO-1234-5678'],
  ]) {
    await page.goto(`http://localhost:5198/tests/browser/admin-tables-harness.html?view=${view}`, { waitUntil: 'networkidle0' })
    await page.type('[data-admin-list-toolbar] input', query)
    await page.waitForFunction((expected) => {
      const rows = [...document.querySelectorAll('tbody tr')].filter((row) => !row.querySelector('td[colspan]'))
      return rows.length === 1 && rows[0].textContent.includes(expected)
    }, {}, expected)
    await page.click('[aria-label="清除搜索"]')
    await page.type('[data-admin-list-toolbar] input', 'no-matching-admin-record')
    await page.waitForFunction(() => document.body.innerText.includes('没有匹配的记录'))
    await page.click('[aria-label="清除搜索"]')
    await page.waitForSelector('tbody tr td:not([colspan])')
    console.log(`ok ${view} search and clear`)
  }

  await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=channels', { waitUntil: 'networkidle0' })
  assert.equal(await page.$('[role="dialog"]'), null, 'Filters should be inline without a dialog')
  await page.click('[data-admin-list-filters] [role="combobox"]')
  await page.waitForSelector('[role="option"]')
  await page.evaluate(() => [...document.querySelectorAll('[role="option"]')].find((option) => option.textContent === 'openai').click())
  await page.waitForFunction(() => !document.querySelector('[role="listbox"]'))
  await page.waitForFunction(() => document.querySelectorAll('tbody tr').length === 1 && document.querySelector('tbody').textContent.includes('OpenAI Production'))
  await page.click('[aria-label="重置筛选"]')
  await page.waitForFunction(() => document.querySelectorAll('tbody tr').length === 3)
  const statusSelect = await page.$$('[data-admin-list-filters] [role="combobox"]')
  await statusSelect[1].click()
  await page.waitForSelector('[role="option"]')
  await page.evaluate(() => [...document.querySelectorAll('[role="option"]')].find((option) => option.textContent === '已启用').click())
  await page.waitForFunction(() => !document.querySelector('[role="listbox"]'))
  await page.waitForFunction(() => document.querySelectorAll('tbody tr').length === 2)
  await page.focus('tbody tr:first-child [aria-label="下移"]')
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => document.querySelector('tbody tr').textContent.includes('Anthropic'))
  const filteredMutation = mutations.filter((mutation) => mutation.path === '/admin/channels/reorder').at(-1)
  assert.deepEqual(filteredMutation?.body.ids, ['c2', 'c1', 'c3'], 'Filtered sort must send all channel IDs')
  await page.click('[aria-label="重置筛选"]')
  await page.waitForFunction(() => document.querySelectorAll('tbody tr').length === 3)
  assert.ok(await page.$eval('tbody', (body) => body.textContent.includes('Google Gemini')), 'Filtered sorting dropped the hidden channel')

  for (const width of [1440, 390]) {
    await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
    await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=channels', { waitUntil: 'networkidle0' })
    await page.click('[aria-label="编辑: OpenAI Production"]')
    await page.waitForSelector('#ch-headers', { visible: true })
    const setHeaders = async (value) => {
      await page.$eval('#ch-headers', (textarea, value) => {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(textarea, value)
        textarea.dispatchEvent(new Event('input', { bubbles: true }))
      }, value)
    }
    const saveHeaders = async () => {
      const buttons = await page.$$('[role="dialog"] button')
      for (const button of buttons) if (await button.evaluate((button) => button.textContent.trim() === '保存')) { await button.click(); return }
      throw new Error('Channel save button missing')
    }
    await setHeaders('{"A":1}')
    const beforeInvalid = mutations.length
    await saveHeaders()
    await page.waitForFunction(() => document.body.innerText.includes('请求头的值必须是字符串'))
    assert.equal(mutations.length, beforeInvalid, 'Invalid headers were submitted')
    await setHeaders('{\n  "A": "a",\n  "X-Tenant": "team"\n}')
    await saveHeaders()
    await page.waitForFunction(() => !document.querySelector('#ch-headers'))
    assert.deepEqual(mutations.at(-1)?.body.headers, { A: 'a', 'X-Tenant': 'team' })
    await page.click('[aria-label="编辑: OpenAI Production"]')
    await page.waitForFunction(() => document.querySelector('#ch-headers')?.value.includes('"A": "a"'))
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0, 'Channel header editor overflows the page')
    await page.screenshot({ path: `${output}/channel-headers-${width}.png`, fullPage: true })
    await setHeaders('{}')
    await saveHeaders()
    await page.waitForFunction(() => !document.querySelector('#ch-headers'))
    assert.deepEqual(mutations.at(-1)?.body.headers, {})
    console.log(`ok channel header validation, save, reopen and clear ${width}px`)
  }

  await page.setViewport({ width: 768, height: 960 })
  await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=sorting', { waitUntil: 'networkidle0' })
  await page.focus('tbody tr:first-child [aria-label="Down"]')
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => window.__ADMIN_ORDER__?.join(',') === 'b,a,c')
  await page.waitForFunction(() => [...document.querySelectorAll('tbody tr')].every((row) => !row.style.transform || row.style.transform === 'none'))
  await page.click('tbody tr:first-child [data-column="actions"] button')
  assert.equal(await page.evaluate(() => window.__ADMIN_ACTION__), 'b')
  const handle = await page.$('tbody tr:first-child [aria-label="Drag"]')
  const box = await handle.boundingBox()
  const last = await page.$eval('tbody tr:last-child', (row) => { const box = row.getBoundingClientRect(); return box.top + box.height * 0.75 })
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width / 2, last, { steps: 10 })
  await page.mouse.up()
  await page.waitForFunction(() => window.__ADMIN_ORDER__?.join(',') === 'a,c,b')

  await page.setViewport({ width: 390, height: 960, isMobile: true, hasTouch: true })
  await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=sorting', { waitUntil: 'networkidle0' })
  const mobileHandle = await page.$('tbody tr:first-child [aria-label="Drag"]')
  const mobileBox = await mobileHandle.boundingBox()
  await page.mouse.move(mobileBox.x + mobileBox.width / 2, mobileBox.y + mobileBox.height / 2)
  await page.mouse.down()
  await page.mouse.move(mobileBox.x + mobileBox.width / 2, mobileBox.y + 80, { steps: 5 })
  await page.waitForSelector('.admin-table-drag-overlay')
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), 0, 'Dragging a wide table must not overflow the page')
  await page.mouse.up()
  await page.waitForFunction(() => !document.querySelector('.admin-table-drag-overlay'))

  await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=users', { waitUntil: 'networkidle0' })
  await page.click('tbody tr:first-child [aria-label="编辑"]')
  await page.waitForSelector('[role="dialog"]')

  const clickText = async (selector, text) => {
    const targets = await page.$$(selector)
    for (const target of targets) {
      if (await target.evaluate((element, expected) => element.textContent.trim() === expected, text)) {
        await target.click()
        return
      }
    }
    assert.fail(`Missing ${selector}: ${text}`)
  }
  const checkDetailTables = async (name, count) => {
    await page.waitForFunction((count) => [...document.querySelectorAll('[role="dialog"] table.admin-data-table')].filter((table) => table.getBoundingClientRect().height > 0).length >= count, { timeout: 10000 }, count).catch(async (error) => {
      throw new Error(`${name}: ${error.message}; ${await page.$eval('body', (body) => body.innerText)}; ${failures.join('; ')}`)
    })
    await page.waitForFunction(() => [...document.querySelectorAll('[role="dialog"]')].every((dialog) => dialog.getAnimations().every((animation) => animation.playState === 'finished')))
    const facts = await page.evaluate(() => [...document.querySelectorAll('[role="dialog"] table.admin-data-table')].filter((table) => table.getBoundingClientRect().height > 0).map((table) => ({
      headers: [...table.querySelectorAll('th')].map((header) => header.textContent.trim()),
      overflowing: table.closest('.admin-table-scroll').getBoundingClientRect().right > innerWidth + 1,
    })))
    await page.screenshot({ path: `${output}/${name}-${page.viewport().width}.png`, fullPage: true })
    assert.ok(facts.every((table) => table.headers.every(Boolean) && !table.overflowing), `${name}: invalid detail table: ${JSON.stringify(facts)}`)
    console.log(`ok ${name} ${page.viewport().width}px`)
  }
  for (const width of [1440, 390]) {
    await page.setViewport({ width, height: 960, isMobile: width === 390, hasTouch: width === 390 })
    const errorCount = failures.length
    await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=resources', { waitUntil: 'networkidle0' })
    await page.click('tbody tr:first-child [data-column="actions"] button')
    await checkDetailTables('knowledge-base-detail', 2)

    await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=resources', { waitUntil: 'networkidle0' })
    await clickText('button[aria-pressed]', '项目')
    await page.waitForFunction(() => document.querySelector('tbody [data-column="name"]')?.textContent.includes('Research workspace'))
    await page.waitForSelector('tbody tr:first-child [data-column="actions"] button')
    await page.click('tbody tr:first-child [data-column="actions"] button')
    await checkDetailTables('project-detail', 2)

    await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=workspaces', { waitUntil: 'networkidle0' })
    await page.click('tbody tr:first-child [data-column="actions"] button')
    await page.waitForFunction(() => document.querySelectorAll('table.admin-data-table').length === 4)
    await page.click('table[aria-label="知识库"] [data-column="actions"] button')
    await checkDetailTables('workspace-detail', 1)

    await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=groups', { waitUntil: 'networkidle0' })
    await page.click('tbody tr:first-child [aria-label^="编辑"]')
    await clickText('[role="tab"]', '用户')
    await checkDetailTables('group-users', 1)

    await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=domains', { waitUntil: 'networkidle0' })
    await page.click('[aria-label="域用户: example.com"]')
    await checkDetailTables('domain-members', 1)
    await page.click('[role="dialog"] [role="combobox"]')
    await clickText('[role="option"]', '仅工作空间')
    await page.waitForSelector('[role="listbox"]', { hidden: true })
    await page.waitForFunction(() => document.querySelector('[role="dialog"] td[data-column="status"]')?.textContent === '仅工作空间')
    await page.waitForFunction(() => ![...document.querySelectorAll('[role="dialog"] button')].find((button) => button.textContent.trim() === '添加成员')?.disabled)
    assert.deepEqual(mutations.at(-1)?.body, { lock_override: true })
    await clickText('[role="dialog"] button', '添加成员')
    await page.waitForSelector('label[for="domain-candidate-u2"]', { timeout: 10000 }).catch(async (error) => {
      await page.screenshot({ path: `${output}/domain-candidate-error.png`, fullPage: true })
      throw new Error(`${error.message}; ${await page.$eval('body', (body) => body.innerText)}; ${failures.join('; ')}`)
    })
    await checkDetailTables('domain-candidates', 1)
    await page.click('label[for="domain-candidate-u2"]')
    assert.equal(await page.$eval('#domain-candidate-u2', (checkbox) => checkbox.checked), true)

    await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=skills', { waitUntil: 'networkidle0' })
    await page.click('tbody tr:first-child [aria-label^="编辑"]')
    await checkDetailTables('skill-assets', 1)
    await page.click('[role="dialog"] [data-column="actions"] button')
    await page.waitForFunction(() => !document.querySelector('[role="dialog"] table'))
    assert.equal(failures.length, errorCount, failures.slice(errorCount).join('; '))
  }
  await page.goto('http://localhost:5198/tests/browser/admin-tables-harness.html?view=quota', { waitUntil: 'networkidle0' })
  await page.waitForSelector('table input[type="number"]')
  await page.$eval('table [data-column="limit"] input', (input) => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set
    setter.call(input, '250')
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await clickText('button', '保存权限')
  await page.waitForFunction(() => !document.querySelector('button[disabled]'))
  assert.equal(mutations.at(-1)?.path, '/admin/models/m1/quotas')
  assert.equal(mutations.at(-1)?.body.quotas[0].limit_value, 250)
  console.log(`Passed ${views.length * 3} view checks, keyboard sorting, pointer drag, user editing, 14 detail checks, membership selection, asset removal and quota saving. Screenshots: ${output}`)
} catch (error) {
  await page.screenshot({ path: `${output}/failure.png`, fullPage: true })
  console.error(await page.$eval('body', (body) => body.innerText))
  throw error
} finally {
  await browser.close()
  await server.close()
}
