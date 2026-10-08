const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('desktopServer', Object.freeze({
  info: () => ipcRenderer.invoke('desktop:server-info'),
  save: (address) => ipcRenderer.invoke('desktop:server-save', address),
}))
