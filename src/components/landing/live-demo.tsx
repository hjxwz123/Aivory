import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowUp, Quote } from 'lucide-react'
import { useMediaQuery } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'

/**
 * LiveDemo — the "product is the hero" centrepiece: an auto-playing, looping
 * mock conversation that streams its replies, switches models mid-thread, and
 * surfaces a citation — the actual feel of Aivory, on the landing page.
 *
 * Pure presentation (no real API). Driven by a small cancellable phase machine;
 * under prefers-reduced-motion it renders the finished transcript with no motion.
 */

interface Model {
  name: string
  slug: string
}
const CLAUDE: Model = { name: 'Claude', slug: 'claude' }
const GPT: Model = { name: 'GPT', slug: 'openai' }

interface Step {
  role: 'user' | 'assistant'
  model?: Model
  textKey: string
  citeKey?: string
}

const SCRIPT: Step[] = [
  { role: 'user', textKey: 'demo.user1' },
  { role: 'assistant', model: CLAUDE, textKey: 'demo.reply1', citeKey: 'demo.cite1' },
  { role: 'user', textKey: 'demo.user2' },
  { role: 'assistant', model: GPT, textKey: 'demo.reply2' },
]

interface Rendered {
  step: Step
  text: string // fully revealed text (for settled messages)
}

function BrandMark({ slug, className }: { slug: string; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn('brand-mark inline-block bg-current', className)}
      style={{ WebkitMaskImage: `url(/brand/${slug}.svg)`, maskImage: `url(/brand/${slug}.svg)` }}
    />
  )
}

export function LiveDemo() {
  const { t } = useTranslation('landing')
  const reduce = useMediaQuery('(prefers-reduced-motion: reduce)')

  // Settled (fully shown) messages, the one currently streaming, and a transient
  // "switched model" banner.
  const [settled, setSettled] = useState<Rendered[]>([])
  const [streaming, setStreaming] = useState<{ step: Step; text: string } | null>(null)
  const [thinking, setThinking] = useState(false)
  const [switchedTo, setSwitchedTo] = useState<Model | null>(null)

  const tRef = useRef(t)
  tRef.current = t

  useEffect(() => {
    // Reduced motion: show the finished transcript, no animation.
    if (reduce) {
      setSettled(SCRIPT.map((step) => ({ step, text: tRef.current(`landing:${step.textKey}`) })))
      setStreaming(null)
      return
    }

    let cancelled = false
    let timers: ReturnType<typeof setTimeout>[] = []
    const wait = (ms: number) =>
      new Promise<void>((resolve) => {
        const id = setTimeout(resolve, ms)
        timers.push(id)
      })

    async function play() {
      while (!cancelled) {
        setSettled([])
        setStreaming(null)
        setSwitchedTo(null)
        setThinking(false)
        await wait(700)

        let prevModel: Model | null = null
        for (const step of SCRIPT) {
          if (cancelled) return
          const text = tRef.current(`landing:${step.textKey}`)

          if (step.role === 'user') {
            setSettled((s) => [...s, { step, text }])
            await wait(750)
            continue
          }

          // Assistant: announce a model switch, "think", then stream the text.
          if (step.model && prevModel && step.model.slug !== prevModel.slug) {
            setSwitchedTo(step.model)
            await wait(1100)
            setSwitchedTo(null)
          }
          prevModel = step.model ?? prevModel

          setThinking(true)
          setStreaming({ step, text: '' })
          await wait(620)
          setThinking(false)

          for (let i = 1; i <= text.length; i++) {
            if (cancelled) return
            setStreaming({ step, text: text.slice(0, i) })
            await wait(16)
          }
          await wait(420)
          setSettled((s) => [...s, { step, text }])
          setStreaming(null)
          await wait(900)
        }
        await wait(2400) // hold the finished thread, then loop
      }
    }

    void play()
    return () => {
      cancelled = true
      timers.forEach(clearTimeout)
      timers = []
    }
  }, [reduce])

  return (
    <div className="relative w-full">
      {/* App frame */}
      <div className="relative overflow-hidden rounded-[18px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-2xl)]">
        {/* Title bar */}
        <div className="flex items-center gap-2 h-10 px-4 border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)]">
          <span className="size-2.5 rounded-full bg-[var(--color-fg-faint)]" />
          <span className="size-2.5 rounded-full bg-[var(--color-fg-faint)]" />
          <span className="size-2.5 rounded-full bg-[var(--color-fg-faint)]" />
          <span className="mx-auto font-mono text-[11px] text-[var(--color-fg-subtle)]">aivory.app</span>
        </div>

        {/* Transcript */}
        <div className="flex min-h-[360px] sm:min-h-[420px] flex-col gap-4 px-5 sm:px-7 py-6">
          {settled.map((m, i) => (
            <Message key={`s-${i}`} step={m.step} text={m.text} t={t} />
          ))}
          {switchedTo ? (
            <div className="self-center inline-flex items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1 text-[11.5px] text-[var(--color-fg-muted)] animate-[message-in_300ms_var(--ease-out)_both]">
              <BrandMark slug={switchedTo.slug} className="size-3 text-[var(--color-fg-muted)]" />
              {t('demo.switched', { model: switchedTo.name })}
            </div>
          ) : null}
          {streaming ? (
            <Message step={streaming.step} text={streaming.text} streaming thinking={thinking} t={t} />
          ) : null}

          {/* Composer (decorative) pinned to the bottom of the frame */}
          <div className="mt-auto flex items-center gap-2 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2.5">
            <span className="flex-1 text-[13px] text-[var(--color-fg-faint)] truncate">{t('demo.placeholder')}</span>
            <span className="inline-flex size-7 items-center justify-center rounded-[9px] bg-[var(--color-accent)] text-[var(--color-accent-fg)]">
              <ArrowUp size={13} aria-hidden />
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}

function Message({
  step,
  text,
  streaming,
  thinking,
  t,
}: {
  step: Step
  text: string
  streaming?: boolean
  thinking?: boolean
  t: (key: string, opts?: Record<string, unknown>) => string
}) {
  if (step.role === 'user') {
    return (
      <div className="flex justify-end animate-[message-in_300ms_var(--ease-out)_both]">
        <div className="max-w-[80%] rounded-[16px] border border-[var(--color-user-bubble-border)] bg-[var(--color-user-bubble)] px-3.5 py-2 text-[13.5px] leading-relaxed text-[var(--color-fg)]">
          {text}
        </div>
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-2 animate-[message-in_300ms_var(--ease-out)_both]">
      <div className="flex items-center gap-2">
        {step.model ? (
          <BrandMark slug={step.model.slug} className="size-4 text-[var(--color-fg-muted)]" />
        ) : null}
        <span className="font-medium text-[13px] text-[var(--color-fg)]">{step.model?.name ?? 'Aivory'}</span>
        {thinking ? (
          <span className="inline-flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
            <span className="size-1.5 rounded-full bg-[var(--color-secondary)] animate-[streaming-pulse_1600ms_ease-in-out_infinite]" />
            {t('demo.thinking')}
          </span>
        ) : null}
      </div>
      {text ? (
        <p className="max-w-[90%] text-[13.5px] leading-relaxed text-[var(--color-fg)] text-pretty">
          {text}
          {streaming ? (
            <span
              aria-hidden
              className="inline-block w-[2px] h-[1.05em] translate-y-[2px] ml-0.5 bg-[var(--color-accent)] animate-[fade-in_500ms_ease-in-out_infinite_alternate]"
            />
          ) : null}
        </p>
      ) : null}
      {!streaming && step.citeKey ? (
        <span className="inline-flex w-fit items-center gap-1.5 rounded-full border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-2.5 py-1 text-[11.5px] text-[var(--color-fg-muted)]">
          <Quote size={11} aria-hidden className="text-[var(--color-secondary)]" />
          {t(step.citeKey)}
        </span>
      ) : null}
    </div>
  )
}
