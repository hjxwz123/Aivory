import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { randomUUID } from 'node:crypto'
import transport from '../api-requests.cjs'

const owner = session => Object.assign(new EventEmitter(), { session })

test('cancellation remains scoped to its owning renderer/server, even before fetch starts', () => {
  const requests = new transport.ApiRequests()
  const session = {}
  const contents = owner(session)
  const unrelated = owner(session)
  const id = randomUUID()
  requests.start(id, contents)
  requests.abort(id, unrelated)
  const pending = requests.attach(id, session)
  assert.equal(pending.signal.aborted, false)
  assert.equal(requests.attach(id, {}), undefined)
  requests.abort(id, contents)
  assert.equal(pending.signal.aborted, true)
  pending.finish()
  assert.equal(requests.requests.size, 0)

  const earlyId = randomUUID()
  requests.start(earlyId, contents)
  requests.abort(earlyId, contents)
  const early = requests.attach(earlyId, session)
  assert.equal(early.signal.aborted, true)
  early.finish()
})

test('closing or switching the renderer cancels its streams and removes native request state', () => {
  const requests = new transport.ApiRequests()
  const session = {}
  const contents = owner(session)
  const id = randomUUID()
  // The protocol handler can arrive before the IPC start message.
  const active = requests.attach(id, session)
  requests.start(id, contents)
  contents.emit('destroyed')
  assert.equal(active.signal.aborted, true)
  assert.equal(requests.requests.size, 0)
  assert.equal(requests.attach('not-a-request', session), undefined)
})
