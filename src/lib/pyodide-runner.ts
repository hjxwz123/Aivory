/**
 * pyodide-runner — in-browser Python execution for assistant code blocks.
 *
 * Design notes:
 * - Pyodide (CPython → wasm) is loaded from the official jsDelivr CDN inside a
 *   dedicated **classic Web Worker** built from a Blob. The worker keeps the
 *   main thread responsive while user snippets run, and lets "Stop" actually
 *   work: we `terminate()` the worker, which is the only reliable way to kill
 *   synchronous Python (no SharedArrayBuffer interrupts without COOP/COEP).
 * - One shared worker (and one ~12 MB Pyodide boot) serves every code block;
 *   runs are serialized through a promise queue. After a terminate, the next
 *   run transparently re-boots the engine.
 * - Each snippet runs in a **fresh namespace** so re-running a block is
 *   deterministic and blocks don't leak variables into each other. Module
 *   state (`sys.modules`) intentionally stays warm — that's what makes a
 *   second `import numpy` instant.
 * - Matplotlib is forced onto the DOM-free AGG backend; finished figures are
 *   harvested as base64 PNGs and shown under the block.
 *
 * This executes *in the user's own browser sandbox* — it is unrelated to the
 * server-side Docker sandbox (`sandbox-service/`) used by backend tool calls.
 *
 * Threat model: snippets are MODEL OUTPUT — treat them as hostile. The worker
 * shares the app's origin, and our API authenticates with httpOnly cookies
 * (`credentials: 'include'`), so unrestricted `from js import fetch` would let
 * a snippet call `/api/*` as the signed-in user, or exfiltrate to any host.
 * The worker therefore locks its own global scope down before any user code
 * runs: fetch/importScripts are allow-listed to the Pyodide CDN origin only,
 * and XHR / WebSocket / EventSource / BroadcastChannel / indexedDB / caches
 * are removed. Wasm cannot reach the DOM, cookies, or localStorage from a
 * worker at all, so what remains is pure compute + CDN package downloads.
 */

import { envNum } from '@/lib/env-config'

const PYODIDE_VERSION = '0.28.3'
const PYODIDE_BASE = `https://cdn.jsdelivr.net/pyodide/v${PYODIDE_VERSION}/full/`
/** Hard wall-clock cap per run — mirrors the §4.5 sandbox exec ceiling. */
const RUN_TIMEOUT_MS = envNum('VITE_AURELIA_RUN_TIMEOUT_MS', 120_000)
/** Stop runaway `print` loops before they flood postMessage / the DOM. */
const MAX_STREAM_CHARS = envNum('VITE_AURELIA_MAX_STREAM_CHARS', 200_000)
/** Cap the repr() of the final expression (think 1M-row DataFrames). */
const MAX_RESULT_CHARS = envNum('VITE_AURELIA_MAX_RESULT_CHARS', 20_000)

export type PythonRunPhase = 'queued' | 'boot' | 'packages' | 'running'

export interface PythonStreamChunk {
  kind: 'stdout' | 'stderr'
  text: string
}

export interface PythonRunResult {
  ok: boolean
  /** repr() of the snippet's final expression, when it ends in one. */
  result?: string
  /** Python traceback / engine failure description. */
  error?: string
  /** Matplotlib figures produced during the run, as base64 PNGs. */
  images: string[]
  durationMs: number
  /** True when the user pressed Stop. */
  aborted?: boolean
  /** True when the run hit RUN_TIMEOUT_MS and the worker was killed. */
  timedOut?: boolean
  /** True when Pyodide itself could not be fetched (offline, CDN blocked). */
  engineFailed?: boolean
}

export interface PythonRunHooks {
  onPhase?: (phase: PythonRunPhase) => void
  onStream?: (chunk: PythonStreamChunk) => void
}

export interface PythonRunHandle {
  promise: Promise<PythonRunResult>
  cancel: () => void
}

type WorkerOutMessage =
  | { type: 'phase'; id: number; phase: 'boot' | 'packages' | 'running' }
  | { type: 'stream'; id: number; kind: 'stdout' | 'stderr'; text: string }
  | { type: 'done'; id: number; ok: boolean; result?: string; error?: string; images: string[] }

/**
 * The worker source ships as a string so no bundler config is needed and the
 * worker stays a *classic* worker (importScripts is the documented way to load
 * Pyodide off-thread). Template interpolations are resolved once, here.
 */
function buildWorkerSource(): string {
  return `'use strict';
importScripts('${PYODIDE_BASE}pyodide.js');

// ---- scope lockdown ------------------------------------------------------
// Runs before any user code can. The worker shares the app origin (httpOnly
// auth cookies!), so every network/storage capability is either allow-listed
// to the Pyodide CDN (engine + package downloads) or removed outright.
(function hardenScope() {
  var CDN_ORIGIN = new URL('${PYODIDE_BASE}').origin;
  var CDN_PREFIX = '${PYODIDE_BASE}';
  var realFetch = self.fetch.bind(self);
  var realImportScripts = self.importScripts.bind(self);

  self.fetch = function (input, init) {
    try {
      var raw = typeof input === 'string' ? input : input && input.url ? input.url : String(input);
      if (new URL(raw).origin === CDN_ORIGIN) return realFetch(input, init);
    } catch (e) { /* relative or malformed URL: fall through to rejection */ }
    return Promise.reject(new TypeError(
      'Aurelia sandbox: network access is disabled - only the Pyodide CDN is reachable.'));
  };
  // pyodide.js pulls pyodide.asm.js through importScripts in classic workers.
  self.importScripts = function () {
    for (var i = 0; i < arguments.length; i++) {
      if (String(arguments[i]).indexOf(CDN_PREFIX) !== 0) {
        throw new Error('Aurelia sandbox: importScripts is restricted to the Pyodide CDN.');
      }
    }
    return realImportScripts.apply(null, arguments);
  };

  var blocked = function (name) {
    return function () { throw new Error('Aurelia sandbox: ' + name + ' is disabled.'); };
  };
  var remove = function (name) {
    try { Object.defineProperty(self, name, { value: undefined, configurable: false }); }
    catch (e) { /* leave as-is if the platform refuses; fetch/XHR are the real vectors */ }
  };
  try { self.XMLHttpRequest = blocked('XMLHttpRequest'); } catch (e) {}
  try { self.WebSocket = blocked('WebSocket'); } catch (e) {}
  try { self.EventSource = blocked('EventSource'); } catch (e) {}
  try { self.BroadcastChannel = blocked('BroadcastChannel'); } catch (e) {}
  remove('indexedDB');
  remove('caches');
})();

var pyodideReady = null;

function boot() {
  if (!pyodideReady) {
    pyodideReady = self.loadPyodide({ indexURL: '${PYODIDE_BASE}' }).then(function (py) {
      // Force a DOM-free matplotlib backend before any user code imports it.
      py.runPython("import os\\nos.environ.setdefault('MPLBACKEND', 'AGG')");
      return py;
    });
  }
  return pyodideReady;
}

// Harvest open matplotlib figures as base64 PNGs, then close them so state
// never bleeds into the next run. Defined in the engine's root namespace —
// user snippets run in their own dict and never see it.
var FIG_HARVEST = [
  "def _aurelia_collect_figs():",
  "    import sys",
  "    if 'matplotlib' not in sys.modules:",
  "        return '[]'",
  "    import io, base64, json",
  "    import matplotlib.pyplot as plt",
  "    out = []",
  "    for num in plt.get_fignums()[:12]:",
  "        fig = plt.figure(num)",
  "        buf = io.BytesIO()",
  "        try:",
  "            fig.savefig(buf, format='png', dpi=110, bbox_inches='tight')",
  "        except Exception:",
  "            continue",
  "        out.append(base64.b64encode(buf.getvalue()).decode('ascii'))",
  "    plt.close('all')",
  "    return json.dumps(out)",
  "_aurelia_collect_figs()"
].join('\\n');

function harvestFigures(py) {
  try { return JSON.parse(py.runPython(FIG_HARVEST)); } catch (e) { return []; }
}

self.onmessage = function (event) {
  var id = event.data.id;
  var code = event.data.code;
  var post = function (msg) { msg.id = id; self.postMessage(msg); };

  var streamed = 0;
  var makeStream = function (kind) {
    return function (text) {
      streamed += text.length;
      if (streamed > ${MAX_STREAM_CHARS}) {
        throw new Error('Output limit (200 KB) exceeded - execution stopped.');
      }
      post({ type: 'stream', kind: kind, text: text + '\\n' });
    };
  };

  var run = async function () {
    var py = null;
    try {
      post({ type: 'phase', phase: 'boot' });
      py = await boot();
      py.setStdout({ batched: makeStream('stdout') });
      py.setStderr({ batched: makeStream('stderr') });

      post({ type: 'phase', phase: 'packages' });
      await py.loadPackagesFromImports(code);

      post({ type: 'phase', phase: 'running' });
      var namespace = py.globals.get('dict')();
      try {
        var value = await py.runPythonAsync(code, { globals: namespace, filename: '<aurelia-cell>' });
        var resultRepr;
        if (value !== undefined) {
          var reprFn = py.runPython('repr');
          try {
            resultRepr = reprFn(value);
          } finally {
            reprFn.destroy();
            if (value && typeof value.destroy === 'function') value.destroy();
          }
          if (typeof resultRepr === 'string' && resultRepr.length > ${MAX_RESULT_CHARS}) {
            resultRepr = resultRepr.slice(0, ${MAX_RESULT_CHARS}) + ' \\u2026';
          }
        }
        post({ type: 'done', ok: true, result: resultRepr, images: harvestFigures(py) });
      } finally {
        namespace.destroy();
      }
    } catch (err) {
      var message = err && err.message ? String(err.message) : String(err);
      post({ type: 'done', ok: false, error: message, images: py ? harvestFigures(py) : [] });
    }
  };
  run();
};
`
}

let worker: Worker | null = null
let workerUrl: string | null = null
let nextRunId = 1
let busy = false
let queueTail: Promise<void> = Promise.resolve()

function getWorker(): Worker {
  if (worker) return worker
  const blob = new Blob([buildWorkerSource()], { type: 'text/javascript' })
  workerUrl = URL.createObjectURL(blob)
  worker = new Worker(workerUrl)
  return worker
}

function killWorker(): void {
  worker?.terminate()
  worker = null
  if (workerUrl) {
    URL.revokeObjectURL(workerUrl)
    workerUrl = null
  }
}

/**
 * Execute a Python snippet. Returns a handle whose `promise` always resolves
 * (never rejects) — failures are encoded in the result so the UI has a single
 * rendering path. `cancel()` is safe to call at any moment, including while
 * the run is still queued behind another block's run.
 */
export function runPython(code: string, hooks: PythonRunHooks = {}): PythonRunHandle {
  const id = nextRunId++
  let settled = false
  let started = false
  let finish!: (result: PythonRunResult) => void
  const promise = new Promise<PythonRunResult>((resolve) => {
    finish = resolve
  })

  let startedAt = 0
  let releaseQueue: (() => void) | null = null
  let teardown: (() => void) | null = null

  const settle = (result: PythonRunResult) => {
    if (settled) return
    settled = true
    finish(result)
  }
  const elapsed = () => (startedAt ? performance.now() - startedAt : 0)

  const cancel = () => {
    if (settled) return
    settle({ ok: false, aborted: true, images: [], durationMs: elapsed() })
    if (started) {
      // Terminating is the only way to stop running wasm; the next run
      // pays the engine re-boot, which beats a stuck tab.
      killWorker()
      teardown?.()
      releaseQueue?.()
    }
  }

  if (busy) hooks.onPhase?.('queued')

  queueTail = queueTail.then(
    () =>
      new Promise<void>((release) => {
        if (settled) {
          release()
          return
        }
        started = true
        busy = true
        startedAt = performance.now()
        releaseQueue = release
        const w = getWorker()

        const timeout = setTimeout(() => {
          killWorker()
          settle({ ok: false, timedOut: true, images: [], durationMs: elapsed() })
          teardown?.()
          release()
        }, RUN_TIMEOUT_MS)

        const onMessage = (event: MessageEvent) => {
          const msg = event.data as WorkerOutMessage
          if (msg.id !== id) return
          if (msg.type === 'phase') {
            hooks.onPhase?.(msg.phase)
          } else if (msg.type === 'stream') {
            hooks.onStream?.({ kind: msg.kind, text: msg.text })
          } else {
            settle({
              ok: msg.ok,
              result: msg.result,
              error: msg.error,
              images: msg.images,
              durationMs: elapsed(),
            })
            teardown?.()
            release()
          }
        }
        const onError = () => {
          // The worker script itself failed — almost always the CDN being
          // unreachable. Reset so a later run can retry from scratch.
          killWorker()
          settle({ ok: false, engineFailed: true, images: [], durationMs: elapsed() })
          teardown?.()
          release()
        }

        teardown = () => {
          clearTimeout(timeout)
          w.removeEventListener('message', onMessage)
          w.removeEventListener('error', onError)
          busy = false
          teardown = null
        }

        w.addEventListener('message', onMessage)
        w.addEventListener('error', onError)
        w.postMessage({ id, code })
      }),
  )

  return { promise, cancel }
}
