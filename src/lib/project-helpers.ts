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
    tint: 'bg-[oklch(94%_0.045_290)] dark:bg-[oklch(28%_0.090_290)]',
    bar: 'bg-[oklch(58%_0.225_290)] dark:bg-[oklch(72%_0.215_290)]',
    text: 'text-[oklch(38%_0.150_290)] dark:text-[oklch(82%_0.180_290)]',
    chip: 'bg-[oklch(94%_0.045_290)] text-[oklch(38%_0.150_290)] dark:bg-[oklch(28%_0.090_290)] dark:text-[oklch(82%_0.180_290)]',
  },
  sage: {
    tint: 'bg-[oklch(94%_0.035_150)] dark:bg-[oklch(28%_0.050_150)]',
    bar: 'bg-[oklch(60%_0.085_150)] dark:bg-[oklch(74%_0.105_150)]',
    text: 'text-[oklch(36%_0.070_150)] dark:text-[oklch(82%_0.090_150)]',
    chip: 'bg-[oklch(94%_0.035_150)] text-[oklch(36%_0.070_150)] dark:bg-[oklch(28%_0.050_150)] dark:text-[oklch(82%_0.090_150)]',
  },
  amber: {
    tint: 'bg-[oklch(95%_0.045_75)] dark:bg-[oklch(28%_0.060_75)]',
    bar: 'bg-[oklch(68%_0.150_75)] dark:bg-[oklch(78%_0.135_75)]',
    text: 'text-[oklch(38%_0.100_70)] dark:text-[oklch(84%_0.120_75)]',
    chip: 'bg-[oklch(95%_0.045_75)] text-[oklch(38%_0.100_70)] dark:bg-[oklch(28%_0.060_75)] dark:text-[oklch(84%_0.120_75)]',
  },
  rose: {
    tint: 'bg-[oklch(95%_0.045_18)] dark:bg-[oklch(28%_0.065_18)]',
    bar: 'bg-[oklch(62%_0.180_18)] dark:bg-[oklch(74%_0.170_18)]',
    text: 'text-[oklch(40%_0.140_18)] dark:text-[oklch(84%_0.150_18)]',
    chip: 'bg-[oklch(95%_0.045_18)] text-[oklch(40%_0.140_18)] dark:bg-[oklch(28%_0.065_18)] dark:text-[oklch(84%_0.150_18)]',
  },
  slate: {
    tint: 'bg-[oklch(93%_0.012_270)] dark:bg-[oklch(26%_0.025_275)]',
    bar: 'bg-[oklch(52%_0.040_270)] dark:bg-[oklch(70%_0.030_275)]',
    text: 'text-[oklch(32%_0.030_270)] dark:text-[oklch(84%_0.020_275)]',
    chip: 'bg-[oklch(93%_0.012_270)] text-[oklch(32%_0.030_270)] dark:bg-[oklch(26%_0.025_275)] dark:text-[oklch(84%_0.020_275)]',
  },
  teal: {
    tint: 'bg-[oklch(94%_0.040_200)] dark:bg-[oklch(26%_0.060_200)]',
    bar: 'bg-[oklch(60%_0.110_200)] dark:bg-[oklch(74%_0.110_200)]',
    text: 'text-[oklch(36%_0.080_200)] dark:text-[oklch(84%_0.100_200)]',
    chip: 'bg-[oklch(94%_0.040_200)] text-[oklch(36%_0.080_200)] dark:bg-[oklch(26%_0.060_200)] dark:text-[oklch(84%_0.100_200)]',
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
