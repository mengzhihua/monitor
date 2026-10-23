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

CI runs `verify_saml_e2e.py` against two isolated, temporary monitord installations: one SAML admin and one SAML viewer. Both configure SAML as their only authentication source, with no static token or users, and enable an empty plugin directory.

The fixture checks that no fallback `web-password` is created; anonymous and invalid-token requests to info, plugin catalog and plugin actions return 401. A genuinely signed login must retain its exact personal identity and configured role. The admin sees platform capabilities and can read the catalog and rescan it; the viewer receives no plugin capabilities and gets 403 for catalog and control requests. Unsigned assertions and replayed signed assertions remain rejected with 401.

Each HTTP connection closes after use. Each daemon receives SIGINT and must exit within a bounded wait, its listener must close, and temporary credentials and files are removed. The script prints only result descriptions and HTTP statuses on failure; session tokens, redirect URLs, private keys and response bodies are never printed.

Go must be available on `PATH` to generate the signed assertions, even when `--bin` points to an already built daemon.

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
