#!/usr/bin/env python3
"""Exercise SAML-only authentication and plugin RBAC against isolated real daemons."""
from __future__ import annotations

import argparse
import base64
import http.client
import json
import pathlib
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parents[2]
HOST = "127.0.0.1"
IDENTITY = "saml-e2e@example.com"


def require(condition: bool, description: str) -> None:
    if not condition:
        raise AssertionError(description)


def expect_status(status: int, expected: int | tuple[int, ...], description: str) -> None:
    allowed = (expected,) if isinstance(expected, int) else expected
    require(status in allowed, f"{description}: HTTP {status}, expected {allowed}")


def request(host: str, port: int, method: str, path: str, body: bytes | None = None,
            headers: dict | None = None, timeout: float = 5) -> tuple[int, dict[str, str], bytes]:
    conn = http.client.HTTPConnection(host, port, timeout=timeout)
    try:
        conn.request(method, path, body=body, headers=headers or {})
        resp = conn.getresponse()
        data = resp.read()
        return resp.status, {key.lower(): value for key, value in resp.getheaders()}, data
    except (OSError, http.client.HTTPException):
        raise AssertionError(f"{method} {path}: HTTP exchange failed") from None
    finally:
        conn.close()


def read_json(body: bytes, description: str) -> dict:
    try:
        value = json.loads(body)
    except (ValueError, UnicodeError):
        raise AssertionError(f"{description}: invalid JSON") from None
    require(isinstance(value, dict), f"{description}: expected a JSON object")
    return value


def wait_ready(port: int, child: subprocess.Popen) -> None:
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        require(child.poll() is None, "daemon exited before readiness")
        try:
            status, _, _ = request(HOST, port, "GET", "/healthz", timeout=1)
            if status == 200:
                return
        except AssertionError:
            pass
        time.sleep(0.1)
    raise AssertionError("daemon readiness timed out")


def stop_daemon(child: subprocess.Popen, port: int) -> None:
    if child.poll() is None:
        child.send_signal(signal.SIGINT)
    try:
        code = child.wait(timeout=15)
    except subprocess.TimeoutExpired:
        child.kill()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            raise AssertionError("daemon did not exit after forced cleanup") from None
        raise AssertionError("daemon did not stop within 15 seconds of SIGINT") from None
    require(code == 0, f"daemon shutdown failed: exit {code}")
    with socket.socket() as probe:
        probe.settimeout(1)
        require(probe.connect_ex((HOST, port)) != 0, "daemon listener remained open after shutdown")


def post_acs(port: int, assertion: bytes) -> tuple[int, str]:
    body = urllib.parse.urlencode({"SAMLResponse": base64.b64encode(assertion).decode()}).encode()
    status, headers, _ = request(HOST, port, "POST", "/api/v1/auth/saml/acs", body=body,
                                 headers={"Content-Type": "application/x-www-form-urlencoded"})
    return status, headers.get("location", "")


def session(port: int, assertion: bytes) -> str:
    status, location = post_acs(port, assertion)
    expect_status(status, (302, 303), "signed SAML login")
    token = urllib.parse.parse_qs(urllib.parse.urlparse(location).query).get("token", [""])[0]
    require(bool(token), "signed SAML login did not issue a session")
    return token


def gen_assertion(out: pathlib.Path, cert: pathlib.Path, key: pathlib.Path, assert_id: str) -> None:
    go = shutil.which("go")
    require(bool(go), "Go is required on PATH to generate the signed SAML fixture")
    command = [go, "run", str(ROOT / "scripts/auth-fixtures/gen_saml_response.go"),
               "-out", str(out), "-cert", str(cert), "-key", str(key),
               "-id", assert_id, "-name", IDENTITY]
    # Generator failures must not echo key material or signed response contents.
    result = subprocess.run(command, cwd=ROOT / "core", capture_output=True, timeout=120)
    require(result.returncode == 0, f"assertion generation failed: exit {result.returncode}")


def verify_role(binary: pathlib.Path, root: pathlib.Path, cert: pathlib.Path,
                signed1: bytes, signed2: bytes, role: str) -> None:
    root.mkdir(mode=0o700)
    plugins = root / "plugins"
    plugins.mkdir()
    data = root / "data"
    config = root / "monitor.yaml"
    indented_cert = "".join(f"      {line}\n" for line in cert.read_text().splitlines())
    config.write_text(
        "collectors:\n  enabled: [cpu]\nhealth:\n  enabled: false\n"
        f"plugins:\n  enabled: true\n  dir: {json.dumps(str(plugins))}\n"
        "web:\n  saml:\n"
        "    entity_id: monitor-e2e\n"
        "    acs_url: http://127.0.0.1/api/v1/auth/saml/acs\n"
        "    sso_url: http://127.0.0.1:9/sso\n"
        f"    idp_cert: |\n{indented_cert}"
        f"    role: {role}\n"
    )
    config.chmod(0o600)
    with socket.socket() as listener:
        listener.bind((HOST, 0))
        port = listener.getsockname()[1]
    child = subprocess.Popen([str(binary), "-config", str(config), "-data-dir", str(data),
                              "-listen", f"{HOST}:{port}"],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        wait_ready(port, child)
        require(not (data / "web-password").exists(), f"{role}: SAML-only setup generated a fallback web-password")
        action = json.dumps({"action": "rescan", "if_revision": 1}).encode()
        for label, authorization in (("anonymous", {}), ("invalid token", {"Authorization": "Bearer saml-invalid-fixture"})):
            for method, path in (("GET", "/api/v1/info"), ("GET", "/api/v1/plugins"),
                                 ("POST", "/api/v1/plugins/actions")):
                status, _, _ = request(HOST, port, method, path, body=action if method == "POST" else None,
                                       headers={**authorization, "Content-Type": "application/json"})
                expect_status(status, 401, f"{role} {label} {method} {path}")

        status, headers, _ = request(HOST, port, "GET", "/api/v1/auth/saml/login")
        expect_status(status, (302, 303), f"{role} SAML login redirect")
        require("SAMLRequest=" in headers.get("location", ""), f"{role}: missing SAML login request")
        token = session(port, signed1)
        authorization = {"Authorization": "Bearer " + token}
        status, _, body = request(HOST, port, "GET", "/api/v1/info", headers=authorization)
        expect_status(status, 200, f"{role} signed session info")
        info = read_json(body, f"{role} info")
        user = info.get("user", {})
        require(user.get("name") == IDENTITY and user.get("role") == role and user.get("personal") is True,
                f"{role}: signed session lost its personal identity or role")
        capabilities = ["plugins.catalog.v1", "plugins.control.v1"] if role == "admin" else []
        platform = info.get("platform", {})
        require(platform.get("api_version") == 1 and platform.get("scope") == "local"
                and platform.get("capabilities") == capabilities,
                f"{role}: info platform capabilities do not match the authenticated role")
        status, _, body = request(HOST, port, "GET", "/api/v1/platform", headers=authorization)
        expect_status(status, 200, f"{role} platform discovery")
        require(read_json(body, f"{role} platform") == platform, f"{role}: platform discovery disagrees with info")

        status, _, body = request(HOST, port, "GET", "/api/v1/plugins", headers=authorization)
        expect_status(status, 200 if role == "admin" else 403, f"{role} plugin catalog")
        if role == "admin":
            catalog = read_json(body, "admin plugin catalog")
            require(catalog.get("schema_version") == 1 and catalog.get("running") is True
                    and catalog.get("plugins") == [] and catalog.get("diagnostics") == [],
                    "admin: expected a running empty plugin catalog")
            revision = catalog.get("revision")
            require(isinstance(revision, int) and revision > 0, "admin: invalid plugin catalog revision")
            action = json.dumps({"action": "rescan", "if_revision": revision}).encode()
        status, _, body = request(HOST, port, "POST", "/api/v1/plugins/actions", body=action,
                                  headers={**authorization, "Content-Type": "application/json"})
        expect_status(status, 200 if role == "admin" else 403, f"{role} plugin rescan")
        if role == "admin":
            result = read_json(body, "admin plugin rescan")
            require(result.get("plugins") == [] and result.get("running") is True
                    and result.get("revision", 0) >= revision, "admin: invalid rescan result")

        unsigned = (b'<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_unsigned">'
                    b"<Subject><NameID>evil@example.com</NameID></Subject></Assertion>")
        status, _ = post_acs(port, unsigned)
        expect_status(status, 401, f"{role} unsigned assertion")
        status, _ = post_acs(port, signed1)
        expect_status(status, 401, f"{role} replayed assertion")
        second = session(port, signed2)
        require(second != token, f"{role}: distinct signed login reused the previous session")
        require(not (data / "web-password").exists(), f"{role}: SAML login created a fallback web-password")
    finally:
        stop_daemon(child, port)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--bin", default=str(ROOT / "core/bin/monitord"))
    args = parser.parse_args()
    binary = pathlib.Path(args.bin).resolve()
    require(binary.is_file(), "missing monitord binary; build it first or supply --bin")
    with tempfile.TemporaryDirectory(prefix="monitor-saml-e2e-") as tmp:
        root = pathlib.Path(tmp)
        cert, key = root / "idp.pem", root / "idp.pem.key"
        signed1, signed2 = root / "signed1.xml", root / "signed2.xml"
        gen_assertion(signed1, cert, key, "_e2e1")
        gen_assertion(signed2, cert, key, "_e2e2")
        for role in ("admin", "viewer"):
            verify_role(binary, root / role, cert, signed1.read_bytes(), signed2.read_bytes(), role)
    require(not root.exists(), "temporary SAML runtime files were not removed")
    print("saml e2e ok: SAML-only admin/viewer identity, anonymous denial, plugin RBAC, unsigned/replay rejection, cleanup")


if __name__ == "__main__":
    try:
        main()
    except AssertionError as error:
        raise SystemExit(f"saml e2e failed: {error}") from None
    except (OSError, subprocess.SubprocessError):
        raise SystemExit("saml e2e failed: fixture process or filesystem operation failed") from None
    except (TypeError, ValueError, AttributeError, KeyError):
        raise SystemExit("saml e2e failed: unexpected response structure") from None
