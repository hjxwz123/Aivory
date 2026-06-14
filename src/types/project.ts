/**
 * Project — a long-lived workspace that holds:
 *  • free-form "instructions" the assistant should follow whenever a
 *    conversation runs inside the project (sometimes called custom
 *    instructions or system prompt),
 *  • a small "knowledge" library of files the assistant should treat
 *    as authoritative context,
 *  • the set of conversations that have been started inside it.
 *
 * Mirrors the shape of ChatGPT Projects / Claude Projects / Gemini
 * Notebooks while staying backend-agnostic.
 */

export type ProjectFileKind = 'pdf' | 'doc' | 'sheet' | 'code' | 'text' | 'image' | 'link' | 'other'

export interface ProjectFile {
  id: string
  /** Display name including extension. */
  name: string
  kind: ProjectFileKind
  /** Approximate size in bytes; for display only. */
  size: number
  addedAt: number
  /** Optional remote URL (for kind === 'link'). */
  url?: string
  /** Mock preview snippet shown in the file detail. */
  excerpt?: string
}

export type ProjectAccent = 'violet' | 'sage' | 'amber' | 'rose' | 'slate' | 'teal'

export interface Project {
  id: string
  name: string
  description?: string
  /** Custom instructions applied to every chat in this project. */
  instructions: string
  files: ProjectFile[]
  accent: ProjectAccent
  /** Visual marker (one emoji or a single character). */
  emoji?: string
  /** When true, files uploaded inside a project chat are auto-added to the
   *  project's knowledge library (backend `auto_add_uploads`). */
  autoAddUploads?: boolean
  createdAt: number
  updatedAt: number
  pinned?: boolean
}
