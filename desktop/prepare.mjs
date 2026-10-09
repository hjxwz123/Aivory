import { readFile, writeFile, mkdir, copyFile, cp, rm } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import dotenv from 'dotenv'
import { Resvg } from '@resvg/resvg-js'
import { WifiOff, RefreshCw, ExternalLink, ArrowRight } from 'lucide'
import policy from './policy.cjs'

export const desktopDir = fileURLToPath(new URL('.', import.meta.url))
export const appDir = path.join(desktopDir, 'generated', 'app')

export async function prepareApp(outputDir = appDir, { webDir = path.join(desktopDir, '..', 'dist-desktop') } = {}) {
  // Process variables take precedence; local overrides take precedence over .env.
  dotenv.config({ path: [path.join(desktopDir, '.env.local'), path.join(desktopDir, '.env')], quiet: true })
  const address = process.env[policy.BASE_URL_ENV]?.trim()
  const baseUrl = address ? policy.normalizeBaseUrl(address) : ''
  const rootPackage = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'))
  await mkdir(path.join(outputDir, 'assets'), { recursive: true })
  for (const file of ['main.cjs', 'policy.cjs', 'locales.cjs', 'connection.cjs',
    'preload.cjs', 'offline-preload.cjs', 'offline.html', 'offline.css', 'offline.js', 'updates.cjs', 'browser-auth.cjs',
    'server-config.cjs', 'server-preload.cjs', 'server.html', 'server.css', 'server.js', 'window-chrome.cjs', 'window-lifecycle.cjs', 'local-web.cjs', 'audio-socket.cjs', 'api-requests.cjs']) {
    await copyFile(path.join(desktopDir, file), path.join(outputDir, file))
  }
  // Installers always carry the frontend. Never fall back to a server website.
  await readFile(path.join(webDir, 'index.html'))
  await rm(path.join(outputDir, 'web'), { recursive: true, force: true })
  await cp(webDir, path.join(outputDir, 'web'), { recursive: true })
  const iconSvg = await readFile(new URL('./assets/icon.svg', import.meta.url))
  await writeFile(path.join(outputDir, 'assets', 'icon.png'), new Resvg(iconSvg).render().asPng())
  const mark = await readFile(new URL('./assets/mark.svg', import.meta.url))
  await writeFile(path.join(outputDir, 'assets', 'trayTemplate.png'), new Resvg(mark, { fitTo: { mode: 'width', value: 22 } }).render().asPng())
  await writeFile(path.join(outputDir, 'assets', 'trayTemplate@2x.png'), new Resvg(mark, { fitTo: { mode: 'width', value: 44 } }).render().asPng())
  await copyFile(new URL('./assets/mark.svg', import.meta.url), path.join(outputDir, 'assets', 'mark.svg'))
  await copyFile(new URL('../src/assets/brand/aivory-wordmark.svg', import.meta.url), path.join(outputDir, 'assets', 'wordmark.svg'))
  await copyFile(new URL('../src/styles/tokens.css', import.meta.url), path.join(outputDir, 'assets', 'tokens.css'))
  for (const [name, nodes] of [['wifi-off', WifiOff], ['refresh-cw', RefreshCw], ['external-link', ExternalLink], ['arrow-right', ArrowRight]]) {
    const shapes = nodes.map(([tag, attributes]) => `<${tag} ${Object.entries(attributes)
      .map(([key, value]) => `${key}="${value}"`).join(' ')}/>`).join('')
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${shapes}</svg>\n`
    await writeFile(path.join(outputDir, 'assets', `${name}.svg`), svg)
  }
  const require = createRequire(import.meta.url)
  const dependencies = {}
  for (const name of ['semver', 'ws']) {
    const directory = path.dirname(require.resolve(`${name}/package.json`))
    await cp(directory, path.join(outputDir, 'node_modules', name), { recursive: true })
    dependencies[name] = JSON.parse(await readFile(path.join(directory, 'package.json'), 'utf8')).version
  }
  await writeFile(path.join(outputDir, 'config.json'), JSON.stringify({ baseUrl }, null, 2) + '\n')
  await writeFile(path.join(outputDir, 'package.json'), JSON.stringify({
    name: 'aivory-desktop',
    productName: 'Aivory',
    version: rootPackage.version,
    description: 'Aivory desktop client',
    homepage: 'https://github.com/hjxwz123/Aivory',
    author: 'Aivory contributors',
    license: 'Apache-2.0',
    main: 'main.cjs',
    dependencies,
    private: true,
  }, null, 2) + '\n')
  return { baseUrl, version: rootPackage.version }
}
