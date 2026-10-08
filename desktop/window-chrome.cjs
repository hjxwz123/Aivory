// Keep native traffic lights while drawing the title area with the page theme.
const MAC_TITLEBAR_HEIGHT = 36
const attached = new WeakSet()

function windowChromeOptions() {
  return process.platform === 'darwin' ? {
    titleBarStyle: 'hidden',
    trafficLightPosition: { x: 16, y: 11 },
  } : {}
}

function attachWindowChrome(contents) {
  if (process.platform !== 'darwin' || attached.has(contents)) return
  attached.add(contents)
  contents.on('dom-ready', () => {
    if (contents.isDestroyed()) return
    void contents.insertCSS(`
      body { box-sizing: border-box; padding-top: ${MAC_TITLEBAR_HEIGHT}px !important; }
      #root { height: calc(min(var(--app-height, 100dvh), 100dvh) - ${MAC_TITLEBAR_HEIGHT}px) !important; }
      [data-slot="sheet-content"][data-side="left"], [data-slot="sheet-content"][data-side="right"] {
        top: ${MAC_TITLEBAR_HEIGHT}px !important; height: calc(100dvh - ${MAC_TITLEBAR_HEIGHT}px) !important;
      }
      [data-slot="dialog-content"] {
        top: calc(50% + ${MAC_TITLEBAR_HEIGHT / 2}px) !important;
        max-height: calc(100dvh - ${MAC_TITLEBAR_HEIGHT + 32}px) !important;
      }
      #aivory-window-drag { position: fixed; inset: 0 0 auto; height: ${MAC_TITLEBAR_HEIGHT}px;
        background: var(--color-bg, #ffffff); -webkit-app-region: drag; z-index: 70; }
      @media (prefers-color-scheme: dark) {
        #aivory-window-drag { background: var(--color-bg, #111113); }
      }
    `).catch(() => {})
    void contents.executeJavaScript(`(() => {
      if (document.getElementById('aivory-window-drag')) return;
      const area = document.createElement('div');
      area.id = 'aivory-window-drag';
      area.setAttribute('aria-hidden', 'true');
      document.body.append(area);
    })()`).catch(() => {})
  })
}

module.exports = { windowChromeOptions, attachWindowChrome }
