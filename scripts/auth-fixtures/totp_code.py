#!/usr/bin/env python3
"""Print a current HOTP/TOTP code for a base32 secret (30s step, 6 digits)."""
import base64, hashlib, hmac, struct, sys, time

def totp(secret: str, now: int | None = None) -> str:
    key = base64.b32decode(secret.upper() + "=" * ((8 - len(secret) % 8) % 8))
    counter = int((now if now is not None else time.time()) // 30)
    digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    code = (struct.unpack(">I", digest[offset : offset + 4])[0] & 0x7FFFFFFF) % 1_000_000
    return f"{code:06d}"

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("usage: totp_code.py <base32-secret>", file=sys.stderr)
        raise SystemExit(2)
    print(totp(sys.argv[1]))
