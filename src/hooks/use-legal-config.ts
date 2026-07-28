import { useEffect, useState } from 'react'
import { authApi } from '@/api'

export const DEFAULT_CONTACT_EMAIL = 'admin@aivory.local'

export interface LegalConfig {
  contactEmail: string
  termsText: string
  privacyText: string
}

const fallbackConfig: LegalConfig = {
  contactEmail: DEFAULT_CONTACT_EMAIL,
  termsText: '',
  privacyText: '',
}

function normalizedEmail(value: unknown): string {
  if (typeof value !== 'string') return DEFAULT_CONTACT_EMAIL
  const email = value.trim()
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) ? email : DEFAULT_CONTACT_EMAIL
}

function loadLegalConfig(): Promise<LegalConfig> {
  return authApi
    .legalConfig()
    .then((value) => ({
      contactEmail: normalizedEmail(value.contact_email),
      termsText: typeof value.terms_text === 'string' ? value.terms_text.trim() : '',
      privacyText: typeof value.privacy_text === 'string' ? value.privacy_text.trim() : '',
    }))
    .catch(() => fallbackConfig)
}

/** Public legal configuration is optional; network failures keep usable defaults. */
export function useLegalConfig(): LegalConfig {
  const [config, setConfig] = useState<LegalConfig>(fallbackConfig)

  useEffect(() => {
    let active = true
    void loadLegalConfig().then((value) => {
      if (active) setConfig(value)
    })
    return () => {
      active = false
    }
  }, [])

  return config
}
