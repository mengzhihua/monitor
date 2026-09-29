package health

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ScriptNotifier runs a configured executable. The alarm is passed in the
// environment, not on a shell command line.
type ScriptNotifier struct {
	Path string
	Args []string
}

func (n *ScriptNotifier) Name() string { return "script" }

func (n *ScriptNotifier) Notify(ctx context.Context, e LogEntry) error {
	if n == nil || n.Path == "" || strings.ContainsAny(n.Path, "\n\r") {
		return fmt.Errorf("script: path required")
	}
	cmd := exec.CommandContext(ctx, n.Path, n.Args...)
	cmd.Env = append(os.Environ(),
		"MONITOR_ALARM="+e.Name,
		"MONITOR_CHART="+e.Chart,
		"MONITOR_STATUS="+e.Status.String(),
		"MONITOR_HOST="+e.Hostname,
		"MONITOR_VALUE="+strconv.FormatFloat(e.Value, 'f', -1, 64),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}
