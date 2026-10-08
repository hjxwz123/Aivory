const semver = require('semver')

function selectUpdate(manifest, { version, platform, arch }) {
  const next = semver.valid(manifest?.version)
  const os = { darwin: 'macos', win32: 'windows', linux: 'linux' }[platform]
  if (manifest?.enabled !== true || !semver.valid(version) || !next || !os || !['x64', 'arm64'].includes(arch)) return null
  if (!semver.gt(next, version) || (semver.prerelease(version) === null && semver.prerelease(next) !== null)) return null
  const address = manifest.downloads?.[`${os}_${arch}`]
  if (typeof address !== 'string' || address.length > 2048) return null
  try {
    const url = new URL(address)
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.hash) return null
    return { version: next, url: url.href }
  } catch { return null }
}

class UpdateChecker {
  constructor({ fetch, version, platform, arch, baseUrl, showResult }) {
    Object.assign(this, { fetch, version, platform, arch, baseUrl, showResult })
    this.promptedVersion = null
  }

  async check(manual = false) {
    if (!this.pending) {
      this.pending = this.find().finally(() => { this.pending = undefined })
    }
    const result = await this.pending
    if (manual || (result.status === 'available' && result.update.version !== this.promptedVersion)) {
      if (result.status === 'available') this.promptedVersion = result.update.version
      await this.showResult(result, manual)
    }
    return { status: result.status, version: result.update?.version }
  }

  async find() {
    try {
      const response = await this.fetch(new URL('/api/public/desktop-update', this.baseUrl).href, {
        headers: { Accept: 'application/json', 'User-Agent': `Aivory-Desktop/${this.version}` },
        credentials: 'omit', redirect: 'error', cache: 'no-store', signal: AbortSignal.timeout(10000),
      })
      if (!response.ok) throw new Error('release_check_failed')
      const update = selectUpdate(await response.json(), this)
      return update ? { status: 'available', update } : { status: 'current' }
    } catch {
      return { status: 'failed' }
    }
  }
}

module.exports = { selectUpdate, UpdateChecker }
