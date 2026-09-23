package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureWebAuth must run under the data-directory lock, before opening any
// listener. Empty configuration creates a per-installation admin password.
// The password is also a bearer token, so existing API and app clients work.
// Return only the file path for logging; never log the credential itself.
func (c *Config) EnsureWebAuth() (string, error) {
	if c.Web.Token != "" {
		if strings.TrimSpace(c.Web.Token) != c.Web.Token {
			return "", fmt.Errorf("web.token must not contain leading or trailing whitespace")
		}
		return "", nil
	}
	if len(c.Web.Users) > 0 || c.Web.OIDC.Issuer != "" || c.Web.OIDC.ClientID != "" || c.Web.LDAP.URL != "" {
		return "", nil
	}
	path := filepath.Join(c.Global.DataDir, "web-password")
	st, err := os.Lstat(path)
	if err == nil {
		if !st.Mode().IsRegular() || st.Size() > 128 {
			return "", fmt.Errorf("web-password must be a regular credential file")
		}
		if err := os.Chmod(path, 0600); err != nil {
			return "", fmt.Errorf("protect web-password: %w", err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read web-password: %w", err)
		}
		token := strings.TrimSpace(string(b))
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
			return "", fmt.Errorf("invalid web-password; restore it or stop the server and remove it to generate a new password")
		}
		c.Web.Token = token
		return path, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect web-password: %w", err)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate login password: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("create web-password: %w", err)
	}
	_, err = f.WriteString(token + "\n")
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", fmt.Errorf("save web-password: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close web-password: %w", closeErr)
	}
	c.Web.Token = token
	return path, nil
}
