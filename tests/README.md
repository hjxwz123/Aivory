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

Three Puppeteer suites drive real headless Chrome against a local Chrome/Edge
binary. They exist because `tsc` and the unit suite cannot prove that a
third-party widget actually mounts, and several shipped bugs were exactly that:

```sh
npm run test:browser   # the four document editors mount and emit edited bytes
npm run test:preview   # the sandboxed HTML preview renders in every open/stream flow
npm run test:panel     # the artifact panel's document preview/edit flow and its resize divider
```

Each uses its own Vite dev server and port, so they must not run concurrently.

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
