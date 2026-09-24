package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A trimmed agent (logs collector disabled) has no logs function: the hub must
// answer with a semantic 404 instead of a bare 502 gateway error.
func TestLogsNodeWithoutLogsFunction(t *testing.T) {
	hs, nodes, _, _ := newHubCfg(t, Options{})
	_, _, client := newAgent(t, hs.URL, nil) // no functions advertised
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)
	waitNode(t, nodes, "agent-1")

	resp, err := http.Get(hs.URL + "/api/v1/logs?node=agent-1&limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "logs collector disabled") {
		t.Fatalf("body = %q", string(buf[:n]))
	}
}
