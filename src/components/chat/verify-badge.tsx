import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ShieldCheck, ShieldAlert, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { VerifyFinding, VerifyResult } from '@/types/chat'

interface VerifyBadgeProps {
  verify: VerifyResult
}

// Per-severity accent classes (full literal strings so Tailwind's JIT keeps them).
const SEVERITY_DOT: Record<VerifyFinding['severity'], string> = {
  error: 'bg-[var(--color-danger)]',
  warning: 'bg-[var(--color-warning)]',
  note: 'bg-[var(--color-secondary)]',
}
const SEVERITY_QUOTE: Record<VerifyFinding['severity'], string> = {
  error: 'bg-[var(--color-danger-soft)]',
  warning: 'bg-[var(--color-warning-soft)]',
  note: 'bg-[var(--color-secondary-soft)]',
}

/**
 * Verify mode (§verify) trust badge. Renders next to an assistant answer:
 *   running  → a calm pulsing "Verifying…" chip (no disclosure yet)
 *   clean    → a static green "Verified" chip
 *   issues   → an amber "N issues found" chip that expands into the report
 * Colors convey state, so success/warning/danger tokens are used (CLAUDE.md §2.4);
 * the sage secondary is reserved for the in-progress AI-status chip.
 */
export function VerifyBadge({ verify }: VerifyBadgeProps) {
  const { t } = useTranslation('chat')
  const [expanded, setExpanded] = useState(false)
  const panelId = useId()

  const running = verify.status === 'running' && !verify.verdict
  const findings = verify.findings ?? []
  const verdict =
    verify.verdict ?? (verify.status === 'clean' || verify.status === 'issues' ? verify.status : undefined)
  const hasIssues = verdict === 'issues' || findings.length > 0

  if (running) {
    return (
      <div className="mt-3">
        <span className="inline-flex items-center gap-1.5 rounded-full bg-[var(--color-secondary-soft)] px-2.5 py-1 text-[12px] font-medium text-[var(--color-secondary)]">
          <ShieldCheck
            size={13}
            strokeWidth={1.5}
            aria-hidden
            className="animate-[streaming-pulse_1600ms_ease-in-out_infinite]"
          />
          {t('verify.verifying', { defaultValue: 'Verifying…' })}
        </span>
      </div>
    )
  }

  if (!verdict) return null // never audited

  const tone = hasIssues
    ? 'bg-[var(--color-warning-soft)] text-[var(--color-warning)]'
    : 'bg-[var(--color-success-soft)] text-[var(--color-success)]'
  const Icon = hasIssues ? ShieldAlert : ShieldCheck
  const label = hasIssues
    ? t('verify.issues', { count: findings.length, defaultValue: '{{count}} issues found' })
    : t('verify.clean', { defaultValue: 'Verified' })
  const auditedBy = verify.auditorLabel
    ? t('verify.auditedBy', { model: verify.auditorLabel, defaultValue: 'Audited by {{model}}' })
    : undefined

  // Clean → static chip (nothing to expand).
  if (!hasIssues) {
    return (
      <div className="mt-3">
        <span
          className={cn('inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[12px] font-medium', tone)}
          title={auditedBy}
        >
          <Icon size={13} strokeWidth={1.5} aria-hidden />
          {label}
        </span>
      </div>
    )
  }

  // Issues → expandable disclosure with the findings report.
  return (
    <div className="mt-3">
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={panelId}
        onClick={() => setExpanded((v) => !v)}
        className={cn(
          'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[12px] font-medium interactive',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          tone,
        )}
      >
        <Icon size={13} strokeWidth={1.5} aria-hidden />
        <span>{label}</span>
        <ChevronRight
          size={12}
          aria-hidden
          className={cn('transition-transform duration-150', expanded && 'rotate-90')}
        />
      </button>

      {/* grid 0fr→1fr animates height without measuring; the global
          prefers-reduced-motion rule neutralises the transition. */}
      <div
        id={panelId}
        className={cn(
          'grid transition-[grid-template-rows] duration-200 ease-out',
          expanded ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]',
        )}
      >
        <div className="overflow-hidden">
          <ul className="mt-2 space-y-2">
            {findings.map((f, i) => (
              <li
                key={i}
                className="rounded-[10px] bg-[var(--color-bg-subtle)] px-3 py-2"
              >
                <div className="flex items-center gap-1.5">
                  <span className={cn('size-1.5 rounded-full', SEVERITY_DOT[f.severity])} aria-hidden />
                  <span className="text-[11px] font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
                    {t(`verify.severity.${f.severity}`, { defaultValue: f.severity })}
                  </span>
                </div>
                {f.quote ? (
                  <p
                    className={cn(
                      'mt-1 rounded-[6px] px-2 py-1.5 text-[12.5px] italic text-[var(--color-fg-muted)]',
                      SEVERITY_QUOTE[f.severity],
                    )}
                  >
                    “{f.quote}”
                  </p>
                ) : null}
                <p className="mt-1 text-[13px] text-[var(--color-fg)]">{f.issue}</p>
              </li>
            ))}
          </ul>
          {auditedBy ? <p className="mt-2 text-[11px] text-[var(--color-fg-subtle)]">{auditedBy}</p> : null}
        </div>
      </div>
    </div>
  )
}
