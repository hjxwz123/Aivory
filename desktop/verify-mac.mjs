import { readdir, mkdtemp, rm } from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import path from 'node:path'
import os from 'node:os'

// Verify the bundles users actually install, as well as the unpacked output.
// Missing artifacts and any invalid nested signature must fail the CI build.
if (process.platform !== 'darwin') throw new Error('macOS is required to verify Apple signatures')
const output = path.resolve(process.argv[2] || 'desktop/release')
const entries = await readdir(output, { withFileTypes: true })
const verify = (bundle) => execFileSync('codesign', ['--verify', '--deep', '--strict', '--verbose=2', bundle], { stdio: 'inherit' })
let bundles = 0
let archives = 0
let images = 0
for (const entry of entries) {
  if (entry.isDirectory()) {
    const app = path.join(output, entry.name, 'Aivory.app')
    if ((await readdir(path.dirname(app))).includes('Aivory.app')) { verify(app); bundles++ }
  }
  if (/^Aivory-.*\.zip$/.test(entry.name)) {
    const folder = await mkdtemp(path.join(os.tmpdir(), 'aivory-verify-zip-'))
    try {
      execFileSync('ditto', ['-x', '-k', path.join(output, entry.name), folder], { stdio: 'inherit' })
      verify(path.join(folder, 'Aivory.app'))
      archives++
    } finally { await rm(folder, { recursive: true, force: true }) }
  }
  if (/^Aivory-.*\.dmg$/.test(entry.name)) {
    const folder = await mkdtemp(path.join(os.tmpdir(), 'aivory-verify-dmg-'))
    let mounted = false
    try {
      execFileSync('hdiutil', ['attach', '-readonly', '-nobrowse', '-mountpoint', folder, path.join(output, entry.name)], { stdio: 'inherit' })
      mounted = true
      verify(path.join(folder, 'Aivory.app'))
      images++
    } finally {
      if (mounted) execFileSync('hdiutil', ['detach', folder], { stdio: 'inherit' })
      await rm(folder, { recursive: true, force: true })
    }
  }
}
if (!bundles || !archives || !images) throw new Error(`Missing Mac output: ${bundles} bundles, ${archives} ZIPs, ${images} DMGs`)
console.log(`Verified ${bundles} bundles, ${archives} ZIPs, and ${images} DMGs`)
