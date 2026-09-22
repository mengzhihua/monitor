package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultPasswordPersistsAndIsUnique(t *testing.T) {
	c := Config{}
	c.Global.DataDir = t.TempDir()
	path, err := c.EnsureWebAuth()
	if err != nil || len(c.Web.Token) != 43 {
		t.Fatalf("generate password: %v", err)
	}
	first := c.Web.Token
	c.Web.Token = ""
	if _, err := c.EnsureWebAuth(); err != nil || c.Web.Token != first {
		t.Fatalf("password changed on restart: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions: %v", st.Mode())
	}
	c.Global.DataDir = t.TempDir()
	c.Web.Token = ""
	if _, err := c.EnsureWebAuth(); err != nil || c.Web.Token == first {
		t.Fatalf("installations must have different passwords: %v", err)
	}
}

func TestExplicitAuthenticationDoesNotCreateDefaultPassword(t *testing.T) {
	for _, name := range []string{"token", "users", "oidc", "ldap"} {
		t.Run(name, func(t *testing.T) {
			c := Config{}
			c.Global.DataDir = t.TempDir()
			switch name {
			case "token":
				c.Web.Token = "configured-secret"
			case "users":
				c.Web.Users = []User{{Name: "admin", Token: "configured-secret", Role: "admin"}}
			case "oidc":
				c.Web.OIDC.ClientID = "client"
			case "ldap":
				c.Web.LDAP.URL = "ldaps://example.test"
			}
			path, err := c.EnsureWebAuth()
			if err != nil || path != "" {
				t.Fatalf("unexpected bootstrap: %v", err)
			}
			if _, err := os.Stat(filepath.Join(c.Global.DataDir, "web-password")); !os.IsNotExist(err) {
				t.Fatal("created an extra admin credential")
			}
		})
	}
}

func TestInvalidDefaultPasswordFailsClosed(t *testing.T) {
	for _, content := range []string{"", "weak-password", "AA=="} {
		c := Config{}
		c.Global.DataDir = t.TempDir()
		if err := os.WriteFile(filepath.Join(c.Global.DataDir, "web-password"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.EnsureWebAuth(); err == nil || c.Web.Token != "" {
			t.Fatal("accepted invalid credential")
		}
	}
	c := Config{}
	c.Global.DataDir = t.TempDir()
	if err := os.Mkdir(filepath.Join(c.Global.DataDir, "web-password"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnsureWebAuth(); err == nil {
		t.Fatal("accepted a credential directory")
	}
	if runtime.GOOS != "windows" {
		c.Global.DataDir = t.TempDir()
		target := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(c.Global.DataDir, "web-password")); err != nil {
			t.Fatal(err)
		}
		if _, err := c.EnsureWebAuth(); err == nil {
			t.Fatal("accepted a credential symlink")
		}
	}
}
