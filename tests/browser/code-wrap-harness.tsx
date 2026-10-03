import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import i18n from '@/i18n'
import '@/styles/globals.css'
import Appearance from '@/pages/settings/Appearance'
import { CodeBlock } from '@/components/chat/code-block'
import { Markdown } from '@/components/chat/markdown'
import { PrivateMarkdown } from '@/components/chat/private-markdown'
import { ArtifactPanel } from '@/components/chat/artifact-panel'
import { TooltipProvider } from '@/components/ui/tooltip'
import { useSettings } from '@/store/settings'
import { useArtifactPanel } from '@/store/artifact-panel'

const original = `\tconst url = "https://example.com/${'very-long-unbroken-value'.repeat(35)}";\n  const chinese = "${'中文长字符串'.repeat(50)}";\n`
const appended = '\n\tconsole.log(url);\n'
const preview = '<!doctype html><html><body><h1>Wrapping preview</h1><script>parent.postMessage({codeWrapPreview:true},"*")</script></body></html>'

function Harness() {
  const [code, setCode] = useState(original)
  const [live, setLive] = useState(true)
  const [width, setWidth] = useState(880)
  useEffect(() => {
    Reflect.set(window, '__CODE_WRAP__', {
      original,
      appended,
      settings: useSettings,
      artifacts: useArtifactPanel,
      append: () => setCode(original + appended),
      finish: () => setLive(false),
      resize: (value: number) => setWidth(value),
    })
  }, [])
  return (
    <TooltipProvider>
      <MemoryRouter>
        <section data-case="settings"><Appearance /></section>
        <main data-case="blocks" style={{ width: '100%', maxWidth: width, margin: '0 auto' }}>
          <section data-case="main"><CodeBlock code={code} lang="javascript" live={live} previewKey="main" /></section>
          <section data-case="second"><CodeBlock code={original} lang="json" live /></section>
          <section data-case="nested"><Markdown content={`- nested\n\n  \`\`\`text\n  ${'long-nested-value'.repeat(70)}\n  \`\`\`\n\n> quote\n>\n> \`\`\`text\n> ${'long-quote-value'.repeat(70)}\n> \`\`\``} blockKeyPrefix="nested" /></section>
          <section data-case="private"><PrivateMarkdown text={`\`\`\`text\n${original}\`\`\``} /></section>
          <section data-case="html"><CodeBlock code={preview} lang="html" allowPublicShare previewKey="preview" /></section>
        </main>
        <ArtifactPanel />
      </MemoryRouter>
    </TooltipProvider>
  )
}

await i18n.changeLanguage('en')
const container = document.getElementById('root')
if (!container) throw new Error('Missing harness root')
createRoot(container).render(<Harness />)
