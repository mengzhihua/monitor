package plugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxFunctionArgs  = 16
	maxArgKey        = 64
	maxArgValue      = 1024
	maxCallLineBytes = 8 << 10
)

var argKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

// FunctionSpec is one callable plugin function. Name is the public
// plugin.<id>.<name> identifier used by /api/v1/function.
type FunctionSpec struct {
	Name    string
	Help    string
	Timeout int
	Run     func(context.Context, map[string]string) (any, error)
}

// Functions lists calls that running packages have registered. Packages that
// did not declare the functions capability, and a process with functions
// disabled, contribute nothing.
func (m *Manager) Functions() []FunctionSpec {
	if m == nil || !m.opt.functionsEnabled() {
		return nil
	}
	m.mu.Lock()
	list := append([]*plugin(nil), m.list...)
	m.mu.Unlock()
	var out []FunctionSpec
	for _, p := range list {
		p.mu.Lock()
		parser, name := p.parser, p.spec.Name
		p.mu.Unlock()
		if parser == nil || !parser.AllowsFunctions {
			continue
		}
		for _, public := range parser.Functions() {
			fn, ok := parser.functionByPublicName(public)
			if !ok {
				continue
			}
			local, help, timeout := fn.Name, fn.Help, fn.Timeout
			out = append(out, FunctionSpec{
				Name: public, Help: help, Timeout: timeout,
				Run: func(ctx context.Context, args map[string]string) (any, error) {
					return m.call(ctx, name, local, args, timeout)
				},
			})
		}
	}
	return out
}

func (m *Manager) call(ctx context.Context, pluginName, local string, args map[string]string, timeout int) (any, error) {
	if err := validateCallArgs(args); err != nil {
		return nil, err
	}
	p := m.pluginNamed(pluginName)
	if p == nil || p.call == nil {
		return nil, errors.New("plugin is not running")
	}
	select {
	case p.call <- struct{}{}:
		defer func() { <-p.call }()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, errors.New("plugin call already in progress")
	}
	p.mu.Lock()
	stdin, parser := p.stdin, p.parser
	p.mu.Unlock()
	if stdin == nil || parser == nil || !parser.AllowsFunctions {
		return nil, errors.New("plugin is not running")
	}
	if timeout <= 0 {
		timeout = 10
	}
	if timeout > 60 {
		timeout = 60
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	id, err := newCallID()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if args == nil {
		payload = []byte("{}")
	}
	line := "CALL " + id + " " + local + " " + string(payload) + "\n"
	if len(line) > maxCallLineBytes {
		return nil, errors.New("function arguments are too large")
	}
	ch := parser.beginCall(id)
	defer parser.cancelCall(id)
	parser.touch()
	if _, err := stdin.Write([]byte(line)); err != nil {
		return nil, err
	}
	select {
	case <-callCtx.Done():
		return nil, callCtx.Err()
	case result := <-ch:
		if result.err != nil {
			return nil, result.err
		}
		if len(result.body) > parser.maxResult() {
			return nil, fmt.Errorf("function result exceeds %d bytes", parser.maxResult())
		}
		var value any
		if err := json.Unmarshal([]byte(result.body), &value); err != nil {
			return nil, errors.New("function result is not JSON")
		}
		return value, nil
	}
}

func (m *Manager) pluginNamed(name string) *plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.list {
		if p.spec.Name == name {
			return p
		}
	}
	return nil
}

func validateCallArgs(args map[string]string) error {
	if len(args) > maxFunctionArgs {
		return errors.New("too many function arguments")
	}
	for key, value := range args {
		if len(key) > maxArgKey || !argKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid function argument %q", key)
		}
		if len(value) > maxArgValue || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
			return fmt.Errorf("invalid function argument %q", key)
		}
	}
	return nil
}

func newCallID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
