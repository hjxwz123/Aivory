const { app, BrowserWindow, Menu, dialog, shell, session, nativeTheme, screen, Tray, nativeImage, ipcMain, net } = require('electron')
const { readFileSync } = require('node:fs')
const path = require('node:path')
const { createHash } = require('node:crypto')
const { pathToFileURL } = require('node:url')
const { normalizeBaseUrl, isTrustedUrl, isWebUrl, popupAction, permissionAllowed } = require('./policy.cjs')
const { getMessages } = require('./locales.cjs')
const { ConnectionStatus } = require('./connection.cjs')
const { UpdateChecker } = require('./updates.cjs')
const { BrowserAuth } = require('./browser-auth.cjs')
const { resolveServerConfig, saveServerConfig, desktopUserAgent } = require('./server-config.cjs')
const { windowChromeOptions, attachWindowChrome } = require('./window-chrome.cjs')
const { APP_URL, localAppUrl, createLocalHandler } = require('./local-web.cjs')
const { registerAudioSocketBridge } = require('./audio-socket.cjs')
const { ApiRequests } = require('./api-requests.cjs')

app.setName('Aivory')
let mainWindow
let serverWindow
let baseUrl
let desktopSession
let messages
let connection
let isQuitting = false
let tray
let browserAuth
let updateChecker
let updateTimer
let updateStartupTimer
let minimizeTimer
let minimizeRequested = false
const configuredSessions = new WeakSet()
const paymentContents = new WeakSet()
const apiRequests = new ApiRequests()

function restoreWindow() {
  minimizeRequested = false
  clearTimeout(minimizeTimer)
  if (!desktopSession) { openServerSettings(); return }
  if (!mainWindow) createWindow()
  if (mainWindow.isMinimized()) mainWindow.restore()
  mainWindow.show()
  mainWindow.focus()
}

function nativeMessages() { return getMessages(connection?.appearance.locale || app.getLocale()) }

function installIconMenu() {
  const menu = Menu.buildFromTemplate([
    { label: messages.showApp, click: restoreWindow },
    { label: messages.serverSettings, click: openServerSettings },
    { label: messages.checkUpdates, enabled: Boolean(baseUrl), click: () => void updateChecker?.check(true) },
    { type: 'separator' },
    { label: messages.quit, click: () => app.quit() },
  ])
  if (process.platform === 'darwin') {
    app.dock.setMenu(menu)
  }
  if (!tray) {
    const icon = process.platform === 'darwin'
      ? nativeImage.createFromPath(path.join(app.getAppPath(), 'assets', 'trayTemplate.png'))
      : nativeImage.createFromPath(path.join(app.getAppPath(), 'assets', 'icon.png')).resize({ width: 32, height: 32 })
    if (process.platform === 'darwin') icon.setTemplateImage(true)
    tray = new Tray(icon)
    tray.setToolTip('Aivory')
    if (process.platform !== 'darwin') tray.on('click', restoreWindow)
    tray.on('double-click', restoreWindow)
    if (process.platform === 'win32' && app.isPackaged) {
      app.setJumpList([{ type: 'tasks', items: [{
        type: 'task', title: messages.quit, description: messages.quit,
        program: process.execPath, args: '--quit', iconPath: process.execPath, iconIndex: 0,
      }] }])
    }
  }
  tray.setContextMenu(menu)
}

function openServerSettings() {
  if (serverWindow && !serverWindow.isDestroyed()) {
    if (serverWindow.isMinimized()) serverWindow.restore()
    serverWindow.show()
    serverWindow.focus()
    return
  }
  serverWindow = new BrowserWindow({
    title: '', ...windowSize(560, 460),
    ...windowChromeOptions(),
    backgroundColor: nativeTheme.shouldUseDarkColors ? '#111113' : '#ffffff',
    icon: path.join(app.getAppPath(), 'assets', 'icon.png'), autoHideMenuBar: true,
    webPreferences: {
      nodeIntegration: false, contextIsolation: true, sandbox: true, webSecurity: true,
      preload: path.join(app.getAppPath(), 'server-preload.cjs'),
    },
  })
  attachWindowChrome(serverWindow.webContents)
  serverWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
  serverWindow.webContents.on('will-navigate', (event) => event.preventDefault())
  serverWindow.on('query-session-end', () => { isQuitting = true })
  serverWindow.on('session-end', () => { isQuitting = true })
  serverWindow.on('close', (event) => {
    if (!baseUrl && !isQuitting) { event.preventDefault(); serverWindow.minimize() }
  })
  serverWindow.on('closed', () => { serverWindow = undefined })
  void serverWindow.loadFile(path.join(app.getAppPath(), 'server.html'))
}

function registerServerBridge() {
  const handle = (channel, callback) => ipcMain.handle(channel, (event, ...args) => {
    if (!serverWindow || event.sender !== serverWindow.webContents
      || event.senderFrame !== serverWindow.webContents.mainFrame
      || event.senderFrame.url !== pathToFileURL(path.join(app.getAppPath(), 'server.html')).href) {
      throw new Error('Untrusted server configuration request')
    }
    return callback(...args)
  })
  handle('desktop:server-info', () => ({
    baseUrl: baseUrl || '', messages: nativeMessages(), locale: connection?.appearance.locale || app.getLocale(),
    theme: connection?.appearance.theme || (nativeTheme.shouldUseDarkColors ? 'dark' : 'light'),
    accent: connection?.appearance.accent || 'violet',
  }))
  handle('desktop:server-save', (address) => {
    let normalized
    try { normalized = normalizeBaseUrl(address) } catch { return { error: 'serverInvalid' } }
    try { saveServerConfig(app.getPath('userData'), normalized) } catch { return { error: 'serverSaveFailed' } }
    // Keep drafts intact when settings are opened or the same server is saved.
    if (normalized !== baseUrl) configureServer(normalized)
    setImmediate(() => { serverWindow?.close(); restoreWindow() })
    return { ok: true }
  })
}

function registerDesktopBridge() {
  const localFrontend = (url) => localAppUrl(url) && !/^\/api(?:\/|$)/.test(new URL(url).pathname)
  const trusted = (event) => Boolean(mainWindow && event.sender === mainWindow.webContents
    && event.senderFrame === mainWindow.webContents.mainFrame && localFrontend(event.senderFrame.url))
  const localTransport = (event) => event.sender.session === desktopSession
    && event.senderFrame === event.sender.mainFrame && localFrontend(event.senderFrame?.url)
  registerAudioSocketBridge(ipcMain, trusted, () => ({ baseUrl, session: desktopSession }))
  ipcMain.on('desktop:api-start', (event, id) => { if (localTransport(event)) apiRequests.start(id, event.sender) })
  ipcMain.on('desktop:api-abort', (event, id) => { if (localTransport(event)) apiRequests.abort(id, event.sender) })
  ipcMain.on('desktop:runtime', (event) => {
    event.returnValue = localTransport(event) ? { serverBaseUrl: baseUrl } : null
  })
  const handle = (channel, callback) => ipcMain.handle(channel, (event, ...args) => {
    if (!trusted(event)) throw new Error('Untrusted desktop request')
    return callback(...args)
  })
  handle('desktop:info', () => ({ version: app.getVersion(), platform: process.platform }))
  handle('desktop:browser-login', () => browserAuth.start())
  handle('desktop:cancel-browser-login', async () => { await browserAuth.cancel(); return { status: 'cancelled' } })
  handle('desktop:check-updates', () => updateChecker.check(true))
  handle('desktop:payment', async (action) => {
    if (!action || !['redirect', 'form_post'].includes(action.type) || !isWebUrl(action.url)) throw new Error('Invalid checkout')
    const fields = action.fields ?? {}
    if (typeof fields !== 'object' || Array.isArray(fields)
      || Object.values(fields).some((value) => typeof value !== 'string')) throw new Error('Invalid checkout fields')
    const payment = new BrowserWindow({
      title: '', ...windowSize(1000, 760), ...windowChromeOptions(), autoHideMenuBar: true,
      webPreferences: { session: desktopSession, nodeIntegration: false, contextIsolation: true, sandbox: true, webSecurity: true },
    })
    paymentContents.add(payment.webContents)
    if (action.type === 'form_post') {
      await payment.loadURL(action.url, {
        postData: [{ type: 'rawData', bytes: Buffer.from(new URLSearchParams(fields).toString()) }],
        extraHeaders: 'Content-Type: application/x-www-form-urlencoded',
      })
    } else await payment.loadURL(action.url)
  })
}

function windowSize(width, height) {
  const area = screen.getDisplayNearestPoint(screen.getCursorScreenPoint()).workAreaSize
  return {
    width: Math.min(width, area.width), height: Math.min(height, area.height),
    minWidth: Math.min(360, area.width), minHeight: Math.min(280, area.height),
  }
}

const rendererOptions = () => ({
  session: desktopSession,
  nodeIntegration: false,
  contextIsolation: true,
  sandbox: true,
  webSecurity: true,
  webviewTag: false,
  preload: path.join(app.getAppPath(), 'preload.cjs'),
})

function openBrowser(url) {
  if (isWebUrl(url)) void shell.openExternal(url).catch((error) => console.error(error.message))
}

function protectContents(contents) {
  attachWindowChrome(contents)
  contents.on('page-title-updated', (event) => event.preventDefault())
  contents.setWindowOpenHandler(({ url }) => {
    const action = popupAction(url, APP_URL)
    if (action === 'external') openBrowser(url)
    return action === 'allow'
      ? { action: 'allow', overrideBrowserWindowOptions: {
        title: '', autoHideMenuBar: true, ...windowSize(1100, 800),
        ...windowChromeOptions(),
        webPreferences: rendererOptions(),
      } }
      : { action: 'deny' }
  })
  // Application routes stay local; websites open in the system browser.
  const guardNavigation = (event, url) => {
    if (paymentContents.has(contents) && isWebUrl(url)) {
      const target = new URL(url)
      if (target.origin === new URL(baseUrl).origin && target.pathname === '/subscription') {
        event.preventDefault()
        restoreWindow()
        mainWindow.webContents.send('desktop:payment-return', target.pathname + target.search)
        setImmediate(() => BrowserWindow.fromWebContents(contents)?.close())
      }
      return
    }
    if (!isTrustedUrl(url, APP_URL)) {
      event.preventDefault()
      if (isWebUrl(url)) openBrowser(url)
    }
  }
  contents.on('will-navigate', guardNavigation)
  contents.on('will-redirect', guardNavigation)
  contents.on('will-attach-webview', (event) => event.preventDefault())
}

async function loadServer() {
  if (!baseUrl) { openServerSettings(); return }
  const window = mainWindow
  if (!window || window.isDestroyed()) return
  try {
    await window.loadURL(APP_URL)
  } catch (error) {
    if (error.code !== 'ERR_ABORTED' && !window.isDestroyed()) connection.failed()
  }
}

function createWindow() {
  mainWindow = new BrowserWindow({
    title: '', ...windowSize(1320, 900),
    ...windowChromeOptions(),
    backgroundColor: nativeTheme.shouldUseDarkColors ? '#111113' : '#ffffff',
    icon: path.join(app.getAppPath(), 'assets', 'icon.png'),
    autoHideMenuBar: true,
    webPreferences: rendererOptions(),
  })
  connection = new ConnectionStatus(mainWindow, baseUrl, openBrowser, desktopSession)
  mainWindow.on('query-session-end', () => { isQuitting = true })
  mainWindow.on('session-end', () => { isQuitting = true })
  mainWindow.on('close', (event) => {
    if (isQuitting) return
    event.preventDefault()
    // Return from the cancelled native close before changing window state.
    const window = mainWindow
    minimizeRequested = true
    setImmediate(() => {
      if (isQuitting || !minimizeRequested || window.isDestroyed()) return
      window.minimize()
      // Some macOS window environments ignore miniaturization. Hiding only
      // this window still retains its renderer and the Dock icon.
      if (process.platform === 'darwin') {
        clearTimeout(minimizeTimer)
        minimizeTimer = setTimeout(() => {
          if (minimizeRequested && !isQuitting && !window.isDestroyed() && !window.isMinimized()) window.hide()
        }, 500)
      }
    })
  })
  mainWindow.on('closed', () => { mainWindow = undefined; connection = undefined })
  void loadServer()
}

function configureServer(address) {
  browserAuth?.cancel()
  clearTimeout(updateStartupTimer)
  clearInterval(updateTimer)
  clearTimeout(minimizeTimer)
  for (const window of BrowserWindow.getAllWindows()) {
    if (window !== serverWindow) window.destroy()
  }
  baseUrl = address
  const serverId = createHash('sha256').update(baseUrl).digest('hex').slice(0, 16)
  desktopSession = session.fromPartition(`persist:aivory-${serverId}`)
  desktopSession.setUserAgent(desktopUserAgent(desktopSession.getUserAgent(), app.getVersion()))
  if (!configuredSessions.has(desktopSession)) {
    const serverAddress = baseUrl
    const serverSession = desktopSession
    desktopSession.protocol.handle('https', createLocalHandler({
      webDir: path.join(app.getAppPath(), 'web'), baseUrl: serverAddress,
      requests: apiRequests, session: serverSession,
      fetch: (url, options) => serverSession.fetch(url, options),
      fileFetch: (url) => net.fetch(url),
      onFailure: () => { if (baseUrl === serverAddress) connection?.show('unreachable') },
    }))
    desktopSession.setPermissionRequestHandler((contents, permission, callback, details) => {
      callback(isTrustedUrl(contents.getURL(), APP_URL)
        && permissionAllowed(details.requestingUrl, APP_URL, permission, details.mediaTypes))
    })
    desktopSession.setPermissionCheckHandler((contents, permission, requestingOrigin, details) => {
      const mediaTypes = [details.mediaType || 'audio']
      return Boolean(contents && isTrustedUrl(contents.getURL(), APP_URL)
        && permissionAllowed(requestingOrigin, APP_URL, permission, mediaTypes))
    })
    desktopSession.on('will-download', (_event, item) => {
      item.setSaveDialogOptions({ defaultPath: path.join(app.getPath('downloads'), path.basename(item.getFilename())) })
    })
    configuredSessions.add(desktopSession)
  }
  const origin = baseUrl
  const serverSession = desktopSession
  browserAuth = new BrowserAuth({
    baseUrl, fetch: (url, options) => serverSession.fetch(url, options),
    openBrowser: (url) => shell.openExternal(url),
    onAuthorized: () => {
      if (origin !== baseUrl || isQuitting) return
      restoreWindow()
      mainWindow.webContents.send('desktop:authorized')
    },
  })
  updateChecker = new UpdateChecker({
    fetch: (url, options) => net.fetch(url, options),
    version: app.getVersion(), platform: process.platform, arch: process.arch,
    baseUrl,
    showResult: async (result, manual) => {
      if (origin !== baseUrl || isQuitting) return
      const m = nativeMessages()
      if (result.status === 'available') {
        const answer = await dialog.showMessageBox(mainWindow, {
          type: 'info', title: m.updateTitle,
          message: m.updateAvailable.replace('{version}', result.update.version),
          buttons: [m.updateNow, m.later], cancelId: 1,
        })
        if (answer.response === 0) await shell.openExternal(result.update.url)
      } else if (manual) {
        await dialog.showMessageBox(mainWindow, {
          type: result.status === 'failed' ? 'warning' : 'info', title: m.updateTitle,
          message: result.status === 'failed' ? m.updateFailed : m.upToDate, buttons: [m.ok],
        })
      }
    },
  })
  installMenus()
  createWindow()
  updateStartupTimer = setTimeout(() => void updateChecker.check(), 15000)
  updateTimer = setInterval(() => void updateChecker.check(), 4 * 60 * 60 * 1000)
  updateStartupTimer.unref()
  updateTimer.unref()
}

function installMenus() {
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    ...(process.platform === 'darwin' ? [{ role: 'appMenu' }] : []),
    { role: 'fileMenu' }, { role: 'editMenu' },
    { label: messages.view, submenu: [
      { label: messages.home, click: () => void loadServer() },
      { label: messages.serverSettings, click: openServerSettings },
      { role: 'reload' }, { role: 'forceReload' },
      ...(!app.isPackaged ? [{ role: 'toggleDevTools' }] : []),
      { type: 'separator' }, { role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' },
      { type: 'separator' }, { role: 'togglefullscreen' },
    ] },
    { role: 'windowMenu' },
    { role: 'help', submenu: [
      { label: messages.openBrowser, enabled: Boolean(baseUrl), click: () => openBrowser(baseUrl) },
      { label: messages.checkUpdates, enabled: Boolean(baseUrl), click: () => void updateChecker?.check(true) },
    ] },
  ]))
  installIconMenu()
}

if (!app.requestSingleInstanceLock()) {
  app.quit()
} else if (process.argv.includes('--quit')) {
  app.quit()
} else {
  app.on('second-instance', (_event, argv) => {
    if (argv.includes('--quit')) app.quit()
    else restoreWindow()
  })
  app.whenReady().then(() => {
    messages = getMessages(app.getLocale())
    let config = {}
    try {
      config = JSON.parse(readFileSync(path.join(app.getAppPath(), 'config.json'), 'utf8'))
    } catch { /* A generic package can start without a build default. */ }
    app.setAppUserModelId('com.aivory.desktop')
    if (process.platform === 'darwin') app.dock.setIcon(path.join(app.getAppPath(), 'assets', 'icon.png'))
    registerDesktopBridge()
    registerServerBridge()
    app.on('web-contents-created', (_event, contents) => protectContents(contents))
    app.on('browser-window-created', (_event, window) => {
      window.setTitle('')
      window.on('page-title-updated', (event) => {
        event.preventDefault()
        window.setTitle('')
      })
    })
    let address
    try { address = resolveServerConfig(app.getPath('userData'), config?.baseUrl) } catch (error) { console.error(error.message) }
    if (address) configureServer(address)
    else { installMenus(); openServerSettings() }
    app.on('activate', restoreWindow)
  }).catch((error) => { console.error(error); app.quit() })
  app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') app.quit()
  })
  app.on('before-quit', () => {
    isQuitting = true
    browserAuth?.cancel()
    clearTimeout(updateStartupTimer)
    clearInterval(updateTimer)
    clearTimeout(minimizeTimer)
    tray?.destroy()
  })
}
