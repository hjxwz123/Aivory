import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown } from 'lucide-react'
import { parseUIDocument, type UIBlock } from '@/lib/generative-ui'
import { InlineHTML } from './inline-html'

const palette = ['var(--color-accent)', 'var(--color-fg)', '#8874c7', '#b97838', '#388c9f', '#ac547d']
const focus = 'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]'

function Chart({ block }: { block: Extract<UIBlock, { type: 'chart' }> }) {
  const { t } = useTranslation('chat')
  const [data, setData] = useState(false)
  const svg = useRef<SVGSVGElement>(null)
  const [chartWidth, setChartWidth] = useState(650)
  useEffect(() => {
    if (!svg.current) return
    const observer = new ResizeObserver(entries => setChartWidth(Math.max(220, entries[0].contentRect.width)))
    observer.observe(svg.current)
    return () => observer.disconnect()
  }, [])
  const plotWidth = chartWidth - 70
  const values = block.series.flatMap(s => s.values)
  const min = Math.min(0, ...values), max = Math.max(0, ...values)
  const range = max - min || 1
  const x = (i: number) => 52 + (i + .5) * plotWidth / block.labels.length
  const y = (v: number) => 190 - (v - min) / range * 160
  const width = Math.min(38, plotWidth * .75 / block.labels.length / block.series.length)
  const labelEvery = Math.max(1, Math.ceil(block.labels.length / Math.max(2, Math.floor(plotWidth / 75))))
  return (
    <figure className="m-0 min-w-0 space-y-3">
      {block.title && <figcaption className="text-sm font-medium">{block.title}</figcaption>}
      <svg ref={svg} viewBox={`0 0 ${chartWidth} 230`} role="img" aria-label={block.title || t('generative.chart')} className="block w-full" style={{ height: 230 }}>
        <title>{block.title || t('generative.chart')}</title>
        {[0, .5, 1].map(n => <g key={n}>
          <line x1="52" x2={chartWidth - 18} y1={30 + 160 * n} y2={30 + 160 * n} stroke="var(--color-divider)" strokeDasharray="3 6" />
          <text x="44" y={34 + 160 * n} textAnchor="end" fill="var(--color-fg-muted)" fontSize="11">{Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(max - range * n)}</text>
        </g>)}
        {block.series.map((s, si) => <g key={si} style={{ color: palette[si] }}>
          {block.kind === 'line' && <polyline points={s.values.map((v, i) => `${x(i)},${y(v)}`).join(' ')} fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinejoin="round" />}
          {s.values.map((v, i) => block.kind === 'bar'
            ? <rect key={i} x={x(i) + (si - block.series.length / 2) * width} y={Math.min(y(v), y(0))} width={Math.max(1, width - 2)} height={Math.max(1, Math.abs(y(v) - y(0)))} rx="2" fill="currentColor"><title>{`${block.labels[i]} · ${s.name}: ${v}`}</title></rect>
            : <circle key={i} cx={x(i)} cy={y(v)} r="3" fill="currentColor"><title>{`${block.labels[i]} · ${s.name}: ${v}`}</title></circle>)}
        </g>)}
        {block.labels.map((label, i) => i % labelEvery === 0 && <text key={i} x={x(i)} y="214" textAnchor="middle" fill="var(--color-fg-muted)" fontSize="11">{label.length > 14 ? `${label.slice(0, 13)}…` : label}</text>)}
      </svg>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-[var(--color-fg-muted)]">
        {block.series.map((s, i) => <span key={i} className="inline-flex items-center gap-1.5"><span aria-hidden className="h-2 w-2 rounded-full" style={{ background: palette[i] }} />{s.name}</span>)}
        <button type="button" className={`ml-auto rounded px-2 py-1 hover:bg-[var(--color-bg-muted)] ${focus}`} onClick={() => setData(!data)} aria-expanded={data}>{t(data ? 'generative.hideData' : 'generative.showData')}</button>
      </div>
      {data && <Block block={{ type: 'table', columns: [t('generative.label'), ...block.series.map(s => s.name)], rows: block.labels.map((label, i) => [label, ...block.series.map(s => s.values[i])]) }} />}
    </figure>
  )
}

function Sections({ block }: { block: Extract<UIBlock, { type: 'tabs' | 'accordion' }> }) {
  const [active, setActive] = useState(0)
  const id = useId()
  if (block.type === 'accordion') return <div className="space-y-2">{block.items.map((item, i) => <details key={i} className="group rounded-lg bg-[var(--color-bg-muted)] px-3.5 py-3">
    <summary className={`flex cursor-pointer list-none items-center justify-between gap-3 rounded text-sm font-medium [&::-webkit-details-marker]:hidden ${focus}`}>{item.title}<ChevronDown size={14} className="shrink-0 group-open:rotate-180" /></summary>
    <div className="pt-4"><Blocks blocks={item.blocks} /></div>
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
    {block.items.map((item, i) => <div key={i} role="tabpanel" id={`${id}-panel-${i}`} aria-labelledby={`${id}-tab-${i}`} hidden={i !== active}><Blocks blocks={item.blocks} /></div>)}
  </div>
}

function Block({ block }: { block: UIBlock }) {
  switch (block.type) {
    case 'text': return <p className="m-0 whitespace-pre-wrap text-sm leading-relaxed">{block.text}</p>
    case 'metrics': return <dl className="m-0 flex flex-wrap gap-x-8 gap-y-5">{block.items.map((item, i) => <div key={i} className="min-w-24 flex-1"><dt className="text-xs text-[var(--color-fg-muted)]">{item.label}</dt><dd className="m-0 mt-1 text-xl font-semibold tabular-nums [overflow-wrap:anywhere]">{item.value}</dd>{item.hint && <dd className="m-0 mt-1 text-xs text-[var(--color-fg-muted)]">{item.hint}</dd>}</div>)}</dl>
    case 'chart': return <Chart block={block} />
    case 'table': return <div className="max-h-[360px] overflow-auto rounded-lg"><table className="quiet-table w-full border-collapse text-sm"><thead className="sticky top-0 bg-[var(--color-bg-muted)]"><tr>{block.columns.map((c, i) => <th key={i} className="whitespace-nowrap px-3 py-2.5 text-left font-medium">{c}</th>)}</tr></thead><tbody>{block.rows.map((row, i) => <tr key={i} className={i % 2 ? 'bg-[var(--color-bg-muted)]/40' : ''}>{row.map((value, j) => <td key={j} className="min-w-24 px-3 py-2.5 align-top">{value === null ? '—' : String(value)}</td>)}</tr>)}</tbody></table></div>
    case 'steps': return <ol className="m-0 list-none space-y-4 p-0">{block.items.map((item, i) => <li key={i} className="flex gap-3"><span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-xs tabular-nums">{i + 1}</span><div className="min-w-0"><div className="text-sm font-medium">{item.title}</div>{item.description && <p className="m-0 mt-1 text-sm text-[var(--color-fg-muted)]">{item.description}</p>}</div></li>)}</ol>
    case 'tabs': case 'accordion': return <Sections block={block} />
  }
}
function Blocks({ blocks }: { blocks: UIBlock[] }) { return <div className="min-w-0 space-y-6">{blocks.map((block, i) => <Block key={i} block={block} />)}</div> }

export function GenerativeUI({ code, lang, live = false }: { code: string; lang: 'aivory-ui' | 'aivory-html'; live?: boolean }) {
  const { t } = useTranslation('chat')
  const doc = useMemo(() => lang === 'aivory-ui' ? parseUIDocument(code) : null, [code, lang])
  const invalid = lang === 'aivory-ui' && !doc
  return <section data-generative-ui={lang} className="my-4 min-w-0 max-w-full rounded-xl bg-[var(--color-surface)] p-4 text-[var(--color-fg)] sm:p-5 [overflow-wrap:anywhere]">
    {invalid ? <p role="status" className="m-0 text-sm text-[var(--color-fg-muted)]">{t(live ? 'generative.generating' : 'generative.invalid')}</p> : lang === 'aivory-html' ? <InlineHTML code={code} live={live} /> : doc && <>
      {(doc.title || doc.description) && <div className="mb-5 space-y-1"><div className="text-base font-semibold">{doc.title}</div>{doc.description && <p className="m-0 text-sm text-[var(--color-fg-muted)]">{doc.description}</p>}</div>}
      <Blocks blocks={doc.blocks} />
    </>}
  </section>
}
