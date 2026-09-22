import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { VoiceStreamController } from '@/lib/audio-stream'

type SocketPayload = ArrayBuffer | string

class FakeNode {
  connect() {}
  disconnect() {}
}

class FakeGainNode extends FakeNode {
  gain = { value: 1 }
}

class FakeScriptProcessor extends FakeNode {
  onaudioprocess: ((event: { inputBuffer: { getChannelData: (channel: number) => Float32Array } }) => void) | null = null

  emit(samples: Float32Array) {
    this.onaudioprocess?.({ inputBuffer: { getChannelData: () => samples } })
  }
}

class FakeAudioContext {
  static instances: FakeAudioContext[] = []

  readonly sampleRate: number
  readonly destination = new FakeNode()
  readonly source = new FakeNode()
  readonly processor = new FakeScriptProcessor()
  readonly gain = new FakeGainNode()
  state: 'running' | 'closed' = 'running'

  constructor(options?: { sampleRate?: number }) {
    this.sampleRate = options?.sampleRate ?? 16_000
    FakeAudioContext.instances.push(this)
  }

  createMediaStreamSource() {
    return this.source
  }

  createScriptProcessor() {
    return this.processor
  }

  createGain() {
    return this.gain
  }

  async resume() {}

  async close() {
    this.state = 'closed'
  }
}

class FakeTrack {
  stopped = false

  stop() {
    this.stopped = true
  }
}

class FakeMediaStream {
  readonly track = new FakeTrack()

  getTracks() {
    return [this.track]
  }
}

class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []

  binaryType = ''
  readyState = FakeWebSocket.CONNECTING
  sent: SocketPayload[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null

  constructor(_url: string) {
    FakeWebSocket.instances.push(this)
  }

  open() {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.()
  }

  receive(message: unknown) {
    this.onmessage?.({ data: JSON.stringify(message) })
  }

  send(payload: SocketPayload) {
    if (this.readyState !== FakeWebSocket.OPEN) throw new Error('socket is not open')
    this.sent.push(payload)
  }

  close() {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.()
  }
}

let mediaStream: FakeMediaStream

/**
 * The session a test started, so teardown can always cancel it.
 *
 * Without this, a test that fails or times out part-way through
 * `startVoiceStream` leaves a live session behind. Its late continuation pushes
 * a socket into the shared static `instances` array AFTER `beforeEach` cleared
 * it, so the next test's `latestSocket()` returns the ZOMBIE socket and then
 * observes the previous test's buffered audio. That is the exact failure this
 * suite used to reproduce under CPU contention:
 *
 *   AssertionError: expected [ ArrayBuffer [51, 19, 51, 19, …] ] to deeply equal []
 *
 * (0x1333 = 4915 = the previous test's 0.15 sample amplitude.)
 */
let activeController: VoiceStreamController | null = null

/**
 * The in-flight `startVoiceStream` of the current test.
 *
 * Teardown MUST await this. A test that fails or times out leaves the call
 * unresolved, and `startVoiceStream` only constructs its socket after awaiting
 * `getUserMedia` — so an un-awaited start would create its socket during the
 * NEXT test, land in that test's freshly-cleared instance registry, and hand the
 * next test the previous test's socket with the previous test's buffered audio:
 *
 *   AssertionError: expected [ ArrayBuffer [51, 19, 51, 19, …] ] to deeply equal []
 *
 * (0x1333 = 4915 = the previous test's 0.15 sample amplitude.)
 *
 * A generation/token tag cannot fix this: a late start reads the CURRENT globals,
 * so it is indistinguishable from the live test's own call. Draining it in
 * teardown is what actually removes the zombie.
 */
let pendingStart: Promise<VoiceStreamController> | null = null

/** Generous on purpose: these tests do no waiting of their own, so exceeding
 *  the 5s default only ever means the machine was starved — and a timeout here
 *  is what creates a zombie session in the first place. */
const TEST_TIMEOUT_MS = 30_000

function samples(length: number, value: number): Float32Array {
  return new Float32Array(length).fill(value)
}

function latestContext(): FakeAudioContext {
  const ctx = FakeAudioContext.instances.at(-1)
  if (!ctx) throw new Error('audio context was not created')
  return ctx
}

function latestSocket(): FakeWebSocket {
  const socket = FakeWebSocket.instances.at(-1)
  if (!socket) throw new Error('websocket was not created')
  return socket
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeAudioContext.instances = []
  FakeWebSocket.instances = []
  mediaStream = new FakeMediaStream()
  vi.stubGlobal('location', { protocol: 'http:', host: 'app.test' })
  vi.stubGlobal('window', { AudioContext: FakeAudioContext })
  vi.stubGlobal('navigator', {
    mediaDevices: {
      getUserMedia: vi.fn().mockResolvedValue(mediaStream),
    },
  })
  vi.stubGlobal('WebSocket', FakeWebSocket)
})

afterEach(async () => {
  // Drain an in-flight start BEFORE the globals are torn down and before the
  // next test clears the registries — otherwise its socket is created under the
  // next test's fakes and that test observes this test's audio.
  const pending = pendingStart
  pendingStart = null
  if (pending) {
    try {
      ;(await pending).cancel()
    } catch {
      /* the start rejected: there is no session to cancel */
    }
  }

  activeController?.cancel()
  activeController = null
  vi.unstubAllGlobals()
  vi.useRealTimers()
  vi.resetModules()
}, TEST_TIMEOUT_MS)

describe('startVoiceStream', () => {
  it('buffers opening PCM until the backend reports ready', async () => {
    const { startVoiceStream } = await import('@/lib/audio-stream')
    pendingStart = startVoiceStream({})
    const controller = await pendingStart
    activeController = controller
    const socket = latestSocket()
    const context = latestContext()

    socket.open()
    context.processor.emit(samples(1_200, 0.15))
    context.processor.emit(samples(2_000, 0.45))
    expect(socket.sent).toEqual([])

    socket.receive({ type: 'ready' })
    expect(socket.sent).toHaveLength(1)
    expect(socket.sent[0]).toBeInstanceOf(ArrayBuffer)

    const pcm = new DataView(socket.sent[0] as ArrayBuffer)
    expect((socket.sent[0] as ArrayBuffer).byteLength).toBe(6_400)
    expect(pcm.getInt16(0, true)).toBeGreaterThan(4_800)
    expect(pcm.getInt16(1_200 * 2, true)).toBeGreaterThan(14_000)

    socket.receive({ type: 'final', text: '' })
    controller.cancel()
  }, TEST_TIMEOUT_MS)

  it('flushes buffered audio before ending when stopped during connection', async () => {
    const { startVoiceStream } = await import('@/lib/audio-stream')
    pendingStart = startVoiceStream({})
    const controller = await pendingStart
    activeController = controller
    const socket = latestSocket()
    const context = latestContext()

    context.processor.emit(samples(1_000, 0.25))
    controller.stop()
    expect(mediaStream.track.stopped).toBe(true)
    expect(socket.sent).toEqual([])

    socket.open()
    expect(socket.sent).toEqual([])
    socket.receive({ type: 'ready' })

    expect(socket.sent).toHaveLength(2)
    expect(socket.sent[0]).toBeInstanceOf(ArrayBuffer)
    expect((socket.sent[0] as ArrayBuffer).byteLength).toBe(2_000)
    expect(socket.sent[1]).toBe('{"type":"end"}')

    socket.receive({ type: 'final', text: '' })
  }, TEST_TIMEOUT_MS)
})
