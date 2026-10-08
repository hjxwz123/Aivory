const form = document.getElementById('server-form')
const input = document.getElementById('server-url')
const button = document.getElementById('save')
const error = document.getElementById('server-error')
let messages

window.desktopServer.info().then((state) => {
  messages = state.messages
  document.documentElement.lang = state.locale
  document.documentElement.dataset.accent = state.accent
  document.documentElement.dataset.theme = state.theme
  document.documentElement.classList.toggle('dark', state.theme === 'dark')
  document.getElementById('title').textContent = messages.serverTitle
  document.getElementById('server-label').textContent = messages.serverAddress
  document.getElementById('save-label').textContent = messages.connect
  input.value = state.baseUrl || ''
  input.focus()
})
form.addEventListener('submit', async (event) => {
  event.preventDefault()
  if (!messages || button.disabled) return
  button.disabled = true
  error.textContent = ''
  input.removeAttribute('aria-invalid')
  try {
    const result = await window.desktopServer.save(input.value)
    if (result.error) {
      error.textContent = messages[result.error]
      input.setAttribute('aria-invalid', 'true')
      input.focus()
    }
  } catch { error.textContent = messages.serverSaveFailed }
  finally { button.disabled = false }
})
