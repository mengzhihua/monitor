package collect

import (
	"path"
	"strings"
)

// globMatch is a case-insensitive path.Match that never errors (a malformed
// pattern simply does not match).
func globMatch(pattern, name string) bool {
	ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	return ok
}
