package preprocess

import (
	"fmt"
	"strconv"
	"time"

	"github.com/dop251/goja"
)

const jsScriptLimit = 4096

// applyJS runs a preprocessing function body. The script sees value and must
// return a number, numeric string, or boolean. The runtime has no host APIs
// and stops after a short time limit.
func applyJS(script, value string) (string, error) {
	if script == "" || len(script) > jsScriptLimit {
		return "", fmt.Errorf("javascript: script must be 1 to %d bytes", jsScriptLimit)
	}
	vm := goja.New()
	timer := time.AfterFunc(100*time.Millisecond, func() { vm.Interrupt("timeout") })
	defer timer.Stop()
	program, err := goja.Compile("preprocess.js", "function __preprocess(value) {\n"+script+"\n}", true)
	if err != nil {
		return "", fmt.Errorf("javascript: %w", err)
	}
	if _, err = vm.RunProgram(program); err != nil {
		return "", fmt.Errorf("javascript: %w", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("__preprocess"))
	if !ok {
		return "", fmt.Errorf("javascript: function missing")
	}
	out, err := fn(goja.Undefined(), vm.ToValue(value))
	if err != nil {
		return "", fmt.Errorf("javascript: %w", err)
	}
	return jsResult(out)
}

func jsResult(v goja.Value) (string, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "", fmt.Errorf("javascript: empty result")
	}
	switch n := v.Export().(type) {
	case string:
		return n, nil
	case int64:
		return strconv.FormatInt(n, 10), nil
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case bool:
		if n {
			return "1", nil
		}
		return "0", nil
	default:
		return "", fmt.Errorf("javascript: result must be a number, string, or boolean")
	}
}
