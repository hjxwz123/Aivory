import { FileText, FileCode2, Image as ImageIcon, Link as LinkIcon, FileSpreadsheet, FileType, FileQuestion } from 'lucide-react'
import type { ProjectAccent, ProjectFileKind } from '@/types/project'

/**
 * Map an abstract project accent to concrete CSS variables. Surfaces a
 * background tint, a foreground accent color (used on chips and the
 * "▎" mark in cards) and a sharper bar color for hover states.
 *
 * All values resolve to tokens defined in `tokens.css`, so accents stay
 * coherent across light / dark.
 */
export interface AccentClasses {
  /** Tint background — used on the project card halo. */
  tint: string
  /** Strong accent — used on the leading bar / emoji bg. */
  bar: string
  /** Text class for the accent. */
  text: string
  /** Soft tinted background for chips. */
  chip: string
}

export const PROJECT_ACCENT_OPTIONS: ProjectAccent[] = ['violet', 'sage', 'amber', 'rose', 'slate', 'teal']

const ACCENT_MAP: Record<ProjectAccent, AccentClasses> = {
  violet: {
    tint: 'bg-project-violet-tint',
    bar: 'bg-project-violet-bar',
    text: 'text-project-violet-text',
    chip: 'bg-project-violet-tint text-project-violet-text',
  },
  sage: {
    tint: 'bg-project-sage-tint',
    bar: 'bg-project-sage-bar',
    text: 'text-project-sage-text',
    chip: 'bg-project-sage-tint text-project-sage-text',
  },
  amber: {
    tint: 'bg-project-amber-tint',
    bar: 'bg-project-amber-bar',
    text: 'text-project-amber-text',
    chip: 'bg-project-amber-tint text-project-amber-text',
  },
  rose: {
    tint: 'bg-project-rose-tint',
    bar: 'bg-project-rose-bar',
    text: 'text-project-rose-text',
    chip: 'bg-project-rose-tint text-project-rose-text',
  },
  slate: {
    tint: 'bg-project-slate-tint',
    bar: 'bg-project-slate-bar',
    text: 'text-project-slate-text',
    chip: 'bg-project-slate-tint text-project-slate-text',
  },
  teal: {
    tint: 'bg-project-teal-tint',
    bar: 'bg-project-teal-bar',
    text: 'text-project-teal-text',
    chip: 'bg-project-teal-tint text-project-teal-text',
  },
}

export function accentClasses(accent: ProjectAccent): AccentClasses {
  return ACCENT_MAP[accent] ?? ACCENT_MAP.violet
}

const FILE_KIND_ICONS = {
  pdf: FileText,
  doc: FileText,
  sheet: FileSpreadsheet,
  code: FileCode2,
  text: FileType,
  image: ImageIcon,
  link: LinkIcon,
  other: FileQuestion,
} as const

export function fileKindIcon(kind: ProjectFileKind) {
  return FILE_KIND_ICONS[kind] ?? FileQuestion
}

const KB = 1024
const MB = KB * 1024
export function formatFileSize(bytes: number): string {
  if (!bytes) return '—'
  if (bytes < KB) return `${bytes} B`
  if (bytes < MB) return `${(bytes / KB).toFixed(1)} KB`
  return `${(bytes / MB).toFixed(1)} MB`
}
