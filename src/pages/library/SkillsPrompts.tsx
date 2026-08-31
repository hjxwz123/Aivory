import { type Dispatch, type ReactNode, type SetStateAction, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Activity,
  Blocks,
  Cable,
  Check,
  Eye,
  EyeOff,
  FileText,
  LibraryBig,
  MoreHorizontal,
  Pencil,
  Plus,
  Search,
  Sparkles,
  Trash2,
  X,
} from 'lucide-react'
import { libraryApi, ApiError } from '@/api'
import type {
  ApiLibraryCatalog,
  ApiLibraryCatalogPrompt,
  ApiLibraryCatalogSkill,
  ApiUserMCP,
  ApiUserPrompt,
  ApiUserSkill,
} from '@/api/types'
import { IconPicker } from '@/components/admin/icon-picker'
import { ContentHeader } from '@/components/layout/content-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { LucideGlyph } from '@/components/ui/lucide-icon'
import { Skeleton } from '@/components/ui/skeleton'
import { SkillIcon } from '@/components/ui/skill-icon'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'
import { skillDisplayDescription } from '@/lib/skill-description'
import { isValidSkillName, parseSkillDocument } from '@/lib/skill-document'
import { cn } from '@/lib/utils'
import { useWorkspaces } from '@/store/workspaces'
import {
  memberCanCreate,
  memberCanUse,
  workspaceCapabilitiesForScope,
  workspacePolicyResolvedForScope,
} from '@/lib/workspace-permissions'
import { revokedLibraryResourceKinds } from '@/lib/library-resource-state'

type ItemKind = 'skill' | 'prompt' | 'mcp'
type KindFilter = 'all' | ItemKind

interface EditorState {
  open: boolean
  kind: ItemKind
  id?: string
  name: string
  description: string
  content: string
  importText: string
}

/** MCP editor draft — a deliberate sibling of EditorState (the skill/prompt
 * form is not contorted to fit header rows / toggles). Field layout mirrors
 * the administrator MCP editor (AdminMCP.tsx). */
interface MCPHeaderDraft {
  id: number
  key: string
  value: string
  revealed: boolean
}

interface MCPDraft {
  name: string
  icon: string
  description: string
  url: string
  headers: MCPHeaderDraft[]
  enabled: boolean
}

interface MCPEditorState {
  open: boolean
  row?: ApiUserMCP
  draft: MCPDraft
}

type DeleteTarget =
  { kind: 'skill'; item: ApiUserSkill } | { kind: 'prompt'; item: ApiUserPrompt } | { kind: 'mcp'; item: ApiUserMCP }

const emptyCatalog: ApiLibraryCatalog = { skills: [], prompts: [] }

function newEditor(kind: ItemKind): EditorState {
  return {
    open: true,
    kind,
    name: '',
    description: '',
    content: '',
    importText: '',
  }
}

let nextMCPHeaderID = 0

function makeMCPHeader(key = '', value = ''): MCPHeaderDraft {
  nextMCPHeaderID += 1
  return { id: nextMCPHeaderID, key, value, revealed: false }
}

function newMCPDraft(): MCPDraft {
  // Contract §2: user MCP rows are enabled-on-create (the picker still only
  // offers servers with a successful discovery snapshot).
  return {
    name: '',
    icon: 'Blocks',
    description: '',
    url: '',
    headers: [],
    enabled: true,
  }
}

function mcpDraftFromRow(server: ApiUserMCP): MCPDraft {
  return {
    name: server.name,
    icon: server.icon || 'Blocks',
    description: server.description,
    url: server.url,
    headers: Object.entries(server.headers ?? {}).map(([key, value]) => makeMCPHeader(key, value)),
    enabled: server.enabled,
  }
}

export default function SkillsPrompts() {
  // 'admin' is subscribed only for the header-row reveal/remove a11y labels in
  // the MCP editor (library.json has no show/hide/remove verbs; the strings are
  // shipped in all five locales and match the AdminMCP editor being mirrored).
  const { t } = useTranslation(['library', 'common', 'admin'])
  const [skills, setSkills] = useState<ApiUserSkill[]>([])
  const [prompts, setPrompts] = useState<ApiUserPrompt[]>([])
  const [mcps, setMcps] = useState<ApiUserMCP[]>([])
  const [catalog, setCatalog] = useState<ApiLibraryCatalog>(emptyCatalog)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [query, setQuery] = useState('')
  const [kindFilter, setKindFilter] = useState<KindFilter>('all')
  const [tab, setTab] = useState<'mine' | 'catalog'>('mine')
  const [editor, setEditor] = useState<EditorState>(() => ({
    ...newEditor('skill'),
    open: false,
  }))
  const [mcpEditor, setMcpEditor] = useState<MCPEditorState>(() => ({
    open: false,
    draft: newMCPDraft(),
  }))
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null)
  const [saving, setSaving] = useState(false)
  const [mcpSaving, setMcpSaving] = useState(false)
  const [busyMCPAction, setBusyMCPAction] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [addingCatalogId, setAddingCatalogId] = useState('')
  const savingRef = useRef(false)
  const mcpSavingRef = useRef(false)
  const busyMCPActionRef = useRef('')
  const deletingRef = useRef(false)
  const deletingKindRef = useRef<ItemKind | null>(null)
  const savingKindRef = useRef<ItemKind | null>(null)
  const addingCatalogRef = useRef('')
  const libraryLoadRequestRef = useRef(0)
  const resourceOperationEpochRef = useRef<Record<ItemKind, number>>({ skill: 0, prompt: 0, mcp: 0 })
  const workspaceId = useWorkspaces((state) => state.activeId ?? undefined)
  const workspaceIdRef = useRef(workspaceId)
  workspaceIdRef.current = workspaceId
  const activeWorkspace = useWorkspaces((state) =>
    state.activeId ? state.workspaces.find((workspace) => workspace.id === state.activeId) : undefined,
  )
  const workspacePolicy = useWorkspaces((state) => (state.activeId ? state.policies[state.activeId] : undefined))
  const workspacesLoaded = useWorkspaces((state) => state.loaded)
  const workspacePolicyLoading = useWorkspaces((state) =>
    state.activeId ? state.policyLoading[state.activeId] === true : false,
  )
  const workspaceSwitching = useWorkspaces((state) => state.switching)
  const workspacePolicyError = useWorkspaces((state) => (workspaceId ? state.policyErrors[workspaceId] : null))
  const workspaceCaps = workspaceCapabilitiesForScope(workspaceId, workspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: workspaceSwitching,
    policyError: workspacePolicyError,
  })
  const workspacePolicyResolved = workspacePolicyResolvedForScope(workspaceId, workspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: workspaceSwitching,
    policyError: workspacePolicyError,
  })
  const skillResourceEnabled = !workspaceId || workspaceCaps.skills
  const promptResourceEnabled = !workspaceId || workspaceCaps.prompts
  const mcpResourceEnabled = !workspaceId || workspaceCaps.mcp
  const resourceEnabled = useMemo(
    () => ({
      skill: skillResourceEnabled,
      prompt: promptResourceEnabled,
      mcp: mcpResourceEnabled,
    }),
    [mcpResourceEnabled, promptResourceEnabled, skillResourceEnabled],
  )
  const resourceEnabledRef = useRef(resourceEnabled)
  // Treat an active workspace whose membership is still loading as read-only
  // until the server-backed role is known. This prevents a guest from seeing a
  // write control for one render during workspace hydration.
  const workspaceAdmin = !workspaceId || activeWorkspace?.is_owner === true || activeWorkspace?.role === 'admin'
  const canCreateSkill = workspaceAdmin || (Boolean(activeWorkspace) && memberCanCreate(activeWorkspace!, 'skill'))
  const canCreatePrompt = workspaceAdmin || (Boolean(activeWorkspace) && memberCanCreate(activeWorkspace!, 'prompt'))
  const canCreateMCP = workspaceAdmin || (Boolean(activeWorkspace) && memberCanCreate(activeWorkspace!, 'mcp'))
  const canUseMCP =
    (!workspaceId || workspaceCaps.mcp) &&
    (!workspaceId || (Boolean(activeWorkspace) && memberCanUse(activeWorkspace!, 'mcp')))
  const canWrite =
    (resourceEnabled.skill && canCreateSkill) ||
    (resourceEnabled.prompt && canCreatePrompt) ||
    (resourceEnabled.mcp && canCreateMCP)

  function operationIsCurrent(kind: ItemKind, epoch: number, requestedWorkspaceID: string | undefined) {
    return (
      resourceOperationEpochRef.current[kind] === epoch &&
      workspaceIdRef.current === requestedWorkspaceID
    )
  }

  async function load() {
    const requestID = ++libraryLoadRequestRef.current
    const requestedWorkspaceID = workspaceIdRef.current
    setLoading(true)
    setLoadError('')
    setSkills([])
    setPrompts([])
    setMcps([])
    setCatalog(emptyCatalog)
    try {
      const [userSkills, userPrompts, userMcps, publicCatalog] = await Promise.all([
        resourceEnabled.skill
          ? libraryApi.skills(requestedWorkspaceID)
          : Promise.resolve([] as ApiUserSkill[]),
        resourceEnabled.prompt
          ? libraryApi.prompts(requestedWorkspaceID)
          : Promise.resolve([] as ApiUserPrompt[]),
        resourceEnabled.mcp
          ? libraryApi.mcps(requestedWorkspaceID)
          : Promise.resolve([] as ApiUserMCP[]),
        libraryApi.catalog(requestedWorkspaceID),
      ])
      if (requestID !== libraryLoadRequestRef.current || workspaceIdRef.current !== requestedWorkspaceID) return
      setSkills(userSkills)
      setPrompts(userPrompts)
      setMcps(userMcps)
      setCatalog(publicCatalog)
    } catch (error) {
      if (requestID !== libraryLoadRequestRef.current || workspaceIdRef.current !== requestedWorkspaceID) return
      const message = error instanceof ApiError ? error.message : t('common:common.error')
      setLoadError(message)
    } finally {
      if (requestID === libraryLoadRequestRef.current && workspaceIdRef.current === requestedWorkspaceID)
        setLoading(false)
    }
  }

  useEffect(() => {
    libraryLoadRequestRef.current += 1
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, workspaceCaps.mcp, workspaceCaps.prompts, workspaceCaps.skills])

  useEffect(() => {
    // Resource editors and destructive confirmations are scoped to the active
    // workspace. Close them as soon as the scope changes so a pending dialog
    // cannot submit its old row with the new workspace id.
    setEditor((current) => ({ ...current, open: false }))
    setMcpEditor((current) => ({ ...current, open: false, row: undefined }))
    setDeleteTarget(null)
    for (const kind of ['skill', 'prompt', 'mcp'] as const) resourceOperationEpochRef.current[kind] += 1
    savingRef.current = false
    savingKindRef.current = null
    setSaving(false)
    mcpSavingRef.current = false
    setMcpSaving(false)
    busyMCPActionRef.current = ''
    setBusyMCPAction('')
    deletingRef.current = false
    deletingKindRef.current = null
    setDeleting(false)
    addingCatalogRef.current = ''
    setAddingCatalogId('')
  }, [workspaceId])

  useEffect(() => {
    // Policy refreshes fail closed while loading, but that transient state must
    // not dismiss work. Compare only settled policy decisions. A workspace
    // switch is handled separately above and the first settled policy then
    // becomes authoritative for family-specific revocation.
    if (!workspacePolicyResolved) return
    const previous = resourceEnabledRef.current
    resourceEnabledRef.current = resourceEnabled
    const revoked = revokedLibraryResourceKinds(previous, resourceEnabled)
    if (revoked.length === 0) return

    for (const kind of revoked) resourceOperationEpochRef.current[kind] += 1

    if (revoked.includes(editor.kind)) {
      setEditor((current) => ({ ...current, open: false }))
    }
    if (savingKindRef.current && revoked.includes(savingKindRef.current)) {
      savingRef.current = false
      savingKindRef.current = null
      setSaving(false)
    }
    if (revoked.includes('mcp')) {
      setMcpEditor((current) => ({ ...current, open: false, row: undefined }))
      mcpSavingRef.current = false
      setMcpSaving(false)
      busyMCPActionRef.current = ''
      setBusyMCPAction('')
    }
    setDeleteTarget((current) => (current && revoked.includes(current.kind) ? null : current))
    if (deletingKindRef.current && revoked.includes(deletingKindRef.current)) {
      deletingRef.current = false
      deletingKindRef.current = null
      setDeleting(false)
    }
    const catalogKind = addingCatalogRef.current.split(':', 1)[0] as ItemKind | ''
    if (catalogKind && revoked.includes(catalogKind)) {
      addingCatalogRef.current = ''
      setAddingCatalogId('')
    }
  }, [editor.kind, resourceEnabled, workspacePolicyResolved])

  // A policy update can arrive while the library is open. Keep the current
  // filter on a visible resource family instead of leaving an empty, disabled
  // tab selected.
  useEffect(() => {
    const enabled =
      kindFilter === 'skill'
        ? skillResourceEnabled
        : kindFilter === 'prompt'
          ? promptResourceEnabled
          : kindFilter === 'mcp'
            ? mcpResourceEnabled
            : true
    if (!enabled) setKindFilter('all')
  }, [kindFilter, mcpResourceEnabled, promptResourceEnabled, skillResourceEnabled])

  const normalizedQuery = query.trim().toLocaleLowerCase()
  const filteredSkills = useMemo(
    () =>
      skills.filter(
        (item) =>
          resourceEnabled.skill &&
          (kindFilter === 'all' || kindFilter === 'skill') &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            skillDisplayDescription(item).toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [kindFilter, normalizedQuery, resourceEnabled.skill, skills],
  )
  const filteredPrompts = useMemo(
    () =>
      prompts.filter(
        (item) =>
          resourceEnabled.prompt &&
          (kindFilter === 'all' || kindFilter === 'prompt') &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            item.description.toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [kindFilter, normalizedQuery, prompts, resourceEnabled.prompt],
  )
  const filteredMcps = useMemo(
    () =>
      mcps.filter(
        (item) =>
          resourceEnabled.mcp &&
          (kindFilter === 'all' || kindFilter === 'mcp') &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            item.description.toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [kindFilter, normalizedQuery, mcps, resourceEnabled.mcp],
  )
  const filteredCatalogSkills = useMemo(
    () =>
      catalog.skills.filter(
        (item) =>
          resourceEnabled.skill &&
          (kindFilter === 'all' || kindFilter === 'skill') &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            skillDisplayDescription(item).toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [catalog.skills, kindFilter, normalizedQuery, resourceEnabled.skill],
  )
  const filteredCatalogPrompts = useMemo(
    () =>
      catalog.prompts.filter(
        (item) =>
          resourceEnabled.prompt &&
          (kindFilter === 'all' || kindFilter === 'prompt') &&
          (!normalizedQuery ||
            item.name.toLocaleLowerCase().includes(normalizedQuery) ||
            item.description.toLocaleLowerCase().includes(normalizedQuery)),
      ),
    [catalog.prompts, kindFilter, normalizedQuery, resourceEnabled.prompt],
  )

  function editSkill(item: ApiUserSkill) {
    if (!resourceEnabled.skill || !item.can_manage) return
    setEditor({
      open: true,
      kind: 'skill',
      id: item.id,
      name: item.name,
      description: item.description,
      content: item.instructions,
      importText: '',
    })
  }

  function editPrompt(item: ApiUserPrompt) {
    if (!resourceEnabled.prompt || !item.can_manage) return
    setEditor({
      open: true,
      kind: 'prompt',
      id: item.id,
      name: item.name,
      description: item.description,
      content: item.content,
      importText: '',
    })
  }

  function editMCP(item: ApiUserMCP) {
    if (!resourceEnabled.mcp || !item.can_manage) return
    setMcpEditor({ open: true, row: item, draft: mcpDraftFromRow(item) })
  }

  function updateMCPDraft(patch: Partial<MCPDraft>) {
    setMcpEditor((current) => ({
      ...current,
      draft: { ...current.draft, ...patch },
    }))
  }

  function updateMCPHeader(id: number, patch: Partial<MCPHeaderDraft>) {
    setMcpEditor((current) => ({
      ...current,
      draft: {
        ...current.draft,
        headers: current.draft.headers.map((header) => (header.id === id ? { ...header, ...patch } : header)),
      },
    }))
  }

  async function saveMCPEditor() {
    if (mcpSavingRef.current) return
    if (!resourceEnabled.mcp || !canCreateMCP) {
      toast.error(
        t('library:permissions.createMcp', {
          defaultValue: 'You cannot create MCP services in this workspace.',
        }),
      )
      return
    }
    const draft = mcpEditor.draft
    const name = draft.name.trim()
    const icon = draft.icon.trim() || 'Blocks'
    const description = draft.description.trim()
    const url = draft.url.trim()
    if (!name || !description || !url) {
      toast.error(t('library:editor.missingFields'))
      return
    }
    // Header-key syntax / duplicate / URL validation is enforced server-side
    // (validateMCPServerMetadata mirrors the admin handler); masked values read
    // back from GET are sent unchanged on PATCH and kept by the server.
    const headers: Record<string, string> = {}
    for (const header of draft.headers) {
      const key = header.key.trim()
      const value = header.value.trim()
      if (!key && !value) continue
      headers[key] = value
    }
    const body = {
      name,
      icon,
      description,
      url,
      headers,
      enabled: draft.enabled,
    }
    const operationEpoch = resourceOperationEpochRef.current.mcp
    const requestedWorkspaceID = workspaceId
    mcpSavingRef.current = true
    setMcpSaving(true)
    try {
      await libraryApi.createMCP(body, workspaceId)
      if (!operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) return
      toast.success(t('library:editor.created'))
      setMcpEditor((current) => ({ ...current, open: false, row: undefined }))
      // POST runs discovery; the fresh row carries its sync state. A re-sync
      // stays available from the row menu if the first attempt failed.
      await load()
    } catch (error) {
      if (!operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) return
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('library:editor.nameExists'))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
      }
    } finally {
      if (operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) {
        mcpSavingRef.current = false
        setMcpSaving(false)
      }
    }
  }

  function saveMCPEdit() {
    if (!resourceEnabled.mcp || !mcpEditor.row?.can_manage) {
      toast.error(
        t('library:permissions.manageMcp', {
          defaultValue: 'You cannot manage this MCP service.',
        }),
      )
      return
    }
    const draft = mcpEditor.draft
    const name = draft.name.trim()
    const icon = draft.icon.trim() || 'Blocks'
    const description = draft.description.trim()
    const url = draft.url.trim()
    if (!name || !description || !url) {
      toast.error(t('library:editor.missingFields'))
      return
    }
    const headers: Record<string, string> = {}
    for (const header of draft.headers) {
      const key = header.key.trim()
      const value = header.value.trim()
      if (!key && !value) continue
      headers[key] = value
    }
    const body = {
      name,
      icon,
      description,
      url,
      headers,
      enabled: draft.enabled,
    }
    const row = mcpEditor.row
    if (!row) return
    void handleMCPMutation(() => libraryApi.updateMCP(row.id, body, workspaceId))
  }

  /** Shared MCP mutation runner: guards concurrency, surface 409 nameExists,
   *  and refreshes the list after the server's re-discovery ran. */
  async function handleMCPMutation(request: () => Promise<unknown>) {
    if (mcpSavingRef.current) return
    const row = mcpEditor.row
    const operationEpoch = resourceOperationEpochRef.current.mcp
    const requestedWorkspaceID = workspaceId
    mcpSavingRef.current = true
    setMcpSaving(true)
    try {
      await request()
      if (!operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) return
      toast.success(row ? t('library:editor.updated') : t('library:editor.created'))
      setMcpEditor((current) => ({ ...current, open: false, row: undefined }))
      // POST runs discovery and PATCH re-runs it when the URL changed, so the
      // visible list should reflect the fresh sync snapshot.
      await load()
    } catch (error) {
      if (!operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) return
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('library:editor.nameExists'))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
      }
    } finally {
      if (operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) {
        mcpSavingRef.current = false
        setMcpSaving(false)
      }
    }
  }

  async function runMCPRowAction(item: ApiUserMCP, action: 'test' | 'sync') {
    // Test/sync are write-side operations: the API intentionally requires the
    // same per-row manage authority as edit/delete because sync persists the
    // discovered snapshot. Keep the guard here as well as in the row menu so a
    // stale event or keyboard invocation cannot issue a guaranteed 403.
    if (!resourceEnabled.mcp || !item.can_manage) {
      toast.error(
        t('library:permissions.manageMcp', {
          defaultValue: 'You cannot manage this MCP service.',
        }),
      )
      return
    }
    if (!canUseMCP) {
      toast.error(
        t('library:permissions.useMcp', {
          defaultValue: 'You cannot use MCP services in this workspace.',
        }),
      )
      return
    }
    if (busyMCPActionRef.current) return
    const actionKey = `${action}:${item.id}`
    const operationEpoch = resourceOperationEpochRef.current.mcp
    const requestedWorkspaceID = workspaceId
    busyMCPActionRef.current = actionKey
    setBusyMCPAction(actionKey)
    try {
      if (action === 'test') {
        const result = await libraryApi.testMCP(item.id, workspaceId)
        if (!operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) return
        if (!result.ok) {
          toast.error(result.error || t('library:mcpEditor.testFailed'))
          return
        }
        toast.success(t('library:mcpEditor.testOk'))
        return
      }
      await libraryApi.syncMCP(item.id, workspaceId)
      if (!operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) return
      toast.success(t('library:mcpEditor.synced'))
      await load()
    } catch (error) {
      if (!operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) return
      toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
    } finally {
      if (operationIsCurrent('mcp', operationEpoch, requestedWorkspaceID)) {
        busyMCPActionRef.current = ''
        setBusyMCPAction('')
      }
    }
  }

  function applySkillImport() {
    const parsed = parseSkillDocument(editor.importText)
    if (!parsed.name || !parsed.description || !parsed.instructions) {
      toast.error(t('library:editor.importInvalid'))
      return
    }
    setEditor((current) => ({
      ...current,
      name: parsed.name ?? current.name,
      description: parsed.description ?? current.description,
      content: parsed.instructions || current.content,
    }))
    toast.success(t('library:editor.imported'))
  }

  async function saveEditor() {
    if (savingRef.current) return
    const canCreate = editor.kind === 'skill' ? canCreateSkill : canCreatePrompt
    const existing = editor.id
      ? editor.kind === 'skill'
        ? skills.find((item) => item.id === editor.id)
        : prompts.find((item) => item.id === editor.id)
      : undefined
    const allowed = editor.id ? existing?.can_manage === true : canCreate
    if (!resourceEnabled[editor.kind] || !allowed) {
      toast.error(
        t(editor.id ? 'library:permissions.manageResource' : 'library:permissions.createResource', {
          defaultValue: editor.id
            ? 'You cannot manage this resource in the workspace.'
            : 'You cannot create this resource in the workspace.',
        }),
      )
      return
    }
    const name = editor.name.trim()
    const description = editor.description.trim()
    const content = editor.content.trim()
    if (!name || !description || !content) {
      toast.error(t('library:editor.missingFields'))
      return
    }
    if (editor.kind === 'skill' && !isValidSkillName(name)) {
      toast.error(t('library:editor.invalidSkillName'))
      return
    }
    savingRef.current = true
    savingKindRef.current = editor.kind
    setSaving(true)
    const operationKind = editor.kind
    const operationEpoch = resourceOperationEpochRef.current[operationKind]
    const requestedWorkspaceID = workspaceId
    try {
      if (editor.kind === 'skill') {
        const body = { name, description, instructions: content }
        if (editor.id) await libraryApi.updateSkill(editor.id, body, workspaceId)
        else await libraryApi.createSkill(body, workspaceId)
      } else {
        const body = { name, description, content }
        if (editor.id) await libraryApi.updatePrompt(editor.id, body, workspaceId)
        else await libraryApi.createPrompt(body, workspaceId)
      }
      if (!operationIsCurrent(operationKind, operationEpoch, requestedWorkspaceID)) return
      toast.success(editor.id ? t('library:editor.updated') : t('library:editor.created'))
      setEditor((current) => ({ ...current, open: false }))
      await load()
    } catch (error) {
      if (!operationIsCurrent(operationKind, operationEpoch, requestedWorkspaceID)) return
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('library:editor.nameExists'))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
      }
    } finally {
      if (operationIsCurrent(operationKind, operationEpoch, requestedWorkspaceID)) {
        savingRef.current = false
        savingKindRef.current = null
        setSaving(false)
      }
    }
  }

  async function removeTarget() {
    if (!deleteTarget || deletingRef.current) return
    const canManageTarget =
      deleteTarget.kind === 'skill'
        ? resourceEnabled.skill && deleteTarget.item.can_manage
        : deleteTarget.kind === 'prompt'
          ? resourceEnabled.prompt && deleteTarget.item.can_manage
          : resourceEnabled.mcp && deleteTarget.item.can_manage
    if (!canManageTarget) {
      setDeleteTarget(null)
      toast.error(
        t(
          deleteTarget.kind === 'mcp'
            ? 'library:permissions.manageMcp'
            : 'library:permissions.manageResource',
          {
            defaultValue:
              deleteTarget.kind === 'mcp'
                ? 'You cannot manage this MCP service.'
                : 'You cannot manage this resource in the workspace.',
          },
        ),
      )
      return
    }
    const target = deleteTarget
    const operationKind = target.kind
    deletingRef.current = true
    deletingKindRef.current = operationKind
    setDeleting(true)
    const operationEpoch = resourceOperationEpochRef.current[operationKind]
    const requestedWorkspaceID = workspaceId
    try {
      if (target.kind === 'skill') await libraryApi.removeSkill(target.item.id, workspaceId)
      else if (target.kind === 'prompt') await libraryApi.removePrompt(target.item.id, workspaceId)
      else await libraryApi.removeMCP(target.item.id, workspaceId)
      if (!operationIsCurrent(operationKind, operationEpoch, requestedWorkspaceID)) return
      toast.success(t('library:remove.done'))
      setDeleteTarget(null)
      await load()
    } catch (error) {
      if (!operationIsCurrent(operationKind, operationEpoch, requestedWorkspaceID)) return
      toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
    } finally {
      if (operationIsCurrent(operationKind, operationEpoch, requestedWorkspaceID)) {
        deletingRef.current = false
        deletingKindRef.current = null
        setDeleting(false)
      }
    }
  }

  async function addCatalogItem(kind: ItemKind, id: string) {
    if (
      (kind === 'skill' && (!resourceEnabled.skill || !canCreateSkill)) ||
      (kind === 'prompt' && (!resourceEnabled.prompt || !canCreatePrompt))
    )
      return
    if (addingCatalogRef.current) return
    const actionKey = `${kind}:${id}`
    const operationEpoch = resourceOperationEpochRef.current[kind]
    const requestedWorkspaceID = workspaceId
    addingCatalogRef.current = actionKey
    setAddingCatalogId(actionKey)
    try {
      if (kind === 'skill') await libraryApi.addCatalogSkill(id, workspaceId)
      else await libraryApi.addCatalogPrompt(id, workspaceId)
      if (!operationIsCurrent(kind, operationEpoch, requestedWorkspaceID)) return
      toast.success(t('library:catalog.added'))
      await load()
    } catch (error) {
      if (!operationIsCurrent(kind, operationEpoch, requestedWorkspaceID)) return
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('library:editor.nameExists'))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('common:common.error'))
      }
    } finally {
      if (operationIsCurrent(kind, operationEpoch, requestedWorkspaceID)) {
        addingCatalogRef.current = ''
        setAddingCatalogId('')
      }
    }
  }

  const mineEmpty = filteredSkills.length === 0 && filteredPrompts.length === 0 && filteredMcps.length === 0
  const catalogEmpty = filteredCatalogSkills.length === 0 && filteredCatalogPrompts.length === 0
  const firstCreatableKind: ItemKind | null =
    resourceEnabled.skill && canCreateSkill
      ? 'skill'
      : resourceEnabled.prompt && canCreatePrompt
        ? 'prompt'
        : resourceEnabled.mcp && canCreateMCP
          ? 'mcp'
          : null
  // In a filtered view, the empty-state action must create the family the
  // user is looking at. Falling back to the first globally-creatable family
  // would open a Skill editor while the MCP filter is active.
  const emptyStateCreatableKind: ItemKind | null =
    kindFilter === 'all'
      ? firstCreatableKind
      : kindFilter === 'skill' && resourceEnabled.skill && canCreateSkill
        ? 'skill'
        : kindFilter === 'prompt' && resourceEnabled.prompt && canCreatePrompt
          ? 'prompt'
          : kindFilter === 'mcp' && resourceEnabled.mcp && canCreateMCP
            ? 'mcp'
            : null

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={t('library:title')}
        actions={
          canWrite ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  size="sm"
                  variant="secondary"
                  leadingIcon={<Plus size={15} aria-hidden />}
                  className="max-sm:min-h-[var(--tap-min)]"
                >
                  {t('library:new')}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {resourceEnabled.skill && canCreateSkill ? (
                  <DropdownMenuItem onSelect={() => setEditor(newEditor('skill'))}>
                    <Sparkles size={14} aria-hidden /> {t('library:kinds.skill')}
                  </DropdownMenuItem>
                ) : null}
                {resourceEnabled.prompt && canCreatePrompt ? (
                  <DropdownMenuItem onSelect={() => setEditor(newEditor('prompt'))}>
                    <FileText size={14} aria-hidden /> {t('library:kinds.prompt')}
                  </DropdownMenuItem>
                ) : null}
                {resourceEnabled.mcp && canCreateMCP ? (
                  <DropdownMenuItem onSelect={() => setMcpEditor({ open: true, draft: newMCPDraft() })}>
                    <Blocks size={14} aria-hidden /> {t('library:kinds.mcp')}
                  </DropdownMenuItem>
                ) : null}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null
        }
      />

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-4 pb-24 pt-4 sm:px-8 sm:pt-6">
          <Tabs
            value={tab}
            onValueChange={(value) => {
              const nextTab = value as typeof tab
              setTab(nextTab)
              if (nextTab === 'catalog' && kindFilter === 'mcp') setKindFilter('all')
            }}
          >
            <div className="flex flex-col gap-3 border-b border-[var(--color-divider)] pb-4 md:flex-row md:items-center md:justify-between">
              <TabsList variant="segmented" className="self-start border-0">
                <TabsTrigger value="mine" variant="segmented">
                  {t('library:tabs.mine')}
                </TabsTrigger>
                <TabsTrigger value="catalog" variant="segmented">
                  {t('library:tabs.catalog')}
                </TabsTrigger>
              </TabsList>
              <div className="flex min-w-0 flex-col gap-2 sm:flex-row sm:items-center">
                <div className="min-w-0 flex-1 sm:w-64 sm:flex-none">
                  <Input
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    leadingIcon={<Search size={14} aria-hidden />}
                    placeholder={t('library:search')}
                    aria-label={t('library:search')}
                    wrapperClassName="max-sm:h-[var(--tap-min)]"
                  />
                </div>
                <KindFilterControl
                  value={kindFilter}
                  onChange={setKindFilter}
                  includeMCP={tab === 'mine' && resourceEnabled.mcp}
                  includeSkill={resourceEnabled.skill}
                  includePrompt={resourceEnabled.prompt}
                  t={t}
                />
              </div>
            </div>

            {loading ? (
              <LibrarySkeleton label={t('common:common.loading')} />
            ) : loadError ? (
              <EmptyState
                className="py-14"
                icon={<LibraryBig size={20} aria-hidden />}
                title={t('common:common.error')}
                description={loadError}
                action={
                  <Button variant="secondary" onClick={() => void load()}>
                    {t('common:actions.tryAgain')}
                  </Button>
                }
              />
            ) : (
              <>
                <TabsContent value="mine" className="mt-0">
                  {mineEmpty ? (
                    <EmptyState
                      className="py-14"
                      icon={<LibraryBig size={20} aria-hidden />}
                      title={query ? t('library:empty.search') : t('library:empty.mine')}
                      action={
                        !query && emptyStateCreatableKind ? (
                          <Button
                            variant="secondary"
                            leadingIcon={<Plus size={14} aria-hidden />}
                            onClick={() => {
                              if (emptyStateCreatableKind === 'mcp')
                                setMcpEditor({
                                  open: true,
                                  draft: newMCPDraft(),
                                })
                              else setEditor(newEditor(emptyStateCreatableKind))
                            }}
                          >
                            {emptyStateCreatableKind === 'mcp'
                              ? t('library:mcpEditor.newTitle')
                              : emptyStateCreatableKind === 'prompt'
                                ? t('library:editor.newPrompt')
                                : t('library:newSkill')}
                          </Button>
                        ) : undefined
                      }
                    />
                  ) : (
                    <LibraryRows>
                      {filteredSkills.map((item) => (
                        <UserLibraryRow
                          key={`skill:${item.id}`}
                          kind="skill"
                          name={item.name}
                          description={skillDisplayDescription(item)}
                          icon={item.icon}
                          imported={Boolean(item.source_skill_id)}
                          canManage={item.can_manage && resourceEnabled.skill}
                          onEdit={() => editSkill(item)}
                          onDelete={() => setDeleteTarget({ kind: 'skill', item })}
                          t={t}
                        />
                      ))}
                      {filteredPrompts.map((item) => (
                        <UserLibraryRow
                          key={`prompt:${item.id}`}
                          kind="prompt"
                          name={item.name}
                          description={item.description}
                          imported={Boolean(item.source_prompt_id)}
                          canManage={item.can_manage && resourceEnabled.prompt}
                          onEdit={() => editPrompt(item)}
                          onDelete={() => setDeleteTarget({ kind: 'prompt', item })}
                          t={t}
                        />
                      ))}
                      {filteredMcps.map((item) => {
                        const toolCount = item.discovered_tools?.length ?? 0
                        const canManageMCP = item.can_manage && resourceEnabled.mcp
                        return (
                          <UserLibraryRow
                            key={`mcp:${item.id}`}
                            kind="mcp"
                            name={item.name}
                            description={item.description}
                            icon={item.icon}
                            canManage={canManageMCP}
                            status={
                              item.last_error ? (
                                <Badge size="xs" variant="danger">
                                  {t('library:mcpStatus.error')}
                                </Badge>
                              ) : toolCount > 0 ? (
                                <Badge size="xs" variant="success">
                                  {t('library:mcpEditor.toolsCount', {
                                    count: toolCount,
                                  })}
                                </Badge>
                              ) : (
                                <Badge size="xs" variant="neutral">
                                  {t('library:mcpStatus.notSynced')}
                                </Badge>
                              )
                            }
                            onEdit={() => editMCP(item)}
                            onTest={canManageMCP && canUseMCP ? () => void runMCPRowAction(item, 'test') : undefined}
                            onSync={canManageMCP && canUseMCP ? () => void runMCPRowAction(item, 'sync') : undefined}
                            testing={busyMCPAction === `test:${item.id}`}
                            syncing={busyMCPAction === `sync:${item.id}`}
                            actionDisabled={Boolean(busyMCPAction)}
                            onDelete={() => setDeleteTarget({ kind: 'mcp', item })}
                            t={t}
                          />
                        )
                      })}
                    </LibraryRows>
                  )}
                </TabsContent>

                <TabsContent value="catalog" className="mt-0">
                  {catalogEmpty ? (
                    <EmptyState
                      className="py-14"
                      icon={<LibraryBig size={20} aria-hidden />}
                      title={query ? t('library:empty.search') : t('library:empty.catalog')}
                    />
                  ) : (
                    <LibraryRows>
                      {filteredCatalogSkills.map((item) => (
                        <CatalogRow
                          key={`catalog-skill:${item.id}`}
                          kind="skill"
                          item={item}
                          adding={addingCatalogId === `skill:${item.id}`}
                          canAdd={resourceEnabled.skill && canCreateSkill}
                          onAdd={() => void addCatalogItem('skill', item.id)}
                          t={t}
                        />
                      ))}
                      {filteredCatalogPrompts.map((item) => (
                        <CatalogRow
                          key={`catalog-prompt:${item.id}`}
                          kind="prompt"
                          item={item}
                          adding={addingCatalogId === `prompt:${item.id}`}
                          canAdd={resourceEnabled.prompt && canCreatePrompt}
                          onAdd={() => void addCatalogItem('prompt', item.id)}
                          t={t}
                        />
                      ))}
                    </LibraryRows>
                  )}
                </TabsContent>
              </>
            )}
          </Tabs>
        </div>
      </div>

      <LibraryEditor
        editor={editor}
        saving={saving}
        setEditor={setEditor}
        onImport={applySkillImport}
        onSave={() => void saveEditor()}
        t={t}
      />

      <MCPEditor
        editor={mcpEditor}
        saving={mcpSaving}
        setEditor={setMcpEditor}
        onUpdateDraft={updateMCPDraft}
        onUpdateHeader={updateMCPHeader}
        onSaveNew={() => void saveMCPEditor()}
        onSaveEdit={() => void saveMCPEdit()}
        t={t}
      />

      <Dialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => {
          if (!open && !deletingRef.current) setDeleteTarget(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('library:remove.title')}</DialogTitle>
            <DialogDescription>
              {deleteTarget ? t('library:remove.body', { name: deleteTarget.item.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => setDeleteTarget(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void removeTarget()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function KindFilterControl({
  value,
  onChange,
  includeMCP,
  includeSkill = true,
  includePrompt = true,
  t,
}: {
  value: KindFilter
  onChange: (value: KindFilter) => void
  includeMCP: boolean
  includeSkill?: boolean
  includePrompt?: boolean
  t: ReturnType<typeof useTranslation>['t']
}) {
  const options: KindFilter[] = [
    'all',
    ...(includeSkill ? (['skill'] as const) : []),
    ...(includePrompt ? (['prompt'] as const) : []),
    ...(includeMCP ? (['mcp'] as const) : []),
  ]
  return (
    <div
      className={cn(
        'grid w-full items-center rounded-[9px] bg-[var(--color-bg-muted)] p-1 sm:inline-flex sm:w-auto sm:shrink-0',
        options.length === 4
          ? 'grid-cols-4'
          : options.length === 3
            ? 'grid-cols-3'
            : options.length === 2
              ? 'grid-cols-2'
              : 'grid-cols-1',
      )}
      role="group"
      aria-label={t('library:filter.label')}
    >
      {options.map((kind) => (
        <button
          key={kind}
          type="button"
          aria-pressed={value === kind}
          onClick={() => onChange(kind)}
          className={cn(
            'inline-flex min-w-0 items-center justify-center rounded-[7px] px-2 text-[12px] font-medium interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] h-[var(--tap-min)] sm:h-8 sm:px-2.5',
            value === kind
              ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
              : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
          )}
        >
          {t(`library:filter.${kind}`)}
        </button>
      ))}
    </div>
  )
}

function LibraryRows({ children }: { children: ReactNode }) {
  return <ul className="divide-y divide-[var(--color-divider)] border-b border-[var(--color-divider)]">{children}</ul>
}

function UserLibraryRow({
  kind,
  name,
  description,
  icon,
  imported,
  canManage,
  status,
  onEdit,
  onTest,
  onSync,
  testing = false,
  syncing = false,
  actionDisabled = false,
  onDelete,
  t,
}: {
  kind: ItemKind
  name: string
  description: string
  icon?: string
  imported?: boolean
  canManage: boolean
  /** Optional status chip (MCP rows) rendered after the kind badge. */
  status?: ReactNode
  onEdit: () => void
  onTest?: () => void
  onSync?: () => void
  testing?: boolean
  syncing?: boolean
  actionDisabled?: boolean
  onDelete: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  return (
    <li className="group flex min-w-0 items-center gap-3 py-3 sm:px-2">
      <span className="grid size-9 shrink-0 place-items-center rounded-[9px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
        {kind === 'skill' ? (
          <SkillIcon name={icon} size={16} aria-hidden />
        ) : kind === 'mcp' ? (
          <LucideGlyph name={icon || 'Blocks'} size={16} aria-hidden />
        ) : (
          <FileText size={16} aria-hidden />
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-[14px] font-medium text-[var(--color-fg)]" title={name}>
            {name}
          </span>
          <span className="shrink-0">
            <Badge size="xs" variant="neutral">
              {t(`library:kinds.${kind}`)}
            </Badge>
          </span>
          {status ? <span className="shrink-0">{status}</span> : null}
          {imported ? (
            <span className="hidden text-[10.5px] text-[var(--color-fg-subtle)] sm:inline">
              {t('library:catalog.fromAdmin')}
            </span>
          ) : null}
        </div>
        <p className="mt-0.5 truncate text-[12.5px] text-[var(--color-fg-muted)]">{description}</p>
      </div>
      {canManage ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label={`${t('common:actions.more')}: ${name}`}
              className="inline-flex size-[var(--tap-min)] shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-100 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:size-8 sm:opacity-0 sm:group-hover:opacity-100 sm:data-[state=open]:opacity-100 sm:focus-visible:opacity-100"
            >
              <MoreHorizontal size={15} aria-hidden />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem disabled={actionDisabled} onSelect={onEdit}>
              <Pencil size={14} aria-hidden /> {t('common:actions.edit')}
            </DropdownMenuItem>
            {onTest ? (
              <DropdownMenuItem disabled={actionDisabled} onSelect={onTest}>
                <Activity size={14} className={testing ? 'animate-pulse' : undefined} aria-hidden />{' '}
                {t('library:mcpEditor.test')}
              </DropdownMenuItem>
            ) : null}
            {onSync ? (
              <DropdownMenuItem disabled={actionDisabled} onSelect={onSync}>
                <Cable size={14} className={syncing ? 'animate-pulse' : undefined} aria-hidden />{' '}
                {t('library:mcpEditor.sync')}
              </DropdownMenuItem>
            ) : null}
            <DropdownMenuItem disabled={actionDisabled} destructive onSelect={onDelete}>
              <Trash2 size={14} aria-hidden /> {t('common:actions.delete')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <span className="size-[var(--tap-min)] shrink-0 sm:size-8" aria-hidden />
      )}
    </li>
  )
}

function CatalogRow({
  kind,
  item,
  adding,
  canAdd,
  onAdd,
  t,
}: {
  kind: ItemKind
  item: ApiLibraryCatalogSkill | ApiLibraryCatalogPrompt
  adding: boolean
  canAdd: boolean
  onAdd: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  const description = kind === 'skill' ? skillDisplayDescription(item as ApiLibraryCatalogSkill) : item.description
  return (
    <li className="flex min-w-0 items-center gap-3 py-3 sm:px-2">
      <span className="grid size-9 shrink-0 place-items-center rounded-[9px] bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
        {kind === 'skill' ? (
          <SkillIcon name={(item as ApiLibraryCatalogSkill).icon} size={16} aria-hidden />
        ) : (
          <FileText size={16} aria-hidden />
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-[14px] font-medium text-[var(--color-fg)]" title={item.name}>
            {item.name}
          </span>
          <span className="shrink-0">
            <Badge size="xs" variant="neutral">
              {t(`library:kinds.${kind}`)}
            </Badge>
          </span>
        </div>
        <p className="mt-0.5 truncate text-[12.5px] text-[var(--color-fg-muted)]">{description}</p>
      </div>
      {item.added ? (
        <span
          className="inline-flex h-9 shrink-0 items-center gap-1.5 px-2 text-[12px] text-[var(--color-success)]"
          aria-label={t('library:catalog.addedLabel')}
          title={t('library:catalog.addedLabel')}
        >
          <Check size={14} aria-hidden />
          <span className="hidden sm:inline">{t('library:catalog.addedLabel')}</span>
        </span>
      ) : canAdd ? (
        <Button
          variant="ghost"
          size="sm"
          loading={adding}
          leadingIcon={<Plus size={14} aria-hidden />}
          onClick={onAdd}
          className="max-sm:min-h-[var(--tap-min)]"
        >
          {t('library:catalog.add')}
        </Button>
      ) : (
        <span className="size-9 shrink-0" aria-hidden />
      )}
    </li>
  )
}

function LibraryEditor({
  editor,
  saving,
  setEditor,
  onImport,
  onSave,
  t,
}: {
  editor: EditorState
  saving: boolean
  setEditor: Dispatch<SetStateAction<EditorState>>
  onImport: () => void
  onSave: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  const skill = editor.kind === 'skill'
  return (
    <Dialog open={editor.open} onOpenChange={(open) => !saving && setEditor((current) => ({ ...current, open }))}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>
            {editor.id
              ? t(skill ? 'library:editor.editSkill' : 'library:editor.editPrompt')
              : t(skill ? 'library:editor.newSkill' : 'library:editor.newPrompt')}
          </DialogTitle>
        </DialogHeader>
        <DialogBody>
          <div className="grid gap-4">
            {skill ? (
              <Field
                label={t('library:editor.importLabel')}
                htmlFor="library-skill-import"
                hint={t('library:editor.importHint')}
              >
                <Textarea
                  id="library-skill-import"
                  rows={4}
                  className="font-mono text-[12px]"
                  value={editor.importText}
                  onChange={(event) =>
                    setEditor((current) => ({
                      ...current,
                      importText: event.target.value,
                    }))
                  }
                  placeholder={
                    '---\nname: meeting-follow-up\ndescription: Extract decisions and next steps from meeting notes.\n---\n\nReview the notes and return...'
                  }
                />
                <div className="mt-2 flex justify-end">
                  <Button size="sm" variant="secondary" onClick={onImport}>
                    {t('library:editor.importAction')}
                  </Button>
                </div>
              </Field>
            ) : null}
            <Field
              label={t('library:editor.name')}
              htmlFor="library-item-name"
              hint={skill ? t('library:editor.skillNameHint') : undefined}
            >
              <Input
                id="library-item-name"
                autoFocus={!skill}
                value={editor.name}
                onChange={(event) =>
                  setEditor((current) => ({
                    ...current,
                    name: event.target.value,
                  }))
                }
                placeholder={skill ? 'meeting-follow-up' : t('library:editor.promptNamePlaceholder')}
              />
            </Field>
            <Field
              label={t(skill ? 'library:editor.when' : 'library:editor.description')}
              htmlFor="library-item-description"
            >
              <Input
                id="library-item-description"
                value={editor.description}
                onChange={(event) =>
                  setEditor((current) => ({
                    ...current,
                    description: event.target.value,
                  }))
                }
              />
            </Field>
            <Field
              label={t(skill ? 'library:editor.instructions' : 'library:editor.promptContent')}
              htmlFor="library-item-content"
            >
              <Textarea
                id="library-item-content"
                rows={12}
                value={editor.content}
                onChange={(event) =>
                  setEditor((current) => ({
                    ...current,
                    content: event.target.value,
                  }))
                }
              />
            </Field>
          </div>
        </DialogBody>
        <DialogFooter>
          <Button
            variant="ghost"
            disabled={saving}
            onClick={() => setEditor((current) => ({ ...current, open: false }))}
          >
            {t('common:actions.cancel')}
          </Button>
          <Button loading={saving} onClick={onSave}>
            {t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function MCPEditor({
  editor,
  saving,
  setEditor,
  onUpdateDraft,
  onUpdateHeader,
  onSaveNew,
  onSaveEdit,
  t,
}: {
  editor: MCPEditorState
  saving: boolean
  setEditor: Dispatch<SetStateAction<MCPEditorState>>
  onUpdateDraft: (patch: Partial<MCPDraft>) => void
  onUpdateHeader: (id: number, patch: Partial<MCPHeaderDraft>) => void
  onSaveNew: () => void
  onSaveEdit: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  return (
    <Dialog
      open={editor.open}
      onOpenChange={(open) => {
        if (!saving) setEditor((current) => ({ ...current, open }))
      }}
    >
      <DialogContent size="lg" closeDisabled={saving}>
        <DialogHeader>
          <DialogTitle>{editor.row ? t('library:mcpEditor.editTitle') : t('library:mcpEditor.newTitle')}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('library:mcpEditor.name')} htmlFor="library-mcp-name">
              <Input
                id="library-mcp-name"
                autoFocus
                value={editor.draft.name}
                onChange={(event) => onUpdateDraft({ name: event.target.value })}
              />
            </Field>
            <Field label={t('library:mcpEditor.icon')} htmlFor="library-mcp-icon">
              <IconPicker
                id="library-mcp-icon"
                value={editor.draft.icon}
                onChange={(icon) => onUpdateDraft({ icon })}
                aria-label={t('library:mcpEditor.icon')}
              />
            </Field>
            <Field
              label={t('library:mcpEditor.description')}
              htmlFor="library-mcp-description"
              className="sm:col-span-2"
            >
              <Textarea
                id="library-mcp-description"
                rows={3}
                value={editor.draft.description}
                onChange={(event) => onUpdateDraft({ description: event.target.value })}
              />
            </Field>
            <Field
              label={t('library:mcpEditor.url')}
              htmlFor="library-mcp-url"
              hint={t('library:mcpEditor.urlHint')}
              className="sm:col-span-2"
            >
              <Input
                id="library-mcp-url"
                type="url"
                dir="ltr"
                autoComplete="url"
                value={editor.draft.url}
                onChange={(event) => onUpdateDraft({ url: event.target.value })}
                placeholder="https://example.com/mcp"
              />
            </Field>

            <fieldset className="min-w-0 sm:col-span-2">
              <legend className="sr-only">{t('library:mcpEditor.headers')}</legend>
              <div className="flex items-start justify-between gap-3">
                <p className="text-sm font-medium leading-tight text-[var(--color-fg)]">
                  {t('library:mcpEditor.headers')}
                </p>
                <Button
                  size="sm"
                  variant="secondary"
                  leadingIcon={<Plus size={13} aria-hidden />}
                  onClick={() =>
                    onUpdateDraft({
                      headers: [...editor.draft.headers, makeMCPHeader()],
                    })
                  }
                  className="max-sm:min-h-[var(--tap-min)]"
                >
                  {t('library:mcpEditor.addHeader')}
                </Button>
              </div>

              {editor.draft.headers.length > 0 ? (
                <div className="mt-3 grid gap-2">
                  {editor.draft.headers.map((header, index) => (
                    <div
                      key={header.id}
                      className="relative grid gap-2 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-3 pr-12 sm:grid-cols-2"
                    >
                      <Input
                        aria-label={`${t('library:mcpEditor.headerKey')}: ${index + 1}`}
                        value={header.key}
                        onChange={(event) => onUpdateHeader(header.id, { key: event.target.value })}
                        placeholder={t('library:mcpEditor.headerKey')}
                        autoCapitalize="off"
                        autoCorrect="off"
                        spellCheck={false}
                        className="font-mono text-[13px]"
                      />
                      <Input
                        aria-label={`${t('library:mcpEditor.headerValue')}: ${index + 1}`}
                        type={header.revealed ? 'text' : 'password'}
                        value={header.value}
                        onChange={(event) =>
                          onUpdateHeader(header.id, {
                            value: event.target.value,
                          })
                        }
                        placeholder={t('library:mcpEditor.headerValue')}
                        autoComplete="new-password"
                        className="font-mono text-[13px]"
                        trailingSlot={
                          <button
                            type="button"
                            aria-label={
                              header.revealed
                                ? t('admin:mcp.actions.hideHeaderL10n')
                                : t('admin:mcp.actions.showHeaderL10n')
                            }
                            title={
                              header.revealed
                                ? t('admin:mcp.actions.hideHeaderL10n')
                                : t('admin:mcp.actions.showHeaderL10n')
                            }
                            onClick={() =>
                              onUpdateHeader(header.id, {
                                revealed: !header.revealed,
                              })
                            }
                            className="inline-flex size-8 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-faint)] interactive hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                          >
                            {header.revealed ? <EyeOff size={14} aria-hidden /> : <Eye size={14} aria-hidden />}
                          </button>
                        }
                      />
                      <button
                        type="button"
                        aria-label={t('admin:mcp.actions.removeHeaderL10n', {
                          index: index + 1,
                        })}
                        title={t('admin:mcp.actions.removeHeaderL10n', {
                          index: index + 1,
                        })}
                        onClick={() =>
                          onUpdateDraft({
                            headers: editor.draft.headers.filter((row) => row.id !== header.id),
                          })
                        }
                        className="absolute right-2 top-2 inline-flex size-8 items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] interactive hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      >
                        <X size={14} aria-hidden />
                      </button>
                    </div>
                  ))}
                </div>
              ) : null}
            </fieldset>

            <label
              htmlFor="library-mcp-enabled"
              className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5 sm:col-span-2"
            >
              <span>
                <span className="block text-sm font-medium text-[var(--color-fg)]">
                  {t('library:mcpEditor.enabled')}
                </span>
                <span className="mt-0.5 block text-[12px] text-[var(--color-fg-subtle)]">
                  {t('library:mcpEditor.enabledHint')}
                </span>
              </span>
              <Switch
                id="library-mcp-enabled"
                checked={editor.draft.enabled}
                onCheckedChange={(enabled) => onUpdateDraft({ enabled })}
              />
            </label>
          </div>
        </DialogBody>
        <DialogFooter>
          <Button
            variant="ghost"
            disabled={saving}
            onClick={() => setEditor((current) => ({ ...current, open: false }))}
          >
            {t('common:actions.cancel')}
          </Button>
          <Button loading={saving} onClick={editor.row ? onSaveEdit : onSaveNew}>
            {t('common:actions.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function LibrarySkeleton({ label }: { label: string }) {
  return (
    <div className="divide-y divide-[var(--color-divider)]" role="status" aria-label={label}>
      {Array.from({ length: 5 }, (_, index) => (
        <div key={index} className="flex items-center gap-3 py-3 sm:px-2">
          <Skeleton className="size-9 shrink-0 rounded-[9px]" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton shape="line" className="h-3.5 w-1/3" />
            <Skeleton shape="line" className="w-2/3" />
          </div>
          <Skeleton className="size-8 rounded-[8px]" />
        </div>
      ))}
      <span className="sr-only">{label}</span>
    </div>
  )
}
