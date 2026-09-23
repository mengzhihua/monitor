// Package mobile is the in-process entry used by a gomobile bind.
// The Android app still execs the static monitord binary; bind this package
// when the process should host the agent without a child executable.
//
//	gomobile bind -target=android -o monitor.aar github.com/mengzhihua/monitor/core/mobile
package mobile

import (
	"fmt"
	"os"
	"os/exec"
)

// YAML is the agent config a mobile host writes before starting monitord.
func YAML(dataDir string, port int, hubURL, apiKey string, hubMode bool) string {
	mode := "agent"
	stream := "stream:\n  enabled: false\n"
	if hubMode {
		mode = "hub"
	}
	if hubURL != "" && apiKey != "" && !hubMode {
		stream = fmt.Sprintf("stream:\n  enabled: true\n  destinations: [%q]\n  api_key: %q\n", hubURL, apiKey)
	}
	return fmt.Sprintf("mode: %s\nglobal:\n  data_dir: %q\nweb:\n  listen: \":%d\"\n%s", mode, dataDir, port, stream)
}

// Start writes config to dataDir/monitor.yaml and execs monitord when the
// binary is on PATH. gomobile callers that already linked the agent should
// invoke monitord's main instead; this helper is the shared config contract.
func Start(dataDir, binary string, port int, hubURL, apiKey string, hubMode bool) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := dataDir + "/monitor.yaml"
	if err := os.WriteFile(path, []byte(YAML(dataDir, port, hubURL, apiKey, hubMode)), 0o644); err != nil {
		return err
	}
	if binary == "" {
		binary = "monitord"
	}
	cmd := exec.Command(binary, "-config", path, "-data-dir", dataDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
