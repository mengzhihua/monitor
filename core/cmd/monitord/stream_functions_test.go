package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/api"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/plugins"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// The test executable is a portable, real plugin process. The manager inserts
// update_every before Args, so dispatch before the testing flag parser runs.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[2] == "--monitor-stream-function-helper" {
		fmt.Println(`FUNCTION ping 5 "plugin echo"`)
		input := bufio.NewScanner(os.Stdin)
		for input.Scan() {
			parts := strings.SplitN(input.Text(), " ", 4)
			if len(parts) != 4 || parts[0] != "CALL" {
				continue
			}
			var args map[string]string
			if json.Unmarshal([]byte(parts[3]), &args) != nil {
				continue
			}
			result, _ := json.Marshal(map[string]any{"pong": args["message"], "pid": os.Getpid()})
			fmt.Printf("FUNCTION_RESULT %s %s\n", parts[1], result)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAgentPluginFunctionsUpdateOnConnectedHub(t *testing.T) {
	for _, protocol := range []string{"stream", "mqtt"} {
		t.Run(protocol, func(t *testing.T) { testAgentPluginFunctions(t, protocol) })
	}
}

func testAgentPluginFunctions(t *testing.T, protocol string) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	openDB := func() *tsdb.Store {
		t.Helper()
		db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir(), Tiers: []tsdb.TierSpec{}, Logger: log})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	hubDB, agentDB := openDB(), openDB()
	nodes, err := hub.Open(hubDB, t.TempDir(), hub.Options{Keys: []string{"fixture-stream-key"}, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	hubReg := registry.New(&registry.Host{ID: "hub", Hostname: "hub", UpdateEvery: 1}, hubDB)
	hubScheduler := collect.NewScheduler(hubReg, log, collect.Options{Names: []string{"none"}})
	t.Cleanup(hubScheduler.Close)
	org, err := hub.OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	space, err := org.CreateSpace("function-fixture")
	if err != nil {
		t.Fatal(err)
	}
	room, err := org.CreateRoom(space.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := org.SetRoomNodes(room.ID, []string{"agent-functions"}); err != nil {
		t.Fatal(err)
	}
	server, err := api.New(hubReg, hubDB, hubScheduler, api.Options{Mode: "hub", Nodes: nodes, Org: org, Logger: log, Users: []api.User{
		{Name: "oncall", Token: "fixture-oncall", Role: api.RoleTroubleshooter},
		{Name: "reader", Token: "fixture-reader", Role: api.RoleViewer},
		{Name: "restricted", Token: "fixture-restricted", Role: api.RoleTroubleshooter, Rooms: []string{"other-room"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	agentReg := registry.New(&registry.Host{ID: "agent-functions", Hostname: "agent-functions", UpdateEvery: 1}, agentDB)
	scheduler := collect.NewScheduler(agentReg, log, collect.Options{Names: []string{"none"}})
	t.Cleanup(scheduler.Close)
	pluginDir := t.TempDir()
	manager := plugins.New(agentReg, nil, plugins.Options{Dir: pluginDir, Logger: log})
	if err := manager.Err(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Stream.Enabled, cfg.Stream.Destinations, cfg.Stream.APIKey = true, []string{httpServer.URL}, "fixture-stream-key"
	cfg.Stream.Protocol, cfg.Stream.Timeout = protocol, 3*time.Second
	client, err := newAgentStream(cfg, "", agentReg, agentDB, scheduler, nil, manager, nil, log, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	managerDone, clientDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(managerDone); manager.Run(ctx) }()
	go func() { defer close(clientDone); client.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		for _, done := range []<-chan struct{}{clientDone, managerDone} {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("function fixture worker did not stop")
			}
		}
	})
	wait := func(what string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s: stream=%+v plugins=%+v", what, client.Status(), manager.Catalog())
	}
	wait("empty connected agent", func() bool { return client.Status().Connected && manager.Catalog().Running })
	node, ok := nodes.Get("agent-functions")
	if !ok || len(node.Functions()) != 0 {
		t.Fatal("fixture must connect before a plugin registers any function")
	}
	packageDir := filepath.Join(pluginDir, "demo")
	if err := os.Mkdir(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := "collector"
	if runtime.GOOS == "windows" {
		command += ".exe"
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := os.OpenFile(filepath.Join(packageDir, command), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		source.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dest, source)
	source.Close()
	closeErr := dest.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy fixture executable: %v %v", copyErr, closeErr)
	}
	manifest, _ := json.Marshal(map[string]any{"api_version": 1, "id": "demo", "name": "Demo", "version": "1", "protocol": "plugins.d/v1", "capabilities": []string{"functions"}, "command": command, "args": []string{"--monitor-stream-function-helper"}, "timeout": 30})
	manifestPath := filepath.Join(packageDir, "plugin.json")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	control := func(action string) {
		t.Helper()
		name := "demo"
		if action == "rescan" {
			name = ""
		}
		controlCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if _, err := manager.Apply(controlCtx, plugins.ControlRequest{Name: name, Action: action, IfRevision: manager.Catalog().Revision}); err != nil {
			t.Fatalf("plugin %s: %v", action, err)
		}
	}
	call := func(token string, want int) int {
		t.Helper()
		query := url.Values{"node": {node.ID}, "function": {"plugin.demo.ping"}, "message": {"from hub"}}
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/function?"+query.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := (&http.Client{Timeout: 6 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("function status %d want %d: %s", response.StatusCode, want, body)
		}
		if want != http.StatusOK {
			return 0
		}
		var body struct {
			Result struct {
				Pong string `json:"pong"`
				PID  int    `json:"pid"`
			}
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.Result.Pong != "from hub" || body.Result.PID == 0 {
			t.Fatalf("invalid plugin result: %+v %v", body, err)
		}
		return body.Result.PID
	}
	assertDirectory := func(present bool) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/functions?node="+node.ID, nil)
		req.Header.Set("Authorization", "Bearer fixture-reader")
		response, err := (&http.Client{Timeout: 6 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var functions []struct {
			Name    string `json:"name"`
			Help    string `json:"help"`
			Timeout int    `json:"timeout"`
		}
		if err := json.NewDecoder(response.Body).Decode(&functions); err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("read function directory: status=%d error=%v", response.StatusCode, err)
		}
		if present {
			if len(functions) != 1 || functions[0].Name != "plugin.demo.ping" || functions[0].Help != "plugin echo" || functions[0].Timeout != 5 {
				t.Fatalf("unexpected public function metadata: %+v", functions)
			}
		} else if len(functions) != 0 {
			t.Fatalf("unavailable functions remain discoverable: %+v", functions)
		}
	}
	visible := func() bool { f := node.Functions(); return len(f) == 1 && f[0].Name == "plugin.demo.ping" }
	control("rescan")
	wait("late plugin directory on hub", visible)
	assertDirectory(true)
	pid := call("fixture-oncall", http.StatusOK)
	call("fixture-reader", http.StatusForbidden)
	call("fixture-restricted", http.StatusForbidden)
	control("disable")
	call("fixture-oncall", http.StatusNotFound) // unavailable even before the next catalog tick
	wait("disabled function removed", func() bool { return len(node.Functions()) == 0 })
	assertDirectory(false)
	control("enable")
	wait("enabled function restored", visible)
	if next := call("fixture-oncall", http.StatusOK); next == pid {
		t.Fatal("enable reused the old plugin process")
	} else {
		pid = next
	}
	control("restart")
	wait("restart function registration", func() bool { return len(manager.Functions()) == 1 })
	if call("fixture-oncall", http.StatusOK) == pid {
		t.Fatal("restart reused the old plugin process")
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	control("rescan")
	call("fixture-oncall", http.StatusNotFound)
	wait("uninstalled function removed", func() bool { return len(node.Functions()) == 0 })
	assertDirectory(false)
	if !client.Status().Connected || client.Status().Reconnects != 0 {
		t.Fatal("catalog updates must not reconnect the agent")
	}
}
