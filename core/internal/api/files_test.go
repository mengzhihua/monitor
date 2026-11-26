package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/hostfiles"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

func TestHostFileRouting(t *testing.T) {
	for _, protocol := range []string{"stream", "mqtt"} {
		t.Run(protocol, func(t *testing.T) {
			local := t.TempDir()
			remote := t.TempDir()
			server, nodes := newHub(t, Options{Files: &hostfiles.Store{Dir: local}, Token: "shared", Users: []User{{Name: "alice", Token: "alice-test", Role: RoleAdmin}, {Name: "reader", Token: "reader-test", Role: RoleViewer}}})
			reg, db, _ := newAgent(t, server.URL, nil)
			client := stream.NewClient(reg, db, stream.ClientOptions{Files: &hostfiles.Store{Dir: remote}, Destinations: []string{server.URL}, APIKey: testKey, Protocol: protocol, Timeout: 5 * time.Second})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); client.Run(ctx) }()
			defer func() { cancel(); <-done }()
			waitNode(t, nodes, "agent-1")
			url := "/api/v1/files/mkdir?node=agent-1"
			body := `{"path":"reports"}`
			for _, token := range []string{"shared", "reader-test"} {
				shellRequest(t, server, url, token, body, server.URL, 403)
			}
			shellRequest(t, server, url, "alice-test", body, "", 403)
			shellRequest(t, server, url, "alice-test", body, server.URL, 200)
			if _, err := os.Stat(filepath.Join(remote, "reports")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(local, "reports")); !os.IsNotExist(err) {
				t.Fatal("operation reached hub directory")
			}
			shellRequest(t, server, "/api/v1/files/list?node=agent-1", "alice-test", `{"path":".."}`, server.URL, 400)
		})
	}
}
