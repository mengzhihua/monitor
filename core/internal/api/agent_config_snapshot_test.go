package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/config"
)

func TestAgentConfigResponseKeepsReadRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	initial := "global:\n  hostname: initial\n"
	if err := config.Save(path, []byte(initial)); err != nil {
		t.Fatal(err)
	}
	past := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	observed := configFileMTime(path)
	captured, err := readAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// A remote apply does not hold the API's configMu. It can replace the
	// file after GET (or a save response) reads it but before rendering JSON.
	remote := "global:\n  hostname: remote\n"
	if err := config.Save(path, []byte(remote)); err != nil {
		t.Fatal(err)
	}
	response := agentConfigView(path, captured)
	if response.YAML != initial || response.Updated != observed {
		t.Fatalf("response paired different file versions: yaml=%q updated=%d want=%d", response.YAML, response.Updated, observed)
	}
	if err := writeAgentConfigIfUpdated(path, "global:\n  hostname: local draft\n", &response.Updated); !errors.Is(err, config.ErrFileConflict) {
		t.Fatalf("a draft based on the old response must conflict with remote apply: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != remote {
		t.Fatalf("stale draft overwrote remote config: %q %v", got, err)
	}
}
