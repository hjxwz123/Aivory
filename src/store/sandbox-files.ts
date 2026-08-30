import { create } from 'zustand'
import { conversationsApi } from '@/api/endpoints'
import type { ApiSandboxFile } from '@/api/types'
import { sandboxParentPath } from '@/lib/sandbox-browser'
import { useConversationFiles } from './conversation-files'
import { useHtmlPreview } from './html-preview'
import { useInlineThreadDrawer } from './inline-thread'

interface SandboxFilesStore {
  open: boolean
  conversationId: string | null
  files: ApiSandboxFile[]
  session: string
  currentPath: string
  loading: boolean
  unavailable: boolean
  error: boolean
  openDrawer: (conversationId: string) => void
  close: () => void
  load: (conversationId?: string) => Promise<void>
  enter: (path: string) => void
  up: () => void
}

export const useSandboxFiles = create<SandboxFilesStore>((set, get) => ({
  open: false,
  conversationId: null,
  files: [],
  session: '',
  currentPath: '',
  loading: false,
  unavailable: false,
  error: false,

  openDrawer(conversationId) {
    useConversationFiles.getState().close()
    useHtmlPreview.getState().close()
    useInlineThreadDrawer.getState().close()
    set({
      open: true,
      conversationId,
      files: [],
      session: '',
      currentPath: '',
      unavailable: false,
      error: false,
    })
    void get().load(conversationId)
  },

  close() {
    set({ open: false })
  },

  async load(requestedConversationId) {
    const conversationId = requestedConversationId ?? get().conversationId
    if (!conversationId) return
    set({ loading: true, error: false })
    try {
      const result = await conversationsApi.sandboxFiles(conversationId)
      if (get().conversationId !== conversationId) return
      set({
        files: result.files ?? [],
        session: result.session ?? '',
        unavailable: result.unavailable === true,
      })
    } catch {
      if (get().conversationId === conversationId) {
        set({ files: [], session: '', unavailable: false, error: true })
      }
    } finally {
      if (get().conversationId === conversationId) set({ loading: false })
    }
  },

  enter(path) {
    set({ currentPath: path })
  },

  up() {
    set((state) => ({ currentPath: sandboxParentPath(state.currentPath) }))
  },
}))
