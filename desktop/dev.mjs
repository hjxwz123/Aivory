import { spawn } from 'node:child_process'
import electron from 'electron'
import { appDir, prepareApp } from './prepare.mjs'
import { buildFrontend } from './frontend.mjs'

try {
  await buildFrontend()
  const { baseUrl } = await prepareApp()
  console.log(`Starting Aivory desktop: ${baseUrl}`)
  const env = { ...process.env }
  delete env.ELECTRON_RUN_AS_NODE
  const child = spawn(electron, [appDir, ...process.argv.slice(2)], { stdio: 'inherit', env })
  child.on('error', (error) => { console.error(error.message); process.exitCode = 1 })
  child.on('exit', (code, signal) => { process.exitCode = signal ? 1 : code ?? 1 })
  for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal))
} catch (error) {
  console.error(error.message)
  process.exitCode = 1
}
