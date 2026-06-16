/**
 * AdminUsage — aggregated cost / token report from usage_logs.
 *
 * Fetches the model list alongside usage data so model_id → label lookup
 * renders human names instead of raw IDs. Purpose values (chat/task/image/
 * embedding) are translated via i18n keys.
 */
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiUsageReportRow } from '@/api/types'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Pagination } from '@/components/ui/pagination'
import { toast } from '@/hooks/use-toast'

const RANGE_IDS = ['1', '7', '30', '90'] as const

export default function AdminUsage() {
  const { t } = useTranslation('admin')
  const [days, setDays] = useState('30')
  const [rows, setRows] = useState<ApiUsageReportRow[]>([])
  const [modelMap, setModelMap] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState(1)
  const PAGE_SIZE = 25

  async function load() {
    setLoading(true)
    try {
      const [r, models] = await Promise.all([
        adminApi.usage(Number(days)),
        adminApi.models(),
      ])
      setRows(r.rows)
      const map: Record<string, string> = {}
      for (const m of models) {
        map[m.id] = m.label
      }
      setModelMap(map)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [days])

  const totalCost = rows.reduce((a, b) => a + b.cost, 0)
  const totalCalls = rows.reduce((a, b) => a + b.calls, 0)
  const pageCount = Math.max(1, Math.ceil(rows.length / PAGE_SIZE))
  const pageRows = rows.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)
  useEffect(() => {
    setPage(1)
  }, [days, rows.length])

  function modelLabel(id: string): string {
    return modelMap[id] || id
  }

  function purposeLabel(purpose: string): string {
    // Backend purposes like "task.title" contain dots; i18next treats "." as a
    // key separator, so normalise to "task_title" to match the flat keys.
    const key = `usage.purposes.${purpose.replace(/\./g, '_')}`
    const translated = t(key, { defaultValue: '' })
    return translated || purpose
  }

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('usage.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('usage.lead')}</p>
        </div>
        <div className="w-40">
          <Select value={days} onValueChange={setDays}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RANGE_IDS.map((id) => (
                <SelectItem key={id} value={id}>
                  {t(`usage.range.${id}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </header>

      <section className="mt-6 grid grid-cols-2 sm:grid-cols-3 gap-3">
        <Stat label={t('usage.stats.totalCost')} value={`$${totalCost.toFixed(4)}`} />
        <Stat label={t('usage.stats.calls')} value={String(totalCalls)} />
        <Stat label={t('usage.stats.rows')} value={String(rows.length)} />
      </section>

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('usage.empty')}
          </div>
        ) : (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] overflow-hidden">
            <table className="w-full text-sm tabular-nums">
              <thead className="bg-[var(--color-bg-muted)] text-[12px] text-[var(--color-fg-subtle)]">
                <tr>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.user')}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.conversation', { defaultValue: 'Conversation' })}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.model')}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.purpose')}</th>
                  <th className="text-right py-2.5 px-4 font-medium">{t('usage.table.in')}</th>
                  <th className="text-right py-2.5 px-4 font-medium">{t('usage.table.out')}</th>
                  <th className="text-right py-2.5 px-4 font-medium">{t('usage.table.calls')}</th>
                  <th className="text-right py-2.5 px-4 font-medium">{t('usage.table.cost')}</th>
                </tr>
              </thead>
              <tbody>
                {pageRows.map((r, i) => (
                  <tr key={i} className="border-t border-[var(--color-divider)]">
                    <td className="py-2 px-4 max-w-[14rem]">
                      {r.conversation_id ? (
                        <Link
                          to={`/admin/users/${encodeURIComponent(r.user_id)}/conversations/${encodeURIComponent(r.conversation_id)}`}
                          className="block truncate text-[var(--color-accent)] hover:underline"
                          title={r.conversation_title || r.conversation_id}
                        >
                          {r.conversation_title || r.conversation_id}
                        </Link>
                      ) : (
                        <span className="text-[var(--color-fg-faint)]">—</span>
                      )}
                    </td>
                    <td className="py-2 px-4 text-[12px]">{modelLabel(r.model_id)}</td>
                    <td className="py-2 px-4 text-[var(--color-fg-muted)]">{purposeLabel(r.purpose)}</td>
                    <td className="py-2 px-4 text-right">{r.input_tokens}</td>
                    <td className="py-2 px-4 text-right">{r.output_tokens}</td>
                    <td className="py-2 px-4 text-right">{r.calls}</td>
                    <td className="py-2 px-4 text-right">${r.cost.toFixed(4)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {!loading && rows.length > 0 ? <Pagination page={page} pageCount={pageCount} onPage={setPage} /> : null}
      </section>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="text-[12px] text-[var(--color-fg-subtle)] uppercase tracking-wide">{label}</div>
      <div className="mt-1 font-serif text-2xl text-[var(--color-fg)]">{value}</div>
    </div>
  )
}
