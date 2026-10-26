import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { X } from 'lucide-react'
import '@fontsource-variable/fraunces'
import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
import i18n from '@/i18n'
import '@/styles/globals.css'
import 'katex/dist/katex.min.css'
import { ArtifactPanel } from '@/components/chat/artifact-panel'
import { Composer } from '@/components/chat/composer'
import { FormulaEditorDialog } from '@/components/chat/formula-editor-dialog'
import { ImageMaskEditor } from '@/components/chat/image-mask-editor'
import { CommandMenu } from '@/components/command-menu/command-menu'
import { FileFiltersPopover } from '@/components/files/file-filters-popover'
import { SetPasswordGate } from '@/components/welcome/set-password-gate'
import { WelcomeCard } from '@/components/welcome/welcome-card'
import { ConversationManagementDialog } from '@/components/settings/conversation-management-dialog'
import { WorkspaceMembersDialog } from '@/components/sidebar/workspace-menu'
import { TemplatePicker } from '@/components/ppt/template-picker'
import SettingsLayout from '@/pages/settings/SettingsLayout'
import { Button } from '@/components/ui/button'
import { Dialog, DialogBody, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Sheet, SheetBody, SheetClose, SheetContent, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useAppViewport } from '@/hooks/use-app-viewport'
import { useCommandMenu } from '@/hooks/use-command-menu'
import { readSurfaceViewport, type SafeAreaInsets } from '@/lib/app-viewport'
import type { FileTypeFilter } from '@/lib/file-preview-kind'
import { useAccent } from '@/store/accent'
import { useArtifactPanel } from '@/store/artifact-panel'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { useLanguage } from '@/store/language'
import { useModels } from '@/store/models'
import { useProjects } from '@/store/projects'
import { useSettings } from '@/store/settings'
import { useSettingsModal } from '@/store/settings-modal'
import { useTheme } from '@/store/theme'
import { useWorkspaces } from '@/store/workspaces'
import type { ApiKnowledgeBase, ApiModel, ApiUser, ApiUserPrompt, ApiUserSkill, ApiWorkspace, ApiWorkspaceMember } from '@/api/types'
import {
  createLongSpreadsheetFixture,
  LONG_SPREADSHEET_LAST_COLUMN,
  LONG_SPREADSHEET_LAST_ROW,
} from './mobile-office-fixtures'

type Mode = 'sheet' | 'password' | 'command' | 'filters' | 'welcome' | 'fullscreen' | 'office' | 'formula' | 'composer' | 'settings' | 'conversations' | 'mask' | 'workspace' | 'template' | 'select'
type OfficeFile = 'docx' | 'pptx' | 'xlsx' | 'long-xlsx' | 'invalid' | 'text'
interface SimulatedViewport {
  left: number
  top: number
  width: number
  height: number
  scale: number
}

const query = new URLSearchParams(location.search)
const modes: Mode[] = ['sheet', 'password', 'command', 'filters', 'welcome', 'fullscreen', 'office', 'formula', 'composer', 'settings', 'conversations', 'mask', 'workspace', 'template', 'select']
const requestedMode = query.get('mode') as Mode | null
const mode: Mode = requestedMode && modes.includes(requestedMode) ? requestedMode : 'sheet'
const officeFiles: OfficeFile[] = ['docx', 'pptx', 'xlsx', 'long-xlsx', 'invalid', 'text']
const requestedFile = query.get('file') as OfficeFile | null
let activeFile: OfficeFile = requestedFile && officeFiles.includes(requestedFile) ? requestedFile : 'docx'
let mounted = false
let fontsReady = false
let selectedChoice = 'choice-1'
const errors: string[] = []
const objectUrls = new Set<string>()
const TEXT_END = 'END-TEXT-LINE-240'
const DOCX_MIME = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
const PPTX_MIME = 'application/vnd.openxmlformats-officedocument.presentationml.presentation'
const XLSX_MIME = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'

// Simulate the browser viewport API; production code still owns all geometry.
const viewportOverrides: Partial<SimulatedViewport> = {}
const simulatedViewport = new EventTarget()
for (const key of ['offsetLeft', 'offsetTop', 'pageLeft', 'pageTop', 'width', 'height', 'scale'] as const) {
  Object.defineProperty(simulatedViewport, key, {
    configurable: true,
    get: () => key === 'width'
      ? viewportOverrides.width ?? window.innerWidth
      : key === 'height'
        ? viewportOverrides.height ?? window.innerHeight
        : key === 'scale'
          ? viewportOverrides.scale ?? 1
          : key === 'offsetLeft' || key === 'pageLeft'
            ? viewportOverrides.left ?? 0
            : viewportOverrides.top ?? 0,
  })
}
Object.defineProperty(window, 'visualViewport', { configurable: true, value: simulatedViewport })

const user: ApiUser = {
  id: 'mobile-layout-user',
  email: 'mobile-layout@example.test',
  name: 'Layout User',
  role: 'admin',
  status: 'active',
  settings: { onboarded: mode !== 'welcome' },
  has_password: mode !== 'password',
  oauth_initial_password_policy: 'required',
  memory_available: false,
  created_at: 1,
}
const model: ApiModel = {
  id: 'mobile-layout-model', channel_id: 'fixture', kind: 'chat', request_id: 'fixture',
  label: 'Layout model', description: '', icon: '', enabled: true, sort_order: 0,
  tool_mode: 'none', vision: false, stream: true, system_prompt: '', param_controls: [],
  price_input: 0, price_output: 0, price_cache_read: 0, price_cache_write: 0,
  price_per_image: 0, currency: 'USD', dim: 0, updated_at: 1,
}
const skills: ApiUserSkill[] = Array.from({ length: 18 }, (_, index) => ({
  id: `layout-skill-${index + 1}`, can_manage: true, name: `Layout skill ${index + 1}`,
  description: `Fixture skill ${index + 1}`, icon: '', instructions: 'Compare the supplied values.',
  created_at: 1, updated_at: 1,
}))
const prompts: ApiUserPrompt[] = Array.from({ length: 18 }, (_, index) => ({
  id: `layout-prompt-${index + 1}`, can_manage: true, name: `Layout prompt ${index + 1}`,
  description: `Fixture prompt ${index + 1}`, content: `Summarize fixture ${index + 1}.`,
  created_at: 1, updated_at: 1,
}))
const knowledgeBases: ApiKnowledgeBase[] = Array.from({ length: 18 }, (_, index) => ({
  id: `layout-kb-${index + 1}`, user_id: user.id, name: `Layout knowledge base ${index + 1}`,
  description: `Fixture collection ${index + 1}`, created_at: 1,
}))
const archivedConversations = Array.from({ length: 20 }, (_, index) => ({
  id: `archived-layout-${index + 1}`, title: `Archived layout conversation ${index + 1}`,
  updated_at: 1700000000 + index, created_at: 1700000000, archived: true,
}))
const workspace: ApiWorkspace = {
  id: 'mobile-layout-workspace', name: 'Layout workspace', owner_id: user.id,
  role: 'admin', is_owner: true, created_at: 1,
  can_create_projects: true, can_private_conversations: true, can_create_skills_prompts: true,
  can_create_kb: true, can_add_kb_files: true, can_delete_kb_content: true, can_delete_conversations: true,
}
const workspaceMembers: ApiWorkspaceMember[] = Array.from({ length: 20 }, (_, index) => ({
  ...workspace, user_id: `layout-member-${index + 1}`, role: 'member', is_owner: false,
  joined_at: 1, name: `Layout member ${index + 1}`, email: `member${index + 1}@example.test`,
  avatar_url: '', can_use_ai_ppt: true,
}))
const imageCanvas = document.createElement('canvas')
imageCanvas.width = 480
imageCanvas.height = 320
const imageContext = imageCanvas.getContext('2d')
if (!imageContext) throw new Error('Fixture canvas unavailable')
imageContext.fillStyle = '#c6d4ea'
imageContext.fillRect(0, 0, imageCanvas.width, imageCanvas.height)
imageContext.fillStyle = '#31496a'
imageContext.fillRect(70, 80, 340, 160)
const imageDataUrl = imageCanvas.toDataURL('image/png')

useAuth.setState({ status: 'authenticated', user })
useWorkspaces.setState({ activeId: mode === 'workspace' ? workspace.id : null, workspaces: mode === 'workspace' ? [workspace] : [], loaded: true, switching: false })
useModels.setState({
  models: [model], defaultId: model.id, loaded: true, loading: false,
  loadedScope: null, loadedPolicyKey: 'personal', imageModels: [], imageModelsLoaded: true,
})
useProjects.setState({ projects: [], loaded: true, loading: false })
useConversations.setState({
  conversations: Array.from({ length: 24 }, (_, index) => ({
    id: `layout-conversation-${index + 1}`, title: `Layout conversation ${index + 1}`,
    modelId: model.id, createdAt: 1, updatedAt: 100 - index, messages: [],
  })),
})
useCommandMenu.getState().setOpen(mode === 'command')
if (mode === 'settings') useSettingsModal.getState().openSettings(query.get('tab') ?? 'appearance')

function rectangle(node: Element | Range | null) {
  if (!node) return null
  const box = node.getBoundingClientRect()
  return { x: box.x, y: box.y, width: box.width, height: box.height, top: box.top, right: box.right, bottom: box.bottom, left: box.left }
}

function officeElements() {
  const panel = document.querySelector<HTMLElement>('[data-slot="sheet-content"][data-state="open"], #chat-side-panel[data-state="open"]')
  const fileBody = panel?.querySelector('header')?.parentElement ?? null
  const clip = fileBody?.children.item(1) instanceof HTMLElement ? fileBody.children.item(1) as HTMLElement : null
  const preview = clip?.firstElementChild instanceof HTMLElement ? clip.firstElementChild : null
  const alert = clip?.querySelector<HTMLElement>('[role="alert"]') ?? null
  const scroller = alert ?? clip?.querySelector<HTMLElement>('[role="document"], [role="tabpanel"], pre') ?? null
  return { panel, fileBody, clip, preview, scroller, alert }
}

function officeReady() {
  const { clip, scroller, alert } = officeElements()
  if (alert) return true
  if (activeFile === 'xlsx' || activeFile === 'long-xlsx') return Boolean(clip?.querySelector('[role="tabpanel"] table'))
  if (activeFile === 'text') return Boolean(scroller?.tagName === 'PRE' && scroller.textContent?.includes(TEXT_END))
  return Boolean(scroller?.getAttribute('aria-busy') === 'false' && scroller.children.length)
}

function probeOffice() {
  const { panel, fileBody, clip, preview, scroller, alert } = officeElements()
  const clipBox = rectangle(clip)
  const scrollerBox = rectangle(scroller)
  const finalText = scroller?.tagName === 'PRE' && scroller.firstChild?.nodeType === Node.TEXT_NODE
    ? document.createRange()
    : null
  if (finalText && scroller?.firstChild) {
    const length = scroller.firstChild.textContent?.length ?? 0
    finalText.setStart(scroller.firstChild, Math.max(0, length - TEXT_END.length))
    finalText.setEnd(scroller.firstChild, length)
  }
  return {
    file: activeFile, ready: officeReady(), errors: [...errors],
    viewport: { width: window.innerWidth, height: window.innerHeight, surface: readSurfaceViewport() },
    panel: rectangle(panel), fileBody: rectangle(fileBody), clip: clipBox,
    preview: rectangle(preview), scroller: scrollerBox,
    previewMinHeight: preview ? getComputedStyle(preview).minHeight : null,
    clipOverflow: clip ? getComputedStyle(clip).overflow : null,
    scrollTop: scroller?.scrollTop ?? null, scrollLeft: scroller?.scrollLeft ?? null,
    scrollHeight: scroller?.scrollHeight ?? null, scrollWidth: scroller?.scrollWidth ?? null,
    clientHeight: scroller?.clientHeight ?? null, clientWidth: scroller?.clientWidth ?? null,
    hiddenScrollerBottom: clipBox && scrollerBox ? Math.max(0, scrollerBox.bottom - clipBox.bottom) : null,
    finalContent: rectangle(finalText ?? scroller?.lastElementChild ?? null),
    tableLastRow: rectangle(scroller?.querySelector('tbody tr:last-child') ?? null),
    tableLastCell: rectangle(scroller?.querySelector('tbody tr:last-child td:last-child') ?? null),
    alert: alert?.textContent ?? null,
    textTail: scroller?.textContent?.slice(-160) ?? null,
  }
}

function nextPaint() {
  return new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
}

async function openOffice(file: OfficeFile) {
  if (!officeFiles.includes(file)) throw new Error(`Unknown Office fixture: ${file}`)
  activeFile = file
  const type = file === 'long-xlsx' ? 'xlsx' : file === 'invalid' ? 'docx' : file
  const mime = type === 'docx' ? DOCX_MIME : type === 'pptx' ? PPTX_MIME : type === 'xlsx' ? XLSX_MIME : 'text/plain'
  let name = `sample.${type}`
  let url = `/tests/fixtures/ooxml/${name}`
  let blob: Blob | null = null
  if (file === 'long-xlsx') {
    const response = await fetch('/tests/fixtures/ooxml/sample.xlsx')
    if (!response.ok) throw new Error(`Workbook fixture failed (${response.status})`)
    blob = await createLongSpreadsheetFixture(await response.arrayBuffer())
    name = 'long-sample.xlsx'
  } else if (file === 'invalid') {
    blob = new Blob(['Invalid OOXML fixture'], { type: DOCX_MIME })
    name = 'invalid.ooxml'
  } else if (file === 'text') {
    blob = new Blob([Array.from({ length: 239 }, (_, index) => `Text row ${index + 1}: ${'Local fixture content. '.repeat(8)}`).join('\n') + `\n${TEXT_END}`], { type: 'text/plain' })
    name = 'long-sample.txt'
  }
  if (blob) {
    url = URL.createObjectURL(blob)
    objectUrls.add(url)
  }
  useArtifactPanel.getState().openArtifact({
    type: 'file', name, url, kind: type === 'xlsx' ? 'sheet' : type === 'text' ? 'text' : 'doc',
    backendKind: mime, authenticated: false,
  })
  await nextPaint()
}

function ready() {
  if (!mounted || !fontsReady) return false
  if (mode === 'office') return officeReady()
  if (mode === 'password') return Boolean(document.querySelector('[data-password-gate]'))
  if (mode === 'welcome') return Boolean(document.querySelector('[data-slot="dialog-content"][data-state="open"]'))
  if (mode === 'filters') return Boolean(document.querySelector('[data-filter-fixture] button'))
  if (mode === 'command') return Boolean(document.querySelector('[cmdk-input]'))
  if (mode === 'formula') return Boolean(document.querySelector('.formula-editor-dialog math-field, .formula-editor-retry'))
  if (mode === 'composer') return Boolean(document.querySelector('[contenteditable="true"]'))
  if (mode === 'settings') return Boolean(document.querySelector('[role="dialog"] section'))
  if (mode === 'conversations') return Boolean(document.querySelector('[data-conversation-view]:not([aria-busy="true"])'))
  if (mode === 'mask') return Boolean(document.querySelector('[role="dialog"] canvas'))
  if (mode === 'workspace') return Boolean(document.querySelector('[data-workspace-management-scroll] li'))
  if (mode === 'template') return Boolean(document.querySelector('[data-template-list] button, [data-template-error]'))
  if (mode === 'select') return Boolean(document.querySelector('[data-slot="select-trigger"]'))
  return Boolean(document.querySelector('[data-slot="sheet-content"], [data-slot="dialog-content"]'))
}

const audit = {
  mode,
  get file() { return activeFile },
  get selectedChoice() { return selectedChoice },
  get ready() { return ready() },
  get officeReady() { return officeReady() },
  errors,
  fixtures: { user, model, skills, prompts, knowledgeBases, archivedConversations, workspaceMembers, imageDataUrl, textEnd: TEXT_END, lastRow: LONG_SPREADSHEET_LAST_ROW, lastColumn: LONG_SPREADSHEET_LAST_COLUMN },
  stores: { auth: useAuth, settings: useSettings, settingsModal: useSettingsModal, artifacts: useArtifactPanel, command: useCommandMenu, language: useLanguage, theme: useTheme, accent: useAccent, workspaces: useWorkspaces, conversations: useConversations, projects: useProjects, models: useModels },
  setViewport(value: Partial<SimulatedViewport>) {
    Object.assign(viewportOverrides, value)
    simulatedViewport.dispatchEvent(new Event('resize'))
    simulatedViewport.dispatchEvent(new Event('scroll'))
    return nextPaint()
  },
  resetViewport() {
    for (const key of Object.keys(viewportOverrides) as (keyof SimulatedViewport)[]) delete viewportOverrides[key]
    simulatedViewport.dispatchEvent(new Event('resize'))
    return nextPaint()
  },
  setSafeInsets(value: Partial<SafeAreaInsets>) {
    readSurfaceViewport()
    const probe = document.querySelector<HTMLElement>('[data-app-safe-area]')
    if (!probe) throw new Error('Safe-area probe missing')
    const style = getComputedStyle(probe)
    for (const side of ['top', 'right', 'bottom', 'left'] as const) {
      const padding = `padding-${side}`
      probe.style.setProperty(padding, `${value[side] ?? (parseFloat(style.getPropertyValue(padding)) || 0)}px`)
    }
    window.dispatchEvent(new Event('resize'))
    return nextPaint()
  },
  openOffice,
  probeOffice,
  async scrollOfficeToEnd() {
    const { scroller } = officeElements()
    if (!scroller) throw new Error('Office preview is not rendered')
    scroller.scrollTop = scroller.scrollHeight
    scroller.scrollLeft = scroller.scrollWidth
    await nextPaint()
    return probeOffice()
  },
  confirmedFormula: null as string | null,
  submitted: null as unknown,
}
Reflect.set(window, '__MOBILE_LAYOUT__', audit)
window.addEventListener('error', (event) => errors.push(event.message))
window.addEventListener('unhandledrejection', (event) => errors.push(String(event.reason)))
window.addEventListener('pagehide', () => {
  for (const url of objectUrls) URL.revokeObjectURL(url)
})

function SheetFixture() {
  return <Sheet open>
    <SheetContent side="right" size="lg" label="Details">
      <SheetHeader className="flex items-center justify-between gap-3">
        <SheetTitle>Details</SheetTitle>
        <SheetClose asChild><Button variant="ghost" size="icon" aria-label="Close"><X size={16} /></Button></SheetClose>
      </SheetHeader>
      <SheetBody>{Array.from({ length: 30 }, (_, index) => <p key={index} className="py-2">Detail row {index + 1}</p>)}</SheetBody>
      <SheetFooter><Button>Done</Button></SheetFooter>
    </SheetContent>
  </Sheet>
}

function FullscreenFixture() {
  return <Dialog open>
    <DialogContent mobileFullscreen scrollMode="body" data-mobile-fullscreen-fixture>
      <DialogHeader><DialogTitle>Workspace details</DialogTitle></DialogHeader>
      <DialogBody>{Array.from({ length: 3 }, (_, index) => <p key={index} className="py-3">Section {index + 1}: {'Workspace content. '.repeat(22)}</p>)}</DialogBody>
      <DialogFooter><Button variant="secondary">Cancel</Button><Button>Save</Button></DialogFooter>
    </DialogContent>
  </Dialog>
}

function FiltersFixture() {
  const [fileType, setFileType] = useState<FileTypeFilter>('all')
  const [origin, setOrigin] = useState('all')
  const [sort, setSort] = useState('created_at')
  const [order, setOrder] = useState<'desc' | 'asc'>('desc')
  const [owner, setOwner] = useState('')
  return <div data-filter-fixture className="flex justify-end p-4">
    <FileFiltersPopover fileType={fileType} onFileTypeChange={setFileType} origin={origin} onOriginChange={setOrigin}
      sort={sort} order={order} onSortChange={(nextSort, nextOrder) => { setSort(nextSort); setOrder(nextOrder) }}
      activeCount={owner ? 1 : 0} onReset={() => { setFileType('all'); setOrigin('all'); setSort('created_at'); setOrder('desc'); setOwner('') }}
      ownerSearch={{ value: owner, onChange: setOwner, label: 'Owner filter', placeholder: 'Owner' }} />
  </div>
}

function FormulaFixture() {
  const [open, setOpen] = useState(true)
  return <FormulaEditorDialog open={open} onOpenChange={setOpen} initialLatex={String.raw`x=\frac{-b\pm\sqrt{b^2-4ac}}{2a}`}
    onConfirm={(latex) => { audit.confirmedFormula = latex }} />
}

function ComposerFixture() {
  const [kbIds, setKBIds] = useState<string[]>([])
  return <main className="flex min-h-0 min-w-0 flex-1 flex-col justify-end p-3">
    <Composer modelId={model.id} onModelChange={() => {}} commandsEnabled conversationId="mobile-layout-conversation"
      kbIds={kbIds} onKBChange={setKBIds} initialValue={query.get('input') ?? '/'}
      onSubmit={(text, attachments, options) => { audit.submitted = { text, attachments, options } }} />
  </main>
}

function MaskFixture() {
  const [open, setOpen] = useState(true)
  return open ? <ImageMaskEditor modelLabel="Fixture image model" onClose={() => setOpen(false)}
    image={{ id: 'mobile-layout-image', filename: 'layout-image.png', url: imageDataUrl, mimeType: 'image/png' }}
    onSubmit={async (prompt, mask) => { audit.submitted = { prompt, maskBytes: mask.size } }} /> : null
}

function TemplateFixture() {
  const [selectedId, setSelectedId] = useState<string | null>(null)
  return <Dialog open><DialogContent size="full" scrollMode="body" data-preview-dialog className="h-[min(44rem,calc(100dvh-3rem))]">
    <DialogHeader><DialogTitle>PPT template fixture</DialogTitle></DialogHeader>
    <DialogBody className="overflow-hidden"><TemplatePicker selectedId={selectedId} onSelect={(template) => setSelectedId(template.id)} /></DialogBody>
    <DialogFooter><Button>Confirm template</Button></DialogFooter>
  </DialogContent></Dialog>
}

function SelectFixture() {
  const [value, setValue] = useState('choice-1')
  return <div className="w-full max-w-sm p-4">
    <label className="mb-2 block text-sm font-medium" id="mobile-choice-label">Long model choice</label>
    <Select value={value} onValueChange={(next) => { setValue(next); selectedChoice = next }}>
      <SelectTrigger aria-labelledby="mobile-choice-label"><SelectValue /></SelectTrigger>
      <SelectContent>
        {Array.from({ length: 24 }, (_, index) => <SelectItem key={index} value={`choice-${index + 1}`}>Model {index + 1}: {'A very long provider and deployment name '.repeat(2)}</SelectItem>)}
      </SelectContent>
    </Select>
  </div>
}

function Harness() {
  useAppViewport()
  useEffect(() => {
    mounted = true
    if (mode === 'office') void openOffice(activeFile).catch((error: unknown) => errors.push(String(error)))
    return () => { mounted = false }
  }, [])
  return <div className="app-viewport flex min-h-0 min-w-0 flex-col overflow-hidden" data-mobile-layout-mode={mode}>
    {mode === 'sheet' ? <SheetFixture /> : null}
    {mode === 'password' ? <SetPasswordGate /> : null}
    {mode === 'command' ? <CommandMenu /> : null}
    {mode === 'filters' ? <FiltersFixture /> : null}
    {mode === 'welcome' ? <WelcomeCard /> : null}
    {mode === 'fullscreen' ? <FullscreenFixture /> : null}
    {mode === 'formula' ? <FormulaFixture /> : null}
    {mode === 'settings' ? <SettingsLayout /> : null}
    {mode === 'conversations' ? <ConversationManagementDialog open mode="archived" onOpenChange={() => {}} /> : null}
    {mode === 'mask' ? <MaskFixture /> : null}
    {mode === 'workspace' ? <WorkspaceMembersDialog open onOpenChange={() => {}} /> : null}
    {mode === 'template' ? <TemplateFixture /> : null}
    {mode === 'select' ? <SelectFixture /> : null}
    {mode === 'office' ? <div className="flex min-h-0 min-w-0 flex-1 overflow-hidden"><main className="min-h-0 min-w-0 flex-1" aria-label="Conversation" /><ArtifactPanel /></div> : null}
    {mode === 'composer' ? <ComposerFixture /> : null}
  </div>
}

const root = document.getElementById('root')
if (!root) throw new Error('Missing mobile layout harness root')
await i18n.changeLanguage(query.get('lang') ?? 'zh')
await Promise.all(['Geist Variable', 'Geist Mono Variable', 'Fraunces Variable'].map((family) => document.fonts.load(`400 16px "${family}"`)))
await document.fonts.ready
fontsReady = true
createRoot(root).render(<TooltipProvider><MemoryRouter initialEntries={['/chat/mobile-layout']}><Harness /></MemoryRouter></TooltipProvider>)
