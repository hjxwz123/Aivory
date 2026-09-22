import { useEffect, useRef } from 'react'
import { EditorState, type Extension } from '@codemirror/state'
import {
  EditorView,
  crosshairCursor,
  drawSelection,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import {
  HighlightStyle,
  bracketMatching,
  foldGutter,
  indentOnInput,
  syntaxHighlighting,
} from '@codemirror/language'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { html } from '@codemirror/lang-html'
import { markdown } from '@codemirror/lang-markdown'
import { tags } from '@lezer/highlight'
import { extensionOf } from '@/lib/file-preview-kind'
import type { DocumentEditorProps } from '@/components/files/editors/editor-types'

/**
 * CodeMirror 6 source editor for markup and text artifacts.
 *
 * Every colour is a design token rather than a hard-coded palette, so the
 * editor follows the app's light/dark themes and matches the read-only code
 * blocks instead of looking like a bolted-on third-party widget.
 */
const editorTheme = EditorView.theme({
  '&': {
    height: '100%',
    color: 'var(--color-code-fg)',
    backgroundColor: 'var(--color-code-bg)',
    fontSize: '0.8125rem',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': {
    fontFamily: 'var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace)',
    lineHeight: '1.6',
  },
  '.cm-content': { caretColor: 'var(--color-accent)', padding: '0.5rem 0' },
  '.cm-gutters': {
    backgroundColor: 'var(--color-code-bg)',
    color: 'var(--color-fg-faint)',
    border: 'none',
    borderRight: '1px solid var(--color-divider)',
  },
  '.cm-activeLine': { backgroundColor: 'var(--color-bg-muted)' },
  '.cm-activeLineGutter': {
    backgroundColor: 'var(--color-bg-muted)',
    color: 'var(--color-fg-muted)',
  },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--color-accent)' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
    backgroundColor: 'var(--color-accent-soft)',
  },
  '.cm-selectionMatch': { backgroundColor: 'var(--color-warning-soft)' },
  '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': {
    backgroundColor: 'var(--color-accent-soft)',
    outline: '1px solid var(--color-accent)',
  },
  '.cm-foldPlaceholder': {
    backgroundColor: 'var(--color-bg-muted)',
    border: '1px solid var(--color-border)',
    color: 'var(--color-fg-muted)',
  },
  '.cm-panels': {
    backgroundColor: 'var(--color-surface)',
    color: 'var(--color-fg)',
    borderColor: 'var(--color-divider)',
  },
  '.cm-panels input, .cm-panels button': {
    backgroundColor: 'var(--color-bg)',
    color: 'var(--color-fg)',
    border: '1px solid var(--color-border)',
    borderRadius: '8px',
    padding: '2px 8px',
  },
  '.cm-searchMatch': { backgroundColor: 'var(--color-warning-soft)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--color-accent-soft)' },
  '.cm-tooltip': {
    backgroundColor: 'var(--color-surface-raised)',
    border: '1px solid var(--color-border)',
    color: 'var(--color-fg)',
  },
})

const editorHighlight = HighlightStyle.define([
  { tag: tags.comment, color: 'var(--color-syntax-comment)', fontStyle: 'italic' },
  { tag: [tags.keyword, tags.operatorKeyword, tags.modifier], color: 'var(--color-syntax-keyword)' },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: 'var(--color-syntax-string)' },
  { tag: [tags.number, tags.bool, tags.null], color: 'var(--color-syntax-number)' },
  { tag: [tags.function(tags.variableName), tags.labelName], color: 'var(--color-syntax-fn)' },
  {
    tag: [tags.typeName, tags.className, tags.tagName, tags.attributeName],
    color: 'var(--color-syntax-type)',
  },
  { tag: [tags.propertyName, tags.variableName], color: 'var(--color-code-fg)' },
  { tag: tags.emphasis, fontStyle: 'italic' },
  { tag: tags.strong, fontWeight: '600' },
  { tag: tags.link, color: 'var(--color-accent)', textDecoration: 'underline' },
  { tag: tags.heading, color: 'var(--color-fg)', fontWeight: '600' },
  { tag: tags.invalid, color: 'var(--color-danger)' },
])

function languageFor(extension: string): Extension[] {
  if (extension === 'html' || extension === 'htm' || extension === 'xhtml') return [html()]
  if (extension === 'md' || extension === 'markdown') return [markdown()]
  return []
}

function mimeFor(extension: string): string {
  switch (extension) {
    case 'html':
    case 'htm':
    case 'xhtml':
      return 'text/html'
    case 'csv':
      return 'text/csv'
    case 'tsv':
      return 'text/tab-separated-values'
    case 'md':
    case 'markdown':
      return 'text/markdown'
    case 'json':
      return 'application/json'
    case 'xml':
      return 'application/xml'
    case 'yml':
    case 'yaml':
      return 'application/x-yaml'
    default:
      return 'text/plain'
  }
}

const MARKUP_EXTENSIONS = new Set(['html', 'htm', 'xhtml'])

export default function CodeMirrorEditor({ name, data, onChange }: DocumentEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  useEffect(() => {
    const host = hostRef.current
    if (!host) return

    const extension = extensionOf(name)
    const original = new TextDecoder('utf-8', { fatal: false }).decode(data)
    const isMarkup = MARKUP_EXTENSIONS.has(extension)

    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: original,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          highlightSpecialChars(),
          history(),
          foldGutter(),
          drawSelection(),
          dropCursor(),
          EditorState.allowMultipleSelections.of(true),
          indentOnInput(),
          bracketMatching(),
          rectangularSelection(),
          crosshairCursor(),
          highlightActiveLine(),
          highlightSelectionMatches(),
          EditorView.lineWrapping,
          // Search is reachable from the keyboard (Ctrl/Cmd-F) rather than a
          // toolbar, which keeps the narrow panel free of editor chrome.
          keymap.of([...defaultKeymap, ...searchKeymap, ...historyKeymap, indentWithTab]),
          syntaxHighlighting(editorHighlight),
          editorTheme,
          ...languageFor(extension),
          EditorView.updateListener.of((update) => {
            if (!update.docChanged) return
            const text = update.state.doc.toString()
            if (text === original) {
              onChangeRef.current(null)
              return
            }
            onChangeRef.current({
              bytes: new Blob([text], { type: mimeFor(extension) }),
              ...(isMarkup ? { previewHtml: text } : {}),
            })
          }),
        ],
      }),
    })

    return () => view.destroy()
  }, [data, name])

  return <div ref={hostRef} className="h-full min-h-0 overflow-hidden" />
}
