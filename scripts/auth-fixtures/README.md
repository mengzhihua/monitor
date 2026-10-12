# Auth fixtures: SAML / TOTP / PSK

These fixtures give a **real** local path for the auth modes that unit tests alone never exercise end-to-end.

## TOTP (static user)

```yaml
# monitor.yaml fragment
web:
  users:
    - name: totp-admin
      token: fixture-totp-admin
      role: admin
      totp: GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ   # base32; test vector secret
```

Generate a current code:

```bash
python3 scripts/auth-fixtures/totp_code.py GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ
curl -H "Authorization: Bearer fixture-totp-admin" \
     -H "X-Monitor-OTP: $(python3 scripts/auth-fixtures/totp_code.py GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ)" \
     http://127.0.0.1:19999/api/v1/info
```

## PSK (Hub stream)

```yaml
# Hub
hub:
  api_keys: [hub-fixture-key]
  psk: fixture-psk-shared-secret
# Agent
stream:
  enabled: true
  destinations: ["http://127.0.0.1:19999"]
  api_key: hub-fixture-key
  psk: fixture-psk-shared-secret
```

Wrong PSK must yield HTTP 401 on the stream upgrade. See `core/internal/hub` PSK tests plus:

```bash
python3 scripts/auth-fixtures/verify_psk.py --hub-bin core/bin/monitord
```

## SAML

`verify_saml.py` checks that `/api/v1/auth/saml/login` redirects when `web.saml` is configured (soft-skips with 404 otherwise).

`saml_idp.py` is a lab stub IdP. By default it posts a **base64-encoded but unsigned** Assertion so you can confirm ACS rejects it. Successful signed ACS login is covered by `core/internal/api/saml_test.go`; to drive ACS from the stub, pass a pre-signed Response XML:

```bash
python3 scripts/auth-fixtures/saml_idp.py --listen 127.0.0.1:18080 \
  --signed-response /path/to/signed-response.xml
python3 scripts/auth-fixtures/verify_saml.py --monitor http://127.0.0.1:19999
```

These scripts are for isolated lab use; they do not talk to production IdPs.
