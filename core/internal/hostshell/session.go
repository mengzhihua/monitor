// Package hostshell owns explicitly configured interactive host connections.
package hostshell

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/crypto/ssh"
)

type Config struct {
	Enabled      bool     `yaml:"enabled"`
	PublicOrigin string   `yaml:"public_origin"`
	Targets      []Target `yaml:"targets"`
}
type Target struct {
	ID               string   `yaml:"id" json:"id"`
	Name             string   `yaml:"name" json:"name"`
	Node             string   `yaml:"node" json:"node"`
	Kind             string   `yaml:"kind" json:"kind"`
	Address          string   `yaml:"address" json:"address,omitempty"`
	User             string   `yaml:"user" json:"user,omitempty"`
	HostKey          string   `yaml:"host_key" json:"host_key,omitempty"`
	HostKeyAlgorithm string   `yaml:"host_key_algorithm" json:"host_key_algorithm,omitempty"`
	Argv             []string `yaml:"argv" json:"-"`
	Dir              string   `yaml:"dir" json:"-"`
}

// Config validation also runs on a Hub editing a Windows agent's YAML.
func absolutePath(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || len(p) >= 3 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func (c Config) Validate() error {
	if c.PublicOrigin != "" {
		u, e := url.Parse(c.PublicOrigin)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return errors.New("shell.public_origin must be an HTTPS origin without path")
		}
	}
	if len(c.Targets) > 64 {
		return errors.New("shell: at most 64 targets")
	}
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if t.ID == "" || len(t.ID) > 64 || strings.Trim(t.ID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.") != "" || seen[t.ID] {
			return errors.New("shell: invalid or duplicate target id")
		}
		seen[t.ID] = true
		if len(t.Name) > 128 || strings.ContainsAny(t.Name+t.Node, "\x00\r\n") || len(t.Node) > 128 {
			return fmt.Errorf("shell target %s: invalid name/node", t.ID)
		}
		switch t.Kind {
		case "local":
			if t.Node != "" && t.Node != "local" {
				return fmt.Errorf("shell target %s: local target must use node local", t.ID)
			}
			if len(t.Argv) < 1 || len(t.Argv) > 16 || !absolutePath(t.Argv[0]) || (t.Dir != "" && !absolutePath(t.Dir)) || t.Address != "" || t.User != "" || t.HostKey != "" || t.HostKeyAlgorithm != "" {
				return fmt.Errorf("shell target %s: local requires absolute argv and optional absolute dir", t.ID)
			}
			for _, a := range t.Argv {
				if len(a) > 1024 || strings.ContainsRune(a, 0) {
					return errors.New("shell: invalid local argv")
				}
			}
		case "ssh":
			switch t.HostKeyAlgorithm {
			case "", ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512:
			default:
				return fmt.Errorf("shell target %s: unsupported host_key_algorithm", t.ID)
			}
			host, port, e := net.SplitHostPort(t.Address)
			var p int
			if e == nil {
				p, e = strconv.Atoi(port)
			}
			// Resolve only the administrator's fixed address; browsers cannot override it.
			if e != nil || host == "" || p < 1 || p > 65535 || strings.ContainsAny(host, " /\r\n\x00") || t.User == "" || len(t.User) > 128 || strings.ContainsAny(t.User, "\r\n\x00") || len(t.Argv) > 0 || t.Dir != "" {
				return fmt.Errorf("shell target %s: invalid SSH address/user", t.ID)
			}
			key, e := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(t.HostKey, "SHA256:"))
			if !strings.HasPrefix(t.HostKey, "SHA256:") || e != nil || len(key) != 32 {
				return fmt.Errorf("shell target %s: host_key must be a SHA256 fingerprint", t.ID)
			}
		default:
			return fmt.Errorf("shell target %s: kind must be local or ssh", t.ID)
		}
	}
	return nil
}

type Credentials struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}
type Session interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() error
}
type localSession struct {
	pty.Pty
	cmd  *pty.Cmd
	once sync.Once
}

func (s *localSession) Wait() error {
	err := s.cmd.Wait()
	if runtime.GOOS == "windows" {
		s.Close()
	}
	return err
}
func (s *localSession) Close() error {
	s.once.Do(func() { killProcess(s.cmd.Process); _ = s.Pty.Close() })
	return nil
}

func Open(ctx context.Context, t Target, c Credentials, cols, rows int) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cols < 2 || cols > 400 || rows < 2 || rows > 200 {
		return nil, errors.New("invalid terminal size")
	}
	if t.Kind == "local" {
		if c.Password != "" || c.PrivateKey != "" || c.Passphrase != "" {
			return nil, errors.New("local shell does not accept credentials")
		}
		p, e := pty.New()
		if e != nil {
			return nil, errors.New("local terminal unavailable on this platform")
		}
		if e = p.Resize(cols, rows); e != nil {
			p.Close()
			return nil, e
		}
		cmd := p.Command(t.Argv[0], t.Argv[1:]...)
		cmd.Dir = t.Dir
		// Do not inherit monitoring tokens, webhook secrets or provider keys into the shell.
		for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "SystemRoot", "WINDIR", "COMSPEC", "TEMP", "TMP"} {
			if v, ok := os.LookupEnv(name); ok {
				cmd.Env = append(cmd.Env, name+"="+v)
			}
		}
		cmd.Env = append(cmd.Env, "TERM=xterm-256color")
		if e = cmd.Start(); e != nil {
			p.Close()
			return nil, errors.New("local shell could not start; check configured executable/directory")
		}
		// Close the parent's slave handle so EOF works when the child exits.
		if u, ok := p.(pty.UnixPty); ok {
			_ = u.Slave().Close()
		}
		return &localSession{Pty: p, cmd: cmd}, nil
	}
	var auth ssh.AuthMethod
	if c.PrivateKey != "" {
		if err := checkKeyCost(c.PrivateKey); err != nil {
			return nil, err
		}
		if c.Password != "" {
			return nil, errors.New("choose password or private key")
		}
		var signer ssh.Signer
		var e error
		if c.Passphrase != "" {
			signer, e = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			signer, e = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if e != nil {
			return nil, errors.New("invalid SSH private key or passphrase")
		}
		auth = ssh.PublicKeys(signer)
	} else {
		if c.Password == "" || c.Passphrase != "" {
			return nil, errors.New("SSH credential required")
		}
		auth = ssh.Password(c.Password)
	}
	cfg := &ssh.ClientConfig{User: t.User, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(key)), []byte(t.HostKey)) != 1 {
			return errors.New("SSH host key mismatch")
		}
		return nil
	}}
	if t.HostKeyAlgorithm != "" {
		cfg.HostKeyAlgorithms = []string{t.HostKeyAlgorithm}
	}
	raw, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", t.Address)
	if e != nil {
		return nil, errors.New("SSH host unreachable")
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	cc, ch, rq, e := ssh.NewClientConn(raw, t.Address, cfg)
	if e != nil {
		raw.Close()
		return nil, errors.New("SSH authentication or host key validation failed")
	}
	cli := ssh.NewClient(cc, ch, rq)
	ses, e := cli.NewSession()
	if e != nil {
		cli.Close()
		return nil, errors.New("SSH session rejected")
	}
	fail := func() (Session, error) {
		ses.Close()
		cli.Close()
		return nil, errors.New("SSH interactive terminal rejected")
	}
	if e = ses.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); e != nil {
		return fail()
	}
	input, e := ses.StdinPipe()
	if e != nil {
		return fail()
	}
	reader, writer := io.Pipe()
	ses.Stdout = writer
	ses.Stderr = writer
	if e = ses.Shell(); e != nil {
		reader.Close()
		writer.Close()
		return fail()
	}
	_ = raw.SetDeadline(time.Time{})
	result := &sshSession{Session: ses, client: cli, in: input, out: reader, writer: writer, done: make(chan error, 1)}
	go func() { err := ses.Wait(); writer.Close(); result.done <- err }()
	return result, nil
}

type sshSession struct {
	*ssh.Session
	client *ssh.Client
	in     io.WriteCloser
	out    *io.PipeReader
	writer *io.PipeWriter
	once   sync.Once
	done   chan error
}

func (s *sshSession) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *sshSession) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *sshSession) Resize(c, r int) error       { return s.WindowChange(r, c) }
func (s *sshSession) Wait() error                 { return <-s.done }
func (s *sshSession) Close() error {
	s.once.Do(func() { s.client.Close(); s.Session.Close(); s.in.Close(); s.out.Close(); s.writer.Close() })
	return nil
}
