type HeaderError = 'invalidJson' | 'invalidObject' | 'invalidName' | 'invalidValue' | 'duplicateName' | 'transportManaged' | 'tooLarge'
type HeaderResult = { headers: Record<string, string>; error?: undefined } | { headers?: undefined; error: HeaderError }

export function parseChannelHeaders(text: string): HeaderResult {
  let value: unknown
  try {
    value = JSON.parse(text.trim() || '{}')
  } catch {
    return { error: 'invalidJson' }
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return { error: 'invalidObject' }
  const entries = Object.entries(value)
  if (entries.length > 64) return { error: 'tooLarge' }
  const names = new Set<string>()
  let size = 0
  for (const [name, content] of entries) {
    if (!/^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/.test(name)) return { error: 'invalidName' }
    if (typeof content !== 'string' || /[\x00-\x08\x0a-\x1f\x7f]/.test(content)) return { error: 'invalidValue' }
    const key = name.toLowerCase()
    if (names.has(key)) return { error: 'duplicateName' }
    if (['content-length', 'transfer-encoding', 'trailer'].includes(key)) return { error: 'transportManaged' }
    names.add(key)
    size += new TextEncoder().encode(name + content).length
    if (size > 16 * 1024) return { error: 'tooLarge' }
  }
  return { headers: Object.fromEntries(entries) as Record<string, string> }
}
