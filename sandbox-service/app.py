"""
Aivory local Python sandbox — sidecar service (design.md §4.5).

Speaks the HTTP protocol that `server/internal/sandbox/sandbox.go` expects, so
the Go backend only needs SANDBOX_BASE_URL pointed at this service:

    POST /sessions  -> {"session_id": "..."}
    POST /exec      {session_id, code, timeout_ms}
                    -> {"stdout", "stderr", "exit_code", "files":[{name,mime_type,data_base64}]}
    POST /files     {session_id, path, data_base64}  -> {"ok": true}
    POST /files/reset-inputs {session_id}            -> {"ok": true}

Each session is one long-lived, locked-down Docker container running the
`aivory-sandbox` image (see Dockerfile.runner). /workspace persists across
exec calls within a session — pip-installed packages, generated files and
intermediate data survive, matching ChatGPT Code Interpreter behaviour.

Design baselines honoured here (§4.5 安全基线):
  - non-root, mandatory --network none, no-new-privileges, dropped caps
  - memory / cpu / pids / nofile limits
  - 120s exec timeout (overridable per-call, capped)
  - stdout/stderr streamed through a 32KB cap before returning to the model
  - artifacts capped per file, per exec count, and per exec total bytes
  - 30min idle TTL reaper

This is a DEV / single-host tool. For production swap `docker run` here for a
gVisor / microVM / E2B backend — the Go side and this HTTP contract do not move.
"""

from __future__ import annotations

import base64
import binascii
from contextlib import contextmanager
from dataclasses import dataclass
import mimetypes
import os
import re
import secrets
import select
import selectors
import subprocess
import sys
import threading
import time
import uuid
from collections.abc import Iterator
from typing import Any, Optional

from fastapi import Body, FastAPI, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field

# --- Config (env-overridable) ----------------------------------------------
IMAGE = os.environ.get("SANDBOX_IMAGE", "aivory-sandbox:latest")
MEMORY = os.environ.get("SANDBOX_MEMORY", "2g")       # §4.5 doc rendering can be heavy
CPUS = os.environ.get("SANDBOX_CPUS", "1")
PIDS_LIMIT = os.environ.get("SANDBOX_PIDS_LIMIT", "256")
API_KEY = os.environ.get("SANDBOX_API_KEY", "")       # required Bearer match (fail-closed; see below)
# F2: refuse to start with no auth unless the operator explicitly opts out for a
# trusted localhost-only dev box. Empty/blank key + no opt-out => sys.exit(1).
# Strict allow-list parse: ONLY canonical truthy tokens disable auth. The prior
# `not in ("","0","false")` made "False"/"no"/"off"/"FALSE" truthy, so an
# operator who wrote SANDBOX_ALLOW_NO_AUTH=False (meaning "keep auth") would have
# disabled it — a fail-OPEN footgun on a host-Docker-driving service.
ALLOW_NO_AUTH = os.environ.get("SANDBOX_ALLOW_NO_AUTH", "").strip().lower() in ("1", "true", "yes", "on")
# Hard operator ceiling for a single exec. The per-call timeout (admin-set,
# forwarded as timeout_ms) is clamped to this. Raised from 120s so the admin's
# `sandbox_exec_timeout_sec` can reach up to 10min without an env change; lower
# it to tighten the ceiling.
EXEC_TIMEOUT_CAP_MS = int(os.environ.get("SANDBOX_EXEC_TIMEOUT_CAP_MS", "600000"))
# Per-exec default when the caller omits timeout_ms. A missing/zero value must
# NOT silently inherit the 10-min hard cap (which would let one cell pin an exec
# slot for the full ceiling); fall back to a sane 120s instead.
DEFAULT_EXEC_TIMEOUT_MS = int(os.environ.get("SANDBOX_DEFAULT_EXEC_TIMEOUT_MS", "120000"))
# Wall-clock bound for the workspace archive tar read-loop. Without it a stalled
# `docker exec tar` blocks the loop forever while the reaper/DELETE hold the
# session lock, freezing all reaping.
ARCHIVE_TIMEOUT_S = float(os.environ.get("SANDBOX_ARCHIVE_TIMEOUT_S", "120"))
# Default timeout (seconds) for any `docker` control call that doesn't pass its
# own. Bounds the daemon-level calls (inspect/version) that were previously
# unbounded, so a stalled daemon surfaces a clear error instead of hanging the
# request until the caller's deadline. Long ops (pull/exec/tar) pass explicit
# higher timeouts and are unaffected.
DOCKER_CALL_TIMEOUT_S = float(os.environ.get("SANDBOX_DOCKER_CALL_TIMEOUT_S", "30"))
IDLE_TTL_SECONDS = int(os.environ.get("SANDBOX_IDLE_TTL_SECONDS", "1800"))  # 30 min
# Hard operator ceiling for the admin-tunable idle-recycle TTL. The per-session
# value Go forwards on create (idle_ttl_sec, from the admin `sandbox_idle_ttl_sec`
# setting) is clamped to this — mirroring EXEC_TIMEOUT_CAP_MS, an admin can
# shorten the recycle window but never push it past the operator's ceiling.
IDLE_TTL_CAP_SECONDS = int(os.environ.get("SANDBOX_IDLE_TTL_CAP_SECONDS", "86400"))  # 24h
MAX_SESSIONS = int(os.environ.get("SANDBOX_MAX_SESSIONS", "16"))
MAX_CONCURRENT_EXECS = int(os.environ.get("SANDBOX_MAX_CONCURRENT_EXECS", "4"))
MAX_CONCURRENT_CREATES = int(os.environ.get("SANDBOX_MAX_CONCURRENT_CREATES", "2"))
QUEUE_TIMEOUT_SECONDS = float(os.environ.get("SANDBOX_QUEUE_TIMEOUT_SECONDS", "150"))
# When truthy, `docker pull` the runtime image once on startup so a fresh
# server doesn't fail the first /sessions call. Best-effort: logs and continues.
PULL_ON_START = os.environ.get("SANDBOX_PULL_ON_START", "") not in ("", "0", "false")
# F6: default the session rootfs to read-only (disk-fill DoS guard). The
# pip-installed runtime stays read-only; only /workspace (+ /tmp, $HOME) are
# size-bounded tmpfs mounts, so a session can't fill the host disk. Operators
# can still force it off with SANDBOX_READ_ONLY_ROOTFS=0.
READ_ONLY_ROOTFS = os.environ.get("SANDBOX_READ_ONLY_ROOTFS", "1") not in ("", "0", "false")
TMPFS_SIZE = os.environ.get("SANDBOX_TMPFS_SIZE", "256m")
# Bounded writable workspace size (tmpfs) when read-only rootfs is on.
WORKSPACE_SIZE = os.environ.get(
    "SANDBOX_WORKSPACE_SIZE",
    os.environ.get("SANDBOX_WORKSPACE_TMPFS_SIZE", "512m"),  # back-compat alias
)
NOFILE_ULIMIT = os.environ.get("SANDBOX_NOFILE_ULIMIT", "1024:1024")
# F6 (best-effort): per-container writable-layer quota. Requires overlay2 +
# pquota/prjquota; `docker run` errors otherwise, so it is opt-in and applied
# best-effort (we retry without it on failure rather than crash). Empty = off.
DISK_SIZE = os.environ.get("SANDBOX_DISK_SIZE", "")
# F1 (best-effort): pinned seccomp profile for session containers. Opt-in —
# only added to `docker run` when set (path readable inside the sidecar).
SECCOMP_PROFILE = os.environ.get("SANDBOX_SECCOMP_PROFILE", "")

MAX_OUTPUT_BYTES = int(os.environ.get("SANDBOX_MAX_OUTPUT_BYTES", str(32 * 1024)))          # stdout/stderr truncation
MAX_ARTIFACT_BYTES = int(os.environ.get("SANDBOX_MAX_ARTIFACT_BYTES", str(20 * 1024 * 1024)))  # single produced file cap

# Exec reader loop (_run_exec_bounded): selector poll cadence, per-read chunk
# size, and the post-loop process-wait grace period.
EXEC_READER_POLL_S = 0.2
EXEC_READER_CHUNK_BYTES = 8192
EXEC_READER_WAIT_GRACE_S = 2.0

# Archive tar-stream reader loop: selector poll cadence, per-read chunk size,
# and the post-loop process-wait grace period.
ARCHIVE_READ_POLL_S = 5.0
ARCHIVE_READ_CHUNK_BYTES = 65536
ARCHIVE_READ_WAIT_GRACE_S = 10.0

# Object-storage SDK timeouts/retries — bound every SDK call so a slow/hung
# bucket can't freeze the reaper, DELETE, or session creation.
S3_MAX_ATTEMPTS = int(os.environ.get("SANDBOX_S3_MAX_ATTEMPTS", "3"))
S3_CONNECT_TIMEOUT_S = float(os.environ.get("SANDBOX_S3_CONNECT_TIMEOUT_S", "10"))
S3_READ_TIMEOUT_S = float(os.environ.get("SANDBOX_S3_READ_TIMEOUT_S", "120"))
OSS_CONNECT_TIMEOUT_S = float(os.environ.get("SANDBOX_OSS_CONNECT_TIMEOUT_S", "30"))
MAX_UPLOAD_BYTES = int(os.environ.get("SANDBOX_MAX_UPLOAD_BYTES", str(40 * 1024 * 1024)))
MAX_FILES_PER_EXEC = int(os.environ.get("SANDBOX_MAX_FILES_PER_EXEC", "20"))
MAX_TOTAL_ARTIFACT_BYTES = int(os.environ.get("SANDBOX_MAX_TOTAL_ARTIFACT_BYTES", str(50 * 1024 * 1024)))
# L4: bound the per-exec artifact-collection wall-clock so the sidecar's worst-
# case /exec latency stays predictable. The Go HTTP client deadlines at
# ExecTimeout + execClientOverhead (120s); that overhead must comfortably cover
# this collection budget PLUS the fixed cell-teardown/snapshot control calls. With
# the default 60s budget the worst case is ~timeout_s + ~80s < client's
# timeout_s + 120s, so a many-file run can no longer outlast the client mid-base64.
MAX_COLLECT_SECONDS = float(os.environ.get("SANDBOX_MAX_COLLECT_SECONDS", "60"))
COLLECT_FILE_TIMEOUT_S = float(os.environ.get("SANDBOX_COLLECT_FILE_TIMEOUT_S", "30"))
# F5: reject oversized request bodies at the control plane before reading them.
# Default 56 MiB comfortably exceeds the 40 MiB upload cap after base64 (+JSON).
MAX_BODY_BYTES = int(os.environ.get("SANDBOX_MAX_BODY_BYTES", str(56 * 1024 * 1024)))
# F5: hard cap on the /exec `code` field itself (1 MiB of source).
MAX_CODE_BYTES = int(os.environ.get("SANDBOX_MAX_CODE_BYTES", str(1 * 1024 * 1024)))

# §4.5: object-storage archive/restore (boto3 / oss2, lazy-imported).
# Presigned GET URL ttl defaults to 1h, hard-capped at 24h.
STORAGE_DEFAULT_TTL = int(os.environ.get("SANDBOX_STORAGE_DEFAULT_TTL", "3600"))
STORAGE_MAX_TTL = int(os.environ.get("SANDBOX_STORAGE_MAX_TTL", "86400"))
# Default object-key prefix when the storage block omits one (matches the Go
# side's `storage_prefix` default).
STORAGE_DEFAULT_PREFIX = "workspaces/"
# Bound the workspace archive captured from `tar czf -`. If a session's
# /workspace exceeds this, the archive is skipped (logged) rather than buffered
# unbounded into the sidecar — the session is still reaped/deleted as before.
MAX_ARCHIVE_BYTES = int(os.environ.get("SANDBOX_MAX_ARCHIVE_BYTES", str(200 * 1024 * 1024)))
# /storage/put carries whole documents (the RAG pipeline uploads PDFs up to
# ~200 MiB, then hands MinerU a presigned URL). Those payloads dwarf the F5
# control-plane cap that protects /exec & /files, so the body-size guard lets
# /storage/put through up to this larger ceiling instead. ~300 MiB comfortably
# fits a 200 MiB object after base64 (+JSON). Other paths keep MAX_BODY_BYTES.
MAX_STORAGE_BODY_BYTES = int(
    os.environ.get("SANDBOX_MAX_STORAGE_BODY_BYTES", str(300 * 1024 * 1024))
)
# §4.5 local (on-disk) archive backend — the zero-dependency alternative to
# S3/OSS. When set, workspace tarballs are written under this directory; mount it
# as a volume for durability across sidecar restarts. Empty => the `local`
# provider is inert (archive/restore/GC no-op), so a mis-mounted dir degrades to
# "reaped = gone" rather than erroring. The path is an OPERATOR env, never an
# admin/forwarded value: the sidecar runs as root driving docker.sock, so letting
# a remote caller pick the write path would be a host-write vector.
LOCAL_STORAGE_DIR = os.environ.get("SANDBOX_LOCAL_STORAGE_DIR", "").strip()

CONTAINER_PREFIX = "aivory-sbx-"
LABEL = "aivory.sandbox=1"
WORKSPACE = "/workspace"
OUTPUTS_DIR = f"{WORKSPACE}/outputs"
UPLOADS_DIR = f"{WORKSPACE}/uploads"
SKILLS_DIR = f"{WORKSPACE}/skills"

# session_id -> last-used epoch seconds (for the idle reaper). Container state
# itself lives in Docker, so a sidecar restart only loses TTL tracking, not
# sessions.
_last_used: dict[str, float] = {}

# session_id -> the `storage` block the Go side forwarded on create. The reaper
# and DELETE consult it to archive /workspace before killing the container. Lost
# on sidecar restart (so a session created pre-restart won't be archived on a
# later TTL kill) — acceptable; storage is a best-effort convenience. Guarded by
# `_state_lock`.
_session_storage: dict[str, dict] = {}

# session_id -> per-session idle TTL in seconds, forwarded by Go on create (from
# the admin `sandbox_idle_ttl_sec` setting, already clamped). The reaper reads it
# with a fallback to the global IDLE_TTL_SECONDS, so an admin change takes effect
# for new sessions without a sidecar restart. Same in-memory, best-effort,
# lost-on-restart model as _session_storage above. Guarded by `_state_lock`.
_session_ttl: dict[str, int] = {}

# session_id -> stable archive key (the Go side forwards the conversation id).
# Workspace tarballs are keyed by THIS, not the ephemeral session id, so a
# workspace survives session recycle (§4.5-C G2 fix): every create mints a fresh
# session uuid, but the reaper archives — and the next create restores — under
# the same stable key. Falls back to the session id when unset. Guarded by
# `_state_lock`; same best-effort, lost-on-restart model as above.
_session_archive_key: dict[str, str] = {}


@dataclass
class _SessionState:
    lock: threading.Lock
    refs: int = 0


_session_states: dict[str, _SessionState] = {}
_state_lock = threading.Lock()
# L5: serialises the MAX_SESSIONS check-then-`docker run` so the count and the
# create are atomic. _create_slots admits MAX_CONCURRENT_CREATES creators at once,
# which would otherwise let several pass the check and all create a container,
# overshooting the cap. `docker run -d` returns quickly, so this critical section
# is short.
_create_count_lock = threading.Lock()
# L6: session ids currently being torn down (reaper/DELETE). The teardown holds
# the per-session lock for the whole archive (up to ARCHIVE_TIMEOUT_S) so the tar
# is consistent; without this flag a concurrent /exec on that session would block
# on the lock for that whole window before getting its (correct) 404. Marking the
# session terminating BEFORE teardown lets /exec bail with a fast 404 instead.
# Guarded by _state_lock.
_terminating: set[str] = set()
_exec_slots = threading.BoundedSemaphore(MAX_CONCURRENT_EXECS) if MAX_CONCURRENT_EXECS > 0 else None
_create_slots = threading.BoundedSemaphore(MAX_CONCURRENT_CREATES) if MAX_CONCURRENT_CREATES > 0 else None

# Set once IMAGE is confirmed present locally, so a fresh pull is attempted at
# most once per process lifetime (never re-checked afterwards — this only
# guards the COLD-CACHE case, not `:latest` freshness; PULL_ON_START / an
# operator-run `docker pull` handle keeping it current).
_image_ready = threading.Event()
_image_pull_lock = threading.Lock()


def _ensure_image_present() -> None:
    """`docker run` at session-create is budgeted 60s on the assumption it never
    pulls (see L5 above — that lock's comment literally says "docker run -d
    returns quickly"). On a cold host (fresh deploy, pruned cache, evicted
    layers) an IMPLICIT pull inside that call routinely exceeds 60s for a
    Python-runtime image, so every session create fails with a generic 500
    until the image happens to land. Pull it here, OUTSIDE _create_count_lock,
    with the same generous budget PULL_ON_START uses, so a cold cache surfaces
    one clear 503 instead of repeated `docker run` timeouts.
    """
    if _image_ready.is_set():
        return
    with _image_pull_lock:
        if _image_ready.is_set():
            return
        cp = _docker(["image", "inspect", IMAGE], timeout=10)
        if cp.returncode == 0:
            _image_ready.set()
            return
        print(f"[sandbox] {IMAGE} not cached locally — pulling before session create")
        cp = _docker(["pull", IMAGE], timeout=600)
        if cp.returncode != 0:
            raise HTTPException(
                status_code=503,
                detail=f"sandbox image not available yet: {cp.stderr.decode(errors='replace')[:200]}",
            )
        _image_ready.set()


app = FastAPI(title="Aivory Sandbox Sidecar", version="1.0")


# --- F2: fail closed on missing auth ---------------------------------------
# This service drives the host Docker daemon, so an unauthenticated reachable
# instance is remote code execution on the host. Historically auth was simply
# off when SANDBOX_API_KEY was blank; refuse to start in that case instead,
# unless the operator explicitly opts out for a trusted localhost-only dev box.
if not API_KEY.strip():
    if ALLOW_NO_AUTH:
        print(
            "[sandbox] WARNING: SANDBOX_API_KEY is empty and "
            "SANDBOX_ALLOW_NO_AUTH=1 — auth is DISABLED. Only acceptable for a "
            "trusted, localhost-only dev box. Never expose this on a network.",
            file=sys.stderr,
        )
    else:
        print(
            "[sandbox] FATAL: SANDBOX_API_KEY is empty/blank. This service "
            "exposes host Docker control (RCE) and refuses to start without "
            "auth. Set SANDBOX_API_KEY to a strong secret, or set "
            "SANDBOX_ALLOW_NO_AUTH=1 for a trusted localhost-only dev box.",
            file=sys.stderr,
        )
        sys.exit(1)


# --- Docker helpers ---------------------------------------------------------
def _container(session_id: str) -> str:
    return CONTAINER_PREFIX + session_id


def _valid_session(session_id: str) -> bool:
    # session ids we mint are uuid4 hex; never interpolate anything else into a
    # container name / shell.
    return bool(re.fullmatch(r"[0-9a-f]{32}", session_id or ""))


def _docker(args: list[str], *, input_bytes: Optional[bytes] = None,
            timeout: Optional[float] = None) -> subprocess.CompletedProcess:
    # Default-bound every control call: a caller that omits `timeout` still can't
    # hang forever on a stalled daemon. Explicit timeouts (incl. long ones) win.
    if timeout is None:
        timeout = DOCKER_CALL_TIMEOUT_S
    return subprocess.run(
        ["docker", *args],
        input=input_bytes,
        capture_output=True,
        timeout=timeout,
    )


def _is_running(session_id: str) -> bool:
    # A stalled daemon must not wedge the whole /exec request here (the step that
    # used to be unbounded). On timeout, treat the session as unavailable so the
    # handler returns a clean 404 fast instead of hanging into the client's
    # deadline; the Go side then provisions a fresh session.
    try:
        cp = _docker(["inspect", "-f", "{{.State.Running}}", _container(session_id)], timeout=20)
    except subprocess.TimeoutExpired:
        return False
    return cp.returncode == 0 and cp.stdout.strip() == b"true"


@dataclass
class _BoundedOutput:
    limit: int = MAX_OUTPUT_BYTES
    data: bytes = b""
    truncated: bool = False

    def append(self, chunk: bytes) -> None:
        if not chunk:
            return
        remaining = self.limit - len(self.data)
        if remaining > 0:
            self.data += chunk[:remaining]
        if len(chunk) > remaining:
            self.truncated = True

    def text(self, label: str) -> str:
        data = self.data
        if self.truncated:
            data += f"\n... [truncated, {label} exceeded {self.limit // 1024}KB]".encode()
        return data.decode("utf-8", errors="replace")


@contextmanager
def _slot(sem: Optional[threading.BoundedSemaphore], what: str) -> Iterator[None]:
    if sem is None:
        yield
        return
    if not sem.acquire(timeout=QUEUE_TIMEOUT_SECONDS):
        raise HTTPException(status_code=429, detail=f"{what} queue is full")
    try:
        yield
    finally:
        sem.release()


@contextmanager
def _session_lock(session_id: str) -> Iterator[None]:
    with _state_lock:
        state = _session_states.setdefault(session_id, _SessionState(threading.Lock()))
        state.refs += 1
    if not state.lock.acquire(timeout=QUEUE_TIMEOUT_SECONDS):
        with _state_lock:
            state.refs -= 1
            if state.refs == 0 and session_id not in _last_used:
                _session_states.pop(session_id, None)
        raise HTTPException(status_code=429, detail="session is busy")
    try:
        yield
    finally:
        state.lock.release()
        with _state_lock:
            state.refs -= 1
            if state.refs == 0 and session_id not in _last_used:
                _session_states.pop(session_id, None)


def _touch(session_id: str) -> None:
    with _state_lock:
        _last_used[session_id] = time.time()
        _session_states.setdefault(session_id, _SessionState(threading.Lock()))


def _forget(session_id: str) -> None:
    with _state_lock:
        _last_used.pop(session_id, None)
        _session_storage.pop(session_id, None)
        _session_ttl.pop(session_id, None)
        _session_archive_key.pop(session_id, None)
        _terminating.discard(session_id)
        state = _session_states.get(session_id)
        if state is not None and state.refs == 0:
            _session_states.pop(session_id, None)


def _set_terminating(session_id: str, on: bool) -> None:
    with _state_lock:
        if on:
            _terminating.add(session_id)
        else:
            _terminating.discard(session_id)


def _is_terminating(session_id: str) -> bool:
    with _state_lock:
        return session_id in _terminating


def _remember_storage(session_id: str, storage: Optional[dict]) -> None:
    """Record (or clear) the session's archive bucket for later reap/delete."""
    with _state_lock:
        if storage:
            _session_storage[session_id] = storage
        else:
            _session_storage.pop(session_id, None)


def _session_storage_for(session_id: str) -> Optional[dict]:
    with _state_lock:
        return _session_storage.get(session_id)


def _remember_ttl(session_id: str, ttl: Optional[int]) -> None:
    """Record (or clear) the session's admin-forwarded idle TTL (seconds)."""
    with _state_lock:
        if ttl and ttl > 0:
            _session_ttl[session_id] = ttl
        else:
            _session_ttl.pop(session_id, None)


def _sanitize_archive_key(key: Optional[str]) -> Optional[str]:
    """Accept a Go-forwarded archive key (a conversation id) only when it is safe
    to use as an object-key / filename stem; else None so we fall back to keying
    the archive by the session id. Bounds length + charset so it can't inject a
    prefix/traversal into the storage key."""
    if key and re.fullmatch(r"[A-Za-z0-9_-]{1,128}", key):
        return key
    return None


def _remember_archive_key(session_id: str, key: Optional[str]) -> None:
    with _state_lock:
        if key:
            _session_archive_key[session_id] = key
        else:
            _session_archive_key.pop(session_id, None)


def _archive_stem(session_id: str) -> str:
    """Object-key stem for this session's workspace tarball: the stable archive
    key when the Go side forwarded one (survives recycle), else the session id."""
    with _state_lock:
        return _session_archive_key.get(session_id) or session_id


def _count_live_sessions() -> int:
    cp = _docker(["ps", "-q", "--filter", f"label={LABEL}"], timeout=20)
    if cp.returncode != 0:
        return len(_last_used)
    return len([line for line in cp.stdout.splitlines() if line.strip()])


def _timeout_arg(seconds: float) -> str:
    return f"{max(seconds, 1.0):.3f}s"


def _discover_sessions() -> None:
    cp = _docker(
        [
            "ps", "-a",
            "--filter", f"label={LABEL}",
            "--format", "{{.Names}}\t{{.State}}\t{{.CreatedAt}}",
        ],
        timeout=30,
    )
    if cp.returncode != 0:
        print(f"[sandbox] warning: failed to discover existing sessions: "
              f"{cp.stderr.decode(errors='replace')[:200]}")
        return

    now = time.time()
    for line in cp.stdout.decode(errors="replace").splitlines():
        parts = line.split("\t")
        if len(parts) < 2:
            continue
        name, state = parts[0], parts[1]
        if not name.startswith(CONTAINER_PREFIX):
            continue
        sid = name[len(CONTAINER_PREFIX):]
        if not _valid_session(sid):
            continue
        if state.lower() != "running":
            _docker(["rm", "-f", name], timeout=30)
            continue
        _touch(sid)
    with _state_lock:
        recovered = len(_last_used)
    if recovered:
        print(f"[sandbox] recovered {recovered} existing session container(s)")


def _run_exec_bounded(name: str, cmd: list[str], *, timeout: float,
                      env: Optional[dict[str, str]] = None) -> tuple[int, str, str]:
    stdout = _BoundedOutput()
    stderr = _BoundedOutput()
    start = time.monotonic()
    eflags: list[str] = []
    for k, v in (env or {}).items():
        eflags.extend(["-e", f"{k}={v}"])
    proc = subprocess.Popen(
        ["docker", "exec", *eflags, name, *cmd],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    assert proc.stdout is not None
    assert proc.stderr is not None

    sel = selectors.DefaultSelector()
    sel.register(proc.stdout, selectors.EVENT_READ, stdout)
    sel.register(proc.stderr, selectors.EVENT_READ, stderr)
    timed_out = False

    try:
        while sel.get_map():
            if time.monotonic() - start > timeout:
                timed_out = True
                proc.kill()
                break
            for key, _ in sel.select(timeout=EXEC_READER_POLL_S):
                chunk = key.fileobj.read1(EXEC_READER_CHUNK_BYTES)
                if chunk:
                    key.data.append(chunk)
                else:
                    sel.unregister(key.fileobj)
                    key.fileobj.close()
        exit_code = proc.wait(timeout=EXEC_READER_WAIT_GRACE_S)
    except subprocess.TimeoutExpired:
        timed_out = True
        proc.kill()
        exit_code = 124
    finally:
        for key in list(sel.get_map().values()):
            try:
                sel.unregister(key.fileobj)
            except Exception:
                pass
            try:
                key.fileobj.close()
            except Exception:
                pass

    if timed_out:
        exit_code = 124
    return exit_code, stdout.text("stdout"), stderr.text("stderr")


def _kill_marked_processes(name: str, token: str) -> None:
    """Kill every process in the session container whose environment carries our
    per-exec marker (AIVORY_CELL_TOKEN=<token>).

    coreutils `timeout` signals only its single direct child (python), and the
    host-side proc.kill() only kills the local `docker exec` client — so a
    background process the cell detached into its own process group/session
    (subprocess.Popen, os.fork, a double-fork, `setsid`) survives, reparents to
    PID 1 (`sleep infinity`, which never reaps it), and keeps burning the
    session's cpu/mem/pids budget until the 30-min idle reaper. The env var is
    inherited across fork/exec/setsid/double-fork, so matching on it reaps the
    whole tree regardless of process group. PID 1 carries no marker, so it is
    never touched. `token` is a uuid4 hex (our own), so it is safe to interpolate.
    """
    if not re.fullmatch(r"[0-9a-f]{32}", token or ""):
        return
    script = (
        'for d in /proc/[0-9]*; do '
        'if tr "\\0" "\\n" < "$d/environ" 2>/dev/null | '
        f'grep -qxF "AIVORY_CELL_TOKEN={token}"; then '
        'kill -9 "${d##*/}" 2>/dev/null; fi; done'
    )
    try:
        _docker(["exec", name, "sh", "-c", script], timeout=15)
    except subprocess.TimeoutExpired:
        pass


# Warm-up program piped to `python -` inside a fresh session container. It
# (re)writes the CJK matplotlibrc and forces matplotlib's FontManager cache to
# build, exercising the CJK glyph-fallback path with a real render. Encoded as
# UTF-8 on the wire; Python reads stdin source as UTF-8 by default (PEP 3120)
# regardless of the container locale. See _warm_matplotlib for the why.
_MPL_WARM_PY = r'''
import os
# Rewrite the CJK matplotlibrc BEFORE importing matplotlib (it reads the rc at
# import). The image bakes one, but /home/sandbox is a fresh tmpfs that shadows
# it, so we restore it into the live (writable, container-lifetime) tmpfs here.
cfg = os.environ.get("MPLCONFIGDIR") or os.path.expanduser("~/.config/matplotlib")
try:
    os.makedirs(cfg, exist_ok=True)
    with open(os.path.join(cfg, "matplotlibrc"), "w") as fh:
        fh.write(
            "font.family: sans-serif\n"
            "font.sans-serif: Noto Sans CJK SC, Noto Sans CJK JP, WenQuanYi Zen Hei, DejaVu Sans\n"
            "axes.unicode_minus: False\n"
            "figure.dpi: 120\n"
            "savefig.bbox: tight\n"
        )
except OSError:
    pass
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import matplotlib.font_manager as fm
# Building the FontManager (the import above) writes fontlist-vNNN.json into
# MPLCONFIGDIR; findfont + a CJK render warm the glyph-fallback path so the
# first real cell renders Chinese instead of tofu boxes.
fm.fontManager.findfont("Noto Sans CJK SC")
fig, ax = plt.subplots()
ax.set_title("中文 CJK warmup")
fig.savefig("/tmp/_mpl_warm.png")
plt.close(fig)
try:
    os.remove("/tmp/_mpl_warm.png")
except OSError:
    pass
'''


def _warm_matplotlib(name: str) -> None:
    """Best-effort: pre-build matplotlib's font cache + restore the CJK
    matplotlibrc inside a freshly-created session container, BEFORE any user
    code runs.

    Why this is needed (the "Chinese is tofu on the first chart, fine on the
    second" bug): the container mounts /home/sandbox as an empty tmpfs (the
    read-only-rootfs hardening, §4.5), which SHADOWS both the matplotlibrc and
    the font cache baked into the image. So every new container starts with a
    COLD FontManager cache and no CJK rc. The first cell that imports matplotlib
    then builds the cache from scratch — and while it is cold, matplotlib's
    glyph-level font fallback can't see Noto CJK yet, so Chinese renders as
    boxes (the tofu). The SECOND cell hits the now-warm cache and renders fine.
    Warming once here makes the FIRST cell behave like the second. The cache
    lives on the /home/sandbox tmpfs, which persists for the container's whole
    lifetime, so it survives across exec calls within the session.

    Bounded + swallowed: a warm-up failure must never block session creation —
    the worst case is simply the old (first-cell-tofu) behaviour.
    """
    try:
        cp = _docker(["exec", "-i", name, "python", "-"],
                     input_bytes=_MPL_WARM_PY.encode("utf-8"), timeout=60)
        if cp.returncode != 0:
            print(f"[sandbox] matplotlib warm-up for {name} returned "
                  f"{cp.returncode}: {cp.stderr.decode(errors='replace')[:200]}")
    except subprocess.TimeoutExpired:
        print(f"[sandbox] matplotlib warm-up for {name} timed out")
    except Exception as exc:  # noqa: BLE001 - never let warm-up break create
        print(f"[sandbox] matplotlib warm-up for {name} failed: {exc}")


# --- Request models ---------------------------------------------------------
class ExecBody(BaseModel):
    session_id: str
    # F5: cap the source length. max_length counts characters; since UTF-8 chars
    # can be up to 4 bytes, this is a cheap upper guard and the /exec handler
    # additionally rejects on the exact encoded byte length.
    code: str = Field(max_length=MAX_CODE_BYTES)
    timeout_ms: Optional[int] = None


class FilesBody(BaseModel):
    session_id: str
    path: str
    data_base64: str


class GetFileBody(BaseModel):
    session_id: str
    path: str


class ListFilesBody(BaseModel):
    session_id: str


# `storage` is a free-form snake_case block the Go side forwards verbatim
# (provider, prefix, s3_*, oss_*); all fields optional. Modelling it as a dict
# keeps the sidecar agnostic to which fields a given provider needs.
class CreateBody(BaseModel):
    storage: Optional[dict[str, Any]] = None
    # Admin-tunable idle-recycle TTL (seconds) forwarded by Go. Clamped sidecar
    # side to [1, IDLE_TTL_CAP_SECONDS]. None/0 => fall back to IDLE_TTL_SECONDS.
    idle_ttl_sec: Optional[int] = None
    # Stable archive key (the conversation id) so the workspace tarball survives
    # session recycle (§4.5-C G2). None => archive is keyed by the session id.
    archive_key: Optional[str] = None


class DeleteBody(BaseModel):
    storage: Optional[dict[str, Any]] = None
    # Admin "clear sandbox": tear down WITHOUT archiving, and delete any existing
    # archive under archive_key, so stable-key restore (§4.5-C G2) can't undo the
    # purge. Default False keeps the normal archive-on-release behaviour.
    discard: Optional[bool] = False
    archive_key: Optional[str] = None


class StoragePutBody(BaseModel):
    key: str
    data_base64: str
    content_type: Optional[str] = None
    expires_in: Optional[int] = None
    storage: Optional[dict[str, Any]] = None


class StorageDeleteBody(BaseModel):
    key: str
    storage: Optional[dict[str, Any]] = None


class StorageGCBody(BaseModel):
    # Delete archived workspace tarballs older than max_age_seconds. The Go side
    # forwards the same `storage` block it uses for archive/restore.
    storage: Optional[dict[str, Any]] = None
    max_age_seconds: int


# --- Endpoints --------------------------------------------------------------
@app.post("/sessions")
def create_session(body: Optional[CreateBody] = Body(default=None)):
    # Auth (when SANDBOX_API_KEY is set) is enforced in the middleware below.
    # The Go side forwards a `storage` block here (or {}); a bare POST is fine.
    storage = body.storage if body is not None else None
    with _slot(_create_slots, "session create"):
        _ensure_image_present()
        session_id = uuid.uuid4().hex
        name = _container(session_id)
        args = [
            "run", "-d",
            "--name", name,
            "--label", LABEL,
            "--label", f"aivory.session_id={session_id}",
            "--label", f"aivory.created_at={int(time.time())}",
            # Runner isolation is an invariant, not an operator setting. The
            # sidecar remains reachable from the Go backend on its own service
            # network, while user Python gets no Docker network namespace route
            # and cannot bypass image-input policy with requests/curl.
            "--network", "none",
            "--memory", MEMORY,
            "--memory-swap", MEMORY,
            "--cpus", CPUS,
            "--pids-limit", PIDS_LIMIT,
            "--ulimit", f"nofile={NOFILE_ULIMIT}",
            "--init",
            "--user", "1000:1000",
            "--security-opt", "no-new-privileges",
            "--cap-drop", "ALL",
            "-w", WORKSPACE,
        ]
        # F1 (opt-in): pin a seccomp profile for the session container. Only
        # added when configured so the default (docker's built-in profile)
        # still applies when unset.
        if SECCOMP_PROFILE:
            args.extend(["--security-opt", f"seccomp={SECCOMP_PROFILE}"])
        if READ_ONLY_ROOTFS:
            # F6: rootfs (the pip-installed runtime) is read-only; only the
            # working dirs are writable, and each is a size-bounded tmpfs so a
            # session cannot fill the host disk. /workspace gets mode=1777 so
            # the non-root sandbox user can create files/outputs under it.
            args.extend([
                "--read-only",
                "--tmpfs", f"/tmp:rw,nosuid,nodev,size={TMPFS_SIZE}",
                "--tmpfs", f"/home/sandbox:rw,nosuid,nodev,size={TMPFS_SIZE}",
                "--tmpfs", f"{WORKSPACE}:rw,nosuid,nodev,size={WORKSPACE_SIZE},mode=1777",
            ])
        # F6 (opt-in, best-effort): bound the container writable layer. Needs
        # overlay2 + pquota/prjquota; if the daemon can't honour it, `docker
        # run` errors, so we retry once without it rather than fail the session.
        # Flags go before the image; the trailing command stays last.
        run_cmd = [IMAGE, "sleep", "infinity"]
        if DISK_SIZE:
            args.extend(["--storage-opt", f"size={DISK_SIZE}"])
        # L5: hold _create_count_lock across the count check AND the `docker run`
        # that makes the container live (counted by `docker ps`), so concurrent
        # creators can't both pass the check and overshoot MAX_SESSIONS.
        with _create_count_lock:
            if MAX_SESSIONS > 0 and _count_live_sessions() >= MAX_SESSIONS:
                raise HTTPException(status_code=429, detail="too many active sessions")
            cp = _docker([*args, *run_cmd], timeout=60)
            if cp.returncode != 0 and DISK_SIZE and b"storage-opt" in cp.stderr.lower():
                print(
                    "[sandbox] warning: --storage-opt size is unsupported by the "
                    "docker storage driver (needs overlay2+prjquota); creating the "
                    "session without a writable-layer quota. "
                    f"({cp.stderr.decode(errors='replace')[:160]})"
                )
                # Drop the trailing "--storage-opt size=..." pair and retry. Clear
                # any half-created container holding the name first (best-effort).
                _docker(["rm", "-f", name], timeout=30)
                args = args[:-2]
                cp = _docker([*args, *run_cmd], timeout=60)
        if cp.returncode != 0:
            raise HTTPException(status_code=500, detail=f"docker run failed: {cp.stderr.decode(errors='replace')}")
        # Make sure the standard dirs exist (image already creates them, but be safe).
        mk = _docker(
            ["exec", name, "mkdir", "-p", UPLOADS_DIR, OUTPUTS_DIR, SKILLS_DIR],
            timeout=20,
        )
        if mk.returncode != 0:
            _docker(["rm", "-f", name], timeout=30)
            raise HTTPException(status_code=500, detail=f"workspace init failed: {mk.stderr.decode(errors='replace')}")
        # Pre-warm matplotlib's font cache + CJK matplotlibrc so the FIRST cell
        # that draws a chart renders Chinese instead of tofu boxes (the empty
        # /home/sandbox tmpfs shadows the image-baked cache, so each new
        # container starts cold). Best-effort; never blocks session creation.
        _warm_matplotlib(name)
        # §4.5: now that /workspace is up, restore a previously-archived
        # workspace for this session id from the bucket (best-effort; missing
        # archive or no storage → no-op). Remember the storage block so the
        # reaper / DELETE can archive it back later.
        # §4.5-C G2: remember the stable archive key (conv id) BEFORE restore, so
        # the workspace is restored from <archive_key>.tgz (survives recycle) and
        # not from this fresh session id's (non-existent) archive.
        if body is not None:
            _remember_archive_key(session_id, _sanitize_archive_key(body.archive_key))
        if _storage_effective(storage):
            _restore_workspace(session_id, storage)
            _remember_storage(session_id, storage)
        # Legacy archives may contain user-uploaded or tool-fetched images under
        # the server-managed input directories. Inputs are authoritative in the
        # main application's database and are re-staged for every python call,
        # so clear both directories after restore. /workspace/outputs is left
        # untouched, preserving Python-generated PNGs and other artifacts.
        reset_error = _reset_input_dirs(name)
        if reset_error is not None:
            _docker(["rm", "-f", name], timeout=30)
            _forget(session_id)
            raise HTTPException(status_code=500, detail=f"reset inputs failed: {reset_error}")
        # §4.5 admin-tunable recycle window: remember the (clamped) idle TTL Go
        # forwarded so the reaper honours it for this session. 0/absent => global.
        if body is not None and body.idle_ttl_sec:
            _remember_ttl(session_id, max(1, min(int(body.idle_ttl_sec), IDLE_TTL_CAP_SECONDS)))
        _touch(session_id)
        return {"session_id": session_id}


@app.post("/exec")
def exec_code(body: ExecBody):
    sid = body.session_id
    if not _valid_session(sid):
        raise HTTPException(status_code=400, detail="invalid session_id")
    name = _container(sid)

    # F5: reject oversized source by exact UTF-8 byte length (Field(max_length)
    # only bounds character count).
    code_bytes = body.code.encode("utf-8")
    if len(code_bytes) > MAX_CODE_BYTES:
        raise HTTPException(status_code=413, detail="code too large")

    timeout_ms = body.timeout_ms or DEFAULT_EXEC_TIMEOUT_MS
    timeout_ms = max(1000, min(timeout_ms, EXEC_TIMEOUT_CAP_MS))
    timeout_s = timeout_ms / 1000.0

    # L6: if the session is being torn down (reaper/DELETE holds its lock for the
    # archive), don't block on the lock for up to ARCHIVE_TIMEOUT_S just to 404 —
    # bail now. The Go side then provisions a fresh session.
    if _is_terminating(sid):
        raise HTTPException(status_code=404, detail="session not found or not running")

    with _session_lock(sid), _slot(_exec_slots, "exec"):
        if not _is_running(sid):
            raise HTTPException(status_code=404, detail="session not found or not running")
        cell_token = uuid.uuid4().hex
        cell_path = f"/tmp/aivory-cell-{cell_token}.py"

        # Write the cell to a file in the container (stdin avoids arg-length
        # limits and shell-quoting hazards).
        w = _docker(["exec", "-i", name, "sh", "-c", f"cat > {_shq(cell_path)}"],
                    input_bytes=code_bytes, timeout=30)
        if w.returncode != 0:
            raise HTTPException(status_code=500, detail=f"write cell failed: {w.stderr.decode(errors='replace')}")

        before = _snapshot_outputs(name)

        # `timeout` kills runaway code inside the container; the host-side
        # reader keeps stdout/stderr bounded so the sidecar cannot be OOMed by
        # a print loop. AIVORY_CELL_TOKEN tags every process this cell spawns so
        # the cleanup sweep below can reap any it detached into its own group.
        try:
            exit_code, stdout, stderr = _run_exec_bounded(
                name,
                ["timeout", "--kill-after=2s", _timeout_arg(timeout_s), "python", cell_path],
                timeout=timeout_s + 15,
                env={"AIVORY_CELL_TOKEN": cell_token},
            )
        finally:
            # Reap any background process the cell detached (subprocess / double
            # fork / setsid) so it doesn't keep consuming the session's
            # cpu/mem/pids budget after /exec returns, then drop the cell file.
            _kill_marked_processes(name, cell_token)
            _docker(["exec", name, "rm", "-f", cell_path], timeout=10)

        if exit_code == 124:  # `timeout` convention
            stderr = stderr + f"\n[sandbox] execution exceeded {timeout_s:.3f}s and was killed"

        _touch(sid)

        files, artifact_warning = _collect_new_files(name, before)
        if artifact_warning:
            stderr = (stderr + "\n" + artifact_warning).strip()
        return {
            "stdout": stdout,
            "stderr": stderr,
            "exit_code": exit_code,
            "files": files,
        }


@app.post("/files")
def put_file(body: FilesBody):
    sid = body.session_id
    if not _valid_session(sid):
        raise HTTPException(status_code=400, detail="invalid session_id")
    name = _container(sid)

    max_b64_len = ((MAX_UPLOAD_BYTES + 2) // 3) * 4
    if len(body.data_base64) > max_b64_len:
        raise HTTPException(status_code=413, detail="file too large")
    try:
        data = base64.b64decode(body.data_base64, validate=True)
    except (binascii.Error, ValueError):
        raise HTTPException(status_code=400, detail="invalid base64")
    if len(data) > MAX_UPLOAD_BYTES:
        raise HTTPException(status_code=413, detail="file too large")

    # Normalise the destination to live under /workspace; reject traversal.
    path = body.path or ""
    if not path.startswith("/"):
        path = f"{WORKSPACE}/{path}"
    if not _safe_under_workspace(path):
        raise HTTPException(status_code=400, detail="path must be under /workspace")
    # The sandbox upload API is only for non-image data and skill inputs. Images
    # must travel through a vision-capable provider API; rejecting by both name
    # and file signature prevents a caller from bypassing the Go-side filter by
    # forging metadata or changing an extension.
    if _looks_like_image_input(path, data):
        raise HTTPException(status_code=415, detail="image files are not accepted as sandbox inputs")

    with _session_lock(sid):
        if not _is_running(sid):
            raise HTTPException(status_code=404, detail="session not found or not running")
        parent = path.rsplit("/", 1)[0] or WORKSPACE
        mk = _docker(["exec", name, "mkdir", "-p", parent], timeout=20)
        if mk.returncode != 0:
            raise HTTPException(status_code=500, detail=f"mkdir failed: {mk.stderr.decode(errors='replace')}")

        # F9: the normpath check above is string-only, and `cat >` follows
        # symlinks — so a symlinked parent (e.g. /workspace/x -> /etc) could let
        # an upload escape /workspace and clobber host-visible files in the
        # container. Now that the parent chain exists, resolve the *real* target
        # inside the container and re-verify it still lives under /workspace.
        real_path = _resolve_in_container(name, path)
        if real_path is None or not _safe_under_workspace(real_path):
            raise HTTPException(status_code=400, detail="path resolves outside /workspace")

        # Write to the resolved real path (not the symlink-bearing input path).
        w = _docker(["exec", "-i", name, "sh", "-c", f"cat > {_shq(real_path)}"],
                    input_bytes=data, timeout=60)
        if w.returncode != 0:
            raise HTTPException(status_code=500, detail=f"write failed: {w.stderr.decode(errors='replace')}")

        _touch(sid)
        return {"ok": True}


@app.post("/files/reset-inputs")
def reset_inputs(body: ListFilesBody):
    """Clear server-managed inputs without touching generated workspace output.

    This also removes image copies left by older versions from persistent
    sessions. The paths are fixed server-side; this endpoint is deliberately not
    a caller-controlled general deletion API.
    """
    sid = body.session_id
    if not _valid_session(sid):
        raise HTTPException(status_code=400, detail="invalid session_id")
    name = _container(sid)
    with _session_lock(sid):
        if not _is_running(sid):
            raise HTTPException(status_code=404, detail="session not found or not running")
        reset_error = _reset_input_dirs(name)
        if reset_error is not None:
            raise HTTPException(status_code=500, detail=f"reset inputs failed: {reset_error}")
        _touch(sid)
        return {"ok": True}


@app.delete("/sessions/{session_id}")
def delete_session(session_id: str, body: Optional[DeleteBody] = Body(default=None)):
    if not _valid_session(session_id):
        raise HTTPException(status_code=400, detail="invalid session_id")
    # The Go side forwards the same `storage` block here; fall back to what we
    # remembered at create time if this call didn't carry one.
    storage = body.storage if body is not None else None
    if not _storage_effective(storage):
        storage = _session_storage_for(session_id)
    # L6: flag terminating BEFORE taking the lock so a racing /exec 404s fast
    # instead of waiting out the archive. _forget clears it; the finally is a
    # belt-and-braces clear if archive/rm raises before _forget runs.
    discard = bool(body.discard) if body is not None else False
    archive_key = body.archive_key if body is not None else None
    _set_terminating(session_id, True)
    try:
        with _session_lock(session_id):
            if discard:
                # §4.5-F admin "clear": DON'T archive, and delete the existing
                # archive so a later create can't restore the "cleared" workspace.
                _purge_archive(session_id, storage, archive_key)
            else:
                # §4.5: archive /workspace before tearing the container down (best-
                # effort; no-op without effective storage — dev: deleted = gone).
                _archive_workspace(session_id, storage)
            _docker(["rm", "-f", _container(session_id)], timeout=30)
            _forget(session_id)
    finally:
        _set_terminating(session_id, False)
    return {"ok": True}


@app.post("/files/get")
def get_file(body: GetFileBody):
    sid = body.session_id
    if not _valid_session(sid):
        raise HTTPException(status_code=400, detail="invalid session_id")
    name = _container(sid)

    # Normalise + confine the source to /workspace (mirrors /files).
    path = body.path or ""
    if not path.startswith("/"):
        path = f"{WORKSPACE}/{path}"
    if not _safe_under_workspace(path):
        raise HTTPException(status_code=400, detail="path must be under /workspace")

    with _session_lock(sid):
        if not _is_running(sid):
            raise HTTPException(status_code=404, detail="session not found or not running")
        # F9-style symlink guard: resolve the real target inside the container
        # and re-verify it stays under /workspace before reading it back out.
        real_path = _resolve_in_container(name, path)
        if real_path is None or not _safe_under_workspace(real_path):
            raise HTTPException(status_code=400, detail="path resolves outside /workspace")
        # Reject directories / missing files up front so we 404 instead of
        # streaming a confusing `cat` error.
        check = _docker(["exec", name, "test", "-f", real_path], timeout=20)
        if check.returncode != 0:
            raise HTTPException(status_code=404, detail="file not found")
        # base64 inside the container avoids piping raw bytes through argv.
        cp = _docker(["exec", name, "base64", "-w0", "--", real_path], timeout=120)
        if cp.returncode != 0:
            raise HTTPException(status_code=500, detail=f"read failed: {cp.stderr.decode(errors='replace')}")
        _touch(sid)
        return {"data_base64": cp.stdout.decode(errors="replace").strip()}


@app.post("/files/list")
def list_files(body: ListFilesBody):
    """List every file under /workspace for a session (admin sandbox inspector).

    Returns relative paths + byte sizes. Read-only; no container mutation.
    """
    sid = body.session_id
    if not _valid_session(sid):
        raise HTTPException(status_code=400, detail="invalid session_id")
    name = _container(sid)
    with _session_lock(sid):
        if not _is_running(sid):
            raise HTTPException(status_code=404, detail="session not found or not running")
        # `find` prints "<size>\t<path>" per file, NUL-TERMINATED so a filename
        # containing a newline can't forge extra rows in the admin inspector. The
        # byte cap keeps a runaway workspace from flooding the response; the count
        # is bounded host-side. Paths are relative to /workspace.
        cp = _docker(
            ["exec", name, "sh", "-c",
             f"find {WORKSPACE} -maxdepth 6 -type f -printf '%s\\t%P\\0' 2>/dev/null | head -c 1000000"],
            timeout=30,
        )
        _touch(sid)
        files = []
        if cp.returncode == 0:
            for record in cp.stdout.decode(errors="replace").split("\x00"):
                if "\t" not in record:
                    continue
                size_str, rel = record.split("\t", 1)
                try:
                    size = int(size_str)
                except ValueError:
                    continue
                files.append({"path": rel, "size": size})
        files.sort(key=lambda f: f["path"])
        return {"files": files[:500]}


@app.post("/storage/put")
def storage_put(body: StoragePutBody):
    if not _storage_effective(body.storage):
        raise HTTPException(status_code=400, detail="storage backend not configured")
    full_key = _storage_full_key(body.storage, body.key)
    try:
        data = base64.b64decode(body.data_base64, validate=True)
    except (binascii.Error, ValueError):
        raise HTTPException(status_code=400, detail="invalid base64")

    ttl = _storage_ttl(body.expires_in)
    provider = _storage_provider(body.storage)
    try:
        _storage_put_object(body.storage, full_key, data, body.content_type)
        url = _storage_presign_get(body.storage, full_key, ttl)
    except HTTPException:
        raise
    except Exception as exc:  # noqa: BLE001 - surface SDK errors as 502
        raise HTTPException(status_code=502, detail=f"storage put failed: {exc}")
    return {"provider": provider, "key": full_key, "url": url, "expires_in": ttl}


@app.post("/storage/delete")
def storage_delete(body: StorageDeleteBody):
    if not _storage_effective(body.storage):
        raise HTTPException(status_code=400, detail="storage backend not configured")
    prefix = _storage_prefix(body.storage)
    # The key may be the full (already-prefixed) key returned by /storage/put or
    # a bare key — normalise to the prefixed form, then refuse to delete
    # anything outside the admin's namespace.
    key = (body.key or "").lstrip("/")
    if not key:
        raise HTTPException(status_code=400, detail="invalid storage key")
    if ".." in key or "\x00" in key:
        raise HTTPException(status_code=400, detail="invalid storage key")
    norm_prefix = prefix.rstrip("/") + "/"
    full_key = key if key.startswith(norm_prefix) else norm_prefix + key
    if not full_key.startswith(norm_prefix):
        raise HTTPException(status_code=400, detail="key outside storage prefix")
    try:
        _storage_delete_object(body.storage, full_key)
    except HTTPException:
        raise
    except Exception as exc:  # noqa: BLE001 - surface SDK errors as 502
        raise HTTPException(status_code=502, detail=f"storage delete failed: {exc}")
    return {"ok": True, "key": full_key}


@app.post("/storage/gc")
def storage_gc(body: StorageGCBody):
    """§4.5 object-storage GC: delete objects older than max_age_seconds so the
    bucket doesn't grow without bound. Two sweeps, both age-bounded:
      - `<prefix>/workspaces/*.tgz` — one workspace tarball piles up per reaped
        session and is never otherwise removed.
      - `<prefix>/mineru/*`        — RAG upload objects whose best-effort delete
        failed (orphans); legitimate ones live only minutes, so anything older
        than the (days-scale) TTL is safe to prune.
    The Go side calls this on a timer with the admin-configured TTL. Safety: only
    objects UNDER the two known prefixes, only those strictly older than cutoff,
    and NEVER an object whose timestamp couldn't be read (treated as 'keep')."""
    if not _storage_effective(body.storage):
        # Nothing to prune. Go's Effective() returns true for "local" without
        # seeing the sidecar's dir, so an admin who sets an archive TTL but never
        # mounts SANDBOX_LOCAL_STORAGE_DIR would otherwise get a hard 400 logged
        # every GC cycle — treat "not configured" as an empty sweep instead.
        return {"deleted": 0, "scanned": 0, "freed_bytes": 0}
    max_age = int(body.max_age_seconds or 0)
    if max_age <= 0:
        # 0 / negative means "disabled" — never mass-delete on a misconfig.
        return {"deleted": 0, "scanned": 0, "freed_bytes": 0}
    cutoff = time.time() - max_age
    base = _storage_prefix(body.storage).rstrip("/")
    ws_prefix = _workspace_archive_prefix(body.storage)  # <base>/workspaces/
    mineru_prefix = base + "/mineru/"
    # (prefix, require_tgz_suffix)
    sweeps = [(ws_prefix, True), (mineru_prefix, False)]
    deleted = scanned = freed = 0
    try:
        for prefix, require_tgz in sweeps:
            for key, last_modified, size in _storage_list(body.storage, prefix):
                scanned += 1
                # Defensive: the listing is already prefix-scoped, but re-check
                # so a surprising key never gets deleted.
                if not key.startswith(prefix):
                    continue
                if require_tgz and not key.endswith(".tgz"):
                    continue
                # Unknown/zero timestamp => KEEP. A listing that fails to surface
                # LastModified must not look "ancient" and get mass-deleted —
                # fail safe, not fail open.
                if last_modified <= 0:
                    continue
                if last_modified >= cutoff:
                    continue
                try:
                    _storage_delete_object(body.storage, key)
                    deleted += 1
                    freed += size
                except Exception as exc:  # noqa: BLE001 - best-effort per object
                    print(f"[sandbox] gc: delete {key} failed: {exc}")
    except HTTPException:
        raise
    except Exception as exc:  # noqa: BLE001 - surface SDK errors as 502
        raise HTTPException(status_code=502, detail=f"storage gc failed: {exc}")
    if deleted:
        print(f"[sandbox] gc: deleted {deleted}/{scanned} stale object(s) "
              f"older than {max_age}s ({freed} bytes freed)")
    return {"deleted": deleted, "scanned": scanned, "freed_bytes": freed}


@app.get("/healthz")
def healthz():
    try:
        cp = _docker(["version", "-f", "{{.Server.Version}}"], timeout=10)
    except subprocess.TimeoutExpired:
        return JSONResponse(status_code=503, content={"ok": False, "docker": "", "image": IMAGE})
    ok = cp.returncode == 0
    return JSONResponse(
        status_code=200 if ok else 503,
        content={
            "ok": ok,
            "docker": cp.stdout.decode(errors="replace").strip(),
            "image": IMAGE,
            # False here (with docker otherwise healthy) means the runtime image
            # isn't cached yet — the NEXT /sessions call will pull it inline
            # (_ensure_image_present) rather than failing outright, but it's
            # useful to see this from monitoring instead of only from a slow
            # first session create.
            "image_ready": _image_ready.is_set(),
        },
    )


# --- Output collection ------------------------------------------------------
def _snapshot_outputs(name: str) -> dict[str, str]:
    """Map of path -> "mtime|size" for every file under /workspace/outputs."""
    cp = _docker(
        ["exec", name, "sh", "-c",
         f"find {OUTPUTS_DIR} -type f -printf '%p\\t%T@\\t%s\\n' 2>/dev/null || true"],
        timeout=20,
    )
    snap: dict[str, str] = {}
    for line in cp.stdout.decode(errors="replace").splitlines():
        parts = line.split("\t")
        if len(parts) == 3:
            snap[parts[0]] = f"{parts[1]}|{parts[2]}"
    return snap


def _collect_new_files(name: str, before: dict[str, str]) -> tuple[list[dict], str]:
    after = _snapshot_outputs(name)
    changed = [p for p, meta in after.items() if before.get(p) != meta]
    changed.sort()
    files: list[dict] = []
    total_bytes = 0
    skipped = 0
    start = time.monotonic()
    for i, path in enumerate(changed):
        # L4: stop once the collection budget is spent so a run emitting many
        # large files can't pin the exec slot (or outlast the Go client deadline)
        # base64-ing them. Remaining files are counted as skipped.
        if time.monotonic() - start > MAX_COLLECT_SECONDS:
            skipped += len(changed) - i
            break
        size = int(after[path].split("|")[1])
        if size > MAX_ARTIFACT_BYTES:
            skipped += 1
            continue  # §4.5: single artifact ≤ 20MB
        if len(files) >= MAX_FILES_PER_EXEC:
            skipped += 1
            continue
        if total_bytes + size > MAX_TOTAL_ARTIFACT_BYTES:
            skipped += 1
            continue  # §4.5: single artifact ≤ 20MB
        cp = _docker(["exec", name, "base64", "-w0", path], timeout=COLLECT_FILE_TIMEOUT_S)
        if cp.returncode != 0:
            skipped += 1
            continue
        b64 = cp.stdout.decode(errors="replace").strip()
        basename = path.rsplit("/", 1)[-1]
        mime = mimetypes.guess_type(basename)[0] or "application/octet-stream"
        files.append({"name": basename, "mime_type": mime, "data_base64": b64})
        total_bytes += size
    warning = ""
    if skipped:
        warning = (
            f"[sandbox] skipped {skipped} output file(s) because artifact limits "
            f"are {MAX_FILES_PER_EXEC} files, {MAX_ARTIFACT_BYTES} bytes per file, "
            f"{MAX_TOTAL_ARTIFACT_BYTES} bytes total"
        )
    return files, warning


# --- Path safety ------------------------------------------------------------
_IMAGE_INPUT_EXTENSIONS = {
    ".apng", ".avif", ".bmp", ".cr2", ".cur", ".dng", ".eps", ".gif",
    ".heic", ".heif", ".ico", ".jfif", ".jpe", ".jpeg", ".jpg", ".jxl",
    ".nef", ".png", ".psd", ".raw", ".svg", ".tif", ".tiff", ".webp",
}


def _looks_like_image_input(path: str, data: bytes) -> bool:
    """Recognise common image inputs without trusting caller-provided metadata."""
    if os.path.splitext(path.lower())[1] in _IMAGE_INPUT_EXTENSIONS:
        return True
    head = data[:4096]
    if head.startswith((b"\x89PNG\r\n\x1a\n", b"\xff\xd8\xff", b"GIF87a", b"GIF89a", b"BM")):
        return True
    if head.startswith((b"II*\x00", b"MM\x00*", b"\x00\x00\x01\x00", b"\x00\x00\x02\x00")):
        return True
    if len(head) >= 12 and head[:4] == b"RIFF" and head[8:12] == b"WEBP":
        return True
    if head.startswith((b"\xff\x0a", b"\x00\x00\x00\x0cJXL \r\n\x87\n", b"8BPS")):
        return True
    # AVIF/HEIF use ISO-BMFF. Check the major/compatible brand area near ftyp.
    if len(head) >= 12 and head[4:8] == b"ftyp":
        brands = head[8:64]
        if any(brand in brands for brand in (
            b"avif", b"avis", b"heic", b"heix", b"hevc", b"hevx", b"heim",
            b"heis", b"mif1", b"msf1",
        )):
            return True
    # SVG is XML/text and therefore has no binary magic. Only inspect the head;
    # this catches BOM/XML declarations and leading comments without scanning an
    # arbitrary large text asset.
    if re.search(br"<svg(?:\s|>)", head, flags=re.IGNORECASE):
        return True
    return False


def _reset_input_dirs(name: str) -> Optional[str]:
    """Drop staged inputs while preserving /workspace/outputs and other state."""
    script = (
        f"rm -rf -- {_shq(UPLOADS_DIR)} {_shq(SKILLS_DIR)} && "
        f"mkdir -p -- {_shq(UPLOADS_DIR)} {_shq(SKILLS_DIR)}"
    )
    cp = _docker(["exec", name, "sh", "-c", script], timeout=30)
    if cp.returncode != 0:
        return cp.stderr.decode(errors="replace")[:300]
    return None


def _safe_under_workspace(path: str) -> bool:
    # collapse and ensure it stays under /workspace (no .. escape)
    norm = os.path.normpath(path)
    return norm == WORKSPACE or norm.startswith(WORKSPACE + "/")


def _resolve_in_container(name: str, path: str) -> Optional[str]:
    """Resolve `path`'s real location *inside the container*, following any
    symlinks in its (already-created) parent chain.

    `realpath -m` canonicalises the path without requiring the final component
    to exist, so a brand-new upload filename resolves fine while a symlinked
    ancestor (e.g. /workspace/x -> /etc) is followed and exposed to the caller's
    re-check. Returns the resolved absolute path, or None if it can't be
    resolved (treated as unsafe by the caller).
    """
    cp = _docker(["exec", name, "realpath", "-m", "--", path], timeout=20)
    if cp.returncode != 0:
        return None
    resolved = cp.stdout.decode(errors="replace").strip()
    return resolved or None


def _shq(s: str) -> str:
    return "'" + s.replace("'", "'\\''") + "'"


# --- Object storage (archive/restore + /storage/*) --------------------------
# boto3 (s3) and oss2 (aliyun_oss) are LAZY-imported the first time a provider
# is actually used, so the sidecar starts fine without them installed when no
# storage backend is configured. Clients are cached keyed by the full creds
# tuple so repeated calls reuse one connection pool.
_storage_clients: dict[tuple, Any] = {}
_storage_clients_lock = threading.Lock()


def _storage_get(storage: Optional[dict], key: str) -> str:
    return str((storage or {}).get(key) or "").strip()


def _storage_provider(storage: Optional[dict]) -> str:
    return _storage_get(storage, "provider")


def _storage_prefix(storage: Optional[dict]) -> str:
    return _storage_get(storage, "prefix") or STORAGE_DEFAULT_PREFIX


def _storage_effective(storage: Optional[dict]) -> bool:
    """Mirror of StorageConfig.Effective() on the Go side: a usable backend
    needs a known provider plus the bucket (and, for OSS, endpoint + creds)."""
    provider = _storage_provider(storage)
    if provider == "s3":
        return bool(_storage_get(storage, "s3_bucket"))
    if provider == "aliyun_oss":
        return bool(
            _storage_get(storage, "oss_bucket")
            and _storage_get(storage, "oss_endpoint")
            and _storage_get(storage, "oss_access_key_id")
            and _storage_get(storage, "oss_access_key_secret")
        )
    if provider == "local":
        # Effective only when the operator actually mounted a dir. A missing mount
        # then degrades to "no archive" (fail-safe) instead of writing into the
        # sidecar's ephemeral rootfs.
        return bool(LOCAL_STORAGE_DIR) and os.path.isdir(LOCAL_STORAGE_DIR)
    return False


def _validate_object_key(key: str) -> str:
    """Reject keys that could escape the configured prefix or break the request
    (path traversal, absolute paths, embedded NUL). Returns the key unchanged."""
    if not key or ".." in key or key.startswith("/") or "\x00" in key:
        raise HTTPException(status_code=400, detail="invalid storage key")
    return key


def _storage_full_key(storage: Optional[dict], key: str) -> str:
    """Join the admin prefix and the caller key: <prefix>/<key>."""
    _validate_object_key(key)
    return _storage_prefix(storage).rstrip("/") + "/" + key


def _local_path(full_key: str) -> str:
    """Resolve an object key to an absolute path confined under LOCAL_STORAGE_DIR.
    Reuses the object-key guard, then asserts containment via realpath so a
    crafted key or a symlink can't escape the archive dir (defence in depth for
    keys that arrive from a directory listing, not just from _storage_full_key)."""
    _validate_object_key(full_key)
    base = os.path.realpath(LOCAL_STORAGE_DIR)
    p = os.path.realpath(os.path.join(base, full_key))
    if p != base and not p.startswith(base + os.sep):
        raise HTTPException(status_code=400, detail="key escapes local storage dir")
    return p


def _storage_ttl(expires_in: Optional[int]) -> int:
    ttl = expires_in or STORAGE_DEFAULT_TTL
    if ttl <= 0:
        ttl = STORAGE_DEFAULT_TTL
    return min(ttl, STORAGE_MAX_TTL)


def _s3_creds_key(storage: dict) -> tuple:
    return (
        "s3",
        _storage_get(storage, "s3_bucket"),
        _storage_get(storage, "s3_region"),
        _storage_get(storage, "s3_endpoint"),
        _storage_get(storage, "s3_access_key"),
        _storage_get(storage, "s3_secret_key"),
    )


def _oss_creds_key(storage: dict) -> tuple:
    return (
        "aliyun_oss",
        _storage_get(storage, "oss_bucket"),
        _storage_get(storage, "oss_endpoint"),
        _storage_get(storage, "oss_access_key_id"),
        _storage_get(storage, "oss_access_key_secret"),
    )


def _storage_client(storage: dict) -> tuple:
    """Return (provider, client_or_bucket) for the configured backend, caching
    the underlying SDK object keyed by the full creds tuple. boto3 / oss2 are
    imported only here, only when a backend is actually used."""
    provider = _storage_provider(storage)
    if provider == "s3":
        cache_key = _s3_creds_key(storage)
    elif provider == "aliyun_oss":
        cache_key = _oss_creds_key(storage)
    elif provider == "local":
        cache_key = ("local", LOCAL_STORAGE_DIR)
    else:
        raise HTTPException(status_code=400, detail=f"unsupported storage provider: {provider or '(none)'}")

    with _storage_clients_lock:
        cached = _storage_clients.get(cache_key)
        if cached is not None:
            return provider, cached

        if provider == "s3":
            try:
                import boto3  # type: ignore
            except ImportError as exc:  # pragma: no cover - dep is pinned
                raise HTTPException(status_code=500, detail=f"boto3 not installed: {exc}")
            kwargs: dict[str, Any] = {}
            region = _storage_get(storage, "s3_region")
            endpoint = _storage_get(storage, "s3_endpoint")
            access = _storage_get(storage, "s3_access_key")
            secret = _storage_get(storage, "s3_secret_key")
            if region:
                kwargs["region_name"] = region
            if endpoint:
                kwargs["endpoint_url"] = endpoint
            if access and secret:
                kwargs["aws_access_key_id"] = access
                kwargs["aws_secret_access_key"] = secret
            # Bound every SDK call: archive/restore/GC run while holding the
            # session lock / a create slot, so a slow or hung bucket would
            # otherwise freeze the reaper, DELETE, and session creation.
            try:
                from botocore.config import Config as _BotoConfig  # type: ignore
                cfg_kwargs: dict[str, Any] = dict(
                    connect_timeout=S3_CONNECT_TIMEOUT_S, read_timeout=S3_READ_TIMEOUT_S,
                    retries={"max_attempts": S3_MAX_ATTEMPTS, "mode": "standard"},
                )
                if endpoint:
                    # A custom endpoint means a non-AWS, S3-compatible store
                    # (MinIO, Ceph RGW, SeaweedFS, …). Those serve PATH-style
                    # addressing (host/bucket), not virtual-hosted (bucket.host),
                    # and expect SigV4 — force both so MinIO works once the admin
                    # just fills in the endpoint. Harmless for real AWS since it
                    # only applies when a custom endpoint is present.
                    cfg_kwargs["s3"] = {"addressing_style": "path"}
                    cfg_kwargs["signature_version"] = "s3v4"
                kwargs["config"] = _BotoConfig(**cfg_kwargs)
            except ImportError:
                pass
            client: Any = boto3.client("s3", **kwargs)
        elif provider == "aliyun_oss":
            try:
                import oss2  # type: ignore
            except ImportError as exc:  # pragma: no cover - dep is pinned
                raise HTTPException(status_code=500, detail=f"oss2 not installed: {exc}")
            auth = oss2.Auth(
                _storage_get(storage, "oss_access_key_id"),
                _storage_get(storage, "oss_access_key_secret"),
            )
            client = oss2.Bucket(
                auth,
                _storage_get(storage, "oss_endpoint"),
                _storage_get(storage, "oss_bucket"),
                connect_timeout=OSS_CONNECT_TIMEOUT_S,  # bound the call so a slow bucket can't wedge the session lock
            )
        else:  # local
            # The "client" is just the base dir; filesystem ops resolve keys
            # against it via _local_path. No SDK, no connection pool.
            if not LOCAL_STORAGE_DIR:
                raise HTTPException(status_code=400, detail="local storage dir not configured")
            client = LOCAL_STORAGE_DIR

        _storage_clients[cache_key] = client
        return provider, client


def _storage_put_object(storage: dict, full_key: str, data: bytes,
                        content_type: Optional[str]) -> None:
    """Upload bytes under the already-prefixed `full_key`."""
    provider, client = _storage_client(storage)
    ctype = content_type or "application/octet-stream"
    if provider == "s3":
        client.put_object(
            Bucket=_storage_get(storage, "s3_bucket"),
            Key=full_key,
            Body=data,
            ContentType=ctype,
        )
    elif provider == "aliyun_oss":
        client.put_object(full_key, data, headers={"Content-Type": ctype})
    else:  # local — ctype is not persisted (see presign gap in DESIGN §4.5).
        path = _local_path(full_key)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        tmp = f"{path}.tmp-{os.getpid()}-{threading.get_ident()}"
        with open(tmp, "wb") as f:
            f.write(data)
        os.replace(tmp, path)  # atomic within the same filesystem


def _storage_presign_get(storage: dict, full_key: str, ttl: int) -> str:
    """Presigned GET URL for `full_key`, valid for ttl seconds."""
    provider, client = _storage_client(storage)
    if provider == "s3":
        return client.generate_presigned_url(
            "get_object",
            Params={"Bucket": _storage_get(storage, "s3_bucket"), "Key": full_key},
            ExpiresIn=ttl,
        )
    if provider == "aliyun_oss":
        # oss2: slash_safe keeps the "/" in the key path un-escaped.
        return client.sign_url("GET", full_key, ttl, slash_safe=True)
    # local has no externally-fetchable URL. Workspace archive/restore never
    # needs one (the sidecar moves bytes directly), but the RAG/MinerU flow does
    # — so fail loudly here rather than hand back a dead URL. local => object
    # storage is required for MinerU document parsing (see DESIGN §4.5).
    raise HTTPException(status_code=400, detail="presigned URLs are unsupported for local storage")


def _storage_get_object(storage: dict, full_key: str,
                        max_bytes: Optional[int] = None) -> Optional[bytes]:
    """Download `full_key`. Returns None when the object is absent (so callers
    can treat a missing workspace archive as "nothing to restore"). When
    `max_bytes` is set, reads at most that many bytes and raises ValueError if
    the object is larger — so a giant/poisoned archive can't be buffered
    unbounded into the sidecar."""
    cap = (max_bytes + 1) if max_bytes is not None else None
    provider, client = _storage_client(storage)
    if provider == "s3":
        try:
            resp = client.get_object(
                Bucket=_storage_get(storage, "s3_bucket"), Key=full_key
            )
        except Exception as exc:  # noqa: BLE001 - normalise not-found to None
            if _is_not_found(provider, exc):
                return None
            raise
        body = resp.get("Body")
        if body is None:
            data = b""
        else:
            data = body.read(cap) if cap is not None else body.read()
    elif provider == "aliyun_oss":
        try:
            resp = client.get_object(full_key)
        except Exception as exc:  # noqa: BLE001
            if _is_not_found(provider, exc):
                return None
            raise
        data = resp.read(cap) if cap is not None else resp.read()
    else:  # local
        try:
            with open(_local_path(full_key), "rb") as f:
                data = f.read(cap) if cap is not None else f.read()
        except FileNotFoundError:
            return None
    if max_bytes is not None and len(data) > max_bytes:
        raise ValueError(f"object {full_key} exceeds {max_bytes} bytes")
    return data


def _storage_delete_object(storage: dict, full_key: str) -> None:
    """Delete `full_key`, swallowing not-found (idempotent)."""
    provider, client = _storage_client(storage)
    try:
        if provider == "s3":
            client.delete_object(
                Bucket=_storage_get(storage, "s3_bucket"), Key=full_key
            )
        elif provider == "aliyun_oss":
            client.delete_object(full_key)
        else:  # local
            os.remove(_local_path(full_key))
    except Exception as exc:  # noqa: BLE001
        if _is_not_found(provider, exc):
            return
        raise


def _is_not_found(provider: str, exc: Exception) -> bool:
    """Best-effort 'object does not exist' classifier across all backends, so a
    delete of an absent key is idempotent and a missing archive restores as a
    no-op."""
    if isinstance(exc, FileNotFoundError):  # local backend
        return True
    if type(exc).__name__ in ("NoSuchKey", "NoSuchBucket", "NotFound"):
        return True
    if provider == "s3":
        # botocore.ClientError carries a structured .response dict.
        resp = getattr(exc, "response", None)
        if isinstance(resp, dict):
            code = resp.get("Error", {}).get("Code", "")
            if code in ("NoSuchKey", "NoSuchBucket", "404", "NotFound"):
                return True
            if resp.get("ResponseMetadata", {}).get("HTTPStatusCode") == 404:
                return True
        return False
    # oss2 raises oss2.exceptions.NoSuchKey (subclass of ServerError) with .status
    return getattr(exc, "status", None) == 404


# --- Workspace archive / restore (§4.5) -------------------------------------
def _workspace_archive_key(storage: dict, stem: str) -> str:
    """Object key for a workspace tarball: <prefix>/workspaces/<stem>.tgz. `stem`
    is the stable archive key (conversation id) when the Go side forwarded one,
    else the session id — see _archive_stem."""
    return _workspace_archive_prefix(storage) + f"{stem}.tgz"


def _workspace_archive_prefix(storage: dict) -> str:
    """The object-key prefix every workspace tarball lives under
    (<prefix>/workspaces/). The GC sweep lists + prunes exactly this prefix, so
    it MUST stay in lockstep with _workspace_archive_key's layout."""
    return _storage_prefix(storage).rstrip("/") + "/workspaces/"


def _storage_list(storage: dict, prefix: str) -> Iterator[tuple[str, float, int]]:
    """Yield (key, last_modified_epoch, size_bytes) for every object under
    `prefix`. Paginates so a large bucket isn't silently truncated to one page."""
    provider, client = _storage_client(storage)
    if provider == "s3":
        bucket = _storage_get(storage, "s3_bucket")
        token: Optional[str] = None
        while True:
            kwargs: dict[str, Any] = {"Bucket": bucket, "Prefix": prefix}
            if token:
                kwargs["ContinuationToken"] = token
            resp = client.list_objects_v2(**kwargs)
            for obj in resp.get("Contents", []) or []:
                lm = obj.get("LastModified")
                epoch = lm.timestamp() if lm is not None else 0.0
                yield obj["Key"], epoch, int(obj.get("Size", 0) or 0)
            if resp.get("IsTruncated") and resp.get("NextContinuationToken"):
                token = resp["NextContinuationToken"]
                continue
            break
    elif provider == "aliyun_oss":
        import oss2  # type: ignore
        # ObjectIterator yields SimplifiedObjectInfo: .key, .last_modified
        # (epoch seconds), .size — and pages internally.
        for obj in oss2.ObjectIterator(client, prefix=prefix):
            yield (
                obj.key,
                float(getattr(obj, "last_modified", 0) or 0),
                int(getattr(obj, "size", 0) or 0),
            )
    else:  # local
        base = os.path.realpath(LOCAL_STORAGE_DIR)
        root = _local_path(prefix) if prefix else base
        if not os.path.isdir(root):
            return
        for dirpath, _dirs, files in os.walk(root):
            for fn in files:
                fp = os.path.join(dirpath, fn)
                # Yield the key RELATIVE to base with "/" separators, so the GC
                # sweep's startswith(prefix)/`.tgz` checks and _storage_delete_object
                # behave identically to the object-store backends.
                key = os.path.relpath(fp, base).replace(os.sep, "/")
                try:
                    st = os.stat(fp)
                except FileNotFoundError:
                    continue
                yield key, st.st_mtime, int(st.st_size)


def _archive_workspace(session_id: str, storage: Optional[dict]) -> None:
    """Best-effort: tar /workspace from the container and upload it. A no-op
    when storage is absent/ineffective. Never raises — logs and returns so it
    can't crash reap / delete."""
    if not _storage_effective(storage):
        return
    name = _container(session_id)
    try:
        proc = subprocess.Popen(
            [
                "docker", "exec", name, "tar", "czf", "-",
                "--exclude=./uploads", "--exclude=./skills",
                "-C", WORKSPACE, ".",
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        assert proc.stdout is not None
        buf = bytearray()
        too_big = False
        timed_out = False
        start = time.monotonic()
        # Read with a wall-clock deadline: select() makes the read interruptible
        # so a wedged `docker exec tar` can't block here forever while the reaper
        # / DELETE hold the session lock.
        while True:
            if time.monotonic() - start > ARCHIVE_TIMEOUT_S:
                timed_out = True
                proc.kill()
                break
            ready, _, _ = select.select([proc.stdout], [], [], ARCHIVE_READ_POLL_S)
            if not ready:
                continue
            chunk = proc.stdout.read1(ARCHIVE_READ_CHUNK_BYTES)
            if not chunk:
                break
            if len(buf) + len(chunk) > MAX_ARCHIVE_BYTES:
                too_big = True
                proc.kill()
                break
            buf.extend(chunk)
        try:
            stderr = proc.stderr.read() if proc.stderr is not None else b""
        finally:
            try:
                rc = proc.wait(timeout=ARCHIVE_READ_WAIT_GRACE_S)
            except subprocess.TimeoutExpired:
                proc.kill()
                rc = -1
        if timed_out:
            print(f"[sandbox] warning: archive of {session_id} exceeded "
                  f"{ARCHIVE_TIMEOUT_S}s; skipping")
            return
        if too_big:
            print(f"[sandbox] warning: workspace for {session_id} exceeds "
                  f"{MAX_ARCHIVE_BYTES} bytes; skipping archive")
            return
        if rc != 0:
            print(f"[sandbox] warning: tar of workspace {session_id} failed "
                  f"(rc={rc}): {stderr.decode(errors='replace')[:200]}")
            return
        full_key = _workspace_archive_key(storage, _archive_stem(session_id))
        _storage_put_object(storage, full_key, bytes(buf), "application/gzip")
        print(f"[sandbox] archived workspace {session_id} -> {full_key} "
              f"({len(buf)} bytes)")
    except HTTPException as exc:
        print(f"[sandbox] warning: archive of {session_id} failed: {exc.detail}")
    except Exception as exc:  # noqa: BLE001 - archive is best-effort
        print(f"[sandbox] warning: archive of {session_id} failed: {exc}")


def _purge_archive(session_id: str, storage: Optional[dict], archive_key: Optional[str]) -> None:
    """Best-effort: delete this session's workspace tarball so an admin "clear"
    is a real purge that stable-key restore (§4.5-C G2) can't undo. Prefers the
    forwarded archive_key (works even if the session was already reaped and its
    remembered key is gone); falls back to the session's stem. Never raises."""
    if not _storage_effective(storage):
        return
    stem = _sanitize_archive_key(archive_key) or _archive_stem(session_id)
    try:
        _storage_delete_object(storage, _workspace_archive_key(storage, stem))
        print(f"[sandbox] purged workspace archive (key={stem})")
    except HTTPException as exc:
        print(f"[sandbox] warning: purge archive for {session_id} failed: {exc.detail}")
    except Exception as exc:  # noqa: BLE001 - purge is best-effort
        print(f"[sandbox] warning: purge archive for {session_id} failed: {exc}")


def _restore_workspace(session_id: str, storage: Optional[dict]) -> None:
    """Best-effort: download <prefix>/workspaces/<session_id>.tgz and untar it
    into the live container's /workspace. Missing archive → skip silently.
    Never raises."""
    if not _storage_effective(storage):
        return
    name = _container(session_id)
    try:
        full_key = _workspace_archive_key(storage, _archive_stem(session_id))
        # Bound the download (an oversized/poisoned archive can't OOM the sidecar)
        # and extract with --no-same-owner so the archive can't impose ownership.
        # Path/symlink escape is contained by the read-only rootfs + the bounded
        # /workspace tmpfs (a decompressed bomb hits ENOSPC, not the host).
        data = _storage_get_object(storage, full_key, max_bytes=MAX_ARCHIVE_BYTES)
        if data is None:
            return  # nothing archived for this session yet
        cp = _docker(["exec", "-i", name, "tar", "--no-same-owner", "-xzf", "-", "-C", WORKSPACE],
                     input_bytes=data, timeout=120)
        if cp.returncode != 0:
            print(f"[sandbox] warning: restore untar for {session_id} failed: "
                  f"{cp.stderr.decode(errors='replace')[:200]}")
            return
        print(f"[sandbox] restored workspace {session_id} from {full_key} "
              f"({len(data)} bytes)")
    except HTTPException as exc:
        print(f"[sandbox] warning: restore of {session_id} failed: {exc.detail}")
    except Exception as exc:  # noqa: BLE001 - restore is best-effort
        print(f"[sandbox] warning: restore of {session_id} failed: {exc}")


# --- Idle reaper ------------------------------------------------------------
IDLE_REAPER_SWEEP_SECONDS = float(os.environ.get("SANDBOX_IDLE_REAPER_SWEEP_INTERVAL", "300"))


def _reaper() -> None:
    while True:
        time.sleep(IDLE_REAPER_SWEEP_SECONDS)
        now = time.time()
        with _state_lock:
            # Per-session TTL (admin-forwarded) wins over the global default. Read
            # inline while holding _state_lock — it is NOT reentrant, so don't call
            # a helper that re-acquires it.
            stale = [sid for sid, t in _last_used.items()
                     if now - t > (_session_ttl.get(sid) or IDLE_TTL_SECONDS)]
        for sid in stale:
            try:
                with _session_lock(sid):
                    # Re-validate staleness now that we hold the session lock. A
                    # concurrent /exec (or /files*) could have won the lock between
                    # the snapshot above and here, done real work, and refreshed
                    # _last_used via _touch. Without this re-check the reaper would
                    # rm -f a session that was used seconds ago, yanking its
                    # /workspace out from under an active conversation.
                    with _state_lock:
                        last = _last_used.get(sid)
                        ttl = _session_ttl.get(sid) or IDLE_TTL_SECONDS
                    if last is not None and time.time() - last <= ttl:
                        continue
                    # L6: flag terminating so a /exec that arrives during the
                    # archive 404s fast instead of blocking on the lock.
                    _set_terminating(sid, True)
                    try:
                        # §4.5: archive /workspace before the TTL kill so the next
                        # session for this id can restore it. Best-effort, no-op
                        # when storage is absent (dev: reaped = gone).
                        _archive_workspace(sid, _session_storage_for(sid))
                        _docker(["rm", "-f", _container(sid)], timeout=30)
                        _forget(sid)
                    finally:
                        _set_terminating(sid, False)
            except HTTPException:
                print(f"[sandbox] warning: session {sid} stayed busy during reap")
            except Exception as exc:  # noqa: BLE001 - one bad session must never kill the reaper
                # e.g. `docker rm` timing out would otherwise propagate out of the
                # reaper thread and stop ALL future reaping. Log and move on.
                print(f"[sandbox] warning: reap of {sid} failed: {exc}")


@app.on_event("startup")
def _start_reaper() -> None:
    if PULL_ON_START:
        # Warm the runtime image in the BACKGROUND (host daemon; not affected
        # by the mandatory per-session --network none). This is an optimization, never a
        # startup gate: a slow registry/proxy used to raise TimeoutExpired out
        # of this hook and crash-loop the whole sidecar. Cold-cache sessions
        # still pull lazily on first use if this hasn't finished.
        def _warm_pull() -> None:
            try:
                cp = _docker(["pull", IMAGE], timeout=3600)
                if cp.returncode != 0:
                    print(f"[sandbox] warning: startup pull of {IMAGE} failed: "
                          f"{cp.stderr.decode(errors='replace')[:200]}")
                else:
                    print(f"[sandbox] runtime image {IMAGE} ready")
            except Exception as exc:  # noqa: BLE001 - warm-up must never kill startup
                print(f"[sandbox] warning: startup pull of {IMAGE} failed: {exc}")

        threading.Thread(target=_warm_pull, daemon=True).start()
    if LOCAL_STORAGE_DIR:
        # §4.5 local archive backend: ensure the mount point exists so
        # _storage_effective("local") turns on. Best-effort — a failure just
        # leaves local archiving off (fail-safe).
        try:
            os.makedirs(LOCAL_STORAGE_DIR, exist_ok=True)
        except OSError as exc:
            print(f"[sandbox] warning: cannot create local storage dir {LOCAL_STORAGE_DIR}: {exc}")
    _discover_sessions()
    threading.Thread(target=_reaper, daemon=True).start()


# --- Bearer-auth middleware -------------------------------------------------
# F2: with the startup fail-closed guard above, API_KEY is guaranteed non-empty
# in normal operation (empty only when the operator set SANDBOX_ALLOW_NO_AUTH=1
# for a trusted localhost-only dev box). The constant-time compare is kept.
@app.middleware("http")
async def _auth_mw(request, call_next):
    if API_KEY and request.url.path not in ("/healthz",):
        if not secrets.compare_digest(request.headers.get("authorization", ""), f"Bearer {API_KEY}"):
            return JSONResponse(status_code=401, content={"error": "unauthorized"})
    return await call_next(request)


# --- F5: request body size guard (control-plane OOM) ------------------------
# Registered last so it runs OUTERMOST — oversized requests are rejected on the
# Content-Length header before the body (or auth, or any handler) is read.
@app.middleware("http")
async def _body_size_mw(request, call_next):
    # /storage/put carries whole documents (RAG → MinerU), which legitimately
    # exceed the F5 cap that guards /exec & /files; it gets a higher ceiling.
    limit = MAX_STORAGE_BODY_BYTES if request.url.path == "/storage/put" else MAX_BODY_BYTES
    raw_len = request.headers.get("content-length")
    if raw_len is not None:
        try:
            declared = int(raw_len)
        except ValueError:
            return JSONResponse(status_code=400, content={"error": "invalid content-length"})
        if declared > limit:
            return JSONResponse(
                status_code=413,
                content={"error": f"request body exceeds {limit} bytes"},
            )
    return await call_next(request)
