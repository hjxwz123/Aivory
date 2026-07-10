/**
 * Projects store — backed by the Go backend. The local Project shape mirrors
 * `@/types/project.Project` so the existing UI (ProjectsList, ProjectDetail,
 * sidebar, command menu) keeps working without changes.
 *
 * Documents inside a project come from the project's knowledge base; we expose
 * them as `files` on Project to match the existing UI's expectations.
 */
import { create } from 'zustand'
import { ApiError, projectsApi } from '@/api'
import { activeWorkspaceId } from '@/store/workspaces'
import type { ApiDocument, ApiProject } from '@/api/types'
import type { UploadProgress } from '@/api/client'
import type { Project, ProjectAccent, ProjectFile, ProjectFileKind } from '@/types/project'
import { toast } from '@/hooks/use-toast'

interface ProjectStore {
  projects: Project[]
  loaded: boolean
  loading: boolean
  error: string | null

  load: () => Promise<void>
  loadOne: (id: string) => Promise<Project | undefined>

  createProject: (init?: Partial<Pick<Project, 'name' | 'description' | 'instructions' | 'accent' | 'emoji'>>) => Promise<Project | null>
  renameProject: (id: string, name: string) => Promise<void>
  updateProject: (id: string, patch: Partial<Pick<Project, 'name' | 'description' | 'instructions' | 'accent' | 'emoji' | 'autoAddUploads'>>) => Promise<void>
  togglePin: (id: string) => Promise<void>
  deleteProject: (id: string) => Promise<void>

  addFile: (id: string, file: Omit<ProjectFile, 'id' | 'addedAt'> & { content?: string }) => Promise<ProjectFile | null>
  /** Upload a real file (multipart) into the project library. */
  uploadFile: (
    id: string,
    file: File,
    opts?: { onProgress?: (progress: UploadProgress) => void },
  ) => Promise<ProjectFile | null>
  removeFile: (id: string, fileId: string) => Promise<void>
  renameFile: (id: string, fileId: string, name: string) => Promise<void>

  getProject: (id: string) => Project | undefined
}

const ACCENT_FALLBACK: ProjectAccent = 'violet'

// §workspaces: every load belongs to the space it was ISSUED for (same epoch
// pattern as the conversations store) — a switch mid-flight bumps the epoch so
// a stale response can't overwrite the new space's list, and a fresh load is
// never silently skipped because an older one is still in flight.
let projLoadEpoch = 0

export const useProjects = create<ProjectStore>((set, get) => ({
  projects: [],
  loaded: false,
  loading: false,
  error: null,

  async load() {
    const epoch = ++projLoadEpoch
    set({ loading: true, error: null })
    try {
      const rows = await projectsApi.list(activeWorkspaceId())
      const projects = await Promise.all(rows.map(async (p) => toLocalProject(p, [])))
      if (epoch !== projLoadEpoch) return // superseded by a workspace switch
      set({ projects, loaded: true, loading: false })
    } catch (e) {
      if (epoch !== projLoadEpoch) return
      set({ error: errorMessage(e, 'Failed to load projects'), loading: false })
    }
  },

  async loadOne(id) {
    try {
      const resp = await projectsApi.get(id)
      const project = await toLocalProject(resp.project, resp.documents)
      set((s) => ({
        projects: replaceOrPrepend(s.projects, project),
      }))
      return project
    } catch {
      return undefined
    }
  },

  async createProject(init = {}) {
    try {
      const created = await projectsApi.create({
        workspace_id: activeWorkspaceId(),
        name: init.name?.trim() ?? '',
        description: init.description ?? '',
        instructions: init.instructions ?? '',
        accent: (init.accent as ApiProject['accent']) ?? ACCENT_FALLBACK,
        emoji: init.emoji ?? '',
      })
      const project = await toLocalProject(created, [])
      set((s) => ({ projects: [project, ...s.projects] }))
      return project
    } catch (e) {
      set({ error: errorMessage(e) })
      return null
    }
  },

  async renameProject(id, name) {
    const trimmed = name.trim()
    if (!trimmed) return
    const prev = get().projects.find((p) => p.id === id)
    set((s) => ({
      projects: s.projects.map((p) => (p.id === id ? { ...p, name: trimmed, updatedAt: Date.now() } : p)),
    }))
    try {
      await projectsApi.update(id, { name: trimmed })
    } catch (e) {
      // Roll back the optimistic name and surface the failure.
      if (prev) set((s) => ({ projects: s.projects.map((p) => (p.id === id ? prev : p)) }))
      toast.error(errorMessage(e, 'Failed to rename project'))
    }
  },

  async updateProject(id, patch) {
    const prev = get().projects.find((p) => p.id === id)
    set((s) => ({
      projects: s.projects.map((p) => (p.id === id ? { ...p, ...patch, updatedAt: Date.now() } : p)),
    }))
    try {
      await projectsApi.update(id, toApiPatch(patch))
    } catch (e) {
      if (prev) set((s) => ({ projects: s.projects.map((p) => (p.id === id ? prev : p)) }))
      toast.error(errorMessage(e, 'Failed to update project'))
    }
  },

  async togglePin(id) {
    const target = get().projects.find((p) => p.id === id)
    const next = !target?.pinned
    set((s) => ({
      projects: s.projects.map((p) => (p.id === id ? { ...p, pinned: next } : p)),
    }))
    try {
      await projectsApi.update(id, { pinned: next })
    } catch (e) {
      // Roll back the toggle on failure.
      set((s) => ({ projects: s.projects.map((p) => (p.id === id ? { ...p, pinned: !next } : p)) }))
      toast.error(errorMessage(e, 'Failed to update pin'))
    }
  },

  async deleteProject(id) {
    const prev = get().projects
    set((s) => ({ projects: s.projects.filter((p) => p.id !== id) }))
    try {
      await projectsApi.remove(id)
    } catch (e) {
      // Restore the removed project so the UI doesn't lie about the delete.
      set({ projects: prev })
      toast.error(errorMessage(e, 'Failed to delete project'))
    }
  },

  async addFile(id, file) {
    try {
      const doc = await projectsApi.addDoc(id, {
        filename: file.name,
        content: file.content ?? `# ${file.name}\n\n${file.excerpt ?? ''}`,
        mime_type: 'text/markdown',
      })
      const f: ProjectFile = {
        id: doc.id,
        name: doc.filename,
        kind: kindFromMime(doc.mime_type, doc.filename),
        size: doc.size_bytes,
        addedAt: doc.created_at * 1000,
        excerpt: file.excerpt,
      }
      set((s) => ({
        projects: s.projects.map((p) =>
          p.id === id ? { ...p, files: [f, ...p.files], updatedAt: Date.now() } : p,
        ),
      }))
      return f
    } catch {
      return null
    }
  },

  async uploadFile(id, file, opts = {}) {
    try {
      const doc = await projectsApi.uploadDoc(id, file, opts)
      const f = toLocalFile(doc)
      set((s) => ({
        projects: s.projects.map((p) =>
          p.id === id ? { ...p, files: [f, ...p.files], updatedAt: Date.now() } : p,
        ),
      }))
      return f
    } catch {
      return null
    }
  },

  async removeFile(id, fileId) {
    const prev = get().projects.find((p) => p.id === id)
    set((s) => ({
      projects: s.projects.map((p) =>
        p.id === id ? { ...p, files: p.files.filter((f) => f.id !== fileId), updatedAt: Date.now() } : p,
      ),
    }))
    try {
      await projectsApi.removeDoc(id, fileId)
    } catch (e) {
      if (prev) set((s) => ({ projects: s.projects.map((p) => (p.id === id ? prev : p)) }))
      toast.error(errorMessage(e, 'Failed to remove file'))
    }
  },

  async renameFile(id, fileId, name) {
    const trimmed = name.trim()
    if (!trimmed) return
    const prev = get().projects.find((p) => p.id === id)
    set((s) => ({
      projects: s.projects.map((p) =>
        p.id === id
          ? {
              ...p,
              files: p.files.map((f) => (f.id === fileId ? { ...f, name: trimmed } : f)),
              updatedAt: Date.now(),
            }
          : p,
      ),
    }))
    try {
      await projectsApi.renameDoc(id, fileId, trimmed)
    } catch (e) {
      if (prev) set((s) => ({ projects: s.projects.map((p) => (p.id === id ? prev : p)) }))
      toast.error(errorMessage(e, 'Failed to rename file'))
    }
  },

  getProject(id) {
    return get().projects.find((p) => p.id === id)
  },
}))

async function toLocalProject(p: ApiProject, docs: ApiDocument[]): Promise<Project> {
  return {
    id: p.id,
    name: p.name,
    description: p.description,
    instructions: p.instructions,
    files: docs.map(toLocalFile),
    accent: (p.accent as ProjectAccent) || ACCENT_FALLBACK,
    emoji: p.emoji || undefined,
    autoAddUploads: p.auto_add_uploads,
    pinned: p.pinned,
    createdAt: p.created_at * 1000,
    updatedAt: p.updated_at * 1000,
  }
}

/** Translate the local camelCase patch into the backend's snake_case wire shape. */
function toApiPatch(
  patch: Partial<Pick<Project, 'name' | 'description' | 'instructions' | 'accent' | 'emoji' | 'autoAddUploads'>>,
): Partial<ApiProject> {
  const { autoAddUploads, ...rest } = patch
  const out: Partial<ApiProject> = { ...(rest as Partial<ApiProject>) }
  if (autoAddUploads !== undefined) out.auto_add_uploads = autoAddUploads
  return out
}

function toLocalFile(d: ApiDocument): ProjectFile {
  return {
    id: d.id,
    name: d.filename,
    kind: kindFromMime(d.mime_type, d.filename),
    size: d.size_bytes,
    addedAt: d.created_at * 1000,
  }
}

function kindFromMime(mime: string, name: string): ProjectFileKind {
  const ext = name.toLowerCase().split('.').pop() ?? ''
  if (mime.startsWith('image/') || ['png', 'jpg', 'jpeg', 'gif', 'webp'].includes(ext)) return 'image'
  if (mime === 'application/pdf' || ext === 'pdf') return 'pdf'
  if (['csv', 'xlsx', 'xls'].includes(ext)) return 'sheet'
  if (['docx', 'doc'].includes(ext)) return 'doc'
  if (['md', 'markdown', 'txt', 'log'].includes(ext)) return 'text'
  if (['go', 'ts', 'tsx', 'js', 'jsx', 'py', 'rs', 'java', 'kt', 'swift'].includes(ext)) return 'code'
  return 'other'
}

function replaceOrPrepend(list: Project[], next: Project): Project[] {
  const idx = list.findIndex((p) => p.id === next.id)
  if (idx < 0) return [next, ...list]
  const out = list.slice()
  out[idx] = next
  return out
}

function errorMessage(e: unknown, fallback = 'Something went wrong'): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return fallback
}
