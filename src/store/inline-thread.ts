import { create } from 'zustand'
import { useHtmlPreview } from './html-preview'
import { useConversationFiles } from './conversation-files'

/**
 * inline-thread — drives the right-side drawer that shows a text-selection
 * sub-conversation (§ text-selection threads). Only one right-edge drawer may be
 * visible at a time, so opening this one closes the HTML preview and vice-versa
 * (the coordination lives here + in html-preview.ts).
 */
interface InlineThreadDrawerStore {
  open: boolean
  /** The child (inline) conversation id rendered in the drawer. */
  childId: string | null
  /** The quoted excerpt the thread is anchored to (for the drawer header). */
  quote: string
  openThread: (args: { childId: string; quote: string }) => void
  close: () => void
}

export const useInlineThreadDrawer = create<InlineThreadDrawerStore>((set) => ({
  open: false,
  childId: null,
  quote: '',
  openThread({ childId, quote }) {
    // Mutual exclusion: the HTML preview and this drawer share the right edge.
    useHtmlPreview.getState().close()
    useConversationFiles.getState().close()
    set({ open: true, childId, quote })
  },
  close() {
    set({ open: false })
  },
}))
