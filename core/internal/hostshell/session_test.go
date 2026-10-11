package hostshell

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestConfigRejectsUnpinnedAndArbitraryTargets(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	valid := Target{ID: "ssh", Kind: "ssh", HostKeyAlgorithm: ssh.KeyAlgoED25519, Address: "127.0.0.1:22", User: "test", HostKey: ssh.FingerprintSHA256(signer.PublicKey())}
	if e := (Config{Targets: []Target{valid}}).Validate(); e != nil {
		t.Fatal(e)
	}
	for _, edit := range []func(*Target){func(v *Target) { v.HostKey = "" }, func(v *Target) { v.Address = "localhost:22oops" }, func(v *Target) { v.Kind = "exec" }, func(v *Target) { v.Argv = []string{"/bin/sh"} }, func(v *Target) { v.User = "" }, func(v *Target) { v.ID = "bad/id" }} {
		x := valid
		edit(&x)
		if (Config{Targets: []Target{x}}).Validate() == nil {
			t.Fatal("unsafe target accepted")
		}
	}
	if (Config{Targets: []Target{valid, valid}}).Validate() == nil {
		t.Fatal("duplicate target accepted")
	}
	if (Config{PublicOrigin: "http://example.com"}).Validate() == nil {
		t.Fatal("insecure origin accepted")
	}
}
func TestHostKeyAlgorithmMustBeModernAndExplicit(t *testing.T) {
	target, _ := sshFixture(t)
	target.HostKeyAlgorithm = "ssh-rsa"
	if (Config{Targets: []Target{target}}).Validate() == nil {
		t.Fatal("legacy host key algorithm accepted")
	}
}

func sshFixture(t *testing.T) (Target, ed25519.PrivateKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "test" && string(p) == "fixture-password" {
			return nil, nil
		}
		return nil, io.EOF
	}, PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(signer.PublicKey().Marshal()) {
			return nil, nil
		}
		return nil, io.EOF
	}}
	cfg.AddHostKey(signer)
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			raw, e := ln.Accept()
			if e != nil {
				return
			}
			go func() {
				defer raw.Close()
				c, ch, rq, e := ssh.NewServerConn(raw, cfg)
				if e != nil {
					return
				}
				defer c.Close()
				go ssh.DiscardRequests(rq)
				for newChannel := range ch {
					if newChannel.ChannelType() != "session" {
						newChannel.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, requests, e := newChannel.Accept()
					if e != nil {
						return
					}
					go func() {
						defer channel.Close()
						for r := range requests {
							switch r.Type {
							case "pty-req", "window-change":
								r.Reply(true, nil)
							case "shell":
								r.Reply(true, nil)
								channel.Write([]byte("SSH_FIXTURE_READY\r\n"))
								buf := make([]byte, 512)
								n, _ := channel.Read(buf)
								channel.Write(append([]byte("SSH_ECHO:"), buf[:n]...))
								channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
								return
							default:
								r.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return Target{ID: "fixture", Kind: "ssh", HostKeyAlgorithm: ssh.KeyAlgoED25519, Address: ln.Addr().String(), User: "test", HostKey: ssh.FingerprintSHA256(signer.PublicKey())}, priv
}
func TestSSHInteractivePinnedPasswordAndKey(t *testing.T) {
	target, priv := sshFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cred := Credentials{Password: "fixture-password"}
	s, e := Open(ctx, target, cred, 80, 24)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Resize(99, 37); e != nil {
		t.Fatal(e)
	}
	go func() { s.Write([]byte("echo fixture\n")) }()
	b, e := io.ReadAll(s)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(b), "SSH_FIXTURE_READY") || !strings.Contains(string(b), "SSH_ECHO:echo fixture") {
		t.Fatal("missing SSH terminal output")
	}
	if e = s.Wait(); e != nil {
		t.Fatal(e)
	}
	s.Close()
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "fixture", []byte("fixture-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite only the work-factor header; reject before attempting costly decryption.
	expensive := append([]byte(nil), block.Bytes...)
	offset := len("openssh-key-v1\x00")
	for i := 0; i < 3; i++ {
		n := int(binary.BigEndian.Uint32(expensive[offset : offset+4]))
		offset += 4
		if i == 2 {
			binary.BigEndian.PutUint32(expensive[offset+n-4:offset+n], ^uint32(0))
		}
		offset += n
	}
	if checkKeyCost(string(pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: expensive}))) == nil {
		t.Fatal("unbounded private-key work factor accepted")
	}
	keySession, err := Open(ctx, target, Credentials{PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: "fixture-passphrase"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	go func() { keySession.Write([]byte("key fixture\n")) }()
	keyOutput, _ := io.ReadAll(keySession)
	if !strings.Contains(string(keyOutput), "SSH_ECHO:key fixture") {
		t.Fatal("private key authentication failed")
	}
	keySession.Wait()
	keySession.Close()
	wrong := target
	wrong.HostKey = "SHA256:" + strings.Repeat("A", 43)
	if s, e := Open(context.Background(), wrong, Credentials{Password: "fixture-password"}, 80, 24); e == nil {
		s.Close()
		t.Fatal("host key mismatch accepted")
	}
	if s, e := Open(context.Background(), target, Credentials{Password: "incorrect"}, 80, 24); e == nil {
		s.Close()
		t.Fatal("bad password accepted")
	}
	if _, e := Open(context.Background(), target, Credentials{PrivateKey: "invalid"}, 80, 24); e == nil {
		t.Fatal("bad key accepted")
	}
}
