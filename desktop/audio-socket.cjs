const WebSocket = require('ws')
const AUDIO_BUFFER_MAX_BYTES = 16000 * 2 * 30

// Voice frames travel through a narrow native bridge so both HTTP and HTTPS
// deployments work without disabling renderer security or exposing cookies.
function registerAudioSocketBridge(ipcMain, trusted, getRuntime) {
  const sockets = new Map()
  const keyFor = (event, id) => `${event.sender.id}:${id}`
  const send = (contents, id, type, data) => {
    if (!contents.isDestroyed()) contents.send('desktop:audio-event', { id, type, data })
  }
  ipcMain.on('desktop:audio-open', async (event, id, options) => {
    if (!trusted(event) || typeof id !== 'string' || !/^[a-f0-9-]{36}$/.test(id)) return
    const key = keyFor(event, id)
    if (sockets.has(key)) return
    const pending = { socket: null, cancel: null, frames: [], bytes: 0, sending: false, end: null, ended: false, writeTimer: null, flush: null }
    sockets.set(key, pending)
    const contents = event.sender
    const release = () => {
      if (sockets.get(key) === pending) sockets.delete(key)
      clearTimeout(pending.writeTimer)
      pending.frames = []
      pending.bytes = 0
      pending.end = null
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
      if (options?.segmented === true) url.searchParams.set('segmented', '1')
      const cookies = await session.cookies.get({ url: url.href })
      if (sockets.get(key) !== pending || contents.isDestroyed() || !trusted(event)) { cleanup(); return }
      url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
      const socket = new WebSocket(url, {
        origin: new URL(baseUrl).origin, maxPayload: 1024 * 1024, handshakeTimeout: 10000,
        headers: { Cookie: cookies.map((cookie) => `${cookie.name}=${cookie.value}`).join('; '), 'User-Agent': session.getUserAgent() },
      })
      pending.socket = socket
      // Only one send is in flight. A stalled socket cannot accumulate an
      // unlimited ws queue; unsent PCM stays in the bounded, replaceable FIFO.
      pending.flush = () => {
        if (pending.sending || socket.readyState !== WebSocket.OPEN || sockets.get(key) !== pending) return
        const frame = pending.frames.shift()
        const data = frame || pending.end
        if (!data) return
        if (frame) pending.bytes -= frame.length
        else pending.end = null
        pending.sending = true
        pending.writeTimer = setTimeout(() => { send(contents, id, 'error'); socket.terminate() }, 10000)
        try {
          socket.send(data, { binary: Buffer.isBuffer(data) }, (error) => {
            clearTimeout(pending.writeTimer)
            if (sockets.get(key) !== pending) return
            pending.sending = false
            send(contents, id, 'sent', Buffer.byteLength(data))
            if (error) { send(contents, id, 'error'); cleanup(); return }
            pending.flush()
          })
        } catch {
          cleanup()
          send(contents, id, 'error')
          send(contents, id, 'close')
        }
      }
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
    const pending = sockets.get(keyFor(event, id))
    if (!pending?.socket || pending.socket.readyState !== WebSocket.OPEN) return
    if (typeof data === 'string' && Buffer.byteLength(data) <= 32768) {
      let control
      try { control = JSON.parse(data) } catch { return }
      if (control?.type !== 'end') return
      if (pending.ended) { send(event.sender, id, 'sent', Buffer.byteLength(data)); return }
      pending.ended = true
      pending.end = data
    } else if (data instanceof ArrayBuffer && data.byteLength > 0 && data.byteLength <= 32768) {
      if (pending.ended) { send(event.sender, id, 'sent', data.byteLength); return }
      const frame = Buffer.from(data)
      while (pending.frames.length && pending.bytes + frame.length > AUDIO_BUFFER_MAX_BYTES) {
        const discarded = pending.frames.shift()
        pending.bytes -= discarded.length
        send(event.sender, id, 'sent', discarded.length)
      }
      pending.frames.push(frame)
      pending.bytes += frame.length
    } else return
    pending.flush()
  })
  ipcMain.on('desktop:audio-close', (event, id) => {
    if (!trusted(event)) return
    sockets.get(keyFor(event, id))?.cancel()
  })
}

module.exports = { registerAudioSocketBridge }
