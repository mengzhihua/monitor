package collect

import (
	"context"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestPodmanCollectorFixture(t *testing.T) {
	p := &podmanCollector{}
	p.get = func(_ context.Context, path string, out any) error {
		switch path {
		case "/v4.0.0/libpod/info", "/version":
			*(out.(*struct {
				Version string `json:"Version"`
			})) = struct {
				Version string `json:"Version"`
			}{Version: "4.0"}
		case "/containers/json?all=1":
			*(out.(*[]dockerListEntry)) = []dockerListEntry{
				{ID: "abc123456789", Names: []string{"/web"}, Image: "nginx", State: "running", Status: "Up"},
				{ID: "def123456789", Names: []string{"/db"}, Image: "pg", State: "exited", Status: "Exited"},
			}
		}
		return nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("podman.containers")
	if !ok {
		t.Fatal("missing containers")
	}
	_, vals := ch.LastValues()
	if vals["running"] != 1 || vals["exited"] != 1 {
		t.Fatalf("%v", vals)
	}
	if _, ok := reg.Chart("podman.container_state.web"); !ok {
		t.Fatal("missing web state")
	}
}
