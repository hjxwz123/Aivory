// Voice-input audio helpers.
//
// MediaRecorder writes a "live"/streaming WebM whose EBML header often carries no
// usable Duration element (it's unknown until the stream ends, and the browser
// doesn't backfill it). Some transcription backends then 500 with
//   "error getting audio duration: webm duration parsing requires full EBML
//    parser (consider using ffprobe for webm files)".
// A WAV file states its length explicitly (sample count ÷ sample rate), so any
// backend can read the duration without an EBML parser. We also downmix to 16 kHz
// mono — exactly what speech-to-text models resample to internally — which keeps
// the upload small. Decoding uses the browser's own codec, so it handles whatever
// the recorder produced (Chrome/Firefox WebM/Opus, Safari MP4/AAC).

type AudioCtor = typeof AudioContext

function audioContextCtor(): AudioCtor | undefined {
  if (typeof window === 'undefined') return undefined
  return window.AudioContext || (window as unknown as { webkitAudioContext?: AudioCtor }).webkitAudioContext
}

const WAV_SAMPLE_RATE = 16_000

// encodeWavFromBlob decodes a recorded audio Blob and re-encodes it as a 16 kHz
// mono 16-bit PCM WAV. Throws if the environment can't decode audio (caller
// should fall back to sending the original recording).
export async function encodeWavFromBlob(blob: Blob): Promise<Blob> {
  const Ctor = audioContextCtor()
  if (!Ctor || typeof OfflineAudioContext === 'undefined') throw new Error('web audio unavailable')

  const arrayBuffer = await blob.arrayBuffer()
  const decodeCtx = new Ctor()
  let decoded: AudioBuffer
  try {
    // decodeAudioData is callback-style in older Safari; wrap for portability.
    decoded = await new Promise<AudioBuffer>((resolve, reject) => {
      decodeCtx.decodeAudioData(arrayBuffer.slice(0), resolve, reject)
    })
  } finally {
    void decodeCtx.close()
  }

  // Resample + downmix to 16 kHz mono via an offline render.
  const frames = Math.max(1, Math.ceil(decoded.duration * WAV_SAMPLE_RATE))
  const offline = new OfflineAudioContext(1, frames, WAV_SAMPLE_RATE)
  const src = offline.createBufferSource()
  src.buffer = decoded
  src.connect(offline.destination)
  src.start()
  const rendered = await offline.startRendering()
  return encodeWav(rendered.getChannelData(0), WAV_SAMPLE_RATE)
}

function encodeWav(samples: Float32Array, sampleRate: number): Blob {
  const dataLen = samples.length * 2 // 16-bit
  const buffer = new ArrayBuffer(44 + dataLen)
  const view = new DataView(buffer)
  const writeStr = (offset: number, s: string) => {
    for (let i = 0; i < s.length; i++) view.setUint8(offset + i, s.charCodeAt(i))
  }
  writeStr(0, 'RIFF')
  view.setUint32(4, 36 + dataLen, true)
  writeStr(8, 'WAVE')
  writeStr(12, 'fmt ')
  view.setUint32(16, 16, true) // PCM fmt chunk size
  view.setUint16(20, 1, true) // PCM
  view.setUint16(22, 1, true) // mono
  view.setUint32(24, sampleRate, true)
  view.setUint32(28, sampleRate * 2, true) // byte rate = rate * blockAlign
  view.setUint16(32, 2, true) // block align (mono 16-bit)
  view.setUint16(34, 16, true) // bits per sample
  writeStr(36, 'data')
  view.setUint32(40, dataLen, true)
  let offset = 44
  for (let i = 0; i < samples.length; i++) {
    const clamped = Math.max(-1, Math.min(1, samples[i]))
    view.setInt16(offset, clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff, true)
    offset += 2
  }
  return new Blob([buffer], { type: 'audio/wav' })
}
