package api

import (
	"encoding/base32"
	"testing"
	"time"
)

func TestTOTPAcceptsCurrentStep(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	now := time.Unix(1700000000, 0)
	code := hotp(decodeSecret(t, secret), uint64(now.Unix()/30))
	if !totpOK(secret, code, now) || totpOK(secret, "000000", now) {
		t.Fatalf("code %s", code)
	}
	if !totpOK("", "", now) {
		t.Fatal("empty secret should not require a code")
	}
}

func decodeSecret(t *testing.T, secret string) []byte {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
