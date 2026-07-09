# syntax=docker/dockerfile:1
#
# Aurelia single-container image: builds the Vite SPA, builds the Go API, and
# ships ONE runtime that serves BOTH from the same origin. The Go process serves
# the built SPA from STATIC_DIR and routes /api/* to the backend, so there is no
# nginx tier, no cross-origin, and any domain the container is reached on works
# without configuring PUBLIC_ORIGIN / ALLOWED_ORIGINS.
#
# Build context is the repository root (see docker-compose.prod.yml -> build.context: ..).
#
#   docker build -f deploy/Dockerfile.app -t aurelia-app:latest .
#
# The Go binary embeds the SQLite driver (mattn/go-sqlite3) as the dev/fallback
# backend, so the build needs CGO + a C toolchain even though production runs
# against Postgres. The runtime image is debian-slim because the binary
# dynamically links glibc.

# ---- Stage 1: build the SPA -------------------------------------------------
# Build on the NATIVE build platform, not the target arch. The SPA output (dist)
# is just static JS/CSS/HTML -- architecture-independent -- so there's no reason
# to run the heavy `npm ci` + Vite build under QEMU emulation when the image
# targets a different arch (e.g. linux/amd64 from an Apple-Silicon host). Emulated
# `npm ci` is slow and routinely crashes mid-install (the `exit code: 146` here).
# The Go stage below still builds for the target arch. ($BUILDPLATFORM is a
# BuildKit-provided build arg; this Dockerfile already opts into BuildKit above.)
FROM --platform=$BUILDPLATFORM node:20-bookworm-slim AS web
WORKDIR /web
COPY package.json package-lock.json ./
# --no-audit/--no-fund trim work + network chatter; the cache mount lets a retry
# reuse already-downloaded tarballs instead of re-fetching the whole tree.
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
# Copy only what the build needs (server/ and node_modules are excluded).
COPY index.html vite.config.ts tsconfig.json tsconfig.app.json tsconfig.node.json ./
COPY tailwind.config.ts postcss.config.js eslint.config.js ./
COPY src ./src
COPY public ./public
# The SPA targets a same-origin /api (src/api/client.ts) -- no build-time URL.
RUN npm run build

# ---- Stage 2: build the Go API ---------------------------------------------
FROM golang:1.24-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=1
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN go build -trimpath -ldflags="-s -w" -o /out/aurelia-api ./cmd/api

# ---- Stage 3: runtime -------------------------------------------------------
FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/aurelia-api /usr/local/bin/aurelia-api
COPY --from=web /web/dist /app/web
# Uploads + artifacts are filesystem-backed regardless of the DB backend; the
# compose file mounts a named volume here so they survive container restarts.
RUN mkdir -p /app/data/uploads /app/data/artifacts
ENV UPLOAD_DIR=/app/data/uploads \
    ARTIFACT_DIR=/app/data/artifacts \
    BACKUP_DIR=/app/data/backups \
    STATIC_DIR=/app/web
EXPOSE 8787
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=5 \
    CMD wget -qO- http://127.0.0.1:8787/api/health >/dev/null 2>&1 || exit 1
ENTRYPOINT ["aurelia-api"]
