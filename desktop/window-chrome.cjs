// Keep native traffic lights while drawing the title area with the page theme.
const MAC_TITLEBAR_HEIGHT = 36
const attached = new WeakSet()

const windowChromeCSS = `
  :root { --aivory-titlebar-height: ${MAC_TITLEBAR_HEIGHT}px; }
  :root[data-aivory-fullscreen] { --aivory-titlebar-height: 0px; }
  body { box-sizing: border-box; padding-top: var(--aivory-titlebar-height) !important; }
  #root { height: calc(min(var(--app-height, 100dvh), 100dvh) - var(--aivory-titlebar-height)) !important; }
  [data-slot="dialog-content"]:not([data-dialog-presentation="drawer"]) {
    top: calc(50% + var(--aivory-titlebar-height) / 2) !important;
    max-height: calc(100dvh - var(--aivory-titlebar-height) - 32px) !important;
  }
  [data-slot="sheet-content"][data-side="left"],
  [data-slot="sheet-content"][data-side="right"],
  [data-slot="dialog-content"][data-dialog-presentation="drawer"] {
    top: var(--aivory-titlebar-height) !important;
    height: calc(100dvh - var(--aivory-titlebar-height)) !important;
    max-height: calc(100dvh - var(--aivory-titlebar-height)) !important;
  }
  #aivory-window-drag {
    position: fixed; inset: 0 0 auto; height: var(--aivory-titlebar-height);
    background: var(--color-bg, #ffffff);
    background-image: linear-gradient(to right,
      var(--color-sidebar-bg, var(--color-bg, #ffffff)) 0 var(--aivory-chrome-sidebar-width, 0px),
      var(--color-bg, #ffffff) var(--aivory-chrome-sidebar-width, 0px) 100%);
    -webkit-app-region: drag; z-index: 40;
  }
  :root[data-aivory-fullscreen] #aivory-window-drag { display: none; }
`

function windowChromeOptions() {
  return process.platform === 'darwin' ? {
    titleBarStyle: 'hidden',
    trafficLightPosition: { x: 16, y: 11 },
  } : {}
}

function attachWindowChrome(contents, window) {
  if (process.platform !== 'darwin' || attached.has(contents)) return
  attached.add(contents)
  let htmlFullscreen = false
  const isFullscreen = () => htmlFullscreen || Boolean(window && !window.isDestroyed() && window.isFullScreen())
  const syncFullscreen = () => {
    if (contents.isDestroyed()) return
    void contents.executeJavaScript(`document.documentElement.toggleAttribute('data-aivory-fullscreen', ${isFullscreen()})`).catch(() => {})
  }
  window?.on('enter-full-screen', syncFullscreen)
  window?.on('leave-full-screen', syncFullscreen)
  contents.on('enter-html-full-screen', () => { htmlFullscreen = true; syncFullscreen() })
  contents.on('leave-html-full-screen', () => { htmlFullscreen = false; syncFullscreen() })
  contents.on('destroyed', () => {
    window?.removeListener('enter-full-screen', syncFullscreen)
    window?.removeListener('leave-full-screen', syncFullscreen)
  })
  contents.on('dom-ready', () => {
    if (contents.isDestroyed()) return
    void contents.insertCSS(windowChromeCSS).catch(() => {})
    void contents.executeJavaScript(`(() => {
      document.documentElement.toggleAttribute('data-aivory-fullscreen', ${isFullscreen()});
      if (document.getElementById('aivory-window-drag')) return;
      const area = document.createElement('div');
      area.id = 'aivory-window-drag';
      area.setAttribute('aria-hidden', 'true');
      document.body.append(area);
      // Extend the sidebar surface into the native title area. Only observe
      // its size; streaming messages do not cause layout measurements.
      let sidebar;
      const update = () => document.documentElement.style.setProperty(
        '--aivory-chrome-sidebar-width', (sidebar?.getBoundingClientRect().width || 0) + 'px');
      const sizes = new ResizeObserver(update);
      const connect = () => {
        if (sidebar?.isConnected) return;
        if (sidebar) sizes.unobserve(sidebar);
        sidebar = document.querySelector('#root aside[data-variant="desktop"], #root aside[data-window-sidebar]');
        if (sidebar) sizes.observe(sidebar);
        update();
      };
      new MutationObserver(connect).observe(document.body, { childList: true, subtree: true });
      connect();
    })()`).catch(() => {})
  })
}

module.exports = { windowChromeOptions, attachWindowChrome, windowChromeCSS }
