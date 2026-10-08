import { test } from 'node:test'
import assert from 'node:assert/strict'
import updates from '../updates.cjs'

const options = { version: '2.5.1-beta.6', platform: 'darwin', arch: 'arm64', baseUrl: 'https://deployment.example.test/' }
const manifest = (version, downloads = { macos_arm64: 'https://downloads.example.test/aivory.dmg' }) => ({ enabled: true, version, downloads })

test('desktop updates require operator publication and a matching installer', () => {
  assert.equal(updates.selectUpdate(manifest('2.5.1-beta.7'), options)?.version, '2.5.1-beta.7')
  assert.equal(updates.selectUpdate({ ...manifest('2.5.2'), enabled: false }, options), null)
  assert.equal(updates.selectUpdate(manifest('2.5.2', {}), options), null)
  assert.equal(updates.selectUpdate(manifest('2.5.2', { macos_x64: 'https://downloads.example.test/intel.dmg' }), options), null)
  assert.equal(updates.selectUpdate(manifest('2.5.1-beta.6'), options), null)
  assert.equal(updates.selectUpdate(manifest('2.5.1-beta.5'), options), null)
})

test('beta versions compare correctly and stable apps ignore prereleases', () => {
  assert.equal(updates.selectUpdate(manifest('2.5.1-beta.10'), { ...options, version: '2.5.1-beta.9' })?.version, '2.5.1-beta.10')
  assert.equal(updates.selectUpdate(manifest('2.6.0-beta.1'), { ...options, version: '2.5.1' }), null)
  assert.equal(updates.selectUpdate(manifest('2.5.1'), options)?.version, '2.5.1')
  assert.equal(updates.selectUpdate(manifest('not-a-version'), options), null)
})

test('operator installer URLs support custom hosts and signed links but reject unsafe schemes', () => {
  const address = 'https://storage.example.test/download?id=installer&signature=abc'
  assert.equal(updates.selectUpdate(manifest('2.5.2', { macos_arm64: address }), options)?.url, address)
  for (const url of ['javascript:alert(1)', 'file:///installer.dmg', 'data:text/html,hello',
    'https://user:secret@example.test/installer', 'https://example.test/installer#redirect', 'not-a-url']) {
    assert.equal(updates.selectUpdate(manifest('2.5.2', { macos_arm64: url }), options), null)
  }
})

test('platform and architecture select the correct deployment package', () => {
  for (const [platform, os] of [['win32', 'windows'], ['darwin', 'macos'], ['linux', 'linux']]) {
    for (const arch of ['x64', 'arm64']) {
      const url = `https://downloads.example.test/${os}-${arch}`
      assert.equal(updates.selectUpdate(manifest('2.5.2', { [`${os}_${arch}`]: url }), { ...options, platform, arch })?.url, url)
    }
  }
  assert.equal(updates.selectUpdate(manifest('2.5.2'), { ...options, arch: 'ia32' }), null)
})

test('checks use the configured deployment, omit credentials and never fall back to GitHub', async () => {
  let requested
  const checker = new updates.UpdateChecker({ ...options, fetch: async (url, init) => {
    requested = { url, init }
    return { ok: true, json: async () => manifest('2.5.2') }
  }, showResult: async () => {} })
  assert.equal((await checker.check()).status, 'available')
  assert.equal(requested.url, 'https://deployment.example.test/api/public/desktop-update')
  assert.equal(requested.init.credentials, 'omit')
  assert.equal(requested.init.redirect, 'error')
  let calls = 0
  const oldServer = new updates.UpdateChecker({ ...options, fetch: async () => { calls++; return { ok: false, status: 404 } }, showResult: async () => {} })
  assert.equal((await oldServer.check()).status, 'failed')
  assert.equal(calls, 1)
})

test('concurrent checks share a fetch and automatic prompts are shown once per version', async () => {
  let calls = 0
  const prompts = []
  const checker = new updates.UpdateChecker({ ...options, fetch: async () => {
    calls++
    return { ok: true, json: async () => manifest('2.5.2') }
  }, showResult: async (result) => { prompts.push(result) } })
  await Promise.all([checker.check(), checker.check()])
  assert.equal(calls, 1)
  assert.equal(prompts.length, 1)
  await checker.check()
  assert.equal(prompts.length, 1)
  await checker.check(true)
  assert.equal(prompts.length, 2)
})
