import { describe, expect, it } from 'vitest'
import { defaultSpeechEngine, resolveSpeechEngine, speechEnginePreference } from '@/lib/speech-recognition'

describe('speech engine selection', () => {
  it('reads only the two known preferences', () => {
    expect(speechEnginePreference({ speech_recognition: 'model' })).toBe('model')
    expect(speechEnginePreference({ speech_recognition: 'browser' })).toBe('browser')
    expect(speechEnginePreference({ speech_recognition: '' })).toBeNull()
    expect(speechEnginePreference({ speech_recognition: 'whisper' })).toBeNull()
    expect(speechEnginePreference(undefined)).toBeNull()
  })

  it('defaults to the model engine only when an administrator configured it', () => {
    expect(defaultSpeechEngine(true)).toBe('model')
    expect(defaultSpeechEngine(false)).toBe('browser')
    expect(resolveSpeechEngine(null, { serverEnabled: true, browserSupported: true })).toBe('model')
    expect(resolveSpeechEngine(null, { serverEnabled: false, browserSupported: true })).toBe('browser')
  })

  it('honours an explicit choice when it works here', () => {
    expect(resolveSpeechEngine('browser', { serverEnabled: true, browserSupported: true })).toBe('browser')
    expect(resolveSpeechEngine('model', { serverEnabled: true, browserSupported: false })).toBe('model')
  })

  it('falls back to the other engine, and to nothing when neither works', () => {
    expect(resolveSpeechEngine('model', { serverEnabled: false, browserSupported: true })).toBe('browser')
    // e.g. Firefox, which has no Web Speech recognition.
    expect(resolveSpeechEngine('browser', { serverEnabled: true, browserSupported: false })).toBe('model')
    expect(resolveSpeechEngine(null, { serverEnabled: false, browserSupported: false })).toBeNull()
  })
})
