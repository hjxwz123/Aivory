# Test layout

Non-Go tests live under this directory so production source trees contain only
runtime code:

- `frontend/` mirrors `src/` and contains every Vitest suite.
- `sandbox-service/` contains the Python standard-library regression suites for
  the sandbox sidecar.

Run them from the repository root:

```sh
npm test
npm run test:sandbox
```

The UI state regression suite starts its own Vite server on port 5204 and uses
mock API responses to check empty collections, failed requests and retry,
project search and pin filters, file preview placeholders, and the visibility
of empty table messages while scrolling on mobile in both themes:

```sh
npm run test:ui-states
```

It accepts `CHROME_PATH` and `AIVORY_UI_STATE_SCREENSHOTS`; screenshots otherwise
go to the OS temporary directory under `aivory-ui-states`.

The domain-rule suite checks creation without a workspace, lock reset when
clearing a workspace, existing rule edits, and mobile layouts in both themes:

```sh
node tests/browser/run-domain-rules.mjs
```

It starts Vite on port 5205 and accepts `CHROME_PATH` and
`AIVORY_DOMAIN_SCREENSHOTS`. Screenshots default to `aivory-domain-rules` in the
OS temporary directory.

The reply-wait suite exercises the real composer, animation-only waiting,
handoff to content/reasoning/tools, stopped and failed turns, private chat,
inline threads, mobile layouts in five languages, and reduced motion:

```sh
npm run test:reply-wait
```

It starts Vite on port 5206 and accepts `CHROME_PATH` and
`AIVORY_REPLY_WAIT_SCREENSHOTS`. Screenshots default to `aivory-reply-wait` in
the OS temporary directory.

Four Puppeteer suites drive real headless Chrome against a local Chrome/Edge
binary. They exist because `tsc` and the unit suite cannot prove that a
third-party widget actually mounts, and several shipped bugs were exactly that:

```sh
npm run test:browser   # the four document editors mount and emit edited bytes
npm run test:preview   # the sandboxed HTML preview renders in every open/stream flow
npm run test:panel     # the artifact panel's document preview/edit flow and its resize divider
npm run test:code-wrap # code wrapping, appearance defaults, clipboard, Shiki and narrow layouts
```

Each uses its own Vite dev server and port, so they must not run concurrently.
The code-wrap suite finds Chrome/Edge on macOS, Linux and Windows, or accepts
`CHROME_PATH`. It creates and removes its own temporary browser profile and Vite
cache, preserving the installed browser's normal profile. It runs on port 5196.

The public sharing suite runs against a production preview so development
StrictMode's duplicate effect probes do not change the request ordering:

```sh
npm run build
npm run preview -- --host 127.0.0.1 --port 5184 --strictPort
# In another terminal:
npm run test:share
```

It mocks API responses in the browser and checks anonymous access even with
automatic OAuth redirects enabled, session restoration during snapshot loading,
retry after rate limiting, and missing links at desktop and mobile widths. It
also checks that public snapshot requests omit session credentials and that
rendering stays within the viewport. It accepts `AIVORY_SHARE_TEST_URL` and
`CHROME_PATH`, discovers Chrome/Edge, and saves screenshots in the OS temporary
directory. If the build reaches Node's default heap limit, run it with
`NODE_OPTIONS=--max-old-space-size=8192 npm run build`.

Go is the deliberate exception. Go only compiles same-package `_test.go` files
from the package directory, and these unit tests exercise package-private
invariants. They therefore remain beside their Go package and run with:

```sh
cd server
go test ./...
```

Moving those files here would either make `go test ./...` silently skip them or
require exporting internal implementation solely for tests, reducing rather
than improving test isolation.
