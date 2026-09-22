package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseProxmoxResources(t *testing.T) {
	raw := []byte(`{"data":[
		{"type":"node","node":"pve","status":"online","cpu":0.25,"mem":100,"maxmem":400},
		{"type":"qemu","vmid":100,"name":"web","status":"running","cpu":0.1,"mem":50,"maxmem":200}
	]}`)
	rs, err := parseProxmoxResources(raw)
	if err != nil || len(rs) != 2 || rs[0].Node != "pve" || rs[1].VMID != 100 {
		t.Fatalf("%v %v", rs, err)
	}
}

func TestProxmoxCollectorFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api2/json/access/ticket":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"ticket": "t", "CSRFPreventionToken": "c"}})
		case "/api2/json/cluster/resources":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"type": "node", "node": "pve", "status": "online", "cpu": 0.5, "mem": 10, "maxmem": 40},
				{"type": "qemu", "vmid": 100, "name": "web", "status": "running", "cpu": 0.2, "mem": 5, "maxmem": 20},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &proxmoxCollector{cfg: proxmoxConfig{URL: srv.URL, User: "root@pam", Password: "x", Timeout: time.Second}, client: srv.Client()}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("proxmox.node_status.pve")
	if !ok {
		t.Fatal("missing node chart")
	}
	_, vals := ch.LastValues()
	if vals["online"] != 1 {
		t.Fatalf("%v", vals)
	}
	if _, ok := reg.Chart("proxmox.vm_cpu.qemu_100"); !ok {
		t.Fatal("missing vm chart")
	}
	if err := (&proxmoxCollector{}).Init(reg); err == nil {
		t.Fatal("expected disable without credentials")
	}
}
