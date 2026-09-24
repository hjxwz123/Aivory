import { describe, expect, it } from 'vitest'

import {
  AI_PPT_INPUT_TYPES,
  aiPPTOutlineStats,
  aiPPTOutlineSubject,
  aiPPTOutlineWarnings,
  aiPPTTypeKey,
  parseAiPPTHeadings,
} from '@/lib/aippt-outline'

const SAMPLE = `# AI 办公趋势

## 行业背景

### 机会与挑战

#### 关键观点

- 内容示例

## 产品规划

### 路线图

#### 里程碑

- 内容示例
`

describe('parseAiPPTHeadings', () => {
  it('reads the vendor Markdown skeleton in document order', () => {
    const headings = parseAiPPTHeadings(SAMPLE)
    expect(headings.map((h) => [h.level, h.text])).toEqual([
      [1, 'AI 办公趋势'],
      [2, '行业背景'],
      [3, '机会与挑战'],
      [4, '关键观点'],
      [2, '产品规划'],
      [3, '路线图'],
      [4, '里程碑'],
    ])
    expect(headings[0].line).toBe(1)
    expect(headings[3].line).toBe(7)
  })

  it('ignores hashes inside fenced code blocks and beyond level 4', () => {
    const markdown = ['# 主题', '```sh', '# not a heading', '```', '##### too deep', '## 章节'].join('\n')
    expect(parseAiPPTHeadings(markdown).map((h) => h.text)).toEqual(['主题', '章节'])
  })

  it('tolerates ATX closing hashes and empty headings', () => {
    expect(parseAiPPTHeadings('# 主题 ##\n###\n## 章节').map((h) => h.text)).toEqual(['主题', '章节'])
  })
})

describe('aiPPTOutlineWarnings', () => {
  it('accepts a well-formed outline', () => {
    expect(aiPPTOutlineWarnings(SAMPLE)).toEqual([])
  })

  it('flags the vendor rule violations the UI must warn about', () => {
    expect(aiPPTOutlineWarnings('## 只有章节')).toContain('missing-title')
    expect(aiPPTOutlineWarnings('# 一个\n# 两个\n## 章节\n### 页面')).toContain('multiple-titles')
    expect(aiPPTOutlineWarnings('# 只有标题')).toContain('missing-chapters')
    expect(aiPPTOutlineWarnings('# 标题\n## 章节')).toContain('missing-pages')
  })
})

describe('outline summaries', () => {
  it('counts chapters, pages and paragraphs', () => {
    expect(aiPPTOutlineStats(SAMPLE)).toEqual({ chapters: 2, pages: 2, paragraphs: 2 })
  })

  it('uses the first title as the deck name', () => {
    expect(aiPPTOutlineSubject(SAMPLE)).toBe('AI 办公趋势')
    expect(aiPPTOutlineSubject('## 没有标题')).toBe('')
  })
})

describe('input types', () => {
  it('exposes the five self-built inputs with stable i18n keys', () => {
    expect(AI_PPT_INPUT_TYPES).toEqual([1, 6, 5, 2, 7])
    expect(AI_PPT_INPUT_TYPES.map(aiPPTTypeKey)).toEqual(['topic', 'text', 'url', 'upload', 'markdown'])
  })
})
