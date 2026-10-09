function attachCloseToMinimize(window, { shouldMinimize, platform = process.platform }) {
  const contents = window.webContents
  let requested = false
  let nativeFullscreen = window.isFullScreen()
  let waitingForNativeExit = false
  let htmlFullscreen = false
  let waitingForHtmlExit = false
  let scheduled
  let fallbackTimer

  const pending = () => requested && !window.isDestroyed() && shouldMinimize()
  function cancel() {
    requested = false
    clearImmediate(scheduled)
    clearTimeout(fallbackTimer)
    scheduled = undefined
    fallbackTimer = undefined
  }

  function schedule() {
    if (!pending() || scheduled) return
    scheduled = setImmediate(() => {
      scheduled = undefined
      minimize()
    })
  }

  function minimize() {
    if (!pending()) return
    if (platform === 'darwin') {
      // Hiding or minimizing an occupied fullscreen Space can leave macOS on
      // a black screen. Wait for both HTML and native fullscreen to finish.
      if (waitingForHtmlExit) return
      if (htmlFullscreen) {
        if (!waitingForHtmlExit) {
          waitingForHtmlExit = true
          void contents.executeJavaScript('document.exitFullscreen()').then(() => {
            waitingForHtmlExit = false
            if (!htmlFullscreen) schedule()
          }).catch(() => {
            waitingForHtmlExit = false
            cancel()
          })
        }
        return
      }
      if (waitingForNativeExit) return
      if (nativeFullscreen || window.isFullScreen()) {
        waitingForNativeExit = true
        window.setFullScreen(false)
        return
      }
    }
    window.minimize()
    if (platform === 'darwin') {
      clearTimeout(fallbackTimer)
      fallbackTimer = setTimeout(() => {
        if (pending() && !waitingForNativeExit && !nativeFullscreen && !htmlFullscreen && !window.isFullScreen() && !window.isMinimized()) {
          window.hide()
          cancel()
        }
      }, 500)
    }
  }

  function onClose(event) {
    if (!shouldMinimize()) { cancel(); return }
    event.preventDefault()
    if (requested) return
    requested = true
    schedule()
  }
  function onNativeEnter() {
    nativeFullscreen = true
    schedule()
  }
  function onNativeExit() {
    nativeFullscreen = false
    waitingForNativeExit = false
    schedule()
  }
  function onHtmlEnter() { htmlFullscreen = true }
  function onHtmlExit() {
    htmlFullscreen = false
    schedule()
  }
  window.on('close', onClose)
  window.on('minimize', cancel)
  window.on('restore', cancel)
  window.on('enter-full-screen', onNativeEnter)
  window.on('leave-full-screen', onNativeExit)
  contents.on('enter-html-full-screen', onHtmlEnter)
  contents.on('leave-html-full-screen', onHtmlExit)
  window.once('closed', () => {
    cancel()
    contents.removeListener('enter-html-full-screen', onHtmlEnter)
    contents.removeListener('leave-html-full-screen', onHtmlExit)
  })

  return {
    cancel,
    restore() {
      cancel()
      if (window.isDestroyed()) return
      if (window.isMinimized()) window.restore()
      window.show()
      window.focus()
    },
  }
}

module.exports = { attachCloseToMinimize }
