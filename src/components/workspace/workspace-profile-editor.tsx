import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import '@/i18n/admin-resources'
import { workspacesApi } from '@/api'
import type { ApiWorkspace } from '@/api/types'
import { useWorkspaces } from '@/store/workspaces'
import { Button } from '@/components/ui/button'
import { WorkspaceProfileFields, type WorkspaceProfileDraft } from './workspace-profile-fields'
import { toast } from '@/hooks/use-toast'

export default function WorkspaceProfileEditor({ workspace }: { workspace: ApiWorkspace }) {
  const { t } = useTranslation(['settings', 'common'])
  const [draft, setDraft] = useState<WorkspaceProfileDraft>({
    icon_url: workspace.icon_url ?? '',
    description: workspace.description ?? '',
  })
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState(false)
  const busy = useRef(false)
  const mounted = useRef(true)
  const canManage = workspace.is_owner || workspace.role === 'admin'

  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  useEffect(() => {
    if (!dirty) setDraft({ icon_url: workspace.icon_url ?? '', description: workspace.description ?? '' })
  }, [workspace.icon_url, workspace.description, dirty])

  async function save() {
    if (busy.current || uploading || !canManage) return
    busy.current = true
    setSaving(true)
    try {
      const updated = await workspacesApi.updateProfile(workspace.id, draft)
      useWorkspaces.setState((state) => ({
        workspaces: state.workspaces.map((w) => w.id === updated.id
          ? { ...w, icon_url: updated.icon_url, description: updated.description }
          : w),
      }))
      if (mounted.current) { setDirty(false); toast.success(t('settings:work.saved')) }
    } catch {
      if (mounted.current) toast.error(t('settings:work.saveFailed'))
    } finally {
      busy.current = false
      if (mounted.current) setSaving(false)
    }
  }

  return (
    <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); void save() }}>
      <WorkspaceProfileFields
        value={draft}
        onChange={(value) => { setDraft(value); setDirty(true) }}
        disabled={saving || uploading || !canManage}
        upload={(file) => workspacesApi.uploadIcon(workspace.id, file)}
        onUploadingChange={setUploading}
      />
      <Button type="submit" loading={saving} disabled={!dirty || uploading || !canManage}>
        {t('common:actions.save')}
      </Button>
    </form>
  )
}
