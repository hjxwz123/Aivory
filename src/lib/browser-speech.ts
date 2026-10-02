// Browser-side speech recognition (§ voice) through the Web Speech API.
//
// Recognition runs inside the browser (Chrome and Edge send the audio to their
// vendor's speech service, Safari may recognise on-device): nothing reaches our
// server and nothing is billed. Support varies — Firefox has no implementation
// — so callers check browserSpeechSupported() and fall back to the server
// engine when it is missing.

interface SpeechRecognitionAlternativeLike {
  transcript: string
}
interface SpeechRecognitionResultLike {
  readonly isFinal: boolean
  readonly length: number
  [index: number]: SpeechRecognitionAlternativeLike
}
interface SpeechRecognitionEventLike {
  readonly results: { readonly length: number; [index: number]: SpeechRecognitionResultLike }
}
interface SpeechRecognitionErrorEventLike {
  readonly error: string
}
interface SpeechRecognitionLike {
  lang: string
  continuous: boolean
  interimResults: boolean
  maxAlternatives: number
  onstart: (() => void) | null
  onresult: ((event: SpeechRecognitionEventLike) => void) | null
  onerror: ((event: SpeechRecognitionErrorEventLike) => void) | null
  onend: (() => void) | null
  start(): void
  stop(): void
  abort(): void
}
type SpeechRecognitionCtor = new () => SpeechRecognitionLike

function recognitionCtor(): SpeechRecognitionCtor | undefined {
  if (typeof window === 'undefined') return undefined
  const w = window as unknown as {
    SpeechRecognition?: SpeechRecognitionCtor
    webkitSpeechRecognition?: SpeechRecognitionCtor
  }
  return w.SpeechRecognition ?? w.webkitSpeechRecognition
}

export function browserSpeechSupported(): boolean {
  return Boolean(recognitionCtor())
}

// The app's UI languages mapped to BCP 47 recognition languages.
const SPEECH_LANGS: Record<string, string> = {
  zh: 'zh-CN',
  'zh-Hans': 'zh-CN',
  'zh-Hant': 'zh-TW',
  ja: 'ja-JP',
  fr: 'fr-FR',
  en: 'en-US',
}

/** The recognition language for the current UI language. */
export function speechLangFor(uiLanguage: string | undefined): string {
  if (uiLanguage && SPEECH_LANGS[uiLanguage]) return SPEECH_LANGS[uiLanguage]
  if (uiLanguage && uiLanguage.includes('-')) return uiLanguage
  return (typeof navigator !== 'undefined' && navigator.language) || 'en-US'
}

const CJK = /[\u3000-\u303f\u3040-\u30ff\u3400-\u9fff\uf900-\ufaff\uac00-\ud7af\uff00-\uffef]/

/** Joins transcript pieces: no space next to CJK text, one space otherwise. */
export function joinTranscript(left: string, right: string): string {
  const a = left.trimEnd()
  const b = right.trimStart()
  if (!a) return b
  if (!b) return a
  return CJK.test(a.slice(-1)) || CJK.test(b[0]) ? a + b : `${a} ${b}`
}

export type BrowserSpeechError = 'permission' | 'no-microphone' | 'network' | 'unsupported' | 'failed'

export interface BrowserSpeechHandlers {
  /** The browser started listening (after any permission prompt). */
  onStart?: () => void
  /** The whole transcript so far, interim words included. */
  onPartial?: (text: string) => void
  /** The settled transcript, once, when listening ends without an error. */
  onFinal?: (text: string) => void
  /** Listening failed; onEnd follows. */
  onError?: (error: BrowserSpeechError) => void
  /** Listening is over (after final or error). Fires once. */
  onEnd?: () => void
}

export interface BrowserSpeechController {
  /** Stop listening and deliver the final transcript. */
  stop: () => void
  /** Abort silently, without any further callbacks (e.g. unmount). */
  cancel: () => void
}

// Chrome ends a continuous session on its own after a stretch of silence or
// about a minute of speech. Sessions are restarted until the user stops, but an
// engine that keeps ending immediately is given up on instead of looping.
const QUICK_END_MS = 1500
const MAX_QUICK_ENDS = 3
// Safety net when the engine never reports the end after stop().
const STOP_TIMEOUT_MS = 4000

const ERRORS: Record<string, BrowserSpeechError> = {
  'not-allowed': 'permission',
  'service-not-allowed': 'permission',
  'audio-capture': 'no-microphone',
  network: 'network',
  'language-not-supported': 'unsupported',
}

/** Starts listening. Throws Error('unsupported') when the browser has no engine. */
export function startBrowserSpeech(lang: string, handlers: BrowserSpeechHandlers): BrowserSpeechController {
  const available = recognitionCtor()
  if (!available) throw new Error('unsupported')
  const Ctor: SpeechRecognitionCtor = available

  let committed = '' // final text of sessions that already ended
  let sessionFinal = ''
  let interim = ''
  let started = false
  let stopRequested = false
  let finished = false
  let quickEnds = 0
  let sessionStartedAt = 0
  let stopTimer: ReturnType<typeof setTimeout> | undefined
  let recognition: SpeechRecognitionLike | null = null

  const transcript = () => joinTranscript(joinTranscript(committed, sessionFinal), interim)

  function detach() {
    if (stopTimer) clearTimeout(stopTimer)
    const current = recognition
    recognition = null
    if (!current) return
    current.onstart = current.onresult = current.onerror = current.onend = null
    try {
      current.abort()
    } catch {
      /* already ended */
    }
  }

  function finish(error?: BrowserSpeechError) {
    if (finished) return
    finished = true
    const text = transcript()
    detach()
    if (error) handlers.onError?.(error)
    else handlers.onFinal?.(text)
    handlers.onEnd?.()
  }

  function startSession() {
    const session = new Ctor()
    session.lang = lang
    session.continuous = true
    session.interimResults = true
    session.maxAlternatives = 1
    sessionFinal = ''
    interim = ''
    sessionStartedAt = Date.now()
    session.onstart = () => {
      if (started) return
      started = true
      handlers.onStart?.()
    }
    session.onresult = (event) => {
      let finalText = ''
      let interimText = ''
      for (let i = 0; i < event.results.length; i++) {
        const result = event.results[i]
        const text = result[0]?.transcript ?? ''
        if (result.isFinal) finalText = joinTranscript(finalText, text)
        else interimText = joinTranscript(interimText, text)
      }
      sessionFinal = finalText
      interim = interimText
      handlers.onPartial?.(transcript())
    }
    session.onerror = (event) => {
      // "no-speech" and "aborted" are followed by onend, which decides.
      if (event.error === 'no-speech' || event.error === 'aborted') return
      finish(ERRORS[event.error] ?? 'failed')
    }
    session.onend = () => {
      if (finished) return
      committed = joinTranscript(committed, sessionFinal)
      sessionFinal = ''
      if (stopRequested) {
        // Keep words the engine had not finalized when the user stopped.
        committed = joinTranscript(committed, interim)
        interim = ''
        finish()
        return
      }
      interim = ''
      quickEnds = Date.now() - sessionStartedAt < QUICK_END_MS ? quickEnds + 1 : 0
      if (quickEnds >= MAX_QUICK_ENDS) {
        finish()
        return
      }
      try {
        startSession()
      } catch {
        finish('failed')
      }
    }
    recognition = session
    session.start()
  }

  try {
    startSession()
  } catch {
    detach()
    throw new Error('unsupported')
  }

  return {
    stop() {
      if (finished || stopRequested) return
      stopRequested = true
      try {
        recognition?.stop()
      } catch {
        finish()
        return
      }
      stopTimer = setTimeout(() => finish(), STOP_TIMEOUT_MS)
    },
    cancel() {
      finished = true
      detach()
    },
  }
}
