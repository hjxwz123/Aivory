import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import '@fontsource-variable/geist'
import i18n from '@/i18n'
import '@/styles/globals.css'
import { MessageRow } from '@/components/chat/message-row'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import type { Message } from '@/types/chat'

const query = new URLSearchParams(location.search)
await i18n.changeLanguage(query.get('lang') || 'zh')
document.documentElement.classList.toggle('dark', query.get('theme') === 'dark')
document.documentElement.dataset.accent = 'indigo'
useAuth.setState({ status: 'authenticated', user: { id: 'actions-user', name: 'Test', role: 'admin', email: 'actions@example.test' } as never })
useModels.setState({ models: [{ id: 'actions-model', label: '快速', icon: '⚡', enabled: true, kind: 'chat' }] as never, loaded: true })

interface Options {
  role: Message['role']
  branchCount: number
  readOnly: boolean
  streaming: boolean
  text: string
  columnWidth?: number
}
const initial: Options = {
  role: 'assistant', branchCount: 4, readOnly: false, streaming: false,
  text: '我是 快速，可以帮你查资料、分析问题、写作、写代码或生成图片。有什么需要？',
}
const events: { name: string; value: string }[] = []
const record = (name: string, value: string) => { events.push({ name, value }) }

function Harness() {
  const [options, setOptions] = useState(initial)
  const [index, setIndex] = useState(3)
  const [feedback, setFeedback] = useState<Partial<Message>>({})
  useEffect(() => {
    Reflect.set(window, '__MESSAGE_ACTIONS__', {
      ready: true, events,
      configure: (patch: Partial<Options>) => {
        setOptions((current) => ({ ...current, ...patch }))
        setIndex((patch.branchCount ?? options.branchCount) - 1)
        setFeedback({})
      },
    })
  }, [options.branchCount])
  const siblings = Array.from({ length: options.branchCount }, (_, number) => `branch-${number + 1}`)
  const message: Message = {
    id: siblings[index] || 'branch-1', role: options.role, modelId: 'actions-model',
    content: options.text, createdAt: 1_793_102_520_000,
    branchIndex: index, branchCount: options.branchCount, siblings,
    streaming: options.streaming, genMs: 1600, credits: 1.25,
    ...feedback,
  }
  return <main className="mx-auto w-full max-w-[1000px] px-4 py-8" style={{ maxWidth: options.columnWidth }}>
    <MessageRow
      message={message}
      readOnly={options.readOnly}
      onBranchSwitch={(id) => { record('branch', id); setIndex(siblings.indexOf(id)) }}
      onRegenerate={(id) => record('regenerate', id)}
      onSaveEdit={async (id, text) => { record('save', `${id}:${text}`) }}
      onEdit={(id, text) => record('edit', `${id}:${text}`)}
      onFork={(id) => record('fork', id)}
      onReport={(id) => record('report', id)}
      onDelete={(id) => record('delete', id)}
      onFeedback={async (id, input) => {
        record('feedback', id)
        setFeedback({ liked: input.liked, disliked: input.disliked })
      }}
    />
  </main>
}

createRoot(document.getElementById('root')!).render(<MemoryRouter><TooltipProvider><Harness /></TooltipProvider></MemoryRouter>)
