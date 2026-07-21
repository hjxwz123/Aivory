import { Mathematics } from '@tiptap/extension-mathematics'
import Placeholder from '@tiptap/extension-placeholder'
import type { Editor } from '@tiptap/core'
import { Fragment, Slice, type Node as ProseMirrorNode } from '@tiptap/pm/model'
import { NodeSelection, TextSelection } from '@tiptap/pm/state'
import { EditorContent, useEditor } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
} from 'react'
import { composerDocumentToValue, composerValueToDocument } from '@/lib/composer-document'
import { hasMathContent } from '@/lib/math-content'
import { cn } from '@/lib/utils'

export interface FormulaTarget {
  type: 'inlineMath' | 'blockMath'
  pos: number
  latex: string
}

interface SelectionRange {
  from: number
  to: number
}

function isInlineSelectionRange(document: ProseMirrorNode, range: SelectionRange): boolean {
  if (!Number.isInteger(range.from) || !Number.isInteger(range.to)) return false
  if (range.from < 0 || range.to < range.from || range.to > document.content.size) return false
  const from = document.resolve(range.from)
  const to = document.resolve(range.to)
  return from.sameParent(to) && from.parent.inlineContent
}

function findFormulaNode(document: ProseMirrorNode, target: FormulaTarget): {
  node: ProseMirrorNode
  pos: number
} | null {
  const direct = document.nodeAt(target.pos)
  if (
    direct?.type.name === target.type &&
    String(direct.attrs.latex ?? '') === target.latex
  ) {
    return { node: direct, pos: target.pos }
  }

  const matches: Array<{ node: ProseMirrorNode; pos: number; distance: number }> = []
  document.descendants((node, pos) => {
    if (node.type.name !== target.type || String(node.attrs.latex ?? '') !== target.latex) return
    matches.push({ node, pos, distance: Math.abs(pos - target.pos) })
  })
  matches.sort((left, right) => left.distance - right.distance)
  const nearest = matches[0]
  return nearest ? { node: nearest.node, pos: nearest.pos } : null
}

export interface RichComposerEditorHandle {
  focus: (position?: 'start' | 'end') => void
  captureSelection: () => SelectionRange
  setFormula: (latex: string, target: FormulaTarget | null, selection?: SelectionRange | null) => void
}

interface RichComposerEditorProps {
  value: string
  onChange: (value: string) => void
  onSubmit: () => void
  onFormulaClick: (target: FormulaTarget) => void
  onPasteFiles: (files: File[]) => void
  onLongPaste: (text: string) => void
  canAttachLongText: boolean
  maxLength: number
  placeholder: string
  ariaLabel: string
  formulaEditLabel: string
  submitOnEnter?: boolean
  onEscape?: () => void
  compact?: boolean
  mobile?: boolean
  className?: string
}

function enhanceFormulaNodes(editor: Editor, editLabel: string): void {
  if (editor.isDestroyed) return
  editor.view.dom.querySelectorAll<HTMLElement>('.tiptap-mathematics-render--editable').forEach((node) => {
    node.tabIndex = 0
    node.setAttribute('role', 'button')
    let label = node.querySelector<HTMLElement>(':scope > .formula-atom-a11y')
    if (!label) {
      label = document.createElement('span')
      label.className = 'formula-atom-a11y'
      node.prepend(label)
    }
    const renderedFormula = node.querySelector<HTMLElement>('.katex-html')?.textContent
      ?.replace(/\u200b/g, '')
      .trim()
    label.textContent = renderedFormula ? `${editLabel}: ${renderedFormula}` : editLabel
  })
}

export const RichComposerEditor = forwardRef<RichComposerEditorHandle, RichComposerEditorProps>(
  function RichComposerEditor(props, forwardedRef) {
    const propsRef = useRef(props)
    propsRef.current = props
    const editorRef = useRef<ReturnType<typeof useEditor>>(null)
    const lastEmittedValueRef = useRef(props.value)

    const editor = useEditor(
      {
        extensions: [
          StarterKit.configure({
            blockquote: false,
            bold: false,
            bulletList: false,
            code: false,
            codeBlock: false,
            dropcursor: false,
            gapcursor: false,
            heading: false,
            horizontalRule: false,
            italic: false,
            link: false,
            listItem: false,
            listKeymap: false,
            orderedList: false,
            strike: false,
            trailingNode: false,
            underline: false,
          }),
          Placeholder.configure({
            placeholder: () => propsRef.current.placeholder,
            emptyEditorClass: 'is-editor-empty',
          }),
          Mathematics.configure({
            katexOptions: { throwOnError: false, strict: false },
            inlineOptions: {
              onClick: (node, pos) => {
                propsRef.current.onFormulaClick({
                  type: 'inlineMath',
                  pos,
                  latex: String(node.attrs.latex ?? ''),
                })
              },
            },
            blockOptions: {
              onClick: (node, pos) => {
                propsRef.current.onFormulaClick({
                  type: 'blockMath',
                  pos,
                  latex: String(node.attrs.latex ?? ''),
                })
              },
            },
          }),
        ],
        content: composerValueToDocument(props.value),
        editorProps: {
          attributes: {
            class: 'rich-composer-prosemirror',
            role: 'textbox',
            'aria-multiline': 'true',
            'aria-label': props.ariaLabel,
            spellcheck: 'true',
          },
          handleKeyDown: (view, event) => {
            if (event.isComposing || event.keyCode === 229) return false
            const formula = event.target instanceof HTMLElement
              ? event.target.closest<HTMLElement>('.tiptap-mathematics-render--editable')
              : null
            const selectedNode = view.state.selection instanceof NodeSelection
              ? view.state.selection.node
              : null
            const selectedMath = selectedNode && (selectedNode.type.name === 'inlineMath' || selectedNode.type.name === 'blockMath')
              ? selectedNode
              : null
            if ((formula || selectedMath) && (event.key === 'Enter' || event.key === ' ')) {
              event.preventDefault()
              if (formula) formula.click()
              else if (selectedMath) {
                propsRef.current.onFormulaClick({
                  type: selectedMath.type.name as FormulaTarget['type'],
                  pos: view.state.selection.from,
                  latex: String(selectedMath.attrs.latex ?? ''),
                })
              }
              return true
            }
            if (event.key === 'Escape' && propsRef.current.onEscape) {
              event.preventDefault()
              propsRef.current.onEscape()
              return true
            }
            if (event.key !== 'Enter') return false
            if (event.metaKey || event.ctrlKey) {
              event.preventDefault()
              propsRef.current.onSubmit()
              return true
            }
            if (propsRef.current.submitOnEnter === false && !event.shiftKey) return false
            if (event.shiftKey && !event.metaKey && !event.ctrlKey) {
              const hardBreak = view.state.schema.nodes.hardBreak
              if (!hardBreak) return false
              event.preventDefault()
              view.dispatch(view.state.tr.replaceSelectionWith(hardBreak.create()).scrollIntoView())
              return true
            }
            event.preventDefault()
            propsRef.current.onSubmit()
            return true
          },
          handlePaste: (view, event) => {
            const clipboardEvent = event as ClipboardEvent
            const images = Array.from(clipboardEvent.clipboardData?.items ?? [])
              .filter((item) => item.kind === 'file' && item.type.startsWith('image/'))
              .map((item) => item.getAsFile())
              .filter((file): file is File => file !== null)
            if (images.length > 0) {
              event.preventDefault()
              propsRef.current.onPasteFiles(images)
              return true
            }

            const pasted = clipboardEvent.clipboardData?.getData('text/plain') ?? ''
            if (!pasted) return false
            const pastedHtml = clipboardEvent.clipboardData?.getData('text/html') ?? ''
            if (/data-type=["'](?:inline|block)-math["']/.test(pastedHtml)) return false
            event.preventDefault()
            const { from, to } = view.state.selection
            let transaction = view.state.tr
            if (hasMathContent(pasted)) {
              const parsed = view.state.schema.nodeFromJSON(composerValueToDocument(pasted))
              transaction = transaction.replaceRange(from, to, Slice.maxOpen(parsed.content))
            } else {
              transaction = transaction.insertText(pasted, from, to)
            }
            const projected = composerDocumentToValue(transaction.doc.toJSON())
            if (propsRef.current.canAttachLongText && projected.length > propsRef.current.maxLength) {
              propsRef.current.onLongPaste(pasted)
              return true
            }
            view.dispatch(transaction.scrollIntoView())
            return true
          },
          clipboardTextSerializer: (slice) =>
            composerDocumentToValue({
              type: 'doc',
              content: slice.content.toJSON() ?? [],
            }),
        },
        onUpdate: ({ editor: currentEditor }) => {
          const next = composerDocumentToValue(currentEditor.getJSON())
          lastEmittedValueRef.current = next
          propsRef.current.onChange(next)
          enhanceFormulaNodes(currentEditor, propsRef.current.formulaEditLabel)
        },
        onMount: ({ editor: mountedEditor }) => {
          mountedEditor.view.dom.setAttribute('aria-label', propsRef.current.ariaLabel)
          const nextValue = propsRef.current.value
          const currentValue = composerDocumentToValue(mountedEditor.getJSON())
          if (nextValue !== currentValue && nextValue !== lastEmittedValueRef.current) {
            mountedEditor.commands.setContent(composerValueToDocument(nextValue), { emitUpdate: false })
            lastEmittedValueRef.current = nextValue
          }
          enhanceFormulaNodes(mountedEditor, propsRef.current.formulaEditLabel)
        },
      },
      [],
    )
    editorRef.current = editor

    useEffect(() => {
      if (!editor || editor.isDestroyed) return
      editor.view.dom.setAttribute('aria-label', props.ariaLabel)
      enhanceFormulaNodes(editor, props.formulaEditLabel)
    }, [editor, props.ariaLabel, props.formulaEditLabel])

    useEffect(() => {
      if (!editor || editor.isDestroyed) return
      const current = composerDocumentToValue(editor.getJSON())
      if (props.value === current || props.value === lastEmittedValueRef.current) return
      editor.commands.setContent(composerValueToDocument(props.value), { emitUpdate: false })
      lastEmittedValueRef.current = props.value
      enhanceFormulaNodes(editor, props.formulaEditLabel)
    }, [editor, props.formulaEditLabel, props.value])

    useImperativeHandle(
      forwardedRef,
      () => ({
        focus: (position) => {
          const focusEditor = (retries: number) => {
            const currentEditor = editorRef.current
            if (currentEditor && !currentEditor.isDestroyed) {
              if (position) currentEditor.commands.focus(position)
              else currentEditor.view.focus()
              return
            }
            if (retries > 0) requestAnimationFrame(() => focusEditor(retries - 1))
          }
          focusEditor(2)
        },
        captureSelection: () => {
          const currentEditor = editorRef.current
          const selection = currentEditor && !currentEditor.isDestroyed
            ? currentEditor.state.selection
            : null
          return selection ? { from: selection.from, to: selection.to } : { from: 1, to: 1 }
        },
        setFormula: (latex, target, selection) => {
          const currentEditor = editorRef.current
          const normalized = latex.trim()
          if (!currentEditor || currentEditor.isDestroyed || !normalized) return
          const { state, view } = currentEditor
          const transaction = state.tr

          if (target) {
            const match = findFormulaNode(transaction.doc, target)
            if (!match) return
            transaction.setNodeMarkup(match.pos, match.node.type, { ...match.node.attrs, latex: normalized })
            transaction.setSelection(TextSelection.near(transaction.doc.resolve(match.pos + match.node.nodeSize)))
          } else {
            const requestedRange = selection ?? { from: state.selection.from, to: state.selection.to }
            const range = isInlineSelectionRange(transaction.doc, requestedRange)
              ? requestedRange
              : (() => {
                  const position = Math.max(0, Math.min(requestedRange.from, transaction.doc.content.size))
                  const fallback = TextSelection.near(transaction.doc.resolve(position))
                  return { from: fallback.from, to: fallback.to }
                })()
            const node = state.schema.nodes.inlineMath?.create({ latex: normalized })
            if (!node) return
            transaction.replaceWith(range.from, range.to, Fragment.from(node))
            transaction.setSelection(TextSelection.near(transaction.doc.resolve(range.from + node.nodeSize)))
          }

          view.dispatch(transaction.scrollIntoView())
          enhanceFormulaNodes(currentEditor, propsRef.current.formulaEditLabel)
          view.focus()
        },
      }),
      [],
    )

    return (
      <EditorContent
        editor={editor}
        className={cn(
          'rich-composer-editor min-w-0 w-full',
          props.mobile && 'rich-composer-editor--mobile',
          props.compact && 'rich-composer-editor--compact',
          props.className,
        )}
      />
    )
  },
)
