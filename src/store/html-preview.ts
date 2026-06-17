import { create } from 'zustand'
import { useInlineThreadDrawer } from './inline-thread'
import { useConversationFiles } from './conversation-files'

/**
 * html-preview — drives the live HTML preview drawer on the right side of the
 * chat area. A single code block "owns" the panel at a time (identified by
 * `sourceKey`, derived from message id + block index); while it streams, the
 * owning block keeps pushing fresh markup through `syncHtml`.
 */
interface HtmlPreviewStore {
  open: boolean
  /** Identity of the code block currently driving the panel. */
  sourceKey: string | null
  html: string
  openPreview: (key: string, html: string) => void
  /** Update markup without stealing ownership — no-op unless `key` owns the panel. */
  syncHtml: (key: string, html: string) => void
  close: () => void
}

export const useHtmlPreview = create<HtmlPreviewStore>((set, get) => ({
  open: false,
  sourceKey: null,
  html: '',
  openPreview(key, html) {
    // Mutual exclusion: only one right-edge drawer at a time.
    useInlineThreadDrawer.getState().close()
    useConversationFiles.getState().close()
    set({ open: true, sourceKey: key, html })
  },
  syncHtml(key, html) {
    const s = get()
    if (s.sourceKey === key && s.html !== html) set({ html })
  },
  close() {
    set({ open: false })
  },
}))

/**
 * Blocks that already popped the drawer once. Lives outside the store so a
 * user closing the panel mid-stream isn't fought by the next token tick —
 * each streaming HTML block auto-opens at most once per session.
 */
const autoOpened = new Set<string>()

export function autoOpenPreview(key: string, html: string): void {
  if (autoOpened.has(key)) {
    useHtmlPreview.getState().syncHtml(key, html)
    return
  }
  autoOpened.add(key)
  useHtmlPreview.getState().openPreview(key, html)
}
