const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('aivoryDesktop', Object.freeze({
  getInfo: () => ipcRenderer.invoke('desktop:info'),
  loginInBrowser: () => ipcRenderer.invoke('desktop:browser-login'),
  cancelBrowserLogin: () => ipcRenderer.invoke('desktop:cancel-browser-login'),
  checkUpdates: () => ipcRenderer.invoke('desktop:check-updates'),
}))

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
