import type { LucideIcon } from 'lucide-react'
import { Pen, FlaskConical, Code2, Library, Compass, Sparkles, BookOpen, Lightbulb } from 'lucide-react'

export interface Suggestion {
  id: string
  icon: LucideIcon
  /** i18n key relative to `chat:suggestions.<id>.title` */
  titleKey: string
  /** i18n key relative to `chat:suggestions.<id>.prompt` */
  promptKey: string
  category: 'write' | 'think' | 'build' | 'learn'
}

export const SUGGESTIONS: Suggestion[] = [
  {
    id: 'outline-essay',
    icon: Pen,
    titleKey: 'chat:suggestions.outline-essay.title',
    promptKey: 'chat:suggestions.outline-essay.prompt',
    category: 'write',
  },
  {
    id: 'study-plan',
    icon: BookOpen,
    titleKey: 'chat:suggestions.study-plan.title',
    promptKey: 'chat:suggestions.study-plan.prompt',
    category: 'learn',
  },
  {
    id: 'debug-snippet',
    icon: Code2,
    titleKey: 'chat:suggestions.debug-snippet.title',
    promptKey: 'chat:suggestions.debug-snippet.prompt',
    category: 'build',
  },
  {
    id: 'research-brief',
    icon: FlaskConical,
    titleKey: 'chat:suggestions.research-brief.title',
    promptKey: 'chat:suggestions.research-brief.prompt',
    category: 'think',
  },
  {
    id: 'travel-idea',
    icon: Compass,
    titleKey: 'chat:suggestions.travel-idea.title',
    promptKey: 'chat:suggestions.travel-idea.prompt',
    category: 'learn',
  },
  {
    id: 'creative-brief',
    icon: Sparkles,
    titleKey: 'chat:suggestions.creative-brief.title',
    promptKey: 'chat:suggestions.creative-brief.prompt',
    category: 'write',
  },
  {
    id: 'difficult-decision',
    icon: Lightbulb,
    titleKey: 'chat:suggestions.difficult-decision.title',
    promptKey: 'chat:suggestions.difficult-decision.prompt',
    category: 'think',
  },
  {
    id: 'library-curation',
    icon: Library,
    titleKey: 'chat:suggestions.library-curation.title',
    promptKey: 'chat:suggestions.library-curation.prompt',
    category: 'learn',
  },
]
