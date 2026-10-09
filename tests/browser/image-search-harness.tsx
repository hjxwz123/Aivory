import { createRoot } from 'react-dom/client'
import i18n from '@/i18n'
import '@/styles/globals.css'
import { CitationList } from '@/components/chat/citation'
import type { Citation } from '@/types/chat'

const image = (id: string, path = id): Citation => ({
  id, index: 1, title: `Kyoto temple · ${id}`, domain: 'source.example.test', source: 'web',
  url: 'https://source.example.test/gallery', imageUrl: `https://images.example.test/${path}.svg`,
  thumbnailUrl: `https://images.example.test/${path}-thumb.svg`,
})
const citations: Citation[] = [
  image('landscape'), image('portrait'), image('fallback'), image('missing'),
  { ...image('thumbnail-only'), imageUrl: undefined },
  image('duplicate', 'landscape'),
  { ...image('metadata-only'), imageDisplay: false },
  { ...image('unsafe'), imageUrl: 'javascript:alert(1)', thumbnailUrl: 'data:image/svg+xml,unsafe' },
].map((citation, index) => ({ ...citation, index: index + 1 }))
const search = new URLSearchParams(location.search)
await i18n.changeLanguage(search.get('locale') || 'en')
document.documentElement.classList.toggle('dark', search.has('dark'))
createRoot(document.getElementById('root')!).render(
  <main style={{ width: '100%', maxWidth: 760, padding: 16, margin: 'auto' }}>
    <p>Here are photos from the image search, with links to their original source pages.</p>
    <CitationList citations={citations} />
    <section data-case="ordinary"><CitationList citations={[{ ...image('ordinary-thumbnail'), imageDisplay: false }]} /></section>
  </main>,
)
