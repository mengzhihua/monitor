package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/plugins"
)

type pluginFunctionRuntime struct{ pluginRuntimeFixture }

func (pluginFunctionRuntime) Functions() []plugins.FunctionSpec {
	return []plugins.FunctionSpec{{
		Name: "plugin.demo.ping", Help: "ping", Timeout: 5,
		Run: func(_ context.Context, args map[string]string) (any, error) {
			return map[string]string{"pong": args["msg"]}, nil
		},
	}}
}

func TestPluginFunctionUsesExistingRoleGate(t *testing.T) {
	ts, _ := newTestServer(t, Options{
		Plugins: &pluginFunctionRuntime{},
		Users: []User{
			{Name: "ops", Token: "admin-token", Role: RoleAdmin},
			{Name: "oncall", Token: "trouble-token", Role: RoleTroubleshooter},
			{Name: "view", Token: "viewer-token", Role: RoleViewer},
		},
	})
	var list []struct {
		Name string `json:"name"`
	}
	if resp := getJSON(t, ts.URL+"/api/v1/functions?token=trouble-token", &list); resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d", resp.StatusCode)
	}
	found := false
	for _, fn := range list {
		if fn.Name == "plugin.demo.ping" {
			found = true
		}
	}
	if !found {
		t.Fatalf("functions=%v", list)
	}
	var result struct {
		Function string            `json:"function"`
		Result   map[string]string `json:"result"`
	}
	if resp := getJSON(t, ts.URL+"/api/v1/function?function=plugin.demo.ping&msg=hi&token=trouble-token", &result); resp.StatusCode != http.StatusOK || result.Result["pong"] != "hi" {
		t.Fatalf("call status=%d result=%+v", resp.StatusCode, result)
	}
	if resp := getJSON(t, ts.URL+"/api/v1/function?function=plugin.demo.ping&msg=hi&token=viewer-token", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer status=%d", resp.StatusCode)
	}
	adminCode, _ := pluginRequest(t, ts.URL+"/api/v1/function?function=plugin.demo.ping&msg=ok", "admin-token", "")
	if adminCode != http.StatusOK {
		body, _ := json.Marshal(result)
		t.Fatalf("admin status=%d %s", adminCode, body)
	}
}
