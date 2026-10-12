#!/usr/bin/env python3
"""Check that Monitor exposes SAML login when configured (soft skip otherwise)."""
import argparse, urllib.error, urllib.request

p = argparse.ArgumentParser()
p.add_argument('--monitor', default='http://127.0.0.1:19999')
a = p.parse_args()
url = a.monitor.rstrip('/') + '/api/v1/auth/saml/login'
try:
    urllib.request.urlopen(url, timeout=3)
except urllib.error.HTTPError as e:
    if e.code == 404:
        print('saml not configured on this instance (404) — start monitord with web.saml to exercise ACS')
        raise SystemExit(0)
    if e.code in (302, 303):
        print('saml login redirect ok')
        raise SystemExit(0)
    raise SystemExit(f'unexpected status {e.code}')
except urllib.error.URLError as e:
    raise SystemExit(f'cannot reach monitor: {e}')
print('saml login returned 200')
