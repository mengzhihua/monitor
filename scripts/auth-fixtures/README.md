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

Generate a signed Assertion and matching IdP certificate (same signing path as Go unit tests):

```bash
cd core && go run ../scripts/auth-fixtures/gen_saml_response.go \
  -out ../scripts/auth-fixtures/signed-assertion.xml \
  -cert ../scripts/auth-fixtures/idp.pem
```

Configure `web.saml.idp_cert` from `idp.pem`, then serve the signed Assertion:

```bash
python3 scripts/auth-fixtures/saml_idp.py --listen 127.0.0.1:18080 \
  --signed-response scripts/auth-fixtures/signed-assertion.xml
python3 scripts/auth-fixtures/verify_saml.py --monitor http://127.0.0.1:19999
```

Without `--signed-response`, the stub posts a base64-encoded **unsigned** Assertion so ACS rejection is easy to observe. These scripts are for isolated lab use; they do not talk to production IdPs.
