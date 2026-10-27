package collect

import (
	"fmt"
	"strconv"
	"strings"
)

type statusSpan struct {
	lo, hi int
}

func parseStatusCodes(raw []string) ([]statusSpan, error) {
	if len(raw) > 8 {
		return nil, fmt.Errorf("at most 8 status codes")
	}
	out := make([]statusSpan, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		loRaw, hiRaw, ranged := strings.Cut(item, "-")
		loRaw = strings.TrimSpace(loRaw)
		hiRaw = strings.TrimSpace(hiRaw)
		lo, err1 := strconv.Atoi(loRaw)
		hi := lo
		var err2 error
		if ranged {
			hi, err2 = strconv.Atoi(hiRaw)
		}
		if err1 != nil || err2 != nil || lo < 100 || hi > 599 || lo > hi || item == "" {
			return nil, fmt.Errorf("status code %q", item)
		}
		out = append(out, statusSpan{lo: lo, hi: hi})
	}
	return out, nil
}

func statusAllowed(spans []statusSpan, code int) bool {
	if len(spans) == 0 {
		return true
	}
	for _, sp := range spans {
		if code >= sp.lo && code <= sp.hi {
			return true
		}
	}
	return false
}
