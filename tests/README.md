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
