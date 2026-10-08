import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import i18n from '@/i18n'
import '@/styles/globals.css'
import { Markdown } from '@/components/chat/markdown'
import { PrivateMarkdown } from '@/components/chat/private-markdown'
import { Composer } from '@/components/chat/composer'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'

const fence = (lang: string, code: string) => `\`\`\`${lang}\n${code}\n\`\`\``
const document = JSON.stringify({ version: 1, title: 'Usage overview', blocks: [
  { type: 'metrics', items: [{ label: 'Tokens', value: 3000, hint: 'Three months' }] },
  { type: 'chart', kind: 'line', labels: ['June', 'July', 'August'], series: [{ name: 'Tokens', values: [800, 1000, 1200] }] },
  { type: 'tabs', items: [{ title: 'Summary', blocks: [{ type: 'text', text: 'Steady growth' }] }, { title: 'Breakdown', blocks: [{ type: 'table', columns: ['Month', 'Tokens'], rows: [['June', 800], ['July', 1000]] }] }] },
  { type: 'accordion', items: [{ title: 'Assumptions', blocks: [{ type: 'steps', items: [{ title: 'Check inputs' }, { title: 'Compare months', description: 'Use the same period' }] }] }] },
] })
function Harness() {
  const [html, setHTML] = useState('<button id="action" onclick="this.textContent=\'Updated\'">Calculate</button>')
  const [live, setLive] = useState(false)
  useEffect(() => {
    Reflect.set(window, '__GUI__', { setHTML, setLive, setLanguage: (locale: string) => i18n.changeLanguage(locale) })
  }, [])
  return <TooltipProvider><MemoryRouter><main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <section data-case="presets"><Markdown allowGenerativeUI content={fence('aivory-ui', document)} /></section>
    <section data-case="inline"><Markdown allowGenerativeUI live={live} content={fence('aivory-html', html)} /></section>
    <section data-case="private"><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', document)} /></section>
    <section data-case="user"><Markdown content={fence('aivory-ui', document)} /></section>
    <section data-case="ordinary"><Markdown allowGenerativeUI content={fence('html', '<h1>Ordinary preview</h1>')} /></section>
    <section data-case="incomplete"><Markdown allowGenerativeUI live content={fence('aivory-ui', '{"version":1,')} /></section>
    <section data-case="composer"><Composer modelId="test-model" onModelChange={() => {}} onSubmit={(_text, _attachments, options) => Reflect.set(window, '__SUBMITTED_SKILLS__', options.selectedUserSkillIds)} /></section>
  </main></MemoryRouter></TooltipProvider>
}
useAuth.setState({ user: { id: 'test-user', email: 'test@example.test', displayName: 'Test', role: 'admin' } as never, status: 'authenticated' })
useModels.setState({ loaded: true, loading: false, loadedScope: null, loadedPolicyKey: 'personal', models: [{ id: 'test-model', label: 'Test', kind: 'chat', enabled: true, capabilities: ['text'], tool_mode: 'none', param_controls: [] }] as never })
await i18n.changeLanguage('en')
createRoot(globalThis.document.getElementById('root')!).render(<Harness />)
