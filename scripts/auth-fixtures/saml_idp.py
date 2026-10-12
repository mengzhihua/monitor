#!/usr/bin/env python3
"""Minimal SAML IdP stub for local ACS wiring checks.

By default the Continue button posts a base64-encoded (unsigned) Assertion so
operators can see Monitor reject unsigned payloads the same way production
would. Pass --signed-pem / --signed-key to wrap a pre-made signed Response
file, or use the Go unit test in core/internal/api/saml_test.go for a full
RSA enveloped-signature round trip (that path owns real signature coverage).
"""
import argparse
import base64
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

UNSIGNED = """<?xml version="1.0"?>
<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="_fixture">
  <Issuer>{entity}</Issuer>
  <Subject><NameID>fixture-user@example.com</NameID></Subject>
</Assertion>
"""

HTML = """<!doctype html><title>fixture IdP</title>
<p>Monitor rejects unsigned assertions; use a signed Response file for a successful ACS, or rely on Go unit tests for signature coverage.</p>
<form method="post" action="{acs}">
<input type="hidden" name="SAMLResponse" value="{payload}">
<button type="submit">Continue to Monitor</button>
</form>"""

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith('/login'):
            body = HTML.format(acs=self.server.acs, payload=self.server.payload).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'text/html; charset=utf-8')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path == '/metadata':
            body = f'<EntityDescriptor entityID="{self.server.entity}"/>'.encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/samlmetadata+xml')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_error(404)

    def log_message(self, fmt, *args):
        return

if __name__ == '__main__':
    p = argparse.ArgumentParser()
    p.add_argument('--listen', default='127.0.0.1:18080')
    p.add_argument('--acs', default='http://127.0.0.1:19999/api/v1/auth/saml/acs')
    p.add_argument('--entity', default='http://127.0.0.1:18080/metadata')
    p.add_argument('--signed-response', help='Path to a pre-signed SAML Response XML file (raw, not base64)')
    a = p.parse_args()
    if a.signed_response:
        raw = Path(a.signed_response).read_bytes()
    else:
        raw = UNSIGNED.format(entity=a.entity).encode()
    host, port = a.listen.rsplit(':', 1)
    httpd = HTTPServer((host, int(port)), Handler)
    httpd.acs = a.acs
    httpd.entity = a.entity
    httpd.payload = base64.b64encode(raw).decode()
    print(f'saml fixture IdP on http://{a.listen}/login -> {a.acs}')
    httpd.serve_forever()
