import { create } from 'zustand'
import { useInlineThreadDrawer } from './inline-thread'
import { useConversationFiles } from './conversation-files'
import { useSandboxFiles } from './sandbox-files'
import type { Attachment } from '@/types/chat'

/**
 * One right-edge Artifact panel, two kinds of source.
 *
 * - `html`: a live HTML code block (identified by `sourceKey`, derived from
 *   message id + block index). While the message streams, the owning block
 *   keeps pushing fresh markup through `syncHtml`.
 * - `file`: a conversation attachment, sandbox output, or knowledge-base
 *   document, fetched once and rendered by the shared DocumentPreview.
 *
 * Both used to live in separate surfaces (a side panel and a centered dialog);
 * unifying them keeps the conversation readable while a document is open.
 */
export type ArtifactSource =
  | { type: 'html'; sourceKey: string; html: string; shareable: boolean }
  | {
      type: 'file'
      name: string
      url: string
      kind: Attachment['kind']
      authenticated: boolean
      attachmentId?: string
      onLoadError?: (status?: number) => void
    }

interface ArtifactPanelStore {
  open: boolean
  source: ArtifactSource | null
  openArtifact: (source: ArtifactSource) => void
  /** Update markup without stealing ownership — no-op unless `sourceKey` owns the panel. */
  syncHtml: (sourceKey: string, html: string, shareable?: boolean) => void
  close: () => void
}

export const useArtifactPanel = create<ArtifactPanelStore>((set, get) => ({
  open: false,
  source: null,
  openArtifact(source) {
    // Mutual exclusion: only one right-edge drawer at a time.
    useInlineThreadDrawer.getState().close()
    useConversationFiles.getState().close()
    useSandboxFiles.getState().close()
    set({ open: true, source })
  },
  syncHtml(sourceKey, html, shareable = false) {
    const { source } = get()
    if (!source || source.type !== 'html' || source.sourceKey !== sourceKey) return
    if (source.html === html && source.shareable === shareable) return
    set({ source: { type: 'html', sourceKey, html, shareable } })
  },
  close() {
    set({ open: false })
  },
}))

/**
 * Blocks that already popped the panel once. Lives outside the store so a
 * user closing the panel mid-stream isn't fought by the next token tick —
 * each streaming HTML block auto-opens at most once per session.
 */
const autoOpened = new Set<string>()

export function autoOpenPreview(sourceKey: string, html: string): void {
  if (autoOpened.has(sourceKey)) {
    useArtifactPanel.getState().syncHtml(sourceKey, html, false)
    return
  }
  autoOpened.add(sourceKey)
  useArtifactPanel.getState().openArtifact({ type: 'html', sourceKey, html, shareable: false })
}
