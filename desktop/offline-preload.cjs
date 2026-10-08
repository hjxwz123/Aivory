const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('desktopStatus', {
  retry: () => ipcRenderer.send('desktop:retry'),
  openBrowser: () => ipcRenderer.send('desktop:open-browser'),
  onState: (callback) => ipcRenderer.on('desktop:status-state', (_event, state) => callback(state)),
})
window.addEventListener('online', () => ipcRenderer.send('desktop:retry'))
