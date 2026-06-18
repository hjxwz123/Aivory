/**
 * inline-highlight — wraps quoted excerpts inside a rendered message with
 * clickable <mark> markers for text-selection sub-conversations (§ inline
 * threads). Operates on the live DOM (not the markdown source) so it survives
 * inline formatting, and matches whitespace-insensitively because a browser
 * selection inserts newlines between block elements that DOM textContent omits.
 */

const MARK_CLASS = 'inline-thread-mark'

interface QuoteThread {
  id: string
  quote: string
}

/** Remove all existing thread markers from a container, restoring plain text. */
function unwrapMarks(container: HTMLElement): void {
  container.querySelectorAll(`mark.${MARK_CLASS}`).forEach((m) => {
    const parent = m.parentNode
    if (!parent) return
    while (m.firstChild) parent.insertBefore(m.firstChild, m)
    parent.removeChild(m)
  })
  container.normalize()
}

/** Collect the container's non-empty text nodes plus a cumulative-offset map so
 *  a char index maps back to node + offset. Code (`<code>`/`<pre>`) is included:
 *  users frequently select a snippet to ask about it, and per-text-node wrapping
 *  (below) only ever surrounds a sub-range of a single text node, so it can't
 *  corrupt the surrounding syntax-highlight spans. */
function collectTextNodes(container: HTMLElement): { node: Text; start: number; end: number }[] {
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, {
    acceptNode(n) {
      return n.nodeValue && n.nodeValue.length > 0 ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT
    },
  })
  const out: { node: Text; start: number; end: number }[] = []
  let offset = 0
  let cur = walker.nextNode()
  while (cur) {
    const len = cur.nodeValue?.length ?? 0
    out.push({ node: cur as Text, start: offset, end: offset + len })
    offset += len
    cur = walker.nextNode()
  }
  return out
}

/** Find [startFull, endFull) of a quote in the joined text, ignoring whitespace
 *  differences. Returns null when not found. */
function findRange(full: string, quote: string): { start: number; end: number } | null {
  const isWs = (c: string) => /\s/.test(c)
  // Build compact (whitespace-stripped) text + map back to full indices.
  const compactToFull: number[] = []
  let compact = ''
  for (let i = 0; i < full.length; i++) {
    if (!isWs(full[i])) {
      compact += full[i]
      compactToFull.push(i)
    }
  }
  const qCompact = quote.replace(/\s+/g, '')
  if (!qCompact) return null
  const ci = compact.indexOf(qCompact)
  if (ci < 0) return null
  return { start: compactToFull[ci], end: compactToFull[ci + qCompact.length - 1] + 1 }
}

/** Wrap the [start,end) char range (in joined-text coordinates) with one or
 *  more <mark> elements carrying the thread id. Processes nodes last→first so
 *  earlier node references stay valid as the DOM mutates. */
function wrapRange(
  nodes: { node: Text; start: number; end: number }[],
  start: number,
  end: number,
  threadId: string,
): void {
  for (let i = nodes.length - 1; i >= 0; i--) {
    const { node, start: ns, end: ne } = nodes[i]
    const from = Math.max(start, ns)
    const to = Math.min(end, ne)
    if (from >= to) continue
    const localStart = from - ns
    const localEnd = to - ns
    try {
      const range = document.createRange()
      range.setStart(node, localStart)
      range.setEnd(node, localEnd)
      const mark = document.createElement('mark')
      mark.className = MARK_CLASS
      mark.dataset.threadId = threadId
      range.surroundContents(mark)
    } catch {
      // A range that can't be cleanly surrounded is skipped — the thread stays
      // reachable from the drawer even without an inline marker.
    }
  }
}

/**
 * Re-highlight a message container with the given threads. Idempotent: clears
 * prior marks first, then wraps the first occurrence of each quote.
 */
export function highlightThreads(container: HTMLElement, threads: QuoteThread[]): void {
  unwrapMarks(container)
  if (threads.length === 0) return
  for (const th of threads) {
    if (!th.quote.trim()) continue
    const nodes = collectTextNodes(container)
    const full = nodes.map((n) => n.node.nodeValue ?? '').join('')
    const found = findRange(full, th.quote)
    if (found) wrapRange(nodes, found.start, found.end, th.id)
  }
}

/** True when the node is inside (or is) a thread marker; returns its thread id. */
export function threadIdForNode(node: Node | null): string | null {
  let el = node instanceof HTMLElement ? node : node?.parentElement ?? null
  while (el) {
    if (el.classList?.contains(MARK_CLASS)) return el.dataset.threadId ?? null
    el = el.parentElement
  }
  return null
}
