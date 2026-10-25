import { createRoot } from 'react-dom/client'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '@fontsource-variable/geist'
import i18n from '@/i18n'
import '@/styles/globals.css'
import 'katex/dist/katex.min.css'
import ChatThread from '@/pages/chat/ChatThread'
import { MessageRow } from '@/components/chat/message-row'
import { PrivateMessageRow } from '@/components/chat/private-message-row'
import { InlineThreadPanel } from '@/components/chat/inline-thread-panel'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { useModels } from '@/store/models'
import { useInlineThreadDrawer } from '@/store/inline-thread'
import type { Conversation, Message } from '@/types/chat'

const query = new URLSearchParams(location.search)
const view = query.get('view') || 'main'
const now = Date.now()
const baseMessages: Message[] = [
  { id: 'question-one', role: 'user', content: '周末想去附近走走，怎么安排比较轻松？', createdAt: now - 120_000 },
  { id: 'answer-one', role: 'assistant', modelId: 'wait-model', content: '可以选择交通方便的小城，留出一整段不用赶路的时间。\n\n第一天慢慢逛，第二天找一处安静的地方吃午饭，再从容返程。', createdAt: now - 110_000 },
]
let fixture: Conversation = {
  id: 'reply-wait', title: '周末短途旅行', modelId: 'wait-model', creatorId: 'wait-user',
  createdAt: now, updatedAt: now, messages: baseMessages,
}
const model = { id: 'wait-model', label: 'Aivory AI', icon: '', kind: 'chat', enabled: true, capabilities: ['text'], tool_mode: 'none', param_controls: [] }

function publish() {
  useConversations.setState({ conversations: [fixture] })
}
function send(text = '帮我整理一个两天的旅行计划，包含交通、预算和下雨时的替代安排。') {
  fixture = { ...fixture, messages: [...baseMessages,
    { id: 'question-two', role: 'user', content: text, createdAt: Date.now() },
    { id: 'waiting-reply', role: 'assistant', modelId: 'wait-model', content: '', createdAt: Date.now(), streaming: true },
  ] }
  publish()
}
function phase(name: string, age = 0) {
  const current = fixture.messages.at(-1)!
  let patch: Partial<Message>
  if (name === 'waiting') {
    patch = { content: '', streaming: true, stopped: false, error: undefined, reasoning: undefined, research: undefined, imageStatus: undefined, artifacts: undefined, createdAt: Date.now() - age }
  } else if (name === 'reasoning') {
    patch = { reasoning: [{ kind: 'thinking', id: 'thought', text: '我会优先考虑交通方便、步行距离短的路线，再安排适合下雨时调整的室内活动。' }] }
  } else if (name === 'tool') {
    patch = { reasoning: [{ kind: 'tool', id: 'search', tool: { id: 'search', name: 'web_search', label: '查询交通与开放时间', startedAt: Date.now(), status: 'running' } }] }
  } else if (name === 'answer' || name === 'done') {
    patch = { content: '## 两天的轻松行程\n\n第一天乘上午的高铁出发，午后在老街散步。第二天参观博物馆，吃过午饭后返程。', streaming: name === 'answer' }
  } else if (name === 'stopped') {
    patch = { content: '', streaming: false, stopped: true }
  } else if (name === 'error') {
    patch = { content: '', streaming: false, error: '测试：模型暂时无法响应' }
  } else if (name === 'image') {
    patch = { imageStatus: 'generating' }
  } else if (name === 'research') {
    patch = { research: { title: '旅行安排', tasks: [{ id: 'task', question: '比较出行方案', status: 'researching' }], sources: [] } }
  } else throw new Error(`Unknown phase: ${name}`)
  fixture = { ...fixture, messages: fixture.messages.map((message) => message.id === current.id ? { ...message, ...patch } : message) }
  publish()
}
useAuth.setState({ user: { id: 'wait-user', name: 'Test', role: 'admin', email: 'wait@example.test', settings: { onboarded: true } } as never, status: 'authenticated' })
useModels.setState({ loaded: true, loading: false, loadedScope: null, loadedPolicyKey: 'personal', models: [model] as never })
useConversations.setState({
  accountId: 'wait-user', conversations: [fixture],
  loadOne: async () => {}, loadInlineThreads: async () => {},
  sendMessage: async ({ text }) => send(text), abortStream: () => phase('stopped'),
})
if (view !== 'main') send()
if (view === 'thread') useInlineThreadDrawer.setState({ open: true, childId: fixture.id, quote: '选择交通方便的小城，留出不用赶路的时间。' })
await i18n.changeLanguage(query.get('lang') || 'zh')
document.documentElement.dataset.theme = query.get('theme') || 'light'
document.documentElement.classList.toggle('dark', query.get('theme') === 'dark')
Reflect.set(window, '__REPLY_WAIT__', { phase, send })

function PrivateRows() {
  const conversation = useConversations((state) => state.conversations[0])
  return <main className="mx-auto flex w-full max-w-[760px] flex-col gap-7 px-5 py-8">
    {conversation.messages.map((message) => <PrivateMessageRow key={message.id} model={model as never} message={{
      id: conversation.messages.indexOf(message), role: message.role === 'user' ? 'user' : 'assistant', text: message.content,
      createdAt: message.createdAt, streaming: message.streaming, stopped: message.stopped,
      error: message.error ? 'private_provider_error' : undefined,
      reasoning: message.reasoning?.filter((item) => item.kind === 'thinking').map((item) => item.text).join('\n'),
    }} />)}
  </main>
}

createRoot(document.getElementById('root')!).render(
  <TooltipProvider><MemoryRouter initialEntries={['/chat/reply-wait']}>
    <div className="flex h-dvh w-full overflow-hidden bg-[var(--color-bg)]">
      {view === 'private' ? <div className="w-full overflow-y-auto"><PrivateRows /></div> : view === 'thread' ? <>
        <div className="hidden min-w-0 flex-1 p-10 lg:block"><MessageRow message={baseMessages[1]} /></div>
        <InlineThreadPanel />
      </> : <Routes><Route path="/chat/:id" element={<ChatThread />} /></Routes>}
    </div>
  </MemoryRouter></TooltipProvider>,
)
