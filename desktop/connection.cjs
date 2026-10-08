const { app, WebContentsView, ipcMain, nativeTheme, net } = require('electron')
const path = require('node:path')
const { getMessages } = require('./locales.cjs')
const { isTrustedUrl, isWebUrl } = require('./policy.cjs')
const { APP_URL, serverUrl } = require('./local-web.cjs')

class ConnectionStatus {
  constructor(window, baseUrl, openBrowser, serverSession) {
    this.window = window
    this.baseUrl = baseUrl
    this.openBrowser = openBrowser
    this.serverSession = serverSession
    this.lastUrl = APP_URL
    this.online = net.isOnline()
    this.loaded = false
    this.visible = false
    this.busy = false
    this.appearance = { theme: nativeTheme.shouldUseDarkColors ? 'dark' : 'light', accent: 'violet', locale: app.getLocale() }
    this.resize = () => {
      const [width, height] = window.getContentSize()
      this.view?.setBounds({ x: 0, y: 0, width, height })
    }
    this.onNetwork = (event, state) => {
      if (!this.fromMainFrame(event, window.webContents) || typeof state?.online !== 'boolean') return
      if (isTrustedUrl(event.sender.getURL(), APP_URL)) {
        if (['light', 'dark'].includes(state.theme)) this.appearance.theme = state.theme
        if (['violet', 'lagoon', 'ember', 'moss', 'indigo', 'rose', 'mono'].includes(state.accent)) this.appearance.accent = state.accent
        if (typeof state.locale === 'string' && state.locale.length < 32 && state.locale) this.appearance.locale = state.locale
      }
      const wasOnline = this.online
      this.online = state.online
      if (!this.online) this.show('offline')
      else if (!wasOnline && this.visible) {
        void this.retry()
      } else this.sendState()
    }
    this.onRetry = (event) => {
      if (this.fromMainFrame(event, this.view?.webContents)) void this.retry()
    }
    this.onOpenBrowser = (event) => {
      if (this.fromMainFrame(event, this.view?.webContents)) openBrowser(serverUrl(this.lastUrl, baseUrl))
    }
    ipcMain.on('desktop:network', this.onNetwork)
    ipcMain.on('desktop:retry', this.onRetry)
    ipcMain.on('desktop:open-browser', this.onOpenBrowser)
    window.on('resize', this.resize)
    window.on('closed', () => this.dispose())
    window.webContents.on('did-navigate', (_event, url) => {
      if (isTrustedUrl(url, APP_URL)) this.lastUrl = url
    })
    window.webContents.on('did-finish-load', () => {
      if (isTrustedUrl(window.webContents.getURL(), APP_URL)) {
        this.loaded = true
      }
    })
    window.webContents.on('did-fail-load', (_event, code, _description, url, isMainFrame) => {
      if (isMainFrame && code !== -3 && isWebUrl(url)) this.failed()
    })
  }

  fromMainFrame(event, contents) {
    return Boolean(contents && !contents.isDestroyed()
      && event.sender === contents && event.senderFrame === contents.mainFrame)
  }

  sendState() {
    if (this.view && !this.view.webContents.isDestroyed()) {
      this.view.webContents.send('desktop:status-state', {
        ...this.appearance, messages: getMessages(this.appearance.locale),
        reason: this.reason, busy: this.busy, visible: this.visible,
      })
    }
  }

  show(reason) {
    if (this.window.isDestroyed()) return
    this.reason = reason
    if (!this.view) {
      this.view = new WebContentsView({ webPreferences: {
        nodeIntegration: false, contextIsolation: true, sandbox: true, webSecurity: true,
        preload: path.join(app.getAppPath(), 'offline-preload.cjs'),
      } })
      this.view.webContents.on('did-finish-load', () => this.sendState())
      void this.view.webContents.loadFile(path.join(app.getAppPath(), 'offline.html'))
        .catch((error) => console.error(error.message))
    }
    if (!this.visible) {
      this.window.contentView.addChildView(this.view)
      this.visible = true
      // Recheck only while disconnected; ordinary chat requests stay untouched.
      this.timer = setInterval(() => { if (this.online) void this.retry() }, 10000)
    }
    this.resize()
    this.sendState()
    this.view.webContents.focus()
  }

  hide() {
    if (!this.visible || this.window.isDestroyed()) return
    this.window.contentView.removeChildView(this.view)
    this.visible = false
    clearInterval(this.timer)
    this.sendState()
    this.window.webContents.send('desktop:connection-restored')
    this.window.webContents.focus()
  }

  failed() {
    this.loaded = false
    this.show(this.online ? 'unreachable' : 'offline')
  }

  async retry() {
    if (this.busy || !this.visible || this.window.isDestroyed()) return
    this.busy = true
    this.sendState()
    try {
      if (!this.online) return
      const response = await this.serverSession.fetch(new URL('/api/public/needs-setup', this.baseUrl).href, {
        cache: 'no-store', signal: AbortSignal.timeout(5000), bypassCustomProtocolHandlers: true,
      })
      if (response.status >= 500) throw new Error('server_unavailable')
      if (this.window.isDestroyed()) return
      this.online = true
      if (!this.loaded) await this.window.loadURL(this.lastUrl)
      this.hide()
    } catch {
      if (!this.window.isDestroyed()) this.show(this.online ? 'unreachable' : 'offline')
    } finally {
      this.busy = false
      this.sendState()
    }
  }

  dispose() {
    clearInterval(this.timer)
    ipcMain.removeListener('desktop:network', this.onNetwork)
    ipcMain.removeListener('desktop:retry', this.onRetry)
    ipcMain.removeListener('desktop:open-browser', this.onOpenBrowser)
    if (this.view && !this.view.webContents.isDestroyed()) this.view.webContents.close()
  }
}

module.exports = { ConnectionStatus }
