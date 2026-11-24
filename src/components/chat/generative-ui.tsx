import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowDownAZ, ArrowUpAZ, ChevronDown, Filter, Image as ImageIcon, ImageOff } from 'lucide-react'
import { PresetChart, Heatmap, Gauge } from './generative-charts'
import { Calculator, Checklist, Quiz } from './generative-interactions'
import { GenerativeCards } from './generative-cards'
import { FunctionPlot } from './function-plot'
import { QuestionSelector } from './generative-question'
import { GenerativeForm } from './generative-form'
import { parseUIDocument, type UIBlock, type UITableCell, type UITableColumn } from '@/lib/generative-ui'
import { InlineHTML } from './inline-html'
import { GenerativeMarkup } from './generative-markup'
import { parseUIAnswer } from '@/lib/generative-markup'
import type { CiteRef } from '@/lib/markdown'
import '@/styles/generative-ui.css'

const focus = 'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]'

function TableImage({ cell }: { cell: Extract<UITableCell, { type: 'image' }> }) {
  const { t } = useTranslation('chat')
  const [state, setState] = useState<'loading' | 'loaded' | 'error'>('loading')
  const alt = cell.alt || cell.caption || t('generative.image')
  return <figure className="m-0 inline-block w-24 max-w-full align-top sm:w-[120px]">
    <a href={cell.url} target="_blank" rel="noopener noreferrer" title={t('generative.openImage')} aria-label={`${t('generative.openImage')}: ${alt}`} className={`relative flex aspect-[4/3] w-full items-center justify-center overflow-hidden rounded-md bg-[var(--color-bg-muted)] ${focus}`}>
      {state !== 'error' && <img src={cell.url} alt={alt} loading="lazy" decoding="async" referrerPolicy="no-referrer" onLoad={() => setState('loaded')} onError={() => setState('error')} className={`absolute inset-0 h-full w-full object-contain ${state === 'loaded' ? '' : 'invisible'}`} />}
      {state === 'loading' && <ImageIcon aria-hidden size={20} className="text-[var(--color-fg-muted)]" />}
      {state === 'error' && <span role="status" className="flex flex-col items-center gap-1 px-2 text-center text-xs text-[var(--color-fg-muted)]"><ImageOff aria-hidden size={18} />{t('generative.imageFailed')}</span>}
    </a>
    {cell.caption && <figcaption className="mt-1.5 whitespace-pre-wrap text-xs leading-relaxed text-[var(--color-fg-muted)]">{cell.caption}</figcaption>}
  </figure>
}

function TableCell({ value }: { value: UITableCell }) {
  if (value === null) return <span className="text-[var(--color-fg-muted)]">—</span>
  if (typeof value !== 'object') return <span className="whitespace-pre-wrap">{String(value)}</span>
  switch (value.type) {
    case 'text': return <div className="space-y-1 whitespace-pre-wrap"><div className="font-medium">{value.text}</div>{value.description && <div className="text-xs leading-relaxed text-[var(--color-fg-muted)]">{value.description}</div>}</div>
    case 'image': return <TableImage key={value.url} cell={value} />
    case 'link': return <a href={value.url} target="_blank" rel="noopener noreferrer" className={`rounded-sm text-[var(--color-accent)] underline decoration-current/40 underline-offset-4 hover:decoration-current ${focus}`}>{value.text}</a>
    case 'list': return <ul className="m-0 list-disc space-y-1 pl-4 marker:text-[var(--color-fg-muted)]">{value.items.map((item, i) => <li key={i} className="whitespace-pre-wrap">{item}</li>)}</ul>
  }
}

function tableColumnClasses(column: UITableColumn) {
  const width = typeof column === 'string' ? 'normal' : column.width || 'normal'
  return { compact: 'min-w-28 max-w-40', normal: 'min-w-36 max-w-64', wide: 'min-w-56 max-w-96' }[width]
}

function tableCellText(value: UITableCell): string {
  if (value === null) return ''
  if (typeof value !== 'object') return String(value)
  if (value.type === 'text') return `${value.text} ${value.description || ''}`
  if (value.type === 'image') return `${value.alt || ''} ${value.caption || ''}`
  if (value.type === 'link') return value.text
  return value.items.join(' ')
}

function Table({ block }: { block: Extract<UIBlock, { type: 'table' }> }) {
  const { t } = useTranslation('chat')
  const columns = block.columns.map(column => typeof column === 'string' ? { label: column } : column)
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<{ index: number; direction: 'asc' | 'desc' } | null>(null)
  const rows = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase()
    const filtered = block.rows.filter(row => !normalized || row.some(value => tableCellText(value).toLocaleLowerCase().includes(normalized)))
    if (!sort || sort.index >= block.columns.length) return filtered
    return [...filtered].sort((left, right) => {
      const a = tableCellText(left[sort.index]).toLocaleLowerCase()
      const b = tableCellText(right[sort.index]).toLocaleLowerCase()
      const numericA = Number(a), numericB = Number(b)
      const result = a !== '' && b !== '' && Number.isFinite(numericA) && Number.isFinite(numericB)
        ? numericA - numericB
        : a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' })
      return sort.direction === 'asc' ? result : -result
    })
  }, [block.rows, block.columns.length, query, sort])
  const toggleSort = (index: number) => setSort(current => current?.index === index
    ? { index, direction: current.direction === 'asc' ? 'desc' : 'asc' }
    : { index, direction: 'asc' })
  return <div role="region" aria-label={block.title || t('generative.table')} tabIndex={0} data-generative-table className={`max-h-[480px] max-w-full overflow-auto rounded-lg ${focus}`}>
    <div data-generative-table-controls className="mb-2 flex flex-wrap items-center gap-2">
      <label className="relative min-w-44 max-w-xs flex-1">
        <Filter aria-hidden size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--color-fg-muted)]" />
        <span className="sr-only">{t('generative.filterTable')}</span>
        <input value={query} onChange={event => setQuery(event.target.value)} placeholder={t('generative.filterTable')} className={`h-8 w-full rounded-md bg-[var(--color-bg-muted)] pl-8 pr-2 text-xs text-[var(--color-fg)] placeholder:text-[var(--color-fg-muted)] ${focus}`} />
      </label>
      {query && <span className="text-xs text-[var(--color-fg-muted)]">{t('generative.rowCount', { count: rows.length })}</span>}
    </div>
    <table className="quiet-table w-full border-collapse text-sm leading-relaxed">
      {block.title && <caption className="pb-3 text-left text-sm font-medium">{block.title}</caption>}
      <thead className="sticky top-0 z-10 bg-[var(--color-bg-muted)]"><tr>{columns.map((column, i) => {
        const active = sort?.index === i
        return <th key={i} scope="col" data-align={column.align || 'left'} aria-sort={active ? sort.direction === 'asc' ? 'ascending' : 'descending' : 'none'} className={tableColumnClasses(column)}>
          <button type="button" onClick={() => toggleSort(i)} className={`inline-flex max-w-full items-center gap-1 text-left font-medium ${focus}`}>
            <span className="truncate">{column.label}</span>{active ? sort.direction === 'asc' ? <ArrowUpAZ aria-hidden size={13} /> : <ArrowDownAZ aria-hidden size={13} /> : null}
          </button>
        </th>
      })}</tr></thead>
      <tbody>{rows.length ? rows.map((row, i) => <tr key={i}>{row.map((value, j) => <td key={j} data-align={columns[j].align || 'left'} className={`[overflow-wrap:anywhere] ${tableColumnClasses(columns[j])}`}><TableCell value={value} /></td>)}</tr>) : <tr><td colSpan={columns.length} className="text-center text-xs text-[var(--color-fg-muted)]"><p role="status" className="m-0">{t('generative.noRows')}</p></td></tr>}</tbody>
    </table>
  </div>
}

function Sections({ block, live }: { block: Extract<UIBlock, { type: 'tabs' | 'accordion' }>; live: boolean }) {
  const [active, setActive] = useState(0)
  const id = useId()
  if (block.type === 'accordion') return <div className="space-y-2">{block.items.map((item, i) => <details key={i} className="group rounded-lg bg-[var(--color-bg-muted)] px-3.5 py-3">
    <summary className={`flex cursor-pointer list-none items-center justify-between gap-3 rounded text-sm font-medium [&::-webkit-details-marker]:hidden ${focus}`}>{item.title}<ChevronDown size={14} className="shrink-0 group-open:rotate-180" /></summary>
    <div className="pt-4"><Blocks blocks={item.blocks} live={live} /></div>
  </details>)}</div>
  return <div className="space-y-4">
    <div role="tablist" className="flex max-w-full gap-1 overflow-x-auto rounded-lg bg-[var(--color-bg-muted)] p-1" onKeyDown={event => {
      if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
      event.preventDefault()
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? block.items.length - 1 : (active + (event.key === 'ArrowRight' ? 1 : -1) + block.items.length) % block.items.length
      setActive(next)
      document.getElementById(`${id}-tab-${next}`)?.focus()
    }}>
      {block.items.map((item, i) => <button key={i} type="button" role="tab" id={`${id}-tab-${i}`} aria-controls={`${id}-panel-${i}`} aria-selected={i === active} tabIndex={i === active ? 0 : -1} onClick={() => setActive(i)} className={`shrink-0 rounded-md px-3 py-1.5 text-sm ${focus} ${i === active ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]' : 'text-[var(--color-fg-muted)]'}`}>{item.title}</button>)}
    </div>
    {block.items.map((item, i) => <div key={i} role="tabpanel" id={`${id}-panel-${i}`} aria-labelledby={`${id}-tab-${i}`} hidden={i !== active}><Blocks blocks={item.blocks} live={live} /></div>)}
  </div>
}

function Block({ block, live }: { block: UIBlock; live: boolean }) {
  switch (block.type) {
    case 'text': return <p className="m-0 whitespace-pre-wrap text-sm leading-relaxed">{block.text}</p>
    case 'metrics': return <dl className="m-0 flex flex-wrap gap-x-8 gap-y-5">{block.items.map((item, i) => <div key={i} className="min-w-24 flex-1"><dt className="text-xs text-[var(--color-fg-muted)]">{item.label}</dt><dd className="m-0 mt-1 text-xl font-semibold tabular-nums [overflow-wrap:anywhere]">{item.value}</dd>{item.hint && <dd className="m-0 mt-1 text-xs text-[var(--color-fg-muted)]">{item.hint}</dd>}</div>)}</dl>
    case 'chart': return <PresetChart block={block} renderTable={table => <Table block={table} />} />
    case 'heatmap': return <Heatmap block={block} />
    case 'gauge': return <Gauge block={block} />
    case 'table': return <Table block={block} />
    case 'card': case 'cards': return <GenerativeCards block={block} />
    case 'steps': return <ol data-generative-steps className="m-0 list-none space-y-4 p-0">{block.items.map((item, i) => <li key={i} className="flex gap-3"><span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-xs tabular-nums">{i + 1}</span><div className="min-w-0"><div className="text-sm font-medium">{item.title}</div>{item.description && <p className="m-0 mt-1 text-sm text-[var(--color-fg-muted)]">{item.description}</p>}</div></li>)}</ol>
    case 'calculator': return <Calculator block={block} />
    case 'checklist': return <Checklist block={block} />
    case 'quiz': return <Quiz block={block} />
    case 'question': return <QuestionSelector key={JSON.stringify(block.questions)} block={block} live={live} />
    case 'form': return <GenerativeForm key={JSON.stringify(block.fields)} block={block} live={live} />
    case 'function': return <FunctionPlot block={block} />
    case 'tabs': case 'accordion': return <Sections block={block} live={live} />
  }
}
function Blocks({ blocks, live }: { blocks: UIBlock[]; live: boolean }) { return <div className="gui-blocks min-w-0">{blocks.map((block, i) => <Block key={i} block={block} live={live} />)}</div> }

export function GenerativeUI({ code, lang, live = false, cites }: { code: string; lang: 'aivory-ui' | 'aivory-html'; live?: boolean; cites?: CiteRef[] }) {
  const { t } = useTranslation('chat')
  const doc = useMemo(() => lang === 'aivory-ui' && /^\s*\{(?!@body)/.test(code) ? parseUIDocument(code) : null, [code, lang])
  const pendingJSON = live && /^\s*\{\s*(?:"|$)/.test(code)
  const markup = useMemo(() => lang === 'aivory-ui' && !doc && !pendingJSON ? parseUIAnswer(code, live) : null, [code, doc, lang, live, pendingJSON])
  const invalid = lang === 'aivory-ui' && !doc && !markup
  return <section data-generative-ui={lang} data-generative-markup={markup ? '' : undefined} className={markup ? 'my-4 min-w-0 max-w-full text-[var(--color-fg)] [overflow-wrap:anywhere]' : 'my-4 min-w-0 max-w-full rounded-xl bg-[var(--color-surface)] p-4 text-[var(--color-fg)] sm:p-5 [overflow-wrap:anywhere]'}>
    {invalid ? <><p role="status" className="m-0 text-sm text-[var(--color-fg-muted)]">{t(live ? 'generative.generating' : 'generative.invalid')}</p>{!live && <pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-words text-xs"><code>{code}</code></pre>}</> : lang === 'aivory-html' ? <InlineHTML code={code} live={live} /> : markup ? <GenerativeMarkup document={markup} live={live} cites={cites} renderBlock={block => <Block block={block} live={live} />} /> : doc && <>
      {(doc.title || doc.description) && <div className="mb-5 space-y-1"><div className="text-base font-semibold">{doc.title}</div>{doc.description && <p className="m-0 text-sm text-[var(--color-fg-muted)]">{doc.description}</p>}</div>}
      <Blocks blocks={doc.blocks} live={live} />
    </>}
  </section>
}
