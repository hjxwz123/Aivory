/**
 * Validate and normalize an OpenAI-compatible channel base URL.
 *
 * Empty values are kept because the server supplies the official default.
 * Non-empty values must point at the API-version root, not just a host or a
 * concrete endpoint such as /v1/chat/completions.
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

  const pathname = parsed.pathname.replace(/\/+$/, '')
  if (!pathname.endsWith('/v1')) return null

  parsed.pathname = pathname
  return parsed.toString()
}
