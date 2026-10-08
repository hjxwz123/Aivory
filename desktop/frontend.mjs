import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createRequire } from 'node:module'
import path from 'node:path'
import { desktopDir } from './prepare.mjs'

export async function buildFrontend() {
  const require = createRequire(new URL('../package.json', import.meta.url))
  const vite = path.join(path.dirname(require.resolve('vite/package.json')), 'bin', 'vite.js')
  const child = spawn(process.execPath, [vite, 'build', '--outDir', 'dist-desktop'], {
    cwd: path.dirname(desktopDir), stdio: 'inherit',
    env: { ...process.env, VITE_AIVORY_DESKTOP: 'true', VITE_API_BASE: '/api',
      NODE_OPTIONS: process.env.NODE_OPTIONS || '--max-old-space-size=6144' },
  })
  const [code] = await once(child, 'exit')
  if (code !== 0) throw new Error('Desktop frontend build failed')
}
