const BASE_URL_ENV = 'AIVORY_DESKTOP_BASE_URL'

function normalizeBaseUrl(value) {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${BASE_URL_ENV} is required. Set it to your Aivory website URL.`)
  }
  if (!/^https?:\/\//i.test(value.trim())) {
    throw new Error(`${BASE_URL_ENV} must start with http:// or https://.`)
  }
  const url = new URL(value.trim())
  if (url.username || url.password || url.search || url.hash || url.pathname !== '/') {
    throw new Error(`${BASE_URL_ENV} must be a website origin without credentials, paths, queries or fragments (for example https://chat.example.com).`)
  }
  return `${url.origin}/`
}

function isTrustedUrl(value, baseUrl) {
  try {
    const url = new URL(value)
    return ['http:', 'https:', 'blob:'].includes(url.protocol) && !url.username && !url.password
      && url.origin === new URL(baseUrl).origin
  } catch {
    return false
  }
}

function isWebUrl(value) {
  try {
    const url = new URL(value)
    return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password
  } catch {
    return false
  }
}

function popupAction(value, baseUrl) {
  if (isTrustedUrl(value, baseUrl) || value === 'about:blank') return 'allow'
  if (isWebUrl(value)) return 'external'
  return 'deny'
}

function permissionAllowed(requestingUrl, baseUrl, permission, mediaTypes = []) {
  if (!isTrustedUrl(requestingUrl, baseUrl)) return false
  if (permission === 'media') return mediaTypes.length > 0 && mediaTypes.every((type) => type === 'audio')
  return ['clipboard-sanitized-write', 'fullscreen'].includes(permission)
}

module.exports = { BASE_URL_ENV, normalizeBaseUrl, isTrustedUrl, isWebUrl, popupAction, permissionAllowed }
