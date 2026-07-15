// Live voice-streaming client for the Volcano (火山引擎 豆包) ASR provider.
//
// The Whisper path (lib/audio.ts + composer) records a whole clip and POSTs it.
// Volcano is real-time: we capture the mic as raw 16 kHz mono 16-bit PCM and
// stream it over a WebSocket to our backend (which relays it to Volcano and
// streams transcripts back), so text appears on screen as the user speaks.
//
// Capture uses a ScriptProcessorNode. It's deprecated but universally supported
// and needs no separately-bundled AudioWorklet module — which matters here
// because the app is also served over plain HTTP on some deployments, where
// loading a worklet module can be awkward. The mic itself already requires a
// secure context (navigator.mediaDevices), the same gate the Whisper path uses.

import { apiUrl } from '@/api/client'

/** Target wire format — must match the backend's full-client-request config. */
const TARGET_RATE = 16_000
/** ~200 ms per packet at 16 kHz — Volcano's recommended packet size. */
const FRAME_SAMPLES = 3_200
/** Keep the user's opening phrase while the backend establishes the ASR link. */
const PRE_READY_BUFFER_SECONDS = 10
const PRE_READY_BUFFER_MAX_BYTES = TARGET_RATE * 2 * PRE_READY_BUFFER_SECONDS

export interface VoiceStreamHandlers {
  /** Backend connected to Volcano and is ready for audio. */
  onReady?: () => void
  /** Incremental (cumulative) transcript while speaking. */
  onPartial?: (text: string) => void
  /** Final transcript for the utterance; the session is finished after this. */
  onFinal?: (text: string) => void
  /** A recoverable error; the session is torn down after this fires. */
  onError?: (message: string) => void
  /** The session ended (after final, error, or a socket close). Fires once. */
  onClose?: () => void
}

export interface VoiceStreamController {
  /** User pressed stop: finish capture and wait for the final transcript. */
  stop: () => void
  /** Abort immediately without waiting for a final result (e.g. unmount). */
  cancel: () => void
}

/** Build the ws(s):// URL for an API path, honouring a same-origin or an
 *  absolute VITE_API_BASE. */
function toWsUrl(path: string): string {
  const u = apiUrl(path)
  if (u.startsWith('https://')) return 'wss://' + u.slice('https://'.length)
  if (u.startsWith('http://')) return 'ws://' + u.slice('http://'.length)
  const scheme = location.protocol === 'https:' ? 'wss://' : 'ws://'
  return scheme + location.host + (u.startsWith('/') ? u : '/' + u)
}

function audioContextCtor(): typeof AudioContext | undefined {
  if (typeof window === 'undefined') return undefined
  return window.AudioContext || (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
}

/** Resample (linear) to the target rate; a no-op when rates already match. */
function resample(input: Float32Array, inRate: number, outRate: number): Float32Array {
  if (inRate === outRate || input.length === 0) return input
  const ratio = inRate / outRate
  const outLen = Math.max(1, Math.floor(input.length / ratio))
  const out = new Float32Array(outLen)
  for (let i = 0; i < outLen; i++) {
    const idx = i * ratio
    const i0 = Math.floor(idx)
    const i1 = Math.min(i0 + 1, input.length - 1)
    out[i] = input[i0] + (input[i1] - input[i0]) * (idx - i0)
  }
  return out
}

/** Encode Float32 samples as little-endian 16-bit PCM (Volcano's s16le). */
function encodePCM16LE(samples: Float32Array): ArrayBuffer {
  const buf = new ArrayBuffer(samples.length * 2)
  const view = new DataView(buf)
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]))
    view.setInt16(i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true)
  }
  return buf
}

function concatFloat32(a: Float32Array, b: Float32Array): Float32Array {
  if (a.length === 0) return b
  if (b.length === 0) return a
  const out = new Float32Array(a.length + b.length)
  out.set(a, 0)
  out.set(b, a.length)
  return out
}

/**
 * Start a live transcription session. Resolves once the mic is open and the
 * socket is connecting; rejects if the browser can't capture audio or the user
 * denies permission (the caller maps that to a toast). Transcript + lifecycle
 * are delivered through `handlers`.
 */
export async function startVoiceStream(handlers: VoiceStreamHandlers): Promise<VoiceStreamController> {
  const Ctor = audioContextCtor()
  if (!Ctor || typeof navigator === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
    throw new Error('unsupported')
  }

  const stream = await navigator.mediaDevices.getUserMedia({ audio: true })

  // Prefer a 16 kHz context so no resampling is needed; fall back to the
  // device rate + manual resample when the browser ignores the hint (Safari).
  let ctx: AudioContext
  try {
    ctx = new Ctor({ sampleRate: TARGET_RATE })
  } catch {
    ctx = new Ctor()
  }
  const inRate = ctx.sampleRate

  const ws = new WebSocket(toWsUrl('/audio/stream'))
  ws.binaryType = 'arraybuffer'

  // Typed as the default buffer variant so appends/slices (whose backing buffer
  // TS widens to ArrayBufferLike) assign back cleanly.
  let pending: Float32Array = new Float32Array(0)
  let preReadyFrames: ArrayBuffer[] = []
  let preReadyBytes = 0
  let capturing = false
  let upstreamReady = false
  let stopRequested = false
  let endSent = false
  let closed = false
  let finalTimer: ReturnType<typeof setTimeout> | null = null

  const source = ctx.createMediaStreamSource(stream)
  const processor = ctx.createScriptProcessor(4096, 1, 1)
  // A muted sink keeps the processor firing without routing the mic to the
  // speakers (which would feed back).
  const mute = ctx.createGain()
  mute.gain.value = 0

  function teardownCapture() {
    capturing = false
    try {
      processor.disconnect()
      source.disconnect()
      mute.disconnect()
    } catch {
      /* already disconnected */
    }
    stream.getTracks().forEach((tr) => tr.stop())
    if (ctx.state !== 'closed') void ctx.close()
  }

  function clearBufferedAudio() {
    pending = new Float32Array(0)
    preReadyFrames = []
    preReadyBytes = 0
  }

  function bufferPreReadyFrame(frame: ArrayBuffer) {
    // A slow or unavailable upstream must not turn an active microphone into
    // unbounded memory use. Preserve the earliest audio first: it is the part
    // users most often lose while waiting for a cold upstream connection.
    if (preReadyBytes + frame.byteLength > PRE_READY_BUFFER_MAX_BYTES) return
    preReadyFrames.push(frame)
    preReadyBytes += frame.byteLength
  }

  function cleanup() {
    if (closed) return
    closed = true
    if (finalTimer) clearTimeout(finalTimer)
    clearBufferedAudio()
    teardownCapture()
    try {
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) ws.close()
    } catch {
      /* ignore */
    }
    handlers.onClose?.()
  }

  function failConnection() {
    if (closed) return
    handlers.onError?.('connection lost')
    cleanup()
  }

  function sendFrame(frame: ArrayBuffer): boolean {
    try {
      ws.send(frame)
      return true
    } catch {
      failConnection()
      return false
    }
  }

  function queueOrSendFrame(frame: ArrayBuffer): boolean {
    if (closed) return false
    if (!upstreamReady || ws.readyState !== WebSocket.OPEN) {
      bufferPreReadyFrame(frame)
      return true
    }
    return sendFrame(frame)
  }

  function flushPreReadyFrames(): boolean {
    if (ws.readyState !== WebSocket.OPEN) return false
    const frames = preReadyFrames
    preReadyFrames = []
    preReadyBytes = 0
    for (const frame of frames) {
      if (!sendFrame(frame)) return false
    }
    return true
  }

  function flushPendingTail(): boolean {
    if (pending.length === 0) return true
    const tail = encodePCM16LE(pending)
    pending = new Float32Array(0)
    return queueOrSendFrame(tail)
  }

  function sendEnd(): boolean {
    if (endSent || !upstreamReady || ws.readyState !== WebSocket.OPEN) return false
    try {
      ws.send(JSON.stringify({ type: 'end' }))
      endSent = true
      return true
    } catch {
      failConnection()
      return false
    }
  }

  processor.onaudioprocess = (e) => {
    if (!capturing || closed) return
    const chunk = resample(e.inputBuffer.getChannelData(0), inRate, TARGET_RATE)
    pending = concatFloat32(pending, chunk)
    while (pending.length >= FRAME_SAMPLES) {
      const frame = pending.subarray(0, FRAME_SAMPLES)
      pending = pending.slice(FRAME_SAMPLES)
      if (!queueOrSendFrame(encodePCM16LE(frame))) return
    }
  }

  function startCapture() {
    if (capturing || closed) return
    capturing = true
    source.connect(processor)
    processor.connect(mute)
    mute.connect(ctx.destination)
    void ctx.resume()
  }

  /** Flush the final PCM tail; before ready it joins the local FIFO buffer. */
  function flushAndEnd() {
    if (!flushPendingTail()) return
    if (!upstreamReady || ws.readyState !== WebSocket.OPEN) return
    if (!flushPreReadyFrames()) return
    sendEnd()
  }

  ws.onopen = () => {
    // Capture already runs locally. The backend's "ready" gates forwarding so
    // the buffered opening audio never reaches a failed upstream.
  }

  ws.onmessage = (e) => {
    if (typeof e.data !== 'string') return
    let msg: { type?: string; text?: string; message?: string }
    try {
      msg = JSON.parse(e.data)
    } catch {
      return
    }
    switch (msg.type) {
      case 'ready':
        if (closed || upstreamReady) break
        upstreamReady = true
        // Preserve chronology: complete frames captured while connecting, then
        // the short current tail, then subsequent live frames.
        if (!flushPreReadyFrames() || !flushPendingTail()) break
        if (stopRequested) {
          sendEnd()
          break
        }
        handlers.onReady?.()
        break
      case 'partial':
        handlers.onPartial?.(msg.text ?? '')
        break
      case 'final':
        handlers.onFinal?.(msg.text ?? '')
        cleanup()
        break
      case 'error':
        handlers.onError?.(msg.message || 'transcription failed')
        cleanup()
        break
    }
  }

  ws.onerror = () => {
    if (closed) return
    handlers.onError?.('connection lost')
    cleanup()
  }

  ws.onclose = () => cleanup()

  // Start capturing as soon as the user has granted microphone access. The
  // socket/upstream handshake can take seconds; samples stay in the bounded
  // local FIFO until the server explicitly says it can relay them.
  startCapture()

  return {
    stop() {
      if (closed || stopRequested) return
      stopRequested = true
      teardownCapture() // stop the mic immediately; keep the socket for the final
      flushAndEnd()
      // Safety net: if the backend never sends a final packet, close anyway.
      finalTimer = setTimeout(cleanup, 8_000)
    },
    cancel() {
      cleanup()
    },
  }
}
