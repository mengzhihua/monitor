package plugins

import (
	"bufio"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	stderrMessageBytes   = 4 << 10
	stderrWindowBytes    = 64 << 10
	stderrWindowLines    = 20
	stderrWindowDuration = 5 * time.Second
)

// StderrStats describes bounded diagnostic forwarding. Counters and the rate
// window belong to the plugin identity and survive process restarts.
type StderrStats struct {
	Bytes      uint64 `json:"bytes"`
	Lines      uint64 `json:"lines"`
	Logged     uint64 `json:"logged"`
	Suppressed uint64 `json:"suppressed"`
	Truncated  uint64 `json:"truncated"`
}

type stderrState struct {
	mu       sync.Mutex
	stats    StderrStats
	window   time.Time
	lines    int
	bytes    int
	notified bool
}

func (s *stderrState) snapshot() StderrStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}
func (s *stderrState) addBytes(n int) { s.mu.Lock(); s.stats.Bytes += uint64(n); s.mu.Unlock() }
func (s *stderrState) accept(now time.Time, size int, truncated bool) (allow, notify bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.Lines++
	if truncated {
		s.stats.Truncated++
	}
	if s.window.IsZero() || now.Sub(s.window) >= stderrWindowDuration {
		s.window, s.lines, s.bytes, s.notified = now, 0, 0, false
	}
	if s.lines < stderrWindowLines && size <= stderrWindowBytes-s.bytes {
		s.lines++
		s.bytes += size
		s.stats.Logged++
		return true, false
	}
	s.stats.Suppressed++
	if !s.notified {
		s.notified = true
		return false, true
	}
	return false, false
}

// ReadSlice bounds memory independently of line length. Even a suppressed or
// oversized diagnostic is fully drained, so it cannot close the child's stderr
// pipe or interfere with valid protocol output on stdout.
func (m *Manager) drainStderr(p *plugin, r io.Reader) {
	reader := bufio.NewReaderSize(r, stderrMessageBytes)
	prefix := make([]byte, 0, stderrMessageBytes)
	redactor := secretReplacer(processSecrets(p.spec))
	var contentBytes uint64
	haveLine := false
	emit := func() {
		truncated := contentBytes > stderrMessageBytes
		text := strings.TrimSuffix(string(prefix), "\r")
		// Do not expose a prefix that may end halfway through an args/env
		// secret. Oversized diagnostics keep only a fixed marker and counters.
		if truncated {
			text = "[oversized diagnostic omitted]"
		} else {
			if redactor != nil {
				text = redactor.Replace(text)
			}
			if len(text) > stderrMessageBytes {
				text = "[oversized diagnostic omitted]"
				truncated = true
			}
		}
		allow, notify := p.stderr.accept(time.Now(), len(text), truncated)
		if allow {
			m.log.Info("plugin stderr", "plugin", p.spec.Name, "msg", text)
		}
		if notify {
			m.log.Warn("plugin stderr rate limited", "plugin", p.spec.Name, "window", stderrWindowDuration)
		}
		prefix = prefix[:0]
		contentBytes = 0
		haveLine = false
	}
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			p.stderr.addBytes(len(fragment))
			haveLine = true
			if fragment[len(fragment)-1] == '\n' {
				fragment = fragment[:len(fragment)-1]
			}
			contentBytes += uint64(len(fragment))
			if remaining := stderrMessageBytes - len(prefix); remaining > 0 {
				prefix = append(prefix, fragment[:min(remaining, len(fragment))]...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if haveLine {
			emit()
		}
		if err != nil {
			return
		}
	}
}

func processSecrets(sp Spec) []string {
	seen := map[string]bool{}
	secrets := []string{}
	add := func(value string) {
		if value != "" && !seen[value] {
			seen[value] = true
			secrets = append(secrets, value)
		}
	}
	for _, entry := range sp.Env {
		_, value, ok := strings.Cut(entry, "=")
		if ok {
			add(value)
		}
	}
	for _, arg := range sp.Args {
		if _, value, ok := strings.Cut(arg, "="); ok {
			add(value)
		} else {
			add(arg)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

func secretReplacer(secrets []string) *strings.Replacer {
	if len(secrets) == 0 {
		return nil
	}
	pairs := make([]string, 0, 2*len(secrets))
	for _, secret := range secrets {
		pairs = append(pairs, secret, "[redacted]")
	}
	// One pass prevents replacement markers from being interpreted as secrets
	// again, which could otherwise amplify diagnostic memory usage.
	return strings.NewReplacer(pairs...)
}

func redactWithSecrets(text string, secrets []string) string {
	if text == "" {
		return text
	}
	if redactor := secretReplacer(secrets); redactor != nil {
		return redactor.Replace(text)
	}
	return text
}

func redactProcessText(sp Spec, text string) string {
	if text == "" {
		return text
	}
	return redactWithSecrets(text, processSecrets(sp))
}
