import { test } from 'node:test'
import assert from 'node:assert/strict'
import policy from '../policy.cjs'
import locales from '../locales.cjs'

test('configured server URLs must point to a valid website origin', () => {
  assert.equal(policy.normalizeBaseUrl(' https://chat.example.com '), 'https://chat.example.com/')
  assert.equal(policy.normalizeBaseUrl('http://127.0.0.1:5173/'), 'http://127.0.0.1:5173/')
  assert.equal(policy.normalizeBaseUrl('http://[::1]:8787'), 'http://[::1]:8787/')
  for (const url of [undefined, '', 'chat.example.com', 'file:///app', 'javascript:alert(1)',
    'https://user:secret@chat.example.com', 'https://chat.example.com/api',
    'https://chat.example.com/?token=secret', 'https://chat.example.com/#settings']) {
    assert.throws(() => policy.normalizeBaseUrl(url))
  }
})

test('internal popups share the session and external web links open in the browser', () => {
  const base = 'https://chat.example.com/'
  assert.equal(policy.popupAction(`${base}share/example`, base), 'allow')
  assert.equal(policy.popupAction('blob:https://chat.example.com/example', base), 'allow')
  assert.equal(policy.popupAction('about:blank', base), 'allow')
  assert.equal(policy.popupAction('https://example.com/article', base), 'external')
  for (const url of ['file:///etc/passwd', 'javascript:alert(1)', 'data:text/html,hello',
    'blob:https://other.example.com/example', 'https://user:secret@example.com']) {
    assert.equal(policy.popupAction(url, base), 'deny')
  }
  assert.equal(policy.isTrustedUrl('https://chat.example.com.evil.test/', base), false)
})

test('microphone and clipboard permissions are limited to the configured website', () => {
  const base = 'https://chat.example.com/'
  assert.equal(policy.permissionAllowed(base, base, 'media', ['audio']), true)
  assert.equal(policy.permissionAllowed(base, base, 'media', ['video']), false)
  assert.equal(policy.permissionAllowed(base, base, 'media', ['audio', 'video']), false)
  assert.equal(policy.permissionAllowed(base, base, 'media'), false)
  assert.equal(policy.permissionAllowed(base, base, 'clipboard-sanitized-write'), true)
  assert.equal(policy.permissionAllowed(base, base, 'fullscreen'), true)
  assert.equal(policy.permissionAllowed('https://other.example.com/', base, 'media', ['audio']), false)
  assert.equal(policy.permissionAllowed('https://other.example.com/', base, 'clipboard-sanitized-write'), false)
  assert.equal(policy.permissionAllowed(base, base, 'geolocation'), false)
})

test('native shell messages follow the OS locale and fall back to English', () => {
  assert.equal(locales.getMessages('zh-CN').retry, '重试')
  assert.equal(locales.getMessages('zh-TW').retry, '重試')
  assert.equal(locales.getMessages('zh-Hant-HK').retry, '重試')
  assert.equal(locales.getMessages('ja-JP').retry, '再試行')
  assert.equal(locales.getMessages('fr-FR').retry, 'Réessayer')
  assert.equal(locales.getMessages('de-DE').retry, 'Retry')
})
