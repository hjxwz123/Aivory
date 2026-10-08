const retry = document.getElementById('retry')
const openBrowser = document.getElementById('open-browser')

window.desktopStatus.onState((state) => {
  document.body.dataset.visible = String(state.visible)
  document.documentElement.lang = state.locale
  document.documentElement.dataset.accent = state.accent
  document.documentElement.dataset.theme = state.theme
  document.documentElement.classList.toggle('dark', state.theme === 'dark')
  document.getElementById('title').textContent = state.reason === 'offline' ? state.messages.offlineTitle : state.messages.connectionTitle
  document.getElementById('description').textContent = state.reason === 'offline' ? state.messages.offlineDescription : state.messages.connectionMessage
  document.getElementById('retry-label').textContent = state.messages.retry
  document.getElementById('browser-label').textContent = state.messages.openBrowser
  document.getElementById('progress').textContent = state.busy ? state.messages.reconnecting : ''
  retry.disabled = state.busy
  retry.setAttribute('aria-busy', String(state.busy))
})
retry.addEventListener('click', () => window.desktopStatus.retry())
openBrowser.addEventListener('click', () => window.desktopStatus.openBrowser())
