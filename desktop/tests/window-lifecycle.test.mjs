import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import test from 'node:test'
import lifecycle from '../window-lifecycle.cjs'

class TestWindow extends EventEmitter {
  fullscreen = false
  minimized = false
  destroyed = false
  actions = []
  webContents = new EventEmitter()

  constructor() {
    super()
    this.webContents.executeJavaScript = async () => { this.actions.push('exit-html') }
  }
  isDestroyed() { return this.destroyed }
  isFullScreen() { return this.fullscreen }
  isMinimized() { return this.minimized }
  setFullScreen(value) { this.actions.push(`fullscreen:${value}`) }
  minimize() { this.actions.push('minimize') }
  hide() { this.actions.push('hide') }
  restore() { this.actions.push('restore') }
  show() { this.actions.push('show') }
  focus() { this.actions.push('focus') }
  close() {
    let prevented = false
    this.emit('close', { preventDefault: () => { prevented = true } })
    return prevented
  }
}

const flush = () => new Promise(resolve => setImmediate(resolve))
function attach(t, window, options = {}) {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const handler = lifecycle.attachCloseToMinimize(window, { shouldMinimize: () => true, platform: 'darwin', ...options })
  t.after(() => handler.cancel())
  return handler
}

test('Mac close waits for native fullscreen exit even after the state flag changes early', async t => {
  const window = new TestWindow()
  window.fullscreen = true
  attach(t, window)
  assert.equal(window.close(), true)
  await flush()
  assert.deepEqual(window.actions, ['fullscreen:false'])
  window.fullscreen = false
  window.close()
  t.mock.timers.tick(2000)
  await flush()
  assert.deepEqual(window.actions, ['fullscreen:false'])
  window.emit('leave-full-screen')
  await flush()
  assert.deepEqual(window.actions, ['fullscreen:false', 'minimize'])
  t.mock.timers.tick(500)
  assert.deepEqual(window.actions, ['fullscreen:false', 'minimize', 'hide'])
})

test('Mac HTML fullscreen exits before native fullscreen and minimization', async t => {
  const window = new TestWindow()
  window.fullscreen = true
  attach(t, window)
  window.webContents.emit('enter-html-full-screen')
  window.close()
  await flush()
  assert.deepEqual(window.actions, ['exit-html'])
  t.mock.timers.tick(2000)
  assert.deepEqual(window.actions, ['exit-html'])
  window.webContents.emit('leave-html-full-screen')
  await flush()
  assert.deepEqual(window.actions, ['exit-html', 'fullscreen:false'])
  window.fullscreen = false
  window.emit('leave-full-screen')
  await flush()
  assert.deepEqual(window.actions, ['exit-html', 'fullscreen:false', 'minimize'])
})

test('Mac HTML close waits for native exit when isFullScreen already reports false', async t => {
  const window = new TestWindow()
  attach(t, window)
  window.emit('enter-full-screen')
  window.webContents.emit('enter-html-full-screen')
  window.close()
  await flush()
  window.webContents.emit('leave-html-full-screen')
  await flush()
  t.mock.timers.tick(2000)
  assert.deepEqual(window.actions, ['exit-html', 'fullscreen:false'])
  window.emit('leave-full-screen')
  await flush()
  assert.deepEqual(window.actions, ['exit-html', 'fullscreen:false', 'minimize'])
})

test('Mac close waits for the DOM fullscreen exit promise after the HTML exit event', async t => {
  const window = new TestWindow()
  let completeExit
  window.webContents.executeJavaScript = () => new Promise(resolve => { completeExit = resolve })
  attach(t, window)
  window.webContents.emit('enter-html-full-screen')
  window.close()
  await flush()
  window.webContents.emit('leave-html-full-screen')
  await flush()
  assert.deepEqual(window.actions, [])
  completeExit()
  await flush()
  await flush()
  assert.deepEqual(window.actions, ['minimize'])
})

test('Restore cancels delayed minimization after a fullscreen close', async t => {
  const window = new TestWindow()
  window.fullscreen = true
  const handler = attach(t, window)
  window.close()
  await flush()
  handler.restore()
  window.fullscreen = false
  window.emit('leave-full-screen')
  await flush()
  t.mock.timers.tick(2000)
  assert.deepEqual(window.actions, ['fullscreen:false', 'show', 'focus'])
})

test('Restore cancels the queued close and ordinary-window hide fallback', async t => {
  const window = new TestWindow()
  const handler = attach(t, window)
  window.close()
  handler.restore()
  await flush()
  assert.deepEqual(window.actions, ['show', 'focus'])
  window.actions = []
  window.close()
  await flush()
  handler.restore()
  t.mock.timers.tick(2000)
  assert.deepEqual(window.actions, ['minimize', 'show', 'focus'])
})

test('Fallback never hides a window that reentered fullscreen', async t => {
  const window = new TestWindow()
  attach(t, window)
  window.close()
  await flush()
  window.fullscreen = true
  t.mock.timers.tick(500)
  assert.deepEqual(window.actions, ['minimize'])
})

test('Quit and destruction cancel pending close actions', async t => {
  const window = new TestWindow()
  let quitting = false
  attach(t, window, { shouldMinimize: () => !quitting })
  window.close()
  await flush()
  quitting = true
  assert.equal(window.close(), false)
  t.mock.timers.tick(500)
  assert.deepEqual(window.actions, ['minimize'])
  quitting = false
  window.close()
  window.destroyed = true
  window.emit('closed')
  await flush()
  t.mock.timers.tick(500)
  assert.deepEqual(window.actions, ['minimize'])
})

test('Windows and Linux minimize directly and never use the Mac hide fallback', async t => {
  const window = new TestWindow()
  attach(t, window, { platform: 'win32' })
  window.close()
  await flush()
  t.mock.timers.tick(2000)
  assert.deepEqual(window.actions, ['minimize'])
})
