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
  renameProject: (id: string, name: string) => Promise<boolean>
  updateProject: (id: string, patch: Partial<Pick<Project, 'name' | 'description' | 'instructions' | 'accent' | 'emoji' | 'autoAddUploads'>>) => Promise<boolean>
  /** §workspace RBAC: flip a workspace project between shared and private. */
  setVisibility: (id: string, isPublic: boolean) => Promise<boolean>
  togglePin: (id: string) => Promise<void>
  deleteProject: (id: string) => Promise<boolean>

  addFile: (id: string, file: Omit<ProjectFile, 'id' | 'addedAt'> & { content?: string }) => Promise<ProjectFile | null>
  /** Upload a real file (multipart) into the project library. */
  uploadFile: (
    id: string,
    file: File,
    opts?: { onProgress?: (progress: UploadProgress) => void; signal?: AbortSignal },
  ) => Promise<ProjectFile | null>
  removeFile: (id: string, fileId: string) => Promise<boolean>
  renameFile: (id: string, fileId: string, name: string) => Promise<boolean>

  getProject: (id: string) => Project | undefined
}

const ACCENT_FALLBACK: ProjectAccent = 'violet'

// §workspaces: every load belongs to the space it was ISSUED for (same epoch
// pattern as the conversations store) — a switch mid-flight bumps the epoch so
// a stale response can't overwrite the new space's list, and a fresh load is
// never silently skipped because an older one is still in flight.
let projLoadEpoch = 0
const projectDetailEpoch = new Map<string, number>()

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
      const listed = await Promise.all(rows.map(async (p) => toLocalProject(p, [])))
      if (epoch !== projLoadEpoch) return // superseded by a workspace switch
      set((state) => ({
        projects: listed.map((project) => {
          const detailed = state.projects.find((item) => item.id === project.id)
          if (!detailed || detailed.canDelete === undefined) return project
          return {
            ...project,
            files: detailed.files,
            canUploadFiles: detailed.canUploadFiles,
            canDeleteContent: detailed.canDeleteContent,
            canDelete: detailed.canDelete,
          }
        }),
        loaded: true,
        loading: false,
      }))
    } catch (e) {
      if (epoch !== projLoadEpoch) return
      set({ error: errorMessage(e, 'Failed to load projects'), loading: false })
    }
  },

  async loadOne(id) {
    const requestEpoch = (projectDetailEpoch.get(id) ?? 0) + 1
    projectDetailEpoch.set(id, requestEpoch)
    try {
      const resp = await projectsApi.get(id)
      if (projectDetailEpoch.get(id) !== requestEpoch) return undefined
      const project = await toLocalProject(resp.project, resp.documents)
      set((s) => ({
        projects: replaceOrPrepend(s.projects, project),
      }))
      return project
    } catch (error) {
      if (projectDetailEpoch.get(id) !== requestEpoch) return undefined
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) {
        set((state) => ({ projects: state.projects.filter((project) => project.id !== id) }))
      }
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
    if (!trimmed) return false
    try {
      const updated = await projectsApi.update(id, { name: trimmed })
      set((s) => ({
        projects: s.projects.map((p) => (p.id === id ? mergeApiProject(p, updated) : p)),
      }))
      return true
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to rename project'))
      return false
    }
  },

  async updateProject(id, patch) {
    try {
      const updated = await projectsApi.update(id, toApiPatch(patch))
      set((s) => ({
        projects: s.projects.map((p) => (p.id === id ? mergeApiProject(p, updated) : p)),
      }))
      return true
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to update project'))
      return false
    }
  },

  /** §workspace RBAC: flip a workspace project between shared and private. */
  async setVisibility(id, isPublic) {
    try {
      const updated = await projectsApi.update(id, { is_public: isPublic })
      set((s) => ({
        projects: s.projects.map((p) => (p.id === id ? mergeApiProject(p, updated) : p)),
      }))
      return true
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to update project'))
      return false
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
    try {
      await projectsApi.remove(id)
      projectDetailEpoch.set(id, (projectDetailEpoch.get(id) ?? 0) + 1)
      set((s) => ({ projects: s.projects.filter((p) => p.id !== id) }))
      return true
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to delete project'))
      return false
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
    try {
      await projectsApi.removeDoc(id, fileId)
      set((s) => ({
        projects: s.projects.map((p) =>
          p.id === id ? { ...p, files: p.files.filter((f) => f.id !== fileId), updatedAt: Date.now() } : p,
        ),
      }))
      return true
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to remove file'))
      return false
    }
  },

  async renameFile(id, fileId, name) {
    const trimmed = name.trim()
    if (!trimmed) return false
    try {
      await projectsApi.renameDoc(id, fileId, trimmed)
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
      return true
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to rename file'))
      return false
    }
  },

  getProject(id) {
    return get().projects.find((p) => p.id === id)
  },
}))

async function toLocalProject(p: ApiProject, docs: ApiDocument[]): Promise<Project> {
  return {
    id: p.id,
    kbId: p.kb_id || undefined,
    name: p.name,
    description: p.description,
    instructions: p.instructions,
    files: docs.map(toLocalFile),
    accent: (p.accent as ProjectAccent) || ACCENT_FALLBACK,
    emoji: p.emoji || undefined,
    autoAddUploads: p.auto_add_uploads,
    pinned: p.pinned,
    workspaceId: p.workspace_id || undefined,
    userId: p.user_id,
    isPublic: p.is_public !== false,
    canUploadFiles: p.can_upload_files,
    canDeleteContent: p.can_delete_content,
    canDelete: p.can_delete,
    createdAt: p.created_at * 1000,
    updatedAt: p.updated_at * 1000,
  }
}

function mergeApiProject(project: Project, updated: ApiProject): Project {
  return {
    ...project,
    name: updated.name,
    description: updated.description,
    instructions: updated.instructions,
    accent: (updated.accent as ProjectAccent) || ACCENT_FALLBACK,
    emoji: updated.emoji || undefined,
    autoAddUploads: updated.auto_add_uploads,
    pinned: updated.pinned,
    isPublic: updated.is_public !== false,
    updatedAt: updated.updated_at * 1000,
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
