import { create } from 'zustand'
import { conversationsApi } from '@/api/endpoints'
import { apiUpload } from '@/api/client'
import type { ApiConversationFile } from '@/api/types'
import { useHtmlPreview } from './html-preview'
import { useInlineThreadDrawer } from './inline-thread'

/**
 * conversation-files — drives the right-side drawer that lists every file the
 * conversation actually references (§ conversation files). The user can upload
 * new files (same effect as attaching in the composer) or remove ones the model
 * should stop seeing. Only one right-edge drawer is visible at a time, so opening
 * this one closes the HTML preview + inline-thread drawers (and they close this).
 */
interface ConversationFilesStore {
  open: boolean
  conversationId: string | null
  files: ApiConversationFile[]
  loading: boolean
  uploading: boolean
  uploadJob: { name: string; progress: number; phase: 'uploading' | 'processing' } | null
  openDrawer: (conversationId: string) => void
  close: () => void
  load: (conversationId: string) => Promise<void>
  upload: (files: FileList | File[]) => Promise<void>
  remove: (fileId: string) => Promise<void>
}

export const useConversationFiles = create<ConversationFilesStore>((set, get) => ({
  open: false,
  conversationId: null,
  files: [],
  loading: false,
  uploading: false,
  uploadJob: null,

  openDrawer(conversationId) {
    // Mutual exclusion: the three right-edge drawers share the same column.
    useHtmlPreview.getState().close()
    useInlineThreadDrawer.getState().close()
    set({ open: true, conversationId })
    void get().load(conversationId)
  },

  close() {
    set({ open: false })
  },

  async load(conversationId) {
    set({ loading: true })
    try {
      const files = await conversationsApi.listFiles(conversationId)
      // Guard against a late response after the drawer moved to another chat.
      if (get().conversationId === conversationId) set({ files })
    } catch {
      if (get().conversationId === conversationId) set({ files: [] })
    } finally {
      if (get().conversationId === conversationId) set({ loading: false })
    }
  },

  async upload(files) {
    const convId = get().conversationId
    if (!convId) return
    const list = Array.from(files)
    if (!list.length) return
    set({ uploading: true })
    try {
      for (const file of list) {
        set({ uploadJob: { name: file.name, progress: 0, phase: 'uploading' } })
        const form = new FormData()
        form.append('file', file)
        // Mirror the composer: anything that isn't an image is ingested as a
        // conversation-scoped RAG document so the model can read it.
        const qs = new URLSearchParams({ conversation_id: convId, rag: '1' })
        await apiUpload(`/files?${qs.toString()}`, form, {
          onProgress: (progress) => {
            if (typeof progress.percent !== 'number') return
            set({ uploadJob: { name: file.name, progress: progress.percent, phase: 'uploading' } })
          },
        })
        set({ uploadJob: { name: file.name, progress: 100, phase: 'processing' } })
      }
      await get().load(convId)
    } finally {
      set({ uploading: false, uploadJob: null })
    }
  },

  async remove(fileId) {
    const convId = get().conversationId
    if (!convId) return
    // Optimistic removal — the model stops referencing it immediately in the UI.
    const prev = get().files
    set({ files: prev.filter((f) => f.id !== fileId) })
    try {
      await conversationsApi.removeFile(convId, fileId)
    } catch {
      // Roll back on failure.
      if (get().conversationId === convId) set({ files: prev })
      throw new Error('remove failed')
    }
  },
}))
