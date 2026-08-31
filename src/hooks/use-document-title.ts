import { useEffect } from 'react'

type DocumentTitleTarget = Pick<Document, 'title'>

export function formatDocumentTitle(pageTitle: string, appName: string): string {
  const normalizedPageTitle = pageTitle.trim()
  const normalizedAppName = appName.trim()

  if (!normalizedPageTitle) return normalizedAppName
  if (!normalizedAppName) return normalizedPageTitle
  return `${normalizedPageTitle} | ${normalizedAppName}`
}

export function applyDocumentTitle(
  title: string,
  target: DocumentTitleTarget = document,
): () => void {
  const previousTitle = target.title
  target.title = title
  return () => {
    target.title = previousTitle
  }
}

/** Sets a page-scoped browser title and restores the previous title on exit. */
export function useDocumentTitle(title?: string): void {
  useEffect(() => {
    if (!title) return
    return applyDocumentTitle(title)
  }, [title])
}
