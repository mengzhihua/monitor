#!/usr/bin/env python3
"""Minimal unsigned-friendly SAML IdP stub for local ACS wiring checks.

This is intentionally tiny: it serves metadata and a POST form that posts a
hard-coded NameID assertion body to the configured ACS URL. Production IdP
signing is covered by Go unit tests; this script only proves the login path
is reachable in a lab.
"""
import argparse
from http.server import BaseHTTPRequestHandler, HTTPServer
import urllib.parse

HTML = """<!doctype html><title>fixture IdP</title>
<form method="post" action="{acs}">
<input type="hidden" name="SAMLResponse" value="{payload}">
<button type="submit">Continue to Monitor</button>
</form>"""

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith('/login'):
            body = HTML.format(acs=self.server.acs, payload='fixture-assertion').encode()
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
    a = p.parse_args()
    host, port = a.listen.rsplit(':', 1)
    httpd = HTTPServer((host, int(port)), Handler)
    httpd.acs = a.acs
    httpd.entity = a.entity
    print(f'saml fixture IdP on http://{a.listen}/login -> {a.acs}')
    httpd.serve_forever()
