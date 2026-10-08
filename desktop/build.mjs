import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import path from 'node:path'
import { desktopDir, prepareApp } from './prepare.mjs'
import { buildFrontend } from './frontend.mjs'

const require = createRequire(import.meta.url)

try {
  await buildFrontend()
  const { baseUrl, version } = await prepareApp()
  console.log(`Building Aivory ${version} desktop for ${baseUrl || 'first-launch server setup'}`)
  const child = spawn(process.execPath, [
    require.resolve('electron-builder/cli.js'),
    '--config', path.join(desktopDir, 'electron-builder.cjs'),
    ...process.argv.slice(2),
    '--publish', 'never',
  ], { cwd: desktopDir, stdio: 'inherit', env: process.env })
  child.on('error', (error) => { console.error(error.message); process.exitCode = 1 })
  child.on('exit', (code, signal) => { process.exitCode = signal ? 1 : code ?? 1 })
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal))
} catch (error) {
  console.error(error.message)
  process.exitCode = 1
}
