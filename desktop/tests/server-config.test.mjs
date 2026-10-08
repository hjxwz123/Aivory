import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import settings from '../server-config.cjs'
import { prepareApp } from '../prepare.mjs'

function directory(t) {
  const dir = mkdtempSync(path.join(tmpdir(), 'aivory-server-config-'))
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  return dir
}

test('a generic package needs setup only once and an update cannot replace its saved server', (t) => {
  const dir = directory(t)
  assert.equal(settings.resolveServerConfig(dir, ''), undefined)
  assert.equal(settings.saveServerConfig(dir, 'https://my-chat.example/'), 'https://my-chat.example/')
  assert.equal(settings.resolveServerConfig(dir, ''), 'https://my-chat.example/')
  assert.equal(settings.resolveServerConfig(dir, 'https://example-default.example/'), 'https://my-chat.example/')
})

test('a build default is persisted on first launch and survives a generic update', (t) => {
  const dir = directory(t)
  assert.equal(settings.resolveServerConfig(dir, ' https://my-chat.example '), 'https://my-chat.example/')
  assert.deepEqual(JSON.parse(readFileSync(path.join(dir, 'server.json'))), { baseUrl: 'https://my-chat.example/' })
  assert.equal(settings.resolveServerConfig(dir, undefined), 'https://my-chat.example/')
})

test('corrupt settings can be repaired and an invalid save cannot overwrite a valid server', (t) => {
  const dir = directory(t)
  writeFileSync(path.join(dir, 'server.json'), '{corrupt')
  assert.equal(settings.resolveServerConfig(dir, ''), undefined)
  settings.saveServerConfig(dir, 'http://localhost:5173')
  for (const address of ['', 'https://user:pass@example.com', 'https://example.com/api', 'file:///tmp/server']) {
    assert.throws(() => settings.saveServerConfig(dir, address))
  }
  assert.equal(settings.readServerConfig(dir), 'http://localhost:5173/')
})

test('desktop identity is added once while preserving browser and OS information', () => {
  const ua = 'Mozilla/5.0 (Macintosh) Chrome/150.0.0.0'
  assert.equal(settings.desktopUserAgent(ua, '2.5.2'), `${ua} AivoryDesktop/2.5.2`)
  assert.equal(settings.desktopUserAgent(`${ua} AivoryDesktop/2.5.1`, '2.5.2'), `${ua} AivoryDesktop/2.5.2`)
})

test('empty build URL prepares a generic app with local setup and retina tray assets', async (t) => {
  const dir = directory(t)
  const previous = process.env.AIVORY_DESKTOP_BASE_URL
  process.env.AIVORY_DESKTOP_BASE_URL = ''
  t.after(() => {
    if (previous === undefined) delete process.env.AIVORY_DESKTOP_BASE_URL
    else process.env.AIVORY_DESKTOP_BASE_URL = previous
  })
  const webDir = directory(t)
  writeFileSync(path.join(webDir, 'index.html'), '<html>Bundled frontend</html>')
  assert.equal((await prepareApp(dir, { webDir })).baseUrl, '')
  for (const file of ['server.html', 'server-preload.cjs', 'server-config.cjs', 'web/index.html', 'local-web.cjs', 'audio-socket.cjs', 'node_modules/ws/index.js', 'assets/trayTemplate.png', 'assets/trayTemplate@2x.png']) {
    assert.ok(readFileSync(path.join(dir, file)).length)
  }
})
