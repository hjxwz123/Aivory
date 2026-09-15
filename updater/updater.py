#!/usr/bin/env python3
"""A narrowly scoped, persistent updater for the Aivory app container."""

from __future__ import annotations

import hmac
import json
import os
import re
import secrets
import stat
import subprocess
import threading
import time
import uuid
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


VERSION_RE = re.compile(r"^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$")
ENV_KEY_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


class Settings:
    def __init__(self) -> None:
        self.listen = os.getenv("AIVORY_UPDATER_LISTEN", "0.0.0.0:8790")
        self.state_dir = Path(os.getenv("AIVORY_UPDATER_STATE_DIR", "/var/lib/aivory-updater"))
        self.token_file = Path(os.getenv("AIVORY_UPDATER_TOKEN_FILE", "/shared/.aivory-update-token"))
        self.compose_file = Path(os.getenv("AIVORY_UPDATER_COMPOSE_FILE", "/deployment/docker-compose.prod.yml"))
        self.env_file = Path(os.getenv("AIVORY_UPDATER_ENV_FILE", "/deployment/.env"))
        self.app_service = os.getenv("AIVORY_UPDATER_APP_SERVICE", "app")
        self.image_registry = os.getenv("AIVORY_UPDATER_IMAGE_REGISTRY", "ghcr.io").rstrip("/")
        self.image_owner = os.getenv("AIVORY_UPDATER_IMAGE_OWNER", "hjxwz123").strip("/").lower()
        self.health_timeout = int(os.getenv("AIVORY_UPDATER_HEALTH_TIMEOUT_SECONDS", "180"))
        if not re.fullmatch(r"[a-z0-9][a-z0-9._/-]*", self.image_owner):
            raise ValueError("invalid image owner")
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", self.app_service):
            raise ValueError("invalid app service")

    @property
    def state_file(self) -> Path:
        return self.state_dir / "state.json"

    @property
    def image_repository(self) -> str:
        return f"{self.image_registry}/{self.image_owner}/aivory-app"


class UpdateManager:
    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.lock = threading.Lock()
        self.settings.state_dir.mkdir(parents=True, exist_ok=True)
        self._ensure_token()
        self.state = self._load_state()
        if self.state.get("status") in {"pulling", "restarting", "checking"}:
            self.state.update(status="failed", error="updater restarted before the update completed", completed_at=int(time.time()))
            self._save_state()

    def _ensure_token(self) -> None:
        self.settings.token_file.parent.mkdir(parents=True, exist_ok=True)
        try:
            current = self.settings.token_file.read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            current = ""
        if len(current) >= 32:
            return
        tmp = self.settings.token_file.with_name(self.settings.token_file.name + ".tmp")
        tmp.write_text(secrets.token_urlsafe(48) + "\n", encoding="utf-8")
        os.chmod(tmp, 0o640)
        os.replace(tmp, self.settings.token_file)

    def token(self) -> str:
        return self.settings.token_file.read_text(encoding="utf-8").strip()

    def _load_state(self) -> dict[str, Any]:
        try:
            value = json.loads(self.settings.state_file.read_text(encoding="utf-8"))
            return value if isinstance(value, dict) else {"status": "idle"}
        except (FileNotFoundError, json.JSONDecodeError, OSError):
            return {"status": "idle"}

    def _save_state(self) -> None:
        tmp = self.settings.state_file.with_suffix(".tmp")
        tmp.write_text(json.dumps(self.state, ensure_ascii=True, indent=2) + "\n", encoding="utf-8")
        os.replace(tmp, self.settings.state_file)

    def snapshot(self) -> dict[str, Any]:
        with self.lock:
            return dict(self.state)

    def start(self, version: str) -> tuple[dict[str, Any], bool]:
        if not VERSION_RE.fullmatch(version):
            raise ValueError("version must be a semantic image tag")
        with self.lock:
            if self.state.get("status") in {"pulling", "restarting", "checking"}:
                return dict(self.state), False
            self.state = {
                "id": uuid.uuid4().hex,
                "status": "pulling",
                "progress": "pulling_image",
                "version": version,
                "started_at": int(time.time()),
            }
            self._save_state()
            job = dict(self.state)
        threading.Thread(target=self._run, args=(version,), daemon=True).start()
        return job, True

    def _set(self, **values: Any) -> None:
        with self.lock:
            self.state.update(values)
            self._save_state()

    def _command(self, args: list[str], timeout: int = 600, env: dict[str, str] | None = None) -> str:
        completed = subprocess.run(args, check=True, capture_output=True, text=True, timeout=timeout, env=env)
        return completed.stdout.strip()

    def _host_data_dir(self) -> str:
        mounts_json = self._command([
            "docker", "inspect", "--format", "{{json .Mounts}}", os.getenv("HOSTNAME", ""),
        ], timeout=30)
        mounts = json.loads(mounts_json)
        for mount in mounts:
            if mount.get("Destination") == "/shared" and mount.get("Source"):
                return str(mount["Source"])
        raise RuntimeError("could not resolve the host data directory")

    def _compose(self, *args: str, timeout: int = 600) -> str:
        # Compose runs inside this container but creates a sibling through the
        # host daemon. Relative bind paths would otherwise resolve to the
        # container's /deployment path on the host and detach the app from its
        # real database/uploads. The /shared mount tells us the exact host path.
        command_env = os.environ.copy()
        command_env["DATA_DIR"] = self._host_data_dir()
        return self._command([
            "docker", "compose", "--env-file", str(self.settings.env_file),
            "-f", str(self.settings.compose_file), *args,
        ], timeout=timeout, env=command_env)

    def _write_env_value(self, key: str, value: str) -> str | None:
        if not ENV_KEY_RE.fullmatch(key) or "\n" in value or "\r" in value:
            raise ValueError("invalid environment value")
        original = self.settings.env_file.read_text(encoding="utf-8") if self.settings.env_file.exists() else ""
        previous: str | None = None
        output: list[str] = []
        replaced = False
        for line in original.splitlines(keepends=True):
            match = re.match(rf"^\s*{re.escape(key)}\s*=\s*(.*?)\s*(?:\r?\n)?$", line)
            if match and not line.lstrip().startswith("#"):
                if previous is None:
                    previous = match.group(1).strip().strip('"').strip("'")
                    output.append(f"{key}={value}\n")
                    replaced = True
                continue
            output.append(line)
        if not replaced:
            if output and not output[-1].endswith("\n"):
                output[-1] += "\n"
            output.append(f"{key}={value}\n")
        self._atomic_write_env("".join(output))
        return previous

    def _atomic_write_env(self, content: str) -> None:
        try:
            original_stat = self.settings.env_file.stat()
        except FileNotFoundError:
            original_stat = None
        tmp = self.settings.env_file.with_name(self.settings.env_file.name + ".aivory-update.tmp")
        tmp.write_text(content, encoding="utf-8")
        os.chmod(tmp, stat.S_IMODE(original_stat.st_mode) if original_stat else 0o600)
        if original_stat:
            try:
                os.chown(tmp, original_stat.st_uid, original_stat.st_gid)
            except PermissionError:
                pass
        os.replace(tmp, self.settings.env_file)

    def _restore_env_value(self, key: str, previous: str | None) -> None:
        if previous is not None:
            self._write_env_value(key, previous)
            return
        if not self.settings.env_file.exists():
            return
        original = self.settings.env_file.read_text(encoding="utf-8")
        output = [
            line for line in original.splitlines(keepends=True)
            if not re.match(rf"^\s*{re.escape(key)}\s*=", line) or line.lstrip().startswith("#")
        ]
        self._atomic_write_env("".join(output))

    def _container_id(self) -> str:
        return self._compose("ps", "-q", self.settings.app_service, timeout=30).strip()

    def _wait_healthy(self) -> None:
        deadline = time.monotonic() + self.settings.health_timeout
        last = "container_missing"
        while time.monotonic() < deadline:
            container_id = self._container_id()
            if container_id:
                try:
                    last = self._command([
                        "docker", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", container_id,
                    ], timeout=15)
                except (subprocess.SubprocessError, OSError):
                    last = "inspect_failed"
                if last == "healthy":
                    return
                if last in {"exited", "dead"}:
                    break
            time.sleep(3)
        raise RuntimeError(f"updated app did not become healthy ({last})")

    def _run(self, version: str) -> None:
        previous_tag: str | None = None
        env_changed = False
        image = f"{self.settings.image_repository}:{version}"
        try:
            if not self.settings.compose_file.is_file():
                raise RuntimeError("deployment compose file is unavailable")
            self._command(["docker", "pull", image], timeout=1800)
            previous_tag = self._write_env_value("APP_IMAGE_TAG", version)
            env_changed = True
            self._set(status="restarting", progress="recreating_app")
            self._compose("up", "-d", "--no-deps", "--force-recreate", self.settings.app_service, timeout=600)
            self._set(status="checking", progress="waiting_for_health")
            self._wait_healthy()
            self._set(status="completed", progress="completed", completed_at=int(time.time()), error="")
        except Exception as exc:  # noqa: BLE001 - this is the task boundary
            rollback_error = ""
            if env_changed:
                try:
                    self._restore_env_value("APP_IMAGE_TAG", previous_tag)
                    self._compose("up", "-d", "--no-deps", "--force-recreate", self.settings.app_service, timeout=600)
                    self._wait_healthy()
                except Exception as rollback_exc:  # noqa: BLE001
                    rollback_error = f"; rollback failed: {rollback_exc}"
            self._set(
                status="failed",
                progress="failed",
                error=f"{exc}{rollback_error}",
                completed_at=int(time.time()),
            )


class Handler(BaseHTTPRequestHandler):
    manager: UpdateManager

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"aivory-updater {self.address_string()} {fmt % args}", flush=True)

    def _json(self, status: int, body: dict[str, Any]) -> None:
        payload = json.dumps(body, ensure_ascii=True).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("X-Content-Type-Options", "nosniff")
        self.end_headers()
        self.wfile.write(payload)

    def _authorized(self) -> bool:
        expected = f"Bearer {self.manager.token()}"
        return hmac.compare_digest(self.headers.get("Authorization", ""), expected)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            self._json(HTTPStatus.OK, {"ok": True})
            return
        if self.path == "/v1/status":
            if not self._authorized():
                self._json(HTTPStatus.UNAUTHORIZED, {"error": "unauthorized"})
                return
            self._json(HTTPStatus.OK, {"job": self.manager.snapshot()})
            return
        self._json(HTTPStatus.NOT_FOUND, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/v1/update":
            self._json(HTTPStatus.NOT_FOUND, {"error": "not found"})
            return
        if not self._authorized():
            self._json(HTTPStatus.UNAUTHORIZED, {"error": "unauthorized"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 4096:
                raise ValueError("invalid request body")
            body = json.loads(self.rfile.read(length))
            version = body.get("version", "") if isinstance(body, dict) else ""
            job, started = self.manager.start(version)
        except (ValueError, json.JSONDecodeError) as exc:
            self._json(HTTPStatus.BAD_REQUEST, {"error": str(exc)})
            return
        self._json(HTTPStatus.ACCEPTED if started else HTTPStatus.CONFLICT, {"job": job})


def main() -> None:
    settings = Settings()
    manager = UpdateManager(settings)
    Handler.manager = manager
    host, port = settings.listen.rsplit(":", 1)
    server = ThreadingHTTPServer((host, int(port)), Handler)
    print(f"aivory-updater listening on {settings.listen}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
