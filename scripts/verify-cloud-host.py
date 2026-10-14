#!/usr/bin/env python3
"""Stage cloud-host.sh layouts and exercise check/ai against a local monitord."""
from __future__ import annotations

import base64
import json
import os
import pathlib
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "packaging" / "cloud-host.sh"
START = ROOT / "packaging" / "start.sh"


def fail(msg: str) -> None:
    print(msg, file=sys.stderr)
    raise SystemExit(1)


def run(args: list[str], env: dict[str, str], check: bool = True) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(args, env=env, text=True, capture_output=True)
    if check and proc.returncode != 0:
        fail(f"{args} -> {proc.returncode}\n{proc.stdout}\n{proc.stderr}")
    return proc


def token43() -> str:
    return base64.urlsafe_b64encode(b"\x01" * 32).decode().rstrip("=")


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_health(url: str, timeout: float = 20.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1) as resp:
                if resp.read().decode().strip() == "ok":
                    return
        except OSError:
            time.sleep(0.1)
    fail("monitord did not become healthy")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args: object) -> None:
        return

    def _json(self, code: int, payload: dict) -> None:
        raw = json.dumps(payload).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def _read(self) -> dict:
        n = int(self.headers.get("Content-Length", "0"))
        if n == 0:
            return {}
        return json.loads(self.rfile.read(n).decode())

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            raw = b"ok\n"
            self.send_response(200)
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        if self.path == "/api/v1/ai/settings":
            state = self.server.state  # type: ignore[attr-defined]
            self._json(200, state["settings"])
            return
        if self.path == "/api/v1/info":
            self._json(200, self.server.info)  # type: ignore[attr-defined]
            return
        if self.path == "/api/v1/charts":
            self._json(200, {"charts": {"system.cpu": {"id": "system.cpu"}, "system.ram": {"id": "system.ram"}}})
            return
        if self.path.startswith("/api/v1/data"):
            self._json(200, {"id": "system.cpu", "points": 1, "result": {"data": [[1, 2.0]]}})
            return
        self._json(404, {"error": self.path})

    def do_PUT(self) -> None:  # noqa: N802
        body = self._read()
        state = self.server.state  # type: ignore[attr-defined]
        if body.get("revision") != state["settings"]["revision"]:
            self._json(409, {"error": "revision"})
            return
        state["settings"] = {
            "base_url": body["base_url"],
            "model": body["model"],
            "has_api_key": bool(body.get("api_key")),
            "allow_private": bool(body.get("allow_private")),
            "revision": body["revision"] + 1,
            "enabled": True,
            "persistent": True,
        }
        state["put"] = body
        self._json(200, state["settings"])

    def do_POST(self) -> None:  # noqa: N802
        body = self._read()
        if self.path == "/api/v1/ai/preview":
            self._json(200, {
                "id": "ab" * 24,
                "snapshot": {"hostname": "vm", "charts": [{}, {}]},
            })
            return
        if self.path == "/api/v1/ai/analyze":
            if body.get("preview_id") != "ab" * 24 or body.get("revision") != 1:
                self._json(400, {"error": "bad analyze"})
                return
            self._json(200, {"text": "cpu steady", "model": body.get("question", "m"), "generated_at": 10, "duration_ms": 5})
            return
        self._json(404, {"error": self.path})


def serve(info: dict) -> tuple[ThreadingHTTPServer, int]:
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.info = info  # type: ignore[attr-defined]
    server.state = {"settings": {  # type: ignore[attr-defined]
        "base_url": "", "model": "", "has_api_key": False, "allow_private": False,
        "revision": 0, "enabled": False, "persistent": True,
    }}
    port = server.server_address[1]
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, port


def stage_tests(tmp: pathlib.Path) -> None:
    binary = tmp / "monitord"
    binary.write_text("#!/bin/sh\nprintf '%s\\n' 'monitord 3.0.17 linux/amd64'\n", encoding="utf-8")
    binary.chmod(0o755)
    root = tmp / "root"
    env = os.environ.copy()
    env.update({
        "MONITOR_ROOT": str(root),
        "MONITOR_BIN": str(binary),
        "MONITOR_DATA_DIR": str(root / "var/lib/monitor"),
    })
    proc = run(["bash", str(SCRIPT), "install"], env)
    if "installed mode=agent" not in proc.stdout:
        fail(proc.stdout)
    cfg = (root / "etc/monitor/monitor.yaml").read_text(encoding="utf-8")
    if 'listen: "127.0.0.1:19999"' not in cfg or "mode: agent" not in cfg:
        fail(cfg)
    unit = (root / "etc/systemd/system/monitord.service").read_text(encoding="utf-8")
    if f"-data-dir {root / 'var/lib/monitor'}" not in unit:
        fail(unit)
    again = run(["bash", str(SCRIPT), "install"], env, check=False)
    if again.returncode == 0 or "already configured" not in again.stderr:
        fail(again.stderr)

    hub_root = tmp / "hub"
    key = "ab" * 24
    env.update({
        "MONITOR_ROOT": str(hub_root),
        "MONITOR_DATA_DIR": str(hub_root / "var/lib/monitor"),
        "MONITOR_MODE": "hub",
        "MONITOR_LISTEN": "10.0.0.10:19999",
        "MONITOR_ALLOW_FROM": "10.0.0.0/24,127.0.0.1/32",
        "MONITOR_STREAM_KEY": key,
    })
    run(["bash", str(SCRIPT), "install"], env)
    hub_cfg = (hub_root / "etc/monitor/monitor.yaml").read_text(encoding="utf-8")
    for needle in ('mode: hub', 'listen: "10.0.0.10:19999"', "- \"10.0.0.0/24\"", "- \"127.0.0.1/32\"", key):
        if needle not in hub_cfg:
            fail(hub_cfg)

    bad = env.copy()
    bad["MONITOR_ROOT"] = str(tmp / "bad")
    bad["MONITOR_DATA_DIR"] = str(tmp / "bad/var/lib/monitor")
    bad["MONITOR_STREAM_KEY"] = "short"
    refused = run(["bash", str(SCRIPT), "install"], bad, check=False)
    if refused.returncode == 0 or "48 hex" not in refused.stderr:
        fail(refused.stderr)

    new_bin = tmp / "monitord-new"
    shutil.copy(binary, new_bin)
    upgraded = run(["bash", str(SCRIPT), "upgrade", "--binary", str(new_bin)], env)
    if f"upgraded binary={hub_root}/usr/bin/monitord" not in upgraded.stdout:
        fail(upgraded.stdout)


def mock_checks(tmp: pathlib.Path) -> None:
    data = tmp / "data"
    data.mkdir()
    (data / "web-password").write_text(token43() + "\n", encoding="utf-8")
    binary = tmp / "fake-monitord"
    binary.write_text("#!/bin/sh\nprintf '%s\\n' 'monitord 9.9.9 linux/amd64'\n", encoding="utf-8")
    binary.chmod(0o755)
    info = {
        "version": "9.9.9",
        "mode": "agent",
        "charts_count": 2,
        "db": {"dir": str(data / "db")},
        "stream": {"enabled": True, "connected": True, "destination": "ws://10.0.0.10:19999/api/v1/stream", "sent": 3},
    }
    server, port = serve(info)
    try:
        env = os.environ.copy()
        env.update({
            "MONITOR_ROOT": str(tmp / "unused-root"),
            "MONITOR_URL": f"http://127.0.0.1:{port}",
            "MONITOR_PASSWORD_FILE": str(data / "web-password"),
            "MONITOR_DATA_DIR": str(data),
            "MONITOR_MODE": "agent",
            "MONITOR_BIN": str(binary),
            "MONITOR_STREAM_DEST": "10.0.0.10:19999",
        })
        proc = run(["bash", str(SCRIPT), "check"], env)
        if "ok version=9.9.9 mode=agent" not in proc.stdout or "stream=ws://10.0.0.10:19999/api/v1/stream" not in proc.stdout:
            fail(proc.stdout)
        env["MODEL_BASE"] = "https://api.example.com/v1"
        env["MODEL_NAME"] = "fixture-model"
        env["MODEL_KEY"] = "secret-key"
        saved = run(["bash", str(SCRIPT), "ai", "--analyze"], env)
        if "saved revision=1 model=fixture-model" not in saved.stdout or "analyzed model=" not in saved.stdout:
            fail(saved.stdout)
        env["MODEL_BASE"] = "http://127.0.0.1:11434/v1"
        env["MONITOR_ALLOW_PRIVATE"] = "false"
        refused = run(["bash", str(SCRIPT), "ai"], env, check=False)
        if refused.returncode == 0 or "MONITOR_ALLOW_PRIVATE=true" not in refused.stderr:
            fail(refused.stderr + refused.stdout)
    finally:
        server.shutdown()


def live_check(tmp: pathlib.Path) -> None:
    binary = pathlib.Path(os.environ.get("MONITOR_TEST_BIN", ROOT / "core/bin/monitord"))
    if not binary.is_file():
        print(f"skip live check, missing {binary}")
        return
    data = tmp / "live"
    data.mkdir()
    port = free_port()
    cfg = tmp / "live.yaml"
    cfg.write_text(
        "mode: agent\nweb:\n  listen: \"127.0.0.1:%d\"\n  token: \"\"\n"
        "collectors:\n  enabled: [cpu, mem]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n" % port,
        encoding="utf-8",
    )
    proc = subprocess.Popen(
        [str(binary), "-config", str(cfg), "-data-dir", str(data), "-listen", f"127.0.0.1:{port}"],
        stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True,
    )
    try:
        wait_health(f"http://127.0.0.1:{port}/healthz")
        env = os.environ.copy()
        env.update({
            "MONITOR_ROOT": str(tmp / "live-root"),
            "MONITOR_URL": f"http://127.0.0.1:{port}",
            "MONITOR_PASSWORD_FILE": str(data / "web-password"),
            "MONITOR_DATA_DIR": str(data),
            "MONITOR_MODE": "agent",
            "MONITOR_BIN": str(binary),
        })
        checked = run(["bash", str(SCRIPT), "check"], env)
        if "ok version=" not in checked.stdout or "cpu_points=" not in checked.stdout:
            fail(checked.stdout)
        print(checked.stdout.strip())
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)


def main() -> int:
    text = START.read_text(encoding="utf-8")
    if "Open http://" in text:
        fail("start.sh still tells operators to open a browser")
    if "health: curl -fsS http://${LISTEN}/healthz" not in text or "password: ${DATA}/web-password" not in text:
        fail("start.sh health/password lines missing")
    syntax = subprocess.run(["bash", "-n", str(SCRIPT)], capture_output=True, text=True)
    if syntax.returncode != 0:
        fail(syntax.stderr)
    help_out = subprocess.run(["bash", str(SCRIPT), "--help"], capture_output=True, text=True)
    if help_out.returncode != 0 or "cloud-host.sh install" not in help_out.stdout:
        fail(help_out.stderr or help_out.stdout)
    import tempfile
    with tempfile.TemporaryDirectory(prefix="monitor-cloud-host-") as raw:
        tmp = pathlib.Path(raw)
        stage_tests(tmp)
        mock_checks(tmp)
        live_check(tmp)
    print("cloud-host packaging checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
