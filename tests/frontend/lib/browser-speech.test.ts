import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  browserSpeechSupported,
  joinTranscript,
  speechLangFor,
  startBrowserSpeech,
  type BrowserSpeechHandlers,
} from '@/lib/browser-speech'

type Handler<T> = ((event: T) => void) | null

class FakeRecognition {
  static instances: FakeRecognition[] = []
  lang = ''
  continuous = false
  interimResults = false
  maxAlternatives = 1
  onstart: (() => void) | null = null
  onresult: Handler<{ results: unknown }> = null
  onerror: Handler<{ error: string }> = null
  onend: (() => void) | null = null
  started = false
  stopped = false
  aborted = false

  constructor() {
    FakeRecognition.instances.push(this)
  }
  start() {
    this.started = true
  }
  stop() {
    this.stopped = true
  }
  abort() {
    this.aborted = true
  }
  emitResults(results: Array<[string, boolean]>) {
    this.onresult?.({
      results: results.map(([transcript, isFinal]) => Object.assign([{ transcript }], { isFinal })),
    })
  }
}

function handlers() {
  return {
    onStart: vi.fn(),
    onPartial: vi.fn(),
    onFinal: vi.fn(),
    onError: vi.fn(),
    onEnd: vi.fn(),
  } satisfies BrowserSpeechHandlers
}

describe('browser speech recognition', () => {
  beforeEach(() => {
    FakeRecognition.instances = []
    vi.stubGlobal('window', { webkitSpeechRecognition: FakeRecognition })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('detects support and maps UI languages to recognition languages', () => {
    expect(browserSpeechSupported()).toBe(true)
    expect(speechLangFor('zh')).toBe('zh-CN')
    expect(speechLangFor('zh-Hant')).toBe('zh-TW')
    expect(speechLangFor('ja')).toBe('ja-JP')
    expect(speechLangFor('pt-BR')).toBe('pt-BR')
  })

  it('joins transcript pieces without spaces around CJK text', () => {
    expect(joinTranscript('hello', 'world')).toBe('hello world')
    expect(joinTranscript('你好', '世界')).toBe('你好世界')
    expect(joinTranscript('Draft ', '')).toBe('Draft')
    expect(joinTranscript('', ' text')).toBe('text')
  })

  it('streams interim text, keeps listening across engine restarts, and settles on stop', () => {
    const h = handlers()
    const controller = startBrowserSpeech('en-US', h)
    const first = FakeRecognition.instances[0]
    expect(first.started && first.continuous && first.interimResults).toBe(true)
    expect(first.lang).toBe('en-US')

    first.onstart?.()
    expect(h.onStart).toHaveBeenCalledTimes(1)
    first.emitResults([['hello', true], ['wor', false]])
    expect(h.onPartial).toHaveBeenLastCalledWith('hello wor')

    // The engine ends a session on its own: a new one starts, the text stays.
    first.onend?.()
    expect(FakeRecognition.instances).toHaveLength(2)
    const second = FakeRecognition.instances[1]
    second.onstart?.()
    expect(h.onStart).toHaveBeenCalledTimes(1)
    second.emitResults([['world', true]])
    expect(h.onPartial).toHaveBeenLastCalledWith('hello world')

    controller.stop()
    expect(second.stopped).toBe(true)
    second.onend?.()
    expect(h.onFinal).toHaveBeenCalledWith('hello world')
    expect(h.onEnd).toHaveBeenCalledTimes(1)
    expect(h.onError).not.toHaveBeenCalled()
  })

  it('keeps unfinalized words when the user stops', () => {
    const h = handlers()
    const controller = startBrowserSpeech('zh-CN', h)
    const session = FakeRecognition.instances[0]
    session.emitResults([['你好', true], ['世界', false]])
    controller.stop()
    session.onend?.()
    expect(h.onFinal).toHaveBeenCalledWith('你好世界')
  })

  it('maps a denied microphone to a permission error and ends', () => {
    const h = handlers()
    startBrowserSpeech('en-US', h)
    const session = FakeRecognition.instances[0]
    session.onerror?.({ error: 'no-speech' })
    expect(h.onError).not.toHaveBeenCalled()
    session.onerror?.({ error: 'not-allowed' })
    expect(h.onError).toHaveBeenCalledWith('permission')
    expect(h.onFinal).not.toHaveBeenCalled()
    expect(h.onEnd).toHaveBeenCalledTimes(1)
    expect(session.aborted).toBe(true)
  })

  it('gives up instead of looping when the engine keeps ending immediately', () => {
    const h = handlers()
    startBrowserSpeech('en-US', h)
    for (let i = 0; i < 3; i++) FakeRecognition.instances[i].onend?.()
    expect(FakeRecognition.instances).toHaveLength(3)
    expect(h.onEnd).toHaveBeenCalledTimes(1)
  })

  it('settles even if the engine never reports the end after stop', () => {
    vi.useFakeTimers()
    const h = handlers()
    const controller = startBrowserSpeech('en-US', h)
    FakeRecognition.instances[0].emitResults([['late', true]])
    controller.stop()
    vi.advanceTimersByTime(5000)
    expect(h.onFinal).toHaveBeenCalledWith('late')
    expect(h.onEnd).toHaveBeenCalledTimes(1)
  })

  it('cancels silently', () => {
    const h = handlers()
    const controller = startBrowserSpeech('en-US', h)
    const session = FakeRecognition.instances[0]
    controller.cancel()
    session.onend?.()
    expect(session.aborted).toBe(true)
    expect(h.onFinal).not.toHaveBeenCalled()
    expect(h.onEnd).not.toHaveBeenCalled()
  })

  it('reports an unsupported browser', () => {
    vi.stubGlobal('window', {})
    expect(browserSpeechSupported()).toBe(false)
    expect(() => startBrowserSpeech('en-US', handlers())).toThrow('unsupported')
  })
})
