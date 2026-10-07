/**
 * ModelQuotaEditor — per-model access + usage caps by user group (§ user groups).
 * Toggle which groups receive a free allowance; for each granted group set a
 * fixed window (period) and a cap (cost in the model's currency, or call count;
 * 0 = unlimited). A missing grant means that group has no free allowance and
 * every call consumes credits. Self-contained: loads + saves its own state.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiModelQuota, ApiUserGroup } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { AdminTable } from './AdminTable'

interface Row {
  granted: boolean
  periodValue: number
  periodUnit: 'hours' | 'days'
  limitType: 'cost' | 'count'
  limitValue: number
}

const UNIT_SECONDS = { hours: 3600, days: 86400 } as const

function toRow(q?: ApiModelQuota): Row {
  if (!q) return { granted: false, periodValue: 7, periodUnit: 'days', limitType: 'count', limitValue: 0 }
  // Prefer days when the period divides evenly, else hours.
  const days = q.period_seconds % 86400 === 0
  return {
    granted: true,
    periodValue: days ? q.period_seconds / 86400 : Math.max(1, Math.round(q.period_seconds / 3600)),
    periodUnit: days ? 'days' : 'hours',
    limitType: q.limit_type === 'cost' ? 'cost' : 'count',
    limitValue: q.limit_value,
  }
}

export function ModelQuotaEditor({ modelId }: { modelId: string }) {
  const { t } = useTranslation(['admin', 'common'])
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [rows, setRows] = useState<Record<string, Row>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    let active = true
    Promise.all([adminApi.userGroups(), adminApi.modelQuotas(modelId)])
      .then(([gs, quotas]) => {
        if (!active) return
        setGroups(gs)
        const byGroup: Record<string, ApiModelQuota> = {}
        for (const q of quotas) byGroup[q.group_id] = q
        const next: Record<string, Row> = {}
        for (const g of gs) next[g.id] = toRow(byGroup[g.id])
        setRows(next)
      })
      .catch((e) => toast.error(e instanceof ApiError ? e.message : t('admin:common.failed')))
      .finally(() => active && setLoading(false))
    return () => {
      active = false
    }
  }, [modelId, t])

  function patch(groupId: string, p: Partial<Row>) {
    setRows((r) => ({ ...r, [groupId]: { ...r[groupId], ...p } }))
  }

  async function save() {
    setSaving(true)
    try {
      const quotas: ApiModelQuota[] = groups
        .filter((g) => rows[g.id]?.granted)
        .map((g) => {
          const row = rows[g.id]
          return {
            model_id: modelId,
            group_id: g.id,
            period_seconds: Math.max(1, Math.round(row.periodValue)) * UNIT_SECONDS[row.periodUnit],
            limit_type: row.limitType,
            limit_value: Math.max(0, row.limitValue),
          }
        })
      await adminApi.setModelQuotas(modelId, quotas)
      toast.success(t('admin:quota.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  const anyGranted = groups.some((g) => rows[g.id]?.granted)

  if (loading) return <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>

  return (
    <div className="grid gap-3">
      <p className="text-[12px] text-[var(--color-fg-muted)]">
        {anyGranted ? t('admin:quota.restrictedHint') : t('admin:quota.openHint')}
      </p>
      <AdminTable
        items={groups.filter((group) => rows[group.id])}
        rowKey={(group) => group.id}
        label={t('admin:users.fields.group')}
        columns={[
          { id: 'group', header: t('admin:users.fields.group'), width: 180, render: (group) => <span className="font-medium">{group.name}</span> },
          { id: 'granted', header: t('admin:models.fields.enabled'), width: 80, render: (group) => <Switch aria-label={group.name} checked={rows[group.id].granted} onCheckedChange={(granted) => patch(group.id, { granted })} disabled={saving} /> },
          { id: 'period', header: t('admin:quota.period'), width: 220, render: (group) => {
            const row = rows[group.id]
            return row.granted ? (
              <div className="grid grid-cols-[5rem_minmax(0,1fr)] items-center gap-2">
                <Input type="number" min={1} step={1} aria-label={`${group.name}: ${t('admin:quota.period')}`} value={String(row.periodValue)} disabled={saving} onChange={(event) => patch(group.id, { periodValue: Number(event.target.value) })} />
                <Select disabled={saving} value={row.periodUnit} onValueChange={(value) => patch(group.id, { periodUnit: value as Row['periodUnit'] })}>
                  <SelectTrigger aria-label={`${group.name}: ${t('admin:quota.days')} / ${t('admin:quota.hours')}`}><SelectValue /></SelectTrigger>
                  <SelectContent><SelectItem value="days">{t('admin:quota.days')}</SelectItem><SelectItem value="hours">{t('admin:quota.hours')}</SelectItem></SelectContent>
                </Select>
              </div>
            ) : '-'
          } },
          { id: 'limit', header: t('admin:quota.limit'), width: 280, render: (group) => {
            const row = rows[group.id]
            return row.granted ? (
              <div>
                <div className="grid grid-cols-[7rem_minmax(0,1fr)] items-center gap-2">
                  <Select disabled={saving} value={row.limitType} onValueChange={(value) => patch(group.id, { limitType: value as Row['limitType'] })}>
                    <SelectTrigger aria-label={`${group.name}: ${t('admin:quota.count')} / ${t('admin:quota.cost')}`}><SelectValue /></SelectTrigger>
                    <SelectContent><SelectItem value="count">{t('admin:quota.count')}</SelectItem><SelectItem value="cost">{t('admin:quota.cost')}</SelectItem></SelectContent>
                  </Select>
                  <Input type="number" min={0} step="any" aria-label={`${group.name}: ${t('admin:quota.limit')}`} value={String(row.limitValue)} disabled={saving} onChange={(event) => patch(group.id, { limitValue: Number(event.target.value) })} placeholder="0" />
                </div>
                <p className="mt-1 text-[12px] text-[var(--color-fg-muted)]">{t(row.limitValue <= 0 ? 'admin:quota.unlimitedHint' : 'admin:quota.capHint')}</p>
              </div>
            ) : '-'
          } },
        ]}
      />
      <div className="flex justify-end">
        <Button variant="secondary" loading={saving} onClick={() => void save()}>
          {t('admin:quota.save')}
        </Button>
      </div>
    </div>
  )
}
