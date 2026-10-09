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
const preview = new URLSearchParams(globalThis.location.search).has('preview')
const tableImageURL = (file: string) => preview
  ? new URL(file === 'missing.png' ? '/missing-table-image.png' : '/icon-512.png', globalThis.location.href).href
  : `https://images.example.test/${file}`
const document = JSON.stringify({ version: 1, title: 'Usage overview', blocks: [
  { type: 'metrics', items: [{ label: 'Tokens', value: 3000, hint: 'Three months' }] },
  { type: 'chart', kind: 'line', labels: ['June', 'July', 'August'], series: [{ name: 'Tokens', values: [800, 1000, 1200] }] },
  { type: 'tabs', items: [{ title: 'Summary', blocks: [{ type: 'text', text: 'Steady growth' }] }, { title: 'Breakdown', blocks: [{ type: 'table', columns: ['Month', 'Tokens'], rows: [['June', 800], ['July', 1000]] }] }] },
  { type: 'accordion', items: [{ title: 'Assumptions', blocks: [{ type: 'steps', items: [{ title: 'Check inputs' }, { title: 'Compare months', description: 'Use the same period' }] }] }] },
] })
const richTable = JSON.stringify({ version: 1, title: 'Deployment comparison', description: 'Compare the same features across three options.', blocks: [{
  type: 'table',
  columns: [{ label: 'Application', width: 'wide' }, { label: 'Preview', width: 'compact', align: 'center' }, { label: 'Capabilities', width: 'wide' }, { label: 'Users', width: 'compact', align: 'right' }, 'Reference'],
  rows: [
    [{ type: 'text', text: 'Desktop client', description: 'Bundled frontend with persistent server settings.' }, { type: 'image', url: tableImageURL('app.png'), alt: 'Desktop application', caption: 'App icon' }, { type: 'list', items: ['macOS, Windows and Linux', 'Native window controls'] }, 120, { type: 'link', url: 'https://example.test/desktop', text: 'Desktop release' }],
    [{ type: 'text', text: 'Browser client', description: 'A long detail that should wrap naturally within its column without expanding the page or hiding other cells.' }, { type: 'image', url: tableImageURL('browser.png'), alt: 'Browser application' }, { type: 'list', items: ['Responsive layout', 'No installation required'] }, 800, { type: 'link', url: 'https://example.test/web', text: 'Open website' }],
    ['Unavailable preview', { type: 'image', url: tableImageURL('missing.png'), alt: 'Unavailable application preview' }, null, false, 'No reference'],
  ],
}] })
function Harness() {
  const [html, setHTML] = useState('<button id="action" onclick="this.textContent=\'Updated\'">Calculate</button>')
  const [live, setLive] = useState(false)
  useEffect(() => {
    Reflect.set(window, '__GUI__', { setHTML, setLive, setLanguage: (locale: string) => i18n.changeLanguage(locale) })
  }, [])
  if (preview) return <main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <Markdown allowGenerativeUI content={fence('aivory-ui', richTable)} />
    <Markdown allowGenerativeUI content={'| Name | Value |\n| --- | --- |\n| Plain Markdown | 12 |'} />
  </main>
  return <TooltipProvider><MemoryRouter><main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <section data-case="rich-table"><Markdown allowGenerativeUI content={fence('aivory-ui', richTable)} /></section>
    <section data-case="presets"><Markdown allowGenerativeUI content={fence('aivory-ui', document)} /></section>
    <section data-case="inline"><Markdown allowGenerativeUI live={live} content={fence('aivory-html', html)} /></section>
    <section data-case="private"><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', document)} /></section>
    <section data-case="private-table"><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', richTable)} /></section>
    <section data-case="markdown-table"><Markdown allowGenerativeUI content={'| Name | Value |\n| --- | --- |\n| Plain Markdown | 12 |'} /></section>
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
