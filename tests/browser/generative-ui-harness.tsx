import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import i18n from '@/i18n'
import '@/styles/globals.css'
import 'katex/dist/katex.min.css'
import { Markdown } from '@/components/chat/markdown'
import { PrivateMarkdown } from '@/components/chat/private-markdown'
import { Composer } from '@/components/chat/composer'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import { GenerativeReplyProvider } from '@/components/chat/generative-reply'
import PrivateChat from '@/pages/chat/PrivateChat'
import { proportionalComplexTable as complexTable } from '../frontend/fixtures/generative-table'
import { nestedComponents } from '../frontend/fixtures/generative-nesting'
import { sampleMap, nestedMap } from '../frontend/fixtures/generative-map'
import { interleavedComponents, weeklyProgress } from '../frontend/fixtures/generative-inline'
import { recoveredComponents } from '../frontend/fixtures/generative-robustness'
import type { ArtifactRef } from '@/types/chat'

const fence = (lang: string, code: string) => `\`\`\`${lang}\n${code}\n\`\`\``
const componentProse = fence('aivory-ui', `{@body const [diam,setDiam] = DIL.useState(16)}
<tabs>
  <tab title="覆盖">
    <slider label="直径" min={16} max={18} value={diam} onChange={setDiam}/>
    <card>
      {diam < 17
        ? "❌ 四个角仍露在圆外！因为直径小于对角线 16.97 inches，无法完全覆盖正方形开口。"
        : "✅ 完全覆盖！直径大于对角线 16.97 inches，正方形的四个顶点均在圆内部。"}
    </card>
  </tab>
  <tab title="说明">
    <card>
      正方形的**“对角线”**决定是否完全覆盖。
    </card>
    <text>
      **比较方式**

      - 比较直径
      - 查看四个顶点

      \`\`\`text
      {diam}
      \`\`\`
    </text>
  </tab>
</tabs>`)
const quotedNews = `{"version":1,"title":"News","blocks":[{"type":"tabs","items":[{"title":"Domestic","blocks":[{"type":"text","text":"新增"学校食品安全""网络食品经营"专节。"}]},{"title":"International","blocks":[{"type":"text","text":"美国表示"强烈谴责"。"}]}]}]}`
const preview = new URLSearchParams(globalThis.location.search).has('preview')
const maps = new URLSearchParams(globalThis.location.search).has('maps')
const interleaved = new URLSearchParams(globalThis.location.search).has('interleaved')
const weekly = new URLSearchParams(globalThis.location.search).has('weekly')
const robustness = new URLSearchParams(globalThis.location.search).has('robustness')
const artifactCase = new URLSearchParams(globalThis.location.search).has('artifacts')
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
const cards = JSON.stringify({ version: 1, blocks: [
  { type: 'cards', title: 'Search highlights', description: 'A compact digest of relevant results.', items: [
    { title: 'A new desktop release', description: 'A local frontend with browser authorization and persistent server settings.', meta: 'Product journal · Today', image: { url: tableImageURL('news.png'), alt: 'Release preview' }, link: { url: 'https://example.test/news' } },
    { title: 'An alternative deployment', description: 'A summary remains readable even when the image cannot be loaded.', meta: 'Deployment guide', image: { url: tableImageURL('missing.png'), alt: 'Deployment preview' }, link: { url: 'https://example.test/deploy', label: 'Read the guide' } },
    { title: 'An-option-with-an-extremely-long-unbroken-name-that-must-wrap-within-the-card-at-every-screen-size', description: 'Compare the trade-offs before choosing.\nText-only results need no image or link.' },
  ] },
  { type: 'card', title: 'Recommended option', description: 'A single card can highlight one useful recommendation without a large layout.', image: { url: tableImageURL('recommendation.png'), alt: 'Recommendation' }, link: { url: 'https://example.test/option', label: 'Explore this option and compare it with the alternatives' } },
] })
const advanced = JSON.stringify({ version: 1, title: 'Explore the data', blocks: [
  { type: 'chart', kind: 'donut', labels: ['Web', 'Desktop', 'API'], series: [{ name: 'Requests', values: [54, 28, 18] }] },
  { type: 'heatmap', title: 'Activity by hour', xLabels: ['Mon', 'Tue', 'Wed'], yLabels: ['Morning', 'Evening'], values: [[12, 16, 9], [22, 18, 28]] },
  { type: 'gauge', label: 'Completion', value: 72, min: 0, max: 100, unit: '%' },
  { type: 'calculator', title: 'Cost explorer', description: 'Compare quantities and plans, with optional 10% tax.', inputs: [{ type: 'range', id: 'quantity', label: 'Quantity', min: 1, max: 100, step: 1, value: 10 }, { type: 'toggle', id: 'tax', label: 'Include tax', value: false }, { type: 'select', id: 'price', label: 'Plan', value: '12', options: [{ label: 'Basic', value: '12' }, { label: 'Pro', value: '24' }] }], outputs: [{ label: 'Total', expression: 'quantity * price * (1 + tax * 0.1)', unit: 'USD' }] },
  { type: 'checklist', title: 'Plan', items: [{ text: 'Compare the options' }, { text: 'Try a scenario' }] },
  { type: 'quiz', question: 'What is 2 + 2?', options: ['3', '4'], answer: 1, explanation: 'Two pairs make four.' },
  { type: 'function', title: 'Editable function', functions: [{ expression: 'a * sin(x)' }, { expression: 'cos(x)' }], variable: 'x', range: { min: -6.28, max: 6.28 }, samples: 120, controls: [{ type: 'range', id: 'a', label: 'Amplitude', min: .1, max: 2, step: .1, value: 1 }] },
  { type: 'chart', kind: 'scatter', title: 'Height and weight', xLabel: 'Height (cm)', yLabel: 'Weight (kg)', labels: ['A', 'B', 'C'], series: [{ name: 'Adults', xValues: [158, 176, 184], values: [51, 72, 82] }, { name: 'Teenagers', xValues: [148, 156, 172], values: [42, 51, 63] }] },
  { type: 'chart', kind: 'stacked-bar', title: 'Requests by platform', labels: ['June', 'July'], series: [{ name: 'Web', values: [60, 75] }, { name: 'App', values: [40, 48] }] },
  { type: 'chart', kind: 'area', title: 'Monthly tokens', labels: ['June', 'July', 'August'], series: [{ name: 'Tokens', values: [800, 1000, 1200] }] },
  { type: 'chart', kind: 'horizontal-bar', title: 'Requests by channel', labels: ['Channel A', 'Channel B'], series: [{ name: 'Requests', values: [120, 220] }] },
  { type: 'chart', kind: 'pie', title: 'A single slice', labels: ['All'], series: [{ name: 'Share', values: [100] }] },
] })
const questions = JSON.stringify({ version: 1, blocks: [{ type: 'question', title: 'A few details', questions: [
  { id: 'platform', question: 'Which platform are you using?', options: [{ label: 'Windows', description: 'Windows 10 or 11' }, 'macOS', 'Linux'] },
  { id: 'needs', question: 'Which features do you need?', options: ['Charts', 'Tables', 'Editable functions'], multiple: true },
] }] })
const singleQuestion = JSON.stringify({ version: 1, blocks: [{ type: 'question', questions: [{ id: 'platform', question: 'Which platform are you using?', options: ['Windows', 'macOS'], allowCustom: false }] }] })
const form = JSON.stringify({ version: 1, blocks: [{ type: 'form', title: 'Plan a trip', description: 'Share a few preferences so I can tailor your itinerary.', submitLabel: 'Plan my trip', fields: [
  { id: 'destination', type: 'text', label: 'Destination', required: true, placeholder: 'City or region' },
  { id: 'budget', type: 'number', label: 'Budget per person', min: 0, max: 5000, step: 100, description: 'Total budget in USD.' },
  { id: 'email', type: 'email', label: 'Email', required: true },
  { id: 'departure', type: 'date', label: 'Departure date' },
  { id: 'pace', type: 'select', label: 'Pace', options: [{ label: 'Relaxed', value: 'relaxed' }, { label: 'Packed', value: 'packed' }], required: true },
  { id: 'transport', type: 'radio', label: 'Transport', options: ['Train', 'Plane'] },
  { id: 'interests', type: 'checkboxes', label: 'Interests', options: ['Nature', 'Food', 'Museums'], required: true },
  { id: 'flexible', type: 'checkbox', label: 'Include alternative destinations' },
  { id: 'notes', type: 'textarea', label: 'Other requirements', placeholder: 'Anything else I should consider?' },
] }] })
const singleFieldForm = JSON.stringify({ version: 1, blocks: [{ type: 'form', fields: [{ id: 'details', type: 'text', label: 'Details', required: true }] }] })
const nativeForm = JSON.stringify({ version: 1, blocks: [{ type: 'form', title: 'Other input types', fields: [
  { id: 'url', type: 'url', label: 'Website' }, { id: 'tel', type: 'tel', label: 'Telephone' },
  { id: 'time', type: 'time', label: 'Time', value: '09:30' }, { id: 'datetime', type: 'datetime-local', label: 'Date and time', value: '2026-10-11T09:30' },
  { id: 'confirmation', type: 'checkbox', label: 'Confirm these preferences', required: true },
] }] })
const markupLibrary = `<box gap={4}>
  <row justify="between"><heading level={2}>Composable UI library</heading><badge tone="success">Ready</badge></row>
  **Markdown and components** can share the same layout.
  <stats><metric label="Components" value="Composable" hint="Layout belongs to the model"/><metric label="Progress" value={72} unit="%"/></stats>
  <grid columns={2} gap={4}>
    <card title="Content and feedback"><row align="start"><avatar src="https://images.example.test/avatar.png" alt="Avatar"/><box flex={1}><text weight="medium">Flexible content</text><text color="secondary" size="sm">Readable in light and dark mode.</text></box></row><alert tone="info" title="A helpful note">Only add the controls the answer needs.</alert><progress label="Completion" value={72}/><link href="https://example.test/guide">Read the guide</link></card>
    <box gap={3}><image src="https://images.example.test/library.png" alt="Component library" aspectRatio="3:2" width="180px"/><steps><step title="Compose">Choose the right components.</step><step title="Explore">Adjust values locally.</step></steps></box>
  </grid>
  <table searchable sortable label="Markup plans"><thead><tr><th>Plan</th><th align="right">Price</th><th>Status</th></tr></thead><tbody><tr><td>Pro</td><td align="right">24</td><td><badge tone="success">Available</badge></td></tr><tr><td>Basic</td><td align="right">12</td><td><badge>Starter</badge></td></tr></tbody></table>
  <tabs label="Explore"><tab title="Controls"><grid columns={2}><box><input name="name" label="Name" value="Alex"/><select name="pace" label="Pace" value="relaxed"><option value="relaxed">Relaxed</option><option value="packed">Packed</option></select><textarea name="notes" label="Notes"></textarea></box><box><slider name="quantity" label="Quantity" min={1} max={20} value={3}/><text>Total: {quantity * 12} USD</text><checkbox name="flexible" label="Flexible dates"/><switch name="tax" label="Include tax"/><rating name="rating" label="Rating" max={5}/></box></grid></tab><tab title="Details"><accordion><item title="More details"><timeline><item title="Today">Choose a direction.</item><item title="Tomorrow">Explore the result.</item></timeline></item></accordion><tooltip content="Available on keyboard focus too">More context</tooltip><empty title="No extra tasks">Everything is ready.</empty></tab><tab title="Charts"><chart kind="bar" title="Usage"><series name="Requests"><point label="Jan" value={120}/><point label="Feb" value={180}/></series></chart><function expression="sin(x)" min={-5} max={5}/><calculator title="Budget"><parameter id="quantity" label="Quantity" min={1} max={20} step={1} value={3}/><output label="Total" expression="quantity*12" unit="USD"/></calculator></tab></tabs>
</box>`
const markupReplyExample = `{@body const [kind,setKind] = DIL.useState("生活 vlog")}
{@body const [fresh,setFresh] = DIL.useState("最新发布")}
<box border padding={4} radius="xl" gap={3}>
  <radio-group value={kind} onChange={setKind} label="内容类型"><radio value="生活 vlog">生活 Vlog</radio><radio value="美食旅游">美食与旅游</radio></radio-group>
  <select value={fresh} onChange={setFresh} label="视频时间"><option value="最新发布">最新发布</option><option value="热门视频">热门视频</option></select>
  <text>已选择：{kind} / {fresh}</text>
  <button block onClick={() => GenUI.issueNewTurn("寻找台湾博主；类型：" + kind + "；时间：" + fresh)}>帮我找台湾博主</button>
</box>`
const markupForm = '<form title="Markup trip"><input name="destination" label="Destination" required/><input type="email" name="email" label="Email" required/><select name="pace" label="Pace" required><option value="relaxed">Relaxed</option><option value="packed">Packed</option></select><button type="submit">Send markup form</button></form>'
const stateDiagram = '<box><slider name="amount" label="Progress" min={0} max={100} value={25}/><svg viewBox="0 0 240 60" aria-label="Live progress"><rect width={amount * 2} height="20" fill="var(--color-accent)"/><text y="50">{amount}%</text></svg></box>'
let markupAttempts = 0
const functionFragment = JSON.stringify({ type: 'function', title: 'Discontinuity examples', functions: [
  { expression: '2/(1+exp(-50*x))-1', label: 'Smooth step' },
  { expression: 'sin(x)/x', label: 'Removable discontinuity' },
], variable: 'x', range: { min: -6, max: 6 } })
let questionAttempts = 0
let formAttempts = 0
function Harness() {
  const [html, setHTML] = useState('<button id="action" onclick="this.textContent=\'Updated\'">Calculate</button>')
  const [live, setLive] = useState(false)
  const [markup, setMarkup] = useState(maps ? sampleMap : robustness ? recoveredComponents : weekly ? weeklyProgress : interleaved ? interleavedComponents : markupLibrary)
  const [artifacts, setArtifacts] = useState<ArtifactRef[]>([])
  useEffect(() => {
    Reflect.set(window, '__GUI__', { setHTML, setLive, setMarkup, setArtifacts, setLanguage: (locale: string) => i18n.changeLanguage(locale) })
  }, [])
  if (artifactCase) return <main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <Markdown allowGenerativeUI content={markup} artifacts={artifacts} live={live} />
  </main>
  if (interleaved || weekly || robustness) return <main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <section data-case="interleaved"><GenerativeReplyProvider messageId="interleaved-message" status="ready" onReply={async (_id, text) => { Reflect.set(window, '__INTERLEAVED_REPLY__', text); return true }}><Markdown allowGenerativeUI content={markup} live={live} /></GenerativeReplyProvider></section>
    <section data-case="interleaved-private"><PrivateMarkdown allowGenerativeUI text={robustness ? recoveredComponents : weekly ? weeklyProgress : interleavedComponents} /></section>
  </main>
  if (maps) return <main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <section data-case="map"><GenerativeReplyProvider messageId="map-message" status="ready" onReply={async (_messageId, text) => { Reflect.set(window, '__MAP_REPLY__', text); return true }}><Markdown allowGenerativeUI content={markup} live={live} /></GenerativeReplyProvider></section>
    <section data-case="map-private"><PrivateMarkdown allowGenerativeUI text={nestedMap} /></section>
  </main>
  if (preview) return <main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <Markdown allowGenerativeUI content={fence('aivory-ui', richTable)} />
    <Markdown allowGenerativeUI content={'| Name | Value |\n| --- | --- |\n| Plain Markdown | 12 |'} />
  </main>
  return <TooltipProvider><MemoryRouter><main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <section data-case="nested-components"><GenerativeReplyProvider messageId="nested-components" status="ready" onReply={async (messageId, text) => { Reflect.set(window, '__NESTED_REPLY__', { messageId, text }); return true }}><Markdown allowGenerativeUI content={nestedComponents} /></GenerativeReplyProvider></section>
    <section data-case="nested-private"><GenerativeReplyProvider messageId="nested-private" status="ready" onReply={async (messageId, text) => { Reflect.set(window, '__NESTED_FORM__', { messageId, text }); return true }}><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', nestedComponents)} /></GenerativeReplyProvider></section>
    <section data-case="svg-state"><Markdown allowGenerativeUI content={stateDiagram} /></section>
    <section data-case="complex-table"><Markdown allowGenerativeUI content={complexTable} /></section>
    <section data-case="complex-table-private"><PrivateMarkdown allowGenerativeUI text={complexTable} /></section>
    <section data-case="markup-library"><Markdown allowGenerativeUI content={markup} /></section>
    <section data-case="markup-private"><PrivateMarkdown allowGenerativeUI text={markupLibrary} /></section>
    <section data-case="component-prose"><Markdown allowGenerativeUI content={componentProse} /></section>
    <section data-case="private-component-prose"><PrivateMarkdown allowGenerativeUI text={componentProse} /></section>
    <section data-case="markup-reply"><GenerativeReplyProvider messageId="markup-message" status="ready" onReply={async (messageId, text) => { Reflect.set(window, '__MARKUP_REPLY__', { messageId, text }); Reflect.set(window, '__MARKUP_ATTEMPTS__', ++markupAttempts); await new Promise(resolve => setTimeout(resolve, 150)); return markupAttempts > 1 }}><Markdown allowGenerativeUI content={markupReplyExample} /></GenerativeReplyProvider></section>
    <section data-case="markup-form"><GenerativeReplyProvider messageId="markup-form-message" status="ready" onReply={async (messageId, text) => { Reflect.set(window, '__MARKUP_FORM__', { messageId, text }); return true }}><Markdown allowGenerativeUI content={fence('aivory-ui', markupForm)} /></GenerativeReplyProvider></section>
    <section data-case="markup-readonly"><Markdown allowGenerativeUI content={markupReplyExample} /></section>
    <section data-case="markup-streaming"><GenerativeReplyProvider messageId="markup-streaming" status="ready" onReply={async () => { throw new Error('Streaming markup cannot send') }}><Markdown allowGenerativeUI live content={markupReplyExample} /></GenerativeReplyProvider></section>
    <section data-case="markup-answered"><GenerativeReplyProvider messageId="markup-answered" status="answered" onReply={async () => { throw new Error('Historical markup cannot send') }}><Markdown allowGenerativeUI content={markupReplyExample} /></GenerativeReplyProvider></section>
    <section data-case="markup-user"><Markdown content={fence('aivory-ui', markupLibrary)} /></section>
    <section data-case="rich-table"><Markdown allowGenerativeUI content={fence('aivory-ui', richTable)} /></section>
    <section data-case="cards"><Markdown allowGenerativeUI content={fence('aivory-ui', cards)} /></section>
    <section data-case="advanced"><Markdown allowGenerativeUI content={fence('aivory-ui', advanced)} /></section>
    <section data-case="function-fragment"><Markdown allowGenerativeUI content={fence('aivory-ui', functionFragment)} /></section>
    <section data-case="private-function-fragment"><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', functionFragment)} /></section>
    <section data-case="unfenced-function"><Markdown allowGenerativeUI content={functionFragment} /></section>
    <section data-case="user-function"><Markdown content={fence('aivory-ui', functionFragment)} /></section>
    <section data-case="questions"><GenerativeReplyProvider messageId="question-message" status="ready" onReply={async (messageId, text) => {
      Reflect.set(window, '__QUESTION_REPLY__', { messageId, text })
      await new Promise(resolve => setTimeout(resolve, 150))
      return true
    }}><Markdown allowGenerativeUI content={fence('aivory-ui', questions)} /></GenerativeReplyProvider></section>
    <section data-case="question-retry"><GenerativeReplyProvider messageId="retry-message" status="ready" onReply={async () => {
      Reflect.set(window, '__QUESTION_ATTEMPTS__', ++questionAttempts)
      return questionAttempts > 1
    }}><Markdown allowGenerativeUI content={fence('aivory-ui', singleQuestion)} /></GenerativeReplyProvider></section>
    <section data-case="question-streaming"><GenerativeReplyProvider messageId="streaming-message" status="ready" onReply={async () => { throw new Error('streaming reply must not submit') }}><Markdown allowGenerativeUI live content={fence('aivory-ui', singleQuestion)} /></GenerativeReplyProvider></section>
    <section data-case="question-readonly"><Markdown allowGenerativeUI content={fence('aivory-ui', singleQuestion)} /></section>
    <section data-case="form"><GenerativeReplyProvider messageId="form-message" status="ready" onReply={async (messageId, text) => {
      Reflect.set(window, '__FORM_REPLY__', { messageId, text })
      Reflect.set(window, '__FORM_ATTEMPTS__', ++formAttempts)
      await new Promise(resolve => setTimeout(resolve, 150))
      return formAttempts > 1
    }}><Markdown allowGenerativeUI content={fence('aivory-ui', form)} /></GenerativeReplyProvider></section>
    <section data-case="form-preview"><GenerativeReplyProvider messageId="form-preview" status="ready" onReply={async () => true}><Markdown allowGenerativeUI content={fence('aivory-ui', form)} /></GenerativeReplyProvider></section>
    <section data-case="form-native"><GenerativeReplyProvider messageId="form-native" status="ready" onReply={async () => { Reflect.set(window, '__NATIVE_FORM_SUBMITTED__', true); return true }}><Markdown allowGenerativeUI content={fence('aivory-ui', nativeForm)} /></GenerativeReplyProvider></section>
    <section data-case="form-streaming"><GenerativeReplyProvider messageId="form-streaming" status="ready" onReply={async () => { throw new Error('streaming form must not submit') }}><Markdown allowGenerativeUI live content={fence('aivory-ui', singleFieldForm)} /></GenerativeReplyProvider></section>
    <section data-case="form-answered"><GenerativeReplyProvider messageId="form-answered" status="answered" onReply={async () => { throw new Error('historical form must not submit') }}><Markdown allowGenerativeUI content={fence('aivory-ui', singleFieldForm)} /></GenerativeReplyProvider></section>
    <section data-case="form-readonly"><Markdown allowGenerativeUI content={fence('aivory-ui', singleFieldForm)} /></section>
    <section data-case="presets"><Markdown allowGenerativeUI content={fence('aivory-ui', document)} /></section>
    <section data-case="quoted-news"><Markdown allowGenerativeUI content={fence('aivory-ui', quotedNews)} /></section>
    <section data-case="inline"><Markdown allowGenerativeUI live={live} content={fence('aivory-html', html)} /></section>
    <section data-case="private"><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', document)} /></section>
    <section data-case="private-table"><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', richTable)} /></section>
    <section data-case="private-cards"><PrivateMarkdown allowGenerativeUI text={fence('aivory-ui', cards)} /></section>
    <section data-case="markdown-table"><Markdown allowGenerativeUI content={'| Name | Value |\n| --- | --- |\n| Plain Markdown | 12 |'} /></section>
    <section data-case="user"><Markdown content={fence('aivory-ui', document)} /></section>
    <section data-case="ordinary"><Markdown allowGenerativeUI content={fence('html', '<h1>Ordinary preview</h1>')} /></section>
    <section data-case="incomplete"><Markdown allowGenerativeUI live content={fence('aivory-ui', '{"version":1,')} /></section>
    <section data-case="composer"><Composer modelId="test-model" onModelChange={() => {}} onSubmit={(_text, _attachments, options) => Reflect.set(window, '__SUBMITTED_SKILLS__', options.selectedUserSkillIds)} /></section>
    <section data-case="private-question-chat"><PrivateChat /></section>
  </main></MemoryRouter></TooltipProvider>
}
useAuth.setState({ user: { id: 'test-user', email: 'test@example.test', displayName: 'Test', role: 'admin' } as never, status: 'authenticated' })
useModels.setState({ loaded: true, loading: false, loadedScope: null, loadedPolicyKey: 'personal', models: [{ id: 'test-model', label: 'Test', kind: 'chat', enabled: true, capabilities: ['text'], tool_mode: 'none', param_controls: [] }] as never })
await i18n.changeLanguage('en')
createRoot(globalThis.document.getElementById('root')!).render(<Harness />)
