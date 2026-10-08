const { contextBridge, ipcRenderer } = require('electron')

const runtime = ipcRenderer.sendSync('desktop:runtime')
function connectAudioSocket(listener) {
  const id = crypto.randomUUID()
  const onEvent = (_event, message) => {
    if (message.id !== id) return
    listener({ type: message.type, data: message.data })
    if (message.type === 'close') ipcRenderer.removeListener('desktop:audio-event', onEvent)
  }
  ipcRenderer.on('desktop:audio-event', onEvent)
  ipcRenderer.send('desktop:audio-open', id)
  return {
    send: (data) => ipcRenderer.send('desktop:audio-send', id, data),
    close: () => { ipcRenderer.removeListener('desktop:audio-event', onEvent); ipcRenderer.send('desktop:audio-close', id) },
  }
}
if (runtime) contextBridge.exposeInMainWorld('aivoryDesktop', Object.freeze({
  serverBaseUrl: runtime.serverBaseUrl,
  getInfo: () => ipcRenderer.invoke('desktop:info'),
  loginInBrowser: () => ipcRenderer.invoke('desktop:browser-login'),
  cancelBrowserLogin: () => ipcRenderer.invoke('desktop:cancel-browser-login'),
  checkUpdates: () => ipcRenderer.invoke('desktop:check-updates'),
  openPayment: (action) => ipcRenderer.invoke('desktop:payment', action),
  connectAudioSocket,
  startApiRequest: (id) => ipcRenderer.send('desktop:api-start', id),
  abortApiRequest: (id) => ipcRenderer.send('desktop:api-abort', id),
}))

ipcRenderer.on('desktop:connection-restored', () => {
  window.dispatchEvent(new Event('online'))
  window.dispatchEvent(new Event('aivory:desktop-reconnected'))
})
ipcRenderer.on('desktop:authorized', () => {
  window.dispatchEvent(new Event('aivory:desktop-authorized'))
})
ipcRenderer.on('desktop:payment-return', (_event, path) => {
  window.dispatchEvent(new CustomEvent('aivory:desktop-payment-return', { detail: path }))
})

function reportStatus() {
  ipcRenderer.send('desktop:network', {
    online: navigator.onLine,
    theme: document.documentElement.dataset.theme,
    accent: document.documentElement.dataset.accent,
    locale: document.documentElement.lang,
  })
}

window.addEventListener('online', reportStatus)
window.addEventListener('offline', reportStatus)
window.addEventListener('DOMContentLoaded', () => {
  reportStatus()
  new MutationObserver(reportStatus).observe(document.documentElement, {
    attributes: true, attributeFilter: ['data-theme', 'data-accent', 'lang'],
  })
})
