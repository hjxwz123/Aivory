const WebSocket = require('ws')

// Voice frames travel through a narrow native bridge so both HTTP and HTTPS
// deployments work without disabling renderer security or exposing cookies.
function registerAudioSocketBridge(ipcMain, trusted, getRuntime) {
  const sockets = new Map()
  const keyFor = (event, id) => `${event.sender.id}:${id}`
  const send = (contents, id, type, data) => {
    if (!contents.isDestroyed()) contents.send('desktop:audio-event', { id, type, data })
  }
  ipcMain.on('desktop:audio-open', async (event, id) => {
    if (!trusted(event) || typeof id !== 'string' || !/^[a-f0-9-]{36}$/.test(id)) return
    const key = keyFor(event, id)
    if (sockets.has(key)) return
    const pending = { socket: null, cancel: null }
    sockets.set(key, pending)
    const contents = event.sender
    const release = () => {
      if (sockets.get(key) === pending) sockets.delete(key)
      contents.removeListener('destroyed', cleanup)
    }
    const cleanup = () => { release(); pending.socket?.terminate() }
    pending.cancel = () => {
      release()
      if (pending.socket?.readyState === WebSocket.OPEN) pending.socket.close()
      else pending.socket?.terminate()
    }
    contents.once('destroyed', cleanup)
    try {
      const { baseUrl, session } = getRuntime()
      const url = new URL('/api/audio/stream', baseUrl)
      const cookies = await session.cookies.get({ url: url.href })
      if (sockets.get(key) !== pending || contents.isDestroyed() || !trusted(event)) { cleanup(); return }
      url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
      const socket = new WebSocket(url, {
        origin: new URL(baseUrl).origin, maxPayload: 1024 * 1024, handshakeTimeout: 10000,
        headers: { Cookie: cookies.map((cookie) => `${cookie.name}=${cookie.value}`).join('; '), 'User-Agent': session.getUserAgent() },
      })
      pending.socket = socket
      socket.on('open', () => send(contents, id, 'open'))
      socket.on('message', (data) => send(contents, id, 'message', data.toString()))
      socket.on('error', () => send(contents, id, 'error'))
      socket.on('close', () => {
        release()
        send(contents, id, 'close')
      })
    } catch {
      cleanup()
      send(contents, id, 'error')
      send(contents, id, 'close')
    }
  })
  ipcMain.on('desktop:audio-send', (event, id, data) => {
    if (!trusted(event)) return
    const socket = sockets.get(keyFor(event, id))?.socket
    if (!socket || socket.readyState !== WebSocket.OPEN) return
    if (typeof data === 'string' && Buffer.byteLength(data) <= 32768) socket.send(data)
    else if (data instanceof ArrayBuffer && data.byteLength <= 32768) socket.send(Buffer.from(data))
  })
  ipcMain.on('desktop:audio-close', (event, id) => {
    if (!trusted(event)) return
    sockets.get(keyFor(event, id))?.cancel()
  })
}

module.exports = { registerAudioSocketBridge }
