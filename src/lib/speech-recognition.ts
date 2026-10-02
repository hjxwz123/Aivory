// Speech-to-text engine selection (§ voice).
//
// Two engines feed the composer microphone:
//  - "model": the administrator's server-side service (an OpenAI-compatible
//    upload or Volcano live streaming). It may cost credits per second.
//  - "browser": the browser's own Web Speech engine (lib/browser-speech.ts).
//    Free, and it never reaches our server.
// Users pick a default in Settings → Models (users.settings.speech_recognition).
// Unset means automatic: the model engine when an administrator configured
// one, otherwise the browser's. A chosen engine that is unavailable here falls
// back to the other one.

import { audioApi } from '@/api/endpoints'

export type SpeechEngine = 'model' | 'browser'

/** What the server-side engine offers this caller. */
export interface SttCapability {
  /** "gpt" (record then transcribe) or "volcano" (live streaming). */
  provider: string
  /** The administrator configured the service's credentials. */
  enabled: boolean
  /** What one minute costs this caller in credits; 0 = free. */
  creditsPerMinute: number
}

const UNAVAILABLE: SttCapability = { provider: 'gpt', enabled: false, creditsPerMinute: 0 }

// Shared by every composer and the settings dialog. Keyed by user because the
// price depends on the caller (administrators are never charged).
let sttCapabilityCache: { key: string; promise: Promise<SttCapability> } | null = null

export function loadSttCapability(userId: string | undefined): Promise<SttCapability> {
  const key = userId ?? ''
  if (!sttCapabilityCache || sttCapabilityCache.key !== key) {
    sttCapabilityCache = {
      key,
      promise: audioApi
        .capabilities()
        .then((c) => ({
          provider: c.provider || 'gpt',
          enabled: Boolean(c.enabled),
          creditsPerMinute: typeof c.credits_per_minute === 'number' && c.credits_per_minute > 0 ? c.credits_per_minute : 0,
        }))
        .catch(() => UNAVAILABLE),
    }
  }
  return sttCapabilityCache.promise
}

/** The user's saved choice, or null for automatic. */
export function speechEnginePreference(settings: Record<string, unknown> | null | undefined): SpeechEngine | null {
  const value = settings?.speech_recognition
  return value === 'model' || value === 'browser' ? value : null
}

/** The automatic default: the administrator's service when it is configured. */
export function defaultSpeechEngine(serverEnabled: boolean): SpeechEngine {
  return serverEnabled ? 'model' : 'browser'
}

/**
 * The engine the microphone uses: the user's choice (or the automatic default)
 * when it works here, otherwise the other engine; null when neither does.
 */
export function resolveSpeechEngine(
  preference: SpeechEngine | null,
  availability: { serverEnabled: boolean; browserSupported: boolean },
): SpeechEngine | null {
  const available = (engine: SpeechEngine) =>
    engine === 'model' ? availability.serverEnabled : availability.browserSupported
  const wanted = preference ?? defaultSpeechEngine(availability.serverEnabled)
  if (available(wanted)) return wanted
  const other: SpeechEngine = wanted === 'model' ? 'browser' : 'model'
  return available(other) ? other : null
}
