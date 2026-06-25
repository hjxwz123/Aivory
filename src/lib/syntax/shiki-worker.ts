import { createHighlighterCore, type HighlighterCore, type LanguageRegistration } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import { aureliaShikiTheme, SHIKI_THEME } from './shiki-theme'

interface ShikiWorkerRequest {
  id: string
  code: string
  lang: string
}

interface ShikiWorkerSuccess {
  id: string
  ok: true
  html: string
  lang: string
}

interface ShikiWorkerFailure {
  id: string
  ok: false
  error: string
  reason: 'unsupported_language' | 'highlight_failed' | 'init_failed'
}

type LanguageModule = { default: LanguageRegistration[] | LanguageRegistration }

const languageModules = import.meta.glob<LanguageModule>(
  '/node_modules/shiki/dist/langs/{html,xml,css,scss,sass,less,postcss,javascript,jsx,typescript,tsx,vue,svelte,astro,angular-html,angular-ts,mdx,python,go,rust,java,kotlin,scala,c,cpp,csharp,fsharp,swift,objective-c,objective-cpp,php,ruby,perl,lua,dart,elixir,erlang,clojure,haskell,ocaml,zig,nim,crystal,v,vala,pascal,fortran-free-form,bash,fish,powershell,batch,docker,make,cmake,nginx,apache,terraform,hcl,bicep,nix,puppet,ssh-config,dotenv,ini,properties,json,jsonc,json5,jsonl,yaml,toml,csv,graphql,protobuf,http,prisma,sql,plsql,kusto,cypher,sparql,markdown,asciidoc,rst,latex,bibtex,typst,mermaid,qml,vb,asm,wasm,verilog,system-verilog,vhdl,glsl,hlsl,wgsl,shaderlab,r,julia,matlab,wolfram,scheme,racket,lisp,common-lisp,prolog,coq,lean,elm,purescript,fennel,solidity,vyper,move,cadence,clarity,diff,git-commit,git-rebase,log,regexp}.mjs',
)
const loadedLanguages = new Set<string>()

let highlighterPromise: Promise<HighlighterCore> | null = null

function languagePath(lang: string): string {
  return `/node_modules/shiki/dist/langs/${lang}.mjs`
}

async function getHighlighter(): Promise<HighlighterCore> {
  highlighterPromise ??= createHighlighterCore({
    themes: [aureliaShikiTheme],
    langs: [],
    engine: createJavaScriptRegexEngine(),
    warnings: false,
  })
  return highlighterPromise
}

async function loadLanguage(highlighter: HighlighterCore, lang: string): Promise<boolean> {
  if (lang === 'text' || lang === 'plain' || lang === 'ansi' || loadedLanguages.has(lang)) return true

  const loader = languageModules[languagePath(lang)]
  if (!loader) return false

  const mod = await loader()
  await highlighter.loadLanguage(mod.default)
  loadedLanguages.add(lang)
  return true
}

self.onmessage = (event: MessageEvent<ShikiWorkerRequest>) => {
  void handleRequest(event.data)
}

async function handleRequest(req: ShikiWorkerRequest) {
  try {
    const highlighter = await getHighlighter()
    const ok = await loadLanguage(highlighter, req.lang)
    if (!ok) {
      postFailure(req.id, 'unsupported_language', `Unsupported language: ${req.lang}`)
      return
    }

    const html = highlighter.codeToHtml(req.code, {
      lang: req.lang,
      theme: SHIKI_THEME,
      tabindex: false,
      tokenizeTimeLimit: 500,
      tokenizeMaxLineLength: 2000,
    })
    const msg: ShikiWorkerSuccess = { id: req.id, ok: true, html, lang: req.lang }
    self.postMessage(msg)
  } catch (err) {
    postFailure(req.id, 'highlight_failed', err instanceof Error ? err.message : 'Shiki highlight failed')
  }
}

function postFailure(id: string, reason: ShikiWorkerFailure['reason'], error: string) {
  const msg: ShikiWorkerFailure = { id, ok: false, reason, error }
  self.postMessage(msg)
}
