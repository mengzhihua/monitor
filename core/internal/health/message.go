package health

import (
	"fmt"
	"strings"
)

const maxMessageTemplate = 1024

// messageTokens are the only placeholders a notification template may use.
var messageTokens = map[string]func(LogEntry) string{
	"HOST.NAME":    func(e LogEntry) string { return e.Hostname },
	"EVENT.NAME":   func(e LogEntry) string { return e.Name },
	"EVENT.STATUS": func(e LogEntry) string { return e.Status.String() },
	"EVENT.VALUE":  func(e LogEntry) string { return formatValue(e.Value) },
	"EVENT.UNITS":  func(e LogEntry) string { return e.Units },
	"EVENT.CHART":  func(e LogEntry) string { return e.Chart },
	"EVENT.INFO":   func(e LogEntry) string { return e.Info },
}

// RenderMessage substitutes {TOKEN} placeholders. Unknown tokens and an
// unclosed brace are rejected. The result is not header-safe by itself.
func RenderMessage(tmpl string, e LogEntry) (string, error) {
	if len(tmpl) > maxMessageTemplate {
		return "", fmt.Errorf("message template is too long")
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if tmpl[i] != '{' {
			b.WriteByte(tmpl[i])
			i++
			continue
		}
		end := strings.IndexByte(tmpl[i:], '}')
		if end <= 1 {
			return "", fmt.Errorf("unclosed message token")
		}
		token := tmpl[i+1 : i+end]
		fn, ok := messageTokens[token]
		if !ok {
			return "", fmt.Errorf("unknown message token %q", token)
		}
		b.WriteString(fn(e))
		i += end + 1
	}
	return b.String(), nil
}
