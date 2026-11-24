import { createElement, Fragment, useId, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { ArrowUpDown, ImageOff, Loader2, Star } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { blockMarkdownToHtml, inlineMarkdownToHtml, type CiteRef } from '@/lib/markdown'
import { dedentMarkupProse, evaluateMarkupExpression, hasBlockMarkupProse, mapMarkupExpressions, markupAttribute, markupBinding, markupElements, markupReply, markupText, spaceMarkupEmphasis, type MarkupDocument, type MarkupElement, type MarkupNode, type MarkupValue } from '@/lib/generative-markup'
import { markupPreset } from '@/lib/generative-markup-presets'
import { markupTableParts, markupTableSpan } from '@/lib/generative-markup-table'
import { isArtifactUrl } from '@/lib/artifact-links'
import type { UIBlock } from '@/lib/generative-ui'
import { useGenerativeReply } from './generative-reply-context'
import { Tooltip, TooltipProvider } from '@/components/ui/tooltip'
import { GenerativeSVG } from './generative-svg'
import { GenerativeMap } from './generative-map'

const safeURL = (value: unknown) => {
  if (typeof value !== 'string') return undefined
  if (isArtifactUrl(value)) return value
  try { const url = new URL(value); return ['http:', 'https:'].includes(url.protocol) && !url.username && !url.password ? url.href : undefined } catch { return undefined }
}
const scalar = (value: MarkupValue | undefined) => value === undefined || value === null ? '' : Array.isArray(value) ? value.join(', ') : String(value)
const palette = new Set(['primary', 'secondary', 'muted', 'accent', 'success', 'warning', 'danger', 'info'])
const length = (value: MarkupValue | undefined) => typeof value === 'number' && value >= 0 && value <= 1200 ? `${value}px` : typeof value === 'string' && /^(?:0|auto|\d{1,4}(?:\.\d{1,2})?(?:px|rem|em|%|ch))$/.test(value) ? value : undefined
const bounded = (value: unknown, min: number, max: number, fallback: number) => typeof value === 'number' && Number.isFinite(value) ? Math.min(max, Math.max(min, value)) : fallback

function layoutStyle(node: MarkupElement, state: Record<string, MarkupValue>): CSSProperties {
  const a = (name: string) => markupAttribute(node, name, state)
  const style: CSSProperties = {}
  const space = (name: string) => a(name) === undefined ? undefined : `${bounded(Number(a(name)), 0, 24, 0) * 4}px`
  style.gap = space('gap'); style.padding = space('padding')
  style.paddingInline = space('px'); style.paddingBlock = space('py')
  style.width = length(a('width')); style.maxWidth = length(a('maxwidth')); style.height = length(a('height'))
  const flex = a('flex'); if (flex !== undefined && Number.isFinite(Number(flex))) style.flex = bounded(Number(flex), 0, 12, 1)
  const align = scalar(a('align')), justify = scalar(a('justify'))
  style.alignItems = ({ start: 'flex-start', end: 'flex-end', center: 'center', stretch: 'stretch', baseline: 'baseline' } as const)[align]
  style.justifyContent = ({ start: 'flex-start', end: 'flex-end', center: 'center', between: 'space-between', around: 'space-around', evenly: 'space-evenly' } as const)[justify]
  const radius = a('radius')
  if (radius !== undefined) style.borderRadius = ({ none: 0, sm: 4, md: 8, lg: 12, xl: 16, full: 999 } as Record<string, number>)[String(radius)] ?? bounded(radius, 0, 16, 8)
  const cols = a('columns') ?? a('cols')
  if (node.tag === 'grid') Object.assign(style, { '--gui-columns': bounded(Number(cols), 1, 6, 2), '--gui-min-column': length(a('minwidth')) ?? '210px' })
  const size = scalar(a('size'))
  if (['xs', 'sm', 'md', 'lg', 'xl', '2xl'].includes(size)) style.fontSize = ({ xs: '.75rem', sm: '.8125rem', md: '.9375rem', lg: '1.125rem', xl: '1.375rem', '2xl': '1.75rem' } as Record<string, string>)[size]
  const weight = scalar(a('weight'))
  if (['normal', 'medium', 'semibold', 'bold'].includes(weight)) style.fontWeight = ({ normal: 400, medium: 500, semibold: 600, bold: 700 } as Record<string, number>)[weight]
  const textAlign = scalar(a('textalign')); if (['left', 'center', 'right'].includes(textAlign)) style.textAlign = textAlign as CSSProperties['textAlign']
  return style
}

function tableCellProps(node: MarkupElement, state: Record<string, MarkupValue>) {
  const a = (name: string) => markupAttribute(node, name, state)
  const style = layoutStyle(node, state)
  const align = scalar(a('align')), vertical = scalar(a('valign') ?? a('verticalalign'))
  if (['left', 'center', 'right'].includes(align)) style.textAlign = align as CSSProperties['textAlign']
  if (['top', 'middle', 'bottom'].includes(vertical)) style.verticalAlign = vertical as CSSProperties['verticalAlign']
  style.minWidth = length(a('minwidth'))
  if (a('nowrap') === true) style.whiteSpace = 'nowrap'
  const scope = scalar(a('scope'))
  return {
    style,
    colSpan: markupTableSpan(a('colspan')),
    rowSpan: markupTableSpan(a('rowspan'), true),
    scope: node.tag === 'th' && ['row', 'col', 'rowgroup', 'colgroup'].includes(scope) ? scope : undefined,
  }
}

function MarkupImage({ node, state }: { node: MarkupElement; state: Record<string, MarkupValue> }) {
  const { t } = useTranslation('chat')
  const [failed, setFailed] = useState(false)
  const src = safeURL(markupAttribute(node, 'src', state) ?? markupAttribute(node, 'url', state))
  const alt = scalar(markupAttribute(node, 'alt', state) ?? markupAttribute(node, 'query', state)) || t('generative.image')
  const ratio = scalar(markupAttribute(node, 'aspectratio', state)).replace(':', '/')
  const style = layoutStyle(node, state)
  if (/^\d{1,2}(?:\.\d+)?\s*\/\s*\d{1,2}(?:\.\d+)?$/.test(ratio)) style.aspectRatio = ratio
  return <figure className={`gui-markup-image ${node.tag === 'avatar' ? 'gui-markup-avatar' : ''}`} style={style}>
    {src && !failed ? <img src={src} alt={alt} loading="lazy" decoding="async" referrerPolicy="no-referrer" onError={() => setFailed(true)} /> : <span role="status"><ImageOff size={18} aria-hidden /><span>{src ? t('generative.imageFailed') : alt}</span></span>}
    {markupAttribute(node, 'caption', state) && <figcaption>{scalar(markupAttribute(node, 'caption', state))}</figcaption>}
  </figure>
}

function MarkupTabs({ node, render }: { node: MarkupElement; render: (nodes: MarkupNode[], path: string) => ReactNode }) {
  const id = useId(), [active, setActive] = useState(0)
  const tabs = markupElements(node.children).filter(child => ['tab', 'item'].includes(child.tag))
  const selected = Math.min(active, Math.max(0, tabs.length - 1))
  const extra = node.children.filter(child => typeof child === 'string' || !['tab', 'item'].includes(child.tag))
  if (!tabs.length) return <div className="gui-markup-tabs">{render(node.children, 'fallback')}</div>
  return <div className="gui-markup-tabs">
    {render(extra, 'extra')}
    <div role="tablist" aria-label={scalar(markupAttribute(node, 'label') ?? markupAttribute(node, 'title')) || 'Tabs'} onKeyDown={event => {
      const next = event.key === 'ArrowRight' ? (selected + 1) % tabs.length : event.key === 'ArrowLeft' ? (selected + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1
      if (next < 0) return
      event.preventDefault(); setActive(next); event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus()
    }}>{tabs.map((tab, i) => <button key={i} type="button" role="tab" id={`${id}-tab-${i}`} aria-controls={`${id}-panel-${i}`} aria-selected={selected === i} tabIndex={selected === i ? 0 : -1} onClick={() => setActive(i)}>{scalar(markupAttribute(tab, 'title') ?? markupAttribute(tab, 'label')) || String(i + 1)}</button>)}</div>
    {tabs.map((tab, i) => <div key={i} role="tabpanel" id={`${id}-panel-${i}`} aria-labelledby={`${id}-tab-${i}`} hidden={selected !== i}>{render(tab.children, `tab-${i}`)}</div>)}
  </div>
}

function MarkupTable({ node, render, state }: { node: MarkupElement; render: (nodes: MarkupNode[], path: string, inline?: boolean) => ReactNode; state: Record<string, MarkupValue> }) {
  const { t } = useTranslation('chat')
  const [query, setQuery] = useState(''), [sort, setSort] = useState<{ column: number; direction: number }>()
  const children = markupElements(node.children)
  const { head, headers, foot, bodies, columns, flat } = markupTableParts(node, state)
  const structural = new Set('caption colgroup col thead tbody tfoot tr th td'.split(' '))
  const flatten = (nodes: MarkupNode[]): MarkupNode[] => nodes.flatMap(child => typeof child !== 'string' && structural.has(child.tag) ? flatten(child.children) : [child])
  const rows = [head, ...bodies, foot].flatMap(group => markupElements(group?.children ?? [], 'tr'))
  const hasCells = rows.some(row => markupElements(row.children).some(cell => cell.tag === 'th' || cell.tag === 'td'))
  const hasContent = flatten(node.children.filter(child => typeof child === 'string' || !['caption', 'colgroup', 'col'].includes(child.tag))).some(child => typeof child !== 'string' || !!child.trim())
  const invalidGroups = [head, ...bodies, foot].some(group => group?.children.some(child => typeof child === 'string' ? !!child.trim() : child.tag !== 'tr'))
  if ((!hasCells && hasContent) || invalidGroups || rows.some(row => row.children.some(child => typeof child === 'string' ? !!child.trim() : !['th', 'td'].includes(child.tag)))) return <div className="gui-markup-prose">{render(flatten(node.children), 'fallback')}</div>
  const sortable = flat && !!markupAttribute(node, 'sortable', state), searchable = flat && !!markupAttribute(node, 'searchable', state)
  const layout = scalar(markupAttribute(node, 'layout', state) ?? markupAttribute(node, 'tablelayout', state)) === 'fixed' ? 'fixed' : 'auto'
  const borders = scalar(markupAttribute(node, 'borders', state))
  const tableStyle = { ...layoutStyle(node, state), minWidth: length(markupAttribute(node, 'minwidth', state)), tableLayout: layout } satisfies CSSProperties
  const valueText = (child: MarkupElement) => markupText(child.children).replace(/\{(\w+)\}/g, (_, name: string) => scalar(state[name]))
  return <div className="gui-markup-table" role="region" aria-label={scalar(markupAttribute(node, 'label', state) ?? markupAttribute(node, 'title', state)) || t('generative.table')} tabIndex={0}>
    {render(node.children.filter(child => typeof child === 'string' || !structural.has(child.tag)), 'extra')}
    {searchable && <input className="gui-input" aria-label={t('generative.filterTable')} placeholder={t('generative.filterTable')} value={query} onChange={event => setQuery(event.target.value)} />}
    <table style={tableStyle} data-layout={layout} data-borders={['all', 'none'].includes(borders) ? borders : markupAttribute(node, 'bordered', state) || markupAttribute(node, 'border', state) ? 'all' : 'rows'}>
      {children.filter(child => child.tag === 'caption').map((child, i) => <caption key={i}>{render(child.children, 'caption', true)}</caption>)}
      {render(children.filter(child => child.tag === 'colgroup' || child.tag === 'col'), 'columns')}
      {head && <thead style={layoutStyle(head, state)}>{sortable && headers[0] ? <tr>{markupElements(headers[0].children).map((cell, i) => <th key={i} {...tableCellProps(cell, state)} scope="col" aria-sort={sort?.column === i ? sort.direction === 1 ? 'ascending' : 'descending' : 'none'}><div className="gui-markup-table-heading">{render(cell.children, `header-${i}`, true)}<button className="gui-markup-table-sort" type="button" aria-label={t('generative.sortColumn', { column: valueText(cell) })} title={t('generative.sortColumn', { column: valueText(cell) })} onClick={() => setSort(current => ({ column: i, direction: current?.column === i ? -current.direction : 1 }))}><ArrowUpDown size={14} aria-hidden /></button></div></th>)}</tr> : render(head.children, 'head')}</thead>}
      {bodies.map((body, index) => {
        const rows = markupElements(body.children, 'tr')
        const filtered = rows.filter(row => !searchable || !query.trim() || valueText(row).toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))
        if (sortable && sort) filtered.sort((a, b) => valueText(markupElements(a.children)[sort.column] ?? a).localeCompare(valueText(markupElements(b.children)[sort.column] ?? b), undefined, { numeric: true }) * sort.direction)
        return <tbody key={index} style={layoutStyle(body, state)}>{render(filtered, `body-${index}`)}{!filtered.length && <tr><td colSpan={columns} role="status">{t('generative.noRows')}</td></tr>}</tbody>
      })}
      {foot && <tfoot style={layoutStyle(foot, state)}>{render(foot.children, 'foot')}</tfoot>}
    </table>
  </div>
}

export function GenerativeMarkup({ document: doc, live, cites = [], renderBlock }: { document: MarkupDocument; live: boolean; cites?: CiteRef[]; renderBlock: (block: UIBlock) => ReactNode }) {
  const { t } = useTranslation('chat'), reply = useGenerativeReply(), id = useId()
  const [values, setValues] = useState<Record<string, MarkupValue>>({})
  const state = useMemo(() => ({ ...doc.initial, ...values }), [doc.initial, values])
  const [pending, setPending] = useState(false), [sent, setSent] = useState(false), [failed, setFailed] = useState(false)
  const sending = useRef(false)
  const locked = live || pending || sent || reply.status !== 'ready' || !reply.submit
  const change = (name: string | undefined, value: MarkupValue) => { if (name && !live && !pending && !sent) { setValues(current => ({ ...current, [name]: value })); setFailed(false) } }
  async function send(text: string | undefined, form?: HTMLFormElement | null) {
    if (locked || sending.current || !text || !reply.submit) return
    if (form) {
      for (const input of form.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('input,textarea')) {
        input.setCustomValidity(input.required && !['checkbox', 'radio', 'range', 'color'].includes(input.type) && !input.value.trim() ? t('generative.formRequired') : '')
      }
      if (!form.reportValidity()) return
    }
    sending.current = true; setPending(true); setFailed(false)
    try { if (await reply.submit(text)) setSent(true); else setFailed(true) } catch { setFailed(true) }
    finally { sending.current = false; setPending(false) }
  }
  const expressionText = (text: string, markdown: boolean | 'inline') => mapMarkupExpressions(text, (expression, name) => {
    const value = name ? (Object.hasOwn(state, name) ? state[name] : undefined) : evaluateMarkupExpression(expression, state)
    if (value === undefined) return undefined
    const result = scalar(value)
    return markdown ? result.replace(/[\\`*_[\]<>~!#&$|{}]/g, '\\$&') : result
  }, markdown)
  const a = (node: MarkupElement, name: string) => markupAttribute(node, name, state)
  function render(nodes: MarkupNode[], path = 'root', inline = false): ReactNode {
    return nodes.map((node, i) => {
      const key = `${path}-${i}`
      if (typeof node === 'string') {
        if (!node.trim()) return inline ? node : null
        const prose = path === 'root' ? node : dedentMarkupProse(node)
        const text = spaceMarkupEmphasis(expressionText(prose, inline ? 'inline' : true), inline)
        return inline
          ? <span key={key} dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(text, cites) }} />
          : <div key={key} className="gui-markup-prose" dangerouslySetInnerHTML={{ __html: blockMarkdownToHtml(text, cites) }} />
      }
      const tag = node.tag, style = layoutStyle(node, state), binding = markupBinding(node, doc.setters)
      const value = binding ? state[binding] : a(node, 'value')
      const children = (asInline = false) => render(node.children, key, asInline)
      const color = scalar(a(node, 'color') ?? a(node, 'tone') ?? a(node, 'variant'))
      const props = { style, 'data-tone': palette.has(color) ? color : undefined, 'data-border': a(node, 'border') ? '' : undefined, 'aria-label': scalar(a(node, 'aria-label')) || undefined }
      if (['chart', 'heatmap', 'gauge', 'function', 'calculator'].includes(tag)) {
        const block = markupPreset(node, state)
        return <div key={key} className="gui-markup-visual">{block ? renderBlock(block) : <p role="status">{t(live ? 'generative.generating' : 'generative.invalid')}</p>}</div>
      }
      switch (tag) {
        case 'state': return null
        case 'svg': return <GenerativeSVG key={key} node={node} state={state} text={value => expressionText(value, false)} />
        case 'map': case 'mapwidget': case 'map-widget': return <GenerativeMap key={key} node={node} state={state} render={(items, subpath) => render(items, `${key}-${subpath}`)} live={live} />
        case 'map-point': return <Fragment key={key}>{children()}</Fragment>
        case 'box': case 'column': case 'stack': case 'row': case 'grid': case 'section': case 'button-group': case 'stats':
          return <div {...props} className={`gui-markup-layout gui-markup-${tag}`} data-wrap={a(node, 'wrap') === false ? 'false' : undefined} data-background={['muted', 'surface'].includes(scalar(a(node, 'background'))) ? scalar(a(node, 'background')) : undefined}>{children()}</div>
        case 'spacer': return <div {...props} aria-hidden className="gui-markup-spacer" />
        case 'divider': case 'separator': return <hr {...props} className="gui-markup-divider" />
        case 'text': case 'p': {
          const block = node.children.some(child => typeof child === 'string' && hasBlockMarkupProse(child))
          if (block) return <div {...props} className={tag === 'text' ? 'gui-markup-text' : undefined}>{children()}</div>
          return createElement(tag === 'text' ? 'span' : 'p', { ...props, className: tag === 'text' ? 'gui-markup-text' : undefined }, children(true))
        }
        case 'span': case 'small': return <span {...props} className="gui-markup-text">{children(true)}</span>
        case 'heading': return createElement(`h${Math.round(bounded(Number(a(node, 'level')), 1, 6, 3))}`, { ...props, className: 'gui-markup-heading' }, children(true))
        case 'h1': case 'h2': case 'h3': case 'h4': case 'h5': case 'h6': return createElement(tag, { ...props, className: 'gui-markup-heading' }, children(true))
        case 'code': case 'pre': return createElement(tag, props, markupText(node.children, false))
        case 'strong': case 'b': case 'em': case 'i': case 'kbd': case 'mark': case 'blockquote': return createElement(tag, props, children(true))
        case 'br': return <br key={key} />
        case 'card': return <article {...props} className="gui-markup-card">{a(node, 'title') && <h3 className="gui-markup-heading">{scalar(a(node, 'title'))}</h3>}{children()}</article>
        case 'card-header': case 'card-content': case 'card-footer': return <div {...props} className={`gui-markup-${tag}`}>{children()}</div>
        case 'badge': case 'tag': return <span {...props} className="gui-markup-badge">{children(true)}</span>
        case 'alert': case 'callout': return <aside {...props} className="gui-markup-alert" role="note">{a(node, 'title') && <strong>{scalar(a(node, 'title'))}</strong>}{children()}</aside>
        case 'image': case 'img': case 'asyncimage': case 'avatar': return <MarkupImage key={`${key}-${scalar(a(node, 'src'))}`} node={node} state={state} />
        case 'figure': case 'figcaption': return createElement(tag, props, children())
        case 'metric': return <dl {...props} className="gui-markup-metric"><dt>{scalar(a(node, 'label'))}</dt><dd>{scalar(a(node, 'value'))}{scalar(a(node, 'unit'))}</dd>{a(node, 'hint') && <dd className="gui-markup-hint">{scalar(a(node, 'hint'))}</dd>}</dl>
        case 'table': return <MarkupTable key={key} node={node} render={(items, subpath, asInline) => render(items, `${key}-${subpath}`, asInline)} state={state} />
        case 'tr': return <tr {...props}>{children()}</tr>
        case 'colgroup': return <colgroup key={key} style={style} span={markupElements(node.children, 'col').length ? undefined : markupTableSpan(a(node, 'span'))}>{render(markupElements(node.children, 'col'), key)}</colgroup>
        case 'col': return <col key={key} style={style} span={markupTableSpan(a(node, 'span'))} />
        case 'th': case 'td': return createElement(tag, { ...props, ...tableCellProps(node, state) }, children(true))
        case 'list': return <ul {...props} className="gui-markup-list">{children()}</ul>
        case 'ol': return <ol {...props} start={Math.round(bounded(Number(a(node, 'start')), -1000000, 1000000, 1))}>{children()}</ol>
        case 'ul': case 'li': case 'dl': case 'dt': case 'dd': return createElement(tag, props, children(tag === 'li' || tag === 'dt' || tag === 'dd'))
        case 'a': case 'link': return <a {...props} className="gui-markup-link" href={safeURL(a(node, 'href'))} target="_blank" rel="noopener noreferrer">{children(true)}</a>
        case 'button': {
          const href = safeURL(a(node, 'href')), prompt = markupReply(node, state), variant = scalar(a(node, 'variant'))
          const className = `gui-markup-button gui-markup-button-${['secondary', 'outline', 'ghost'].includes(variant) ? variant : 'primary'}`
          if (href) return <a {...props} className={className} data-block={a(node, 'block') ? '' : undefined} href={href} target="_blank" rel="noopener noreferrer">{children(true)}</a>
          return <button {...props} type={a(node, 'type') === 'submit' ? 'submit' : 'button'} className={className} data-block={a(node, 'block') ? '' : undefined} disabled={!!a(node, 'disabled') || locked || (!prompt && a(node, 'type') !== 'submit')} onClick={event => { if (prompt) { event.preventDefault(); void send(prompt, event.currentTarget.closest('form')) } }}>{pending && <Loader2 size={14} className="animate-spin" aria-hidden />}{children(true)}</button>
        }
        case 'form': return <form {...props} noValidate className="gui-markup-form" onSubmit={event => {
          event.preventDefault()
          const fields = markupElementsDeep(node.children).filter(field => markupBinding(field, doc.setters) && !['radio', 'option'].includes(field.tag))
          const text = markupReply(node, state) || `${scalar(a(node, 'title')) || t('generative.formIntro')}\n\n${fields.map(field => {
            const value = state[markupBinding(field, doc.setters)!]
            const option = markupElements(field.children).find(option => scalar(a(option, 'value')) === scalar(value))
            const answer = ['checkbox', 'switch'].includes(field.tag) ? t(value ? 'generative.formYes' : 'generative.formNo') : option ? markupText(option.children) : scalar(value)
            return `${scalar(a(field, 'label') ?? a(field, 'name'))}: ${answer}`
          }).join('\n')}`
          void send(text, event.currentTarget)
        }}>{a(node, 'title') && <h3 className="gui-markup-heading">{scalar(a(node, 'title'))}</h3>}{children()}</form>
        case 'field': case 'label': return <div {...props} className="gui-markup-field">{a(node, 'label') && <span>{scalar(a(node, 'label'))}</span>}{children()}</div>
        case 'fieldset': return <fieldset {...props} disabled={!!a(node, 'disabled') || live || pending || sent}>{a(node, 'label') && <legend>{scalar(a(node, 'label'))}</legend>}{children()}</fieldset>
        case 'legend': return <legend {...props}>{children(true)}</legend>
        case 'input': case 'textarea': case 'select': {
          const type = scalar(a(node, 'type')) || 'text'
          if (tag === 'input' && ['checkbox', 'radio'].includes(type)) return control(node, key, type, binding, value)
          const nativeType = ['text', 'email', 'url', 'tel', 'number', 'date', 'time', 'datetime-local', 'search', 'range', 'color'].includes(type) ? type : 'text'
          const inputProps = { className: 'gui-input gui-markup-input', name: binding, value: scalar(value), disabled: !!a(node, 'disabled') || live || pending || sent,
            required: !!a(node, 'required'), placeholder: scalar(a(node, 'placeholder')), 'aria-label': scalar(a(node, 'label') ?? a(node, 'aria-label') ?? a(node, 'name')) || undefined,
            onChange: (event: { target: { value: string } }) => change(binding, ['number', 'range'].includes(nativeType) && event.target.value !== '' ? Number(event.target.value) : event.target.value) }
          const input = tag === 'textarea' ? <textarea {...inputProps} rows={bounded(Number(a(node, 'rows')), 2, 12, 3)} maxLength={bounded(Number(a(node, 'maxlength')), 1, 8000, 4000)} />
            : tag === 'select' ? <select {...inputProps}>{!value && <option value="">{scalar(a(node, 'placeholder')) || '—'}</option>}{children()}</select>
            : <input {...inputProps} type={nativeType} min={a(node, 'min') === undefined ? undefined : Number(a(node, 'min'))} max={a(node, 'max') === undefined ? undefined : Number(a(node, 'max'))} step={a(node, 'step') === undefined ? undefined : Number(a(node, 'step'))} maxLength={bounded(Number(a(node, 'maxlength')), 1, 8000, 4000)} />
          return <label {...props} className="gui-markup-field">{a(node, 'label') && <span>{scalar(a(node, 'label'))}{a(node, 'required') ? ' *' : ''}</span>}{input}{a(node, 'description') && <small>{scalar(a(node, 'description'))}</small>}</label>
        }
        case 'option': return <option key={key} value={scalar(a(node, 'value') ?? markupText(node.children))}>{expressionText(markupText(node.children), false)}</option>
        case 'radio-group': {
          const name = binding ?? key
          return <fieldset {...props} className="gui-markup-radio-group" data-direction={a(node, 'direction') === 'row' ? 'row' : 'col'} disabled={!!a(node, 'disabled') || live || pending || sent}>
            <legend>{scalar(a(node, 'label') ?? a(node, 'title')) || <span className="sr-only">{name}</span>}</legend>
            {render(node.children.filter(child => typeof child === 'string' || child.tag !== 'radio'), `${key}-extra`)}
            {markupElements(node.children, 'radio').map((radio, n) => control(a(node, 'required') ? { ...radio, attrs: { ...radio.attrs, required: { literal: true } } } : radio, `${key}-${n}`, 'radio', name, value))}
          </fieldset>
        }
        case 'radio': return control(node, key, 'radio', binding, value)
        case 'checkbox': case 'switch': return control(node, key, tag, binding, value)
        case 'slider': case 'range': return <label {...props} className="gui-markup-field"><span>{scalar(a(node, 'label'))} <output>{scalar(value)}{scalar(a(node, 'unit'))}</output></span><input type="range" name={binding} min={Number(a(node, 'min') ?? 0)} max={Number(a(node, 'max') ?? 100)} step={Number(a(node, 'step') ?? 1)} value={Number(value ?? a(node, 'min') ?? 0)} disabled={!!a(node, 'disabled') || live || pending || sent} onChange={event => change(binding, Number(event.target.value))} aria-label={scalar(a(node, 'label') ?? binding)} /></label>
        case 'rating': return <fieldset {...props} className="gui-markup-rating" disabled={!!a(node, 'disabled') || live || pending || sent}><legend>{scalar(a(node, 'label'))}</legend>{Array.from({ length: bounded(Number(a(node, 'max')), 1, 10, 5) }, (_, n) => <label key={n}><input type="radio" name={`${id}-${binding ?? key}`} value={n + 1} checked={Number(value) === n + 1} onChange={() => change(binding, n + 1)} aria-label={String(n + 1)} /><Star aria-hidden size={20} fill={Number(value) >= n + 1 ? 'currentColor' : 'none'} /></label>)}</fieldset>
        case 'progress': case 'meter': {
          const max = bounded(Number(a(node, 'max')), .001, 1e9, 100), current = bounded(Number(a(node, 'value')), 0, max, 0)
          return <label {...props} className="gui-markup-progress"><span>{scalar(a(node, 'label'))}<output>{Math.round(current / max * 100)}%</output></span><progress max={max} value={current} aria-label={scalar(a(node, 'label')) || `${current}/${max}`} /></label>
        }
        case 'tabs': return <MarkupTabs key={key} node={node} render={(items, subpath) => render(items, `${key}-${subpath}`)} />
        case 'accordion': return <div {...props} className="gui-markup-accordion">{markupElements(node.children).map((item, n) => <details key={n} open={a(item, 'open') === true}><summary>{scalar(a(item, 'title') ?? a(item, 'label'))}</summary><div>{render(item.children, `${key}-${n}`)}</div></details>)}</div>
        case 'details': return <details {...props} open={a(node, 'open') === true || undefined}>{children()}</details>
        case 'summary': return <summary {...props}>{children(true)}</summary>
        case 'steps': case 'timeline': return <ol {...props} className={`gui-markup-sequence gui-markup-${tag}`}>{markupElements(node.children).map((item, n) => <li key={n}><span className="gui-markup-step-marker" aria-hidden>{tag === 'steps' ? n + 1 : '•'}</span><div>{a(item, 'title') && <strong>{scalar(a(item, 'title'))}</strong>}{a(item, 'description') && <p>{scalar(a(item, 'description'))}</p>}{render(item.children, `${key}-${n}`)}</div></li>)}</ol>
        case 'tooltip': return <TooltipProvider key={key}><Tooltip content={scalar(a(node, 'content') ?? a(node, 'label'))}><span {...props} className="gui-markup-tooltip" tabIndex={0}>{children(true)}</span></Tooltip></TooltipProvider>
        case 'skeleton': return <span {...props} aria-hidden className="gui-markup-skeleton" />
        case 'spinner': return <span {...props} role="status" className="gui-markup-spinner"><Loader2 size={18} className="animate-spin" aria-hidden />{scalar(a(node, 'label')) || t('generative.generating')}</span>
        case 'empty': return <div {...props} className="gui-markup-empty">{a(node, 'title') && <strong>{scalar(a(node, 'title'))}</strong>}{children()}</div>
        case 'cite': {
          const refs = a(node, 'refs'), indexes = Array.isArray(refs) ? refs : scalar(refs).split(',')
          return <span key={key} dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(indexes.map(n => `[${n}]`).join(''), cites) }} />
        }
        default: return <Fragment key={key}>{children()}</Fragment>
      }
    }).map((content, i) => <Fragment key={`${path}-${i}`}>{content}</Fragment>)
  }
  function control(node: MarkupElement, key: string, type: string, binding: string | undefined, value: MarkupValue | undefined) {
    const checked = type === 'radio' ? scalar(value) === scalar(a(node, 'value')) : !!value
    return <label key={key} className={`gui-markup-choice ${type === 'switch' ? 'gui-markup-switch' : ''}`}><input type={type === 'radio' ? 'radio' : 'checkbox'} role={type === 'switch' ? 'switch' : undefined} name={`${id}-${binding ?? key}`} checked={checked} value={scalar(a(node, 'value'))} disabled={!!a(node, 'disabled') || live || pending || sent} required={!!a(node, 'required')} onChange={event => change(binding, type === 'radio' ? a(node, 'value') ?? '' : event.target.checked)} /><span>{node.children.length ? render(node.children, key, true) : scalar(a(node, 'label'))}</span></label>
  }
  const hasReply = markupElementsDeep(doc.children).some(node => node.tag === 'form' || !!markupReply(node, state))
  return <div className="gui-markup" aria-busy={pending}>{render(doc.children)}{hasReply && <p role="status" className={`gui-markup-feedback ${failed ? 'gui-markup-error' : ''}`}>{t(`generative.${failed ? 'formFailed' : pending ? 'formSending' : sent ? 'formSent' : live ? 'formStreaming' : reply.status === 'answered' ? 'formAnswered' : reply.status === 'readonly' || !reply.submit ? 'formReadonly' : 'formHint'}`)}</p>}</div>
}

function markupElementsDeep(nodes: MarkupNode[]): MarkupElement[] { return markupElements(nodes).flatMap(node => [node, ...markupElementsDeep(node.children)]) }
