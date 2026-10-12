#!/usr/bin/env python3
"""SAML ACS e2e against a real monitord: signed login works; unsigned and replay are rejected."""
from __future__ import annotations

import argparse
import base64
import http.client
import pathlib
import signal
import socket
import subprocess
import tempfile
import time
import urllib.parse

ROOT = pathlib.Path(__file__).resolve().parents[2]


def wait_ready(host: str, port: int, child: subprocess.Popen, log_path: pathlib.Path) -> None:
    for _ in range(200):
        if child.poll() is not None:
            raise AssertionError(f"daemon exited early:\n{log_path.read_text(errors='replace')}")
        try:
            conn = http.client.HTTPConnection(host, port, timeout=1)
            conn.request("GET", "/healthz")
            resp = conn.getresponse()
            resp.read()
            if resp.status == 200:
                return
        except OSError:
            time.sleep(0.1)
    raise AssertionError(f"daemon readiness timed out:\n{log_path.read_text(errors='replace')}")


def request(host: str, port: int, method: str, path: str, body: bytes | None = None, headers: dict | None = None) -> tuple[int, dict[str, str], bytes]:
    conn = http.client.HTTPConnection(host, port, timeout=5)
    conn.request(method, path, body=body, headers=headers or {})
    resp = conn.getresponse()
    data = resp.read()
    hdrs = {k.lower(): v for k, v in resp.getheaders()}
    return resp.status, hdrs, data


def post_acs(host: str, port: int, assertion: bytes) -> tuple[int, str]:
    body = urllib.parse.urlencode({"SAMLResponse": base64.b64encode(assertion).decode()}).encode()
    status, hdrs, _ = request(
        host, port, "POST", "/api/v1/auth/saml/acs", body=body,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    return status, hdrs.get("location", "")


def gen_assertion(out: pathlib.Path, cert: pathlib.Path, key: pathlib.Path, assert_id: str) -> None:
    cmd = [
        "go", "run", str(ROOT / "scripts/auth-fixtures/gen_saml_response.go"),
        "-out", str(out), "-cert", str(cert), "-key", str(key),
        "-id", assert_id, "-name", "saml-e2e@example.com",
    ]
    proc = subprocess.run(cmd, cwd=ROOT / "core", capture_output=True, text=True)
    if proc.returncode != 0:
        raise AssertionError(f"gen_saml_response failed:\n{proc.stdout}\n{proc.stderr}")


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--bin", default=str(ROOT / "core/bin/monitord"))
    args = p.parse_args()
    binary = pathlib.Path(args.bin).resolve()
    if not binary.is_file():
        raise SystemExit(f"missing binary {binary}; build monitord first")

    with tempfile.TemporaryDirectory(prefix="monitor-saml-e2e-") as tmp:
        root = pathlib.Path(tmp)
        cert = root / "idp.pem"
        key = root / "idp.pem.key"
        signed1 = root / "signed1.xml"
        signed2 = root / "signed2.xml"
        gen_assertion(signed1, cert, key, "_e2e1")
        gen_assertion(signed2, cert, key, "_e2e2")

        idp_cert = cert.read_text()
        indented = "".join(f"      {line}\n" for line in idp_cert.splitlines())
        config = root / "monitor.yaml"
        config.write_text(
            "collectors:\n  enabled: [cpu]\nplugins:\n  enabled: false\nhealth:\n  enabled: false\n"
            "web:\n  token: saml-e2e-admin\n  saml:\n"
            "    entity_id: monitor-e2e\n"
            "    acs_url: http://127.0.0.1/api/v1/auth/saml/acs\n"
            "    sso_url: http://127.0.0.1:9/sso\n"
            f"    idp_cert: |\n{indented}"
            "    role: admin\n"
        )
        data = root / "data"
        log_path = root / "server.log"
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        host = "127.0.0.1"
        log = log_path.open("w")
        child = subprocess.Popen(
            [str(binary), "-config", str(config), "-data-dir", str(data), "-listen", f"{host}:{port}"],
            stdout=log, stderr=log,
        )
        try:
            wait_ready(host, port, child, log_path)
            status, hdrs, _ = request(host, port, "GET", "/api/v1/auth/saml/login")
            assert status in (302, 303), status
            assert "SAMLRequest=" in hdrs.get("location", ""), hdrs.get("location")

            status, loc = post_acs(host, port, signed1.read_bytes())
            assert status in (302, 303), status
            assert "token=" in loc, loc
            token = urllib.parse.parse_qs(urllib.parse.urlparse(loc).query).get("token", [""])[0]
            assert token, loc
            status, _, body = request(host, port, "GET", "/api/v1/info", headers={"Authorization": "Bearer " + token})
            assert status == 200, status
            assert b"saml-e2e@example.com" in body, body

            unsigned = (
                b'<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_unsigned">'
                b"<Subject><NameID>evil@example.com</NameID></Subject></Assertion>"
            )
            status, _ = post_acs(host, port, unsigned)
            assert status == 401, status

            status, _ = post_acs(host, port, signed1.read_bytes())
            assert status == 401, f"replay accepted: {status}"

            status, loc = post_acs(host, port, signed2.read_bytes())
            assert status in (302, 303), status
            assert "token=" in loc, loc
            print("saml e2e ok: signed login, unsigned rejected, replay rejected")
        finally:
            log.close()
            if child.poll() is None:
                child.send_signal(signal.SIGINT)
                try:
                    child.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()


if __name__ == "__main__":
    main()
