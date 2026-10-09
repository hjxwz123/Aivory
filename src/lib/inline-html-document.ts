import { MAX_GENERATIVE_SOURCE } from './generative-ui'

const contentCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'"
const wrapperCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-src blob: data:; connect-src 'none'; img-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
function js(value: unknown): string { return JSON.stringify(value).replace(/</g, '\\u003c') }

/** Never navigate away from a working preview for a partially streamed script. */
export function inlineHTMLSnapshot(code: string): string | null {
  if (code.length > MAX_GENERATIVE_SOURCE) return null
  // Comments must not look like unfinished script/style tags.
  const source = code.replace(/<!--[\s\S]*?(?:-->|$)/g, '')
  const rawTags = /<(script|style)\b[^>]*>[\s\S]*?(?:<\/\1\s*>|$)/gi
  for (const match of source.matchAll(rawTags)) {
    if (!new RegExp(`</${match[1]}\\s*>$`, 'i').test(match[0])) return null
  }
  if (/<[^>]*$/.test(source)) return null
  return code
}

/** Two opaque origins: the trusted wrapper also blocks the inner frame's navigation. */
export function buildInlineHTML(code: string, channel: string, theme: Record<string, string>): string {
  if (code.length > MAX_GENERATIVE_SOURCE) throw new Error('source too large')
  const doc = new DOMParser().parseFromString(code, 'text/html')
  // No refresh directives, alternate CSP, relative URL base, or native frames.
  doc.querySelectorAll('meta,base,iframe,frame,object,embed').forEach(el => el.remove())
  const css = Object.entries(theme).filter(([k]) => /^--[a-z-]+$/.test(k)).map(([k, v]) => `${k}:${v.replace(/[<>;{}]/g, '')}`).join(';')
  const bootstrap = `(()=>{const channel=${js(channel)};let pending=false;const report=()=>{pending=false;const body=document.body;const ready=Boolean(body?.innerText.trim()||Array.from(body?.querySelectorAll('svg,canvas,img,input,button,select,textarea')||[]).some(e=>e.getClientRects().length));parent.postMessage({channel,ready,height:Math.min(900,Math.max(120,body?.scrollHeight||120))},'*')};const schedule=()=>{if(!pending){pending=true;requestAnimationFrame(report)}};const fail=()=>parent.postMessage({channel,error:true},'*');addEventListener('error',fail);addEventListener('unhandledrejection',fail);addEventListener('DOMContentLoaded',()=>{new ResizeObserver(schedule).observe(document.body);new MutationObserver(schedule).observe(document.body,{childList:true,subtree:true,characterData:true,attributes:true});schedule()});addEventListener('load',schedule)})()`
  const inner = `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${contentCSP}"><meta name="referrer" content="no-referrer"><meta name="viewport" content="width=device-width,initial-scale=1"><style>:root{${css}}*{box-sizing:border-box}html{color-scheme:${theme['--scheme'] === 'dark' ? 'dark' : 'light'}}body{margin:0;background:var(--color-surface);color:var(--color-fg);font:14px/1.5 system-ui,sans-serif;overflow-wrap:anywhere}img,svg,canvas{max-width:100%}button,input,select,textarea{font:inherit}button{cursor:pointer} :focus-visible{outline:2px solid var(--color-accent);outline-offset:2px}@media(prefers-reduced-motion:reduce){*,*::before,*::after{animation:none!important;transition:none!important}}</style><script>${bootstrap}</script>${doc.head.innerHTML}</head><body>${doc.body.innerHTML}</body></html>`
  // The wrapper never runs generated code. Its frame-src excludes http(s), so
  // scripts cannot use self-navigation to bypass the content's connect-src.
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${wrapperCSP}"><meta name="referrer" content="no-referrer"><style>html,body{margin:0;overflow:hidden;background:${theme['--color-surface'] || 'transparent'}}iframe{display:block;width:100%;height:120px;border:0}</style></head><body><iframe title="Interactive response" sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe><script>const frame=document.querySelector('iframe');addEventListener('message',e=>{if(e.source!==frame.contentWindow||e.data?.channel!==${js(channel)})return;const data={channel:${js(channel)}};if(Number.isFinite(e.data.height)){data.height=Math.min(900,Math.max(120,e.data.height));frame.style.height=data.height+'px'}if(typeof e.data.ready==='boolean')data.ready=e.data.ready;if(e.data.error===true)data.error=true;parent.postMessage(data,'*')});frame.srcdoc=${js(inner)}</script></body></html>`
}
