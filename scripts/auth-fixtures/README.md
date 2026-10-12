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

CI runs `verify_saml_e2e.py`: starts monitord with a fixture IdP cert, posts a signed Assertion (login ok), an unsigned one (401), and replays the signed Assertion (401).

```bash
# after building core/bin/monitord
python3 scripts/auth-fixtures/verify_saml_e2e.py --bin core/bin/monitord
```

Generate a signed Assertion and matching IdP certificate manually:

```bash
cd core && go run ../scripts/auth-fixtures/gen_saml_response.go \
  -out ../scripts/auth-fixtures/signed-assertion.xml \
  -cert ../scripts/auth-fixtures/idp.pem
# second Assertion ID with the same key:
go run ../scripts/auth-fixtures/gen_saml_response.go \
  -out ../scripts/auth-fixtures/signed-assertion-2.xml \
  -cert ../scripts/auth-fixtures/idp.pem \
  -key ../scripts/auth-fixtures/idp.pem.key -id _fixture2
```

`verify_saml.py` only checks that `/api/v1/auth/saml/login` redirects when configured (soft-skips with 404). Optional browser-style stub:

```bash
python3 scripts/auth-fixtures/saml_idp.py --listen 127.0.0.1:18080 \
  --signed-response scripts/auth-fixtures/signed-assertion.xml
python3 scripts/auth-fixtures/verify_saml.py --monitor http://127.0.0.1:19999
```

These scripts are for isolated lab / CI use; they do not talk to production IdPs.
