import { Suspense, lazy, type ComponentType, type LazyExoticComponent } from 'react'
import { Skeleton } from '@/components/ui/skeleton'
import type { DocumentEditorProps, EditorId } from '@/components/files/editors/editor-types'

/**
 * Editor registry. Every entry is a dynamic import, so a format's parser and
 * heavy editor code stay out of the route chunk and are fetched only when the
 * user actually opens the editor. `pptx-react-viewer` alone unpacks to tens of
 * megabytes, which is why this indirection exists even for the small editors.
 */
const EDITORS: Partial<
  Record<EditorId, LazyExoticComponent<ComponentType<DocumentEditorProps>>>
> = {
  code: lazy(() => import('@/components/files/editors/codemirror-editor')),
  sheet: lazy(() => import('@/components/files/editors/spreadsheet-editor')),
  docx: lazy(() => import('@/components/files/editors/docx-editor')),
  pptx: lazy(() => import('@/components/files/editors/pptx-editor')),
}

export interface DocumentEditorHostProps extends DocumentEditorProps {
  editor: EditorId
}

export function DocumentEditor({ editor, ...props }: DocumentEditorHostProps) {
  const Component = EDITORS[editor]
  if (!Component) return null

  return (
    <Suspense
      fallback={
        <div className="h-full min-h-0 p-4" role="status">
          <Skeleton className="h-full w-full rounded-[6px]" />
        </div>
      }
    >
      <Component {...props} />
    </Suspense>
  )
}
