const { readFileSync, writeFileSync, mkdirSync, renameSync, rmSync } = require('node:fs')
const path = require('node:path')
const { normalizeBaseUrl } = require('./policy.cjs')

function readServerConfig(directory) {
  try {
    const raw = readFileSync(path.join(directory, 'server.json'), 'utf8')
    if (raw.length > 4096) return undefined
    return normalizeBaseUrl(JSON.parse(raw).baseUrl)
  } catch { return undefined }
}

function saveServerConfig(directory, address) {
  const baseUrl = normalizeBaseUrl(address)
  mkdirSync(directory, { recursive: true })
  const temporary = path.join(directory, 'server.json.tmp')
  try {
    writeFileSync(temporary, JSON.stringify({ baseUrl }) + '\n', { mode: 0o600 })
    renameSync(temporary, path.join(directory, 'server.json'))
  } finally { rmSync(temporary, { force: true }) }
  return baseUrl
}

function resolveServerConfig(directory, buildDefault) {
  const saved = readServerConfig(directory)
  if (saved) return saved
  if (!buildDefault) return undefined
  let normalized
  try { normalized = normalizeBaseUrl(buildDefault) } catch { return undefined }
  return saveServerConfig(directory, normalized)
}

function desktopUserAgent(userAgent, version) {
  return `${userAgent.replace(/\s*AivoryDesktop\/[^\s]+/g, '')} AivoryDesktop/${version}`
}

module.exports = { readServerConfig, saveServerConfig, resolveServerConfig, desktopUserAgent }
