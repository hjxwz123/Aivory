import { MAX_GENERATIVE_SOURCE } from './generative-ui'

const contentCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; frame-src 'none'; object-src 'none'; worker-src 'none'; base-uri 'none'; form-action 'none'"
const wrapperCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-src blob: data:; connect-src 'none'; img-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
function js(value: unknown): string { return JSON.stringify(value).replace(/</g, '\\u003c') }

/** Two opaque origins: the trusted wrapper also blocks the inner frame's navigation. */
export function buildInlineHTML(code: string, channel: string, theme: Record<string, string>): string {
  if (code.length > MAX_GENERATIVE_SOURCE) throw new Error('source too large')
  const doc = new DOMParser().parseFromString(code, 'text/html')
  // No refresh directives, alternate CSP, relative URL base, or native frames.
  doc.querySelectorAll('meta,base,iframe,frame,object,embed').forEach(el => el.remove())
  const css = Object.entries(theme).filter(([k]) => /^--[a-z-]+$/.test(k)).map(([k, v]) => `${k}:${v.replace(/[<>;{}]/g, '')}`).join(';')
  const bootstrap = `(()=>{const channel=${js(channel)};let pending=false;const report=()=>{pending=false;parent.postMessage({channel,height:Math.min(900,Math.max(120,document.body?.scrollHeight||120))},'*')};const schedule=()=>{if(!pending){pending=true;requestAnimationFrame(report)}};addEventListener('DOMContentLoaded',()=>{new ResizeObserver(schedule).observe(document.body);schedule()});addEventListener('load',schedule)})()`
  const inner = `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${contentCSP}"><meta name="referrer" content="no-referrer"><meta name="viewport" content="width=device-width,initial-scale=1"><style>:root{${css}}*{box-sizing:border-box}html{color-scheme:${theme['--scheme'] === 'dark' ? 'dark' : 'light'}}body{margin:0;background:var(--color-surface);color:var(--color-fg);font:14px/1.5 system-ui,sans-serif;overflow-wrap:anywhere}img,svg,canvas{max-width:100%}button,input,select,textarea{font:inherit}button{cursor:pointer} :focus-visible{outline:2px solid var(--color-accent);outline-offset:2px}@media(prefers-reduced-motion:reduce){*,*::before,*::after{animation:none!important;transition:none!important}}</style><script>${bootstrap}</script>${doc.head.innerHTML}</head><body>${doc.body.innerHTML}</body></html>`
  // The wrapper never runs generated code. Its frame-src excludes http(s), so
  // scripts cannot use self-navigation to bypass the content's connect-src.
  return `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${wrapperCSP}"><meta name="referrer" content="no-referrer"><style>html,body{margin:0;overflow:hidden}iframe{display:block;width:100%;height:120px;border:0}</style></head><body><iframe title="Interactive response" sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe><script>const frame=document.querySelector('iframe');frame.srcdoc=${js(inner)};addEventListener('message',e=>{if(e.source!==frame.contentWindow||e.data?.channel!==${js(channel)}||!Number.isFinite(e.data.height))return;const height=Math.min(900,Math.max(120,e.data.height));frame.style.height=height+'px';parent.postMessage({channel:${js(channel)},height},'*')})</script></body></html>`
}
