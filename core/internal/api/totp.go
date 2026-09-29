package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"strings"
	"time"
)

// totpOK reports whether code matches secret at now, within one 30s step.
// An empty secret means the credential does not use a second factor.
func totpOK(secret, code string, now time.Time) bool {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	if secret == "" {
		return true
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(secret, " ", ""))
	if err != nil {
		raw, err = base32.StdEncoding.DecodeString(secret)
		if err != nil {
			return false
		}
	}
	counter := uint64(now.Unix() / 30)
	for _, c := range []uint64{counter - 1, counter, counter + 1} {
		if hotp(raw, c) == code {
			return true
		}
	}
	return false
}

func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	n := bin % 1000000
	out := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		out[i] = byte('0' + n%10)
		n /= 10
	}
	return string(out)
}

func (s *Server) otpOK(secret string, rHeader string, now time.Time) bool {
	return totpOK(secret, rHeader, now)
}
