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
  let capturing = false
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

  function cleanup() {
    if (closed) return
    closed = true
    if (finalTimer) clearTimeout(finalTimer)
    teardownCapture()
    try {
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) ws.close()
    } catch {
      /* ignore */
    }
    handlers.onClose?.()
  }

  processor.onaudioprocess = (e) => {
    if (!capturing || ws.readyState !== WebSocket.OPEN) return
    const chunk = resample(e.inputBuffer.getChannelData(0), inRate, TARGET_RATE)
    pending = concatFloat32(pending, chunk)
    while (pending.length >= FRAME_SAMPLES) {
      const frame = pending.subarray(0, FRAME_SAMPLES)
      ws.send(encodePCM16LE(frame))
      pending = pending.slice(FRAME_SAMPLES)
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

  /** Flush any buffered tail and tell the backend the user stopped. */
  function flushAndEnd() {
    if (ws.readyState === WebSocket.OPEN) {
      if (pending.length > 0) {
        ws.send(encodePCM16LE(pending))
        pending = new Float32Array(0)
      }
      try {
        ws.send(JSON.stringify({ type: 'end' }))
      } catch {
        /* ignore */
      }
    }
  }

  ws.onopen = () => {
    // Wait for the backend's "ready" (Volcano connected) before capturing so we
    // don't stream audio into a failed upstream.
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
        startCapture()
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

  return {
    stop() {
      if (closed) return
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
