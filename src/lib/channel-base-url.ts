/**
 * Validate and normalize an OpenAI-compatible channel base URL.
 *
 * Empty values are kept because the server supplies the official default.
 * Non-empty values may use any upstream API root, including /v1, /v2, /v3,
 * or a vendor-specific path. Provider code appends the resource endpoint.
 */
export function normalizeOpenAIBaseUrl(value: string): string | null {
  const trimmed = value.trim()
  if (!trimmed) return ''
  if (/\s/.test(trimmed)) return null

  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch {
    return null
  }

  if (
    !['http:', 'https:'].includes(parsed.protocol)
    || !parsed.hostname
    || parsed.username
    || parsed.password
    || parsed.search
    || parsed.hash
  ) {
    return null
  }

  return trimmed.replace(/\/+$/, '')
}
